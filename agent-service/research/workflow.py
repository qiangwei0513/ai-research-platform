import asyncio
from time import perf_counter
from uuid import uuid4
from datetime import datetime, timezone

from pydantic import BaseModel

from research.planner_agent import create_plan
from research.schemas import AgentRun, SearchBundle, SearchPlan
from research.cache import bypass_search_cache, needs_fresh_search
from research.progress import emit_progress
from research.searcher import execute_search_plan
from research.timeouts import (
    PLANNER_TIMEOUT,
    RESEARCH_TIMEOUT,
    SEARCH_STAGE_TIMEOUT,
    WRITER_TIMEOUT,
)
from research.writer_agent import prepare_sources, write_report


class WorkflowResult(BaseModel):
    result: str
    plan: SearchPlan
    search: SearchBundle
    runs: list[AgentRun]


class WorkflowError(Exception):
    def __init__(self, code: str, stage: str, message: str):
        super().__init__(message)
        self.code = code
        self.stage = stage
        self.runs: list[AgentRun] = []


class WorkflowTimeout(TimeoutError):
    def __init__(self, stage: str):
        super().__init__(f"研究流程在 {stage} 阶段超时，请稍后重试")
        self.stage = stage
        self.runs: list[AgentRun] = []


async def run_research_workflow(query: str, *, fresh: bool = False) -> WorkflowResult:
    """HTTP 和命令行共用的规划 → 搜索 → 写作流程。"""
    query = query.strip()
    if not 1 <= len(query) <= 5000:
        raise ValueError("研究问题长度必须在 1～5000 字符之间")

    run_id = uuid4().hex[:12]
    overall_started = perf_counter()
    current_stage = "planner"
    runs: list[AgentRun] = []
    cache_token = bypass_search_cache.set(fresh or bypass_search_cache.get() or needs_fresh_search(query))

    def log(stage, status, started, detail=""):
        print(
            f"[workflow] run={run_id} stage={stage} status={status} "
            f"elapsed={perf_counter() - started:.2f}s {detail}".rstrip(),
            flush=True,
        )

    async def run_stage(name, timeout, action):
        nonlocal current_stage
        current_stage = name
        started = perf_counter()
        log(name, "started", started)
        run = AgentRun(stage=name, status="running", started_at=datetime.now(timezone.utc), finished_at=datetime.now(timezone.utc))
        runs.append(run)
        try:
            if name != "search":
                await emit_progress(name, 0, 1)
            async with asyncio.timeout(timeout):
                value = await action()
        except asyncio.CancelledError:
            run.status = "cancelled"
            run.error_code = "cancelled"
            log(name, "cancelled", started)
            raise
        except TimeoutError:
            run.status = "failed"
            run.error_code = "research_timeout"
            log(name, "failed", started, "reason=timeout")
            raise WorkflowTimeout(name) from None
        except WorkflowError as exc:
            run.status = "failed"
            run.error_code = exc.code
            log(name, "failed", started, f"reason={exc.code}")
            raise
        except Exception as exc:
            run.status = "failed"
            run.error_code = "stage_failed"
            # 记录异常类型，避免将模型服务原始响应或凭据写入日志。
            log(name, "failed", started, f"reason={type(exc).__name__}")
            raise WorkflowError(
                "stage_failed", name, f"{name} 阶段执行失败，请查看服务端阶段日志"
            ) from None
        finally:
            run.finished_at = datetime.now(timezone.utc)
        run.status = "succeeded"
        detail = ""
        if isinstance(value, SearchBundle):
            detail = f"search_status={value.status} sources={len(value.sources)}"
        log(name, "completed", started, detail)
        if name != "search":
            await emit_progress(name, 1, 1)
        return value

    async def search(plan):
        bundle = await execute_search_plan(plan, concurrency=2)
        if bundle.status == "failed":
            raise WorkflowError(
                "search_failed", "search", "全部搜索任务失败，未生成报告；这不表示不存在相关资料"
            )
        try:
            prepare_sources(bundle)
        except ValueError:
            raise WorkflowError(
                "no_sources", "search", "本次未获得可用于写作的搜索片段，未生成报告"
            ) from None
        return bundle

    async def write(plan, bundle):
        report = await write_report(query, plan, bundle)
        if not isinstance(report, str) or not report.strip():
            raise WorkflowError("empty_report", "writer", "Writer 返回了空报告")
        return report

    log("workflow", "started", overall_started)
    try:
        async with asyncio.timeout(RESEARCH_TIMEOUT):
            plan = await run_stage("planner", PLANNER_TIMEOUT, lambda: create_plan(query))
            bundle = await run_stage("search", SEARCH_STAGE_TIMEOUT, lambda: search(plan))
            report = await run_stage(
                "writer", WRITER_TIMEOUT, lambda: write(plan, bundle)
            )
    except WorkflowTimeout as exc:
        exc.runs = runs
        log("workflow", "failed", overall_started, f"reason=stage_timeout stage_at_failure={current_stage}")
        raise
    except TimeoutError:
        log("workflow", "failed", overall_started, f"reason=overall_timeout stage_at_failure={current_stage}")
        exc = WorkflowTimeout(current_stage)
        exc.runs = runs
        raise exc from None
    except asyncio.CancelledError:
        log("workflow", "cancelled", overall_started)
        raise
    except WorkflowError as exc:
        exc.runs = runs
        log("workflow", "failed", overall_started, f"reason={exc.code}")
        raise
    finally:
        bypass_search_cache.reset(cache_token)
    log("workflow", "completed", overall_started)
    return WorkflowResult(result=report, plan=plan, search=bundle, runs=runs)
