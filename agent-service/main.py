import asyncio
import logging
from typing import Literal
from datetime import datetime, timezone

from fastapi import FastAPI, HTTPException, Request
from pydantic import BaseModel, ConfigDict, Field

from research.research_agent import run_research
from research.schemas import AgentRun, SearchBundle, SearchPlan
from research.cache import SearchCache, bypass_search_cache, needs_fresh_search
from research.timeouts import CHAT_TIMEOUT
from research.progress import emit_progress
from research.streaming import ResearchStream, research_frames
from research.telemetry import Measurement, RequestTelemetry, measurement
from research.workflow import WorkflowError, WorkflowTimeout, run_research_workflow


app = FastAPI(title="Research Agent Service")
app.add_middleware(RequestTelemetry)
logger = logging.getLogger(__name__)


# 规定客户端发来的 JSON 应该是什么格式
class ResearchRequest(BaseModel):
    model_config = ConfigDict(str_strip_whitespace=True)

    query: str = Field(min_length=1, max_length=5000)
    mode: Literal["chat", "research"] = "chat"
    fresh: bool = False


# 规定我们返回的 JSON 是什么格式
class ResearchResponse(BaseModel):
    usage: dict | None = None
    result: str
    mode: Literal["chat", "research"]
    plan: SearchPlan | None = None
    search: SearchBundle | None = None
    runs: list[AgentRun] = Field(default_factory=list)


# 用来检查 HTTP 服务是否启动
@app.get("/health")
async def health():
    return {"status": "ok", "redis": await SearchCache().health()}


# 接收问题，调用 Agent，返回答案
async def execute_research(request: ResearchRequest):
    token = None
    if measurement.get() is None:
        token = measurement.set(Measurement())
    try:
        result = await _execute_research(request)
        result.usage = measurement.get().payload()
        return result
    except HTTPException as exc:
        if isinstance(exc.detail, dict):
            exc.detail["usage"] = measurement.get().payload()
        raise
    finally:
        if token is not None:
            measurement.reset(token)


async def _execute_research(request: ResearchRequest):
    started = datetime.now(timezone.utc)
    cache_token = bypass_search_cache.set(request.fresh or needs_fresh_search(request.query))
    def chat_runs(status, code=""):
        return [AgentRun(stage="chat", status=status, started_at=started, finished_at=datetime.now(timezone.utc), error_code=code)]
    try:
        if request.mode == "research":
            outcome = await run_research_workflow(request.query, fresh=request.fresh)
            return ResearchResponse(mode="research", **outcome.model_dump())

        await emit_progress("chat", 0, 1)
        answer = await asyncio.wait_for(
            run_research(request.query),
            timeout=CHAT_TIMEOUT,
        )
        if not isinstance(answer, str) or not answer.strip():
            raise ValueError("问答返回空结果")
        await emit_progress("chat", 1, 1)
    except WorkflowTimeout as exc:
        raise HTTPException(status_code=504, detail={
            "code": "research_timeout", "stage": exc.stage, "message": str(exc),
            "runs": [run.model_dump(mode="json") for run in exc.runs],
        }) from None
    except WorkflowError as exc:
        raise HTTPException(status_code=502, detail={
            "code": exc.code, "stage": exc.stage, "message": str(exc),
            "runs": [run.model_dump(mode="json") for run in exc.runs],
        }) from None
    except TimeoutError:
        raise HTTPException(
            status_code=504,
            detail={"code": "chat_timeout", "stage": "chat", "message": "研究请求超时，请稍后重试", "runs": [r.model_dump(mode="json") for r in chat_runs("failed", "chat_timeout")]},
        )
    except Exception as exc:
        logger.error("请求执行失败 mode=%s error_type=%s", request.mode, type(exc).__name__)
        raise HTTPException(
            status_code=500,
            detail={"code": "chat_failed", "stage": "chat", "message": "研究请求执行失败，请查看服务端日志", "runs": [r.model_dump(mode="json") for r in chat_runs("failed", "chat_failed")]},
        )
    finally:
        bypass_search_cache.reset(cache_token)

    return ResearchResponse(result=answer, mode="chat", runs=chat_runs("succeeded"))


async def wait_for_disconnect(request: Request):
    # FastAPI has already consumed the JSON body. Await the ASGI disconnect event
    # directly; polling is_disconnected() uses a nested cancellation scope which
    # can swallow task cancellation in an in-process ASGI transport.
    while True:
        message = await request.receive()
        if message["type"] == "http.disconnect":
            return


@app.post("/research", response_model=ResearchResponse, response_model_exclude_none=True)
async def research(payload: ResearchRequest, request: Request):
    if "application/x-ndjson" in request.headers.get("accept", ""):
        return ResearchStream(
            research_frames(execute_research, payload),
            media_type="application/x-ndjson",
            headers={"Cache-Control": "no-store", "X-Accel-Buffering": "no"},
        )
    # Worker cancellation closes this HTTP connection. Stop local Agent tasks too;
    # a remote model provider may still finish a request already accepted by it.
    execution = asyncio.create_task(execute_research(payload))
    disconnected = asyncio.create_task(wait_for_disconnect(request))
    try:
        done, _ = await asyncio.wait(
            {execution, disconnected}, return_when=asyncio.FIRST_COMPLETED
        )
        if execution in done:
            return await execution
        raise HTTPException(status_code=499, detail="调用方已断开连接")
    finally:
        for task in (execution, disconnected):
            if not task.done():
                task.cancel()
        await asyncio.gather(execution, disconnected, return_exceptions=True)
