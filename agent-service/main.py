import asyncio
import logging
from typing import Literal

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, ConfigDict, Field

from research.research_agent import run_research
from research.schemas import SearchBundle, SearchPlan
from research.timeouts import CHAT_TIMEOUT
from research.workflow import WorkflowError, WorkflowTimeout, run_research_workflow


app = FastAPI(title="Research Agent Service")
logger = logging.getLogger(__name__)


# 规定客户端发来的 JSON 应该是什么格式
class ResearchRequest(BaseModel):
    model_config = ConfigDict(str_strip_whitespace=True)

    query: str = Field(min_length=1, max_length=5000)
    mode: Literal["chat", "research"] = "chat"


# 规定我们返回的 JSON 是什么格式
class ResearchResponse(BaseModel):
    result: str
    mode: Literal["chat", "research"]
    plan: SearchPlan | None = None
    search: SearchBundle | None = None


# 用来检查 HTTP 服务是否启动
@app.get("/health")
async def health():
    return {"status": "ok"}


# 接收问题，调用 Agent，返回答案
@app.post("/research", response_model=ResearchResponse, response_model_exclude_none=True)
async def research(request: ResearchRequest):
    try:
        if request.mode == "research":
            outcome = await run_research_workflow(request.query)
            return ResearchResponse(mode="research", **outcome.model_dump())

        answer = await asyncio.wait_for(
            run_research(request.query),
            timeout=CHAT_TIMEOUT,
        )
        if not isinstance(answer, str) or not answer.strip():
            raise ValueError("问答返回空结果")
    except WorkflowTimeout as exc:
        raise HTTPException(status_code=504, detail={
            "code": "research_timeout", "stage": exc.stage, "message": str(exc),
        }) from None
    except WorkflowError as exc:
        raise HTTPException(status_code=502, detail={
            "code": exc.code, "stage": exc.stage, "message": str(exc),
        }) from None
    except TimeoutError:
        raise HTTPException(
            status_code=504,
            detail="研究请求超时，请稍后重试",
        )
    except Exception as exc:
        logger.error("请求执行失败 mode=%s error_type=%s", request.mode, type(exc).__name__)
        raise HTTPException(
            status_code=500,
            detail="研究请求执行失败，请查看服务端日志",
        )

    return ResearchResponse(result=answer, mode="chat")
