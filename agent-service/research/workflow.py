import asyncio
from time import perf_counter
from uuid import uuid4

from pydantic import BaseModel

from research.planner_agent import create_plan
from research.schemas import SearchBundle, SearchPlan
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


class WorkflowError(Exception):
    def __init__(self, code: str, stage: str, message: str):
        super().__init__(message)
        self.code = code
        self.stage = stage


class WorkflowTimeout(TimeoutError):
    def __init__(self, stage: str):
        super().__init__(f"研究流程在 {stage} 阶段超时，请稍后重试")
        self.stage = stage


async def run_research_workflow(query: str) -> WorkflowResult:
    """HTTP 和命令行共用的规划 → 搜索 → 写作流程。"""
    query = query.strip()
    if not 1 <= len(query) <= 5000:
        raise ValueError("研究问题长度必须在 1～5000 字符之间")

    run_id = uuid4().hex[:12]
    overall_started = perf_counter()
    current_stage = "planner"

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
        try:
            async with asyncio.timeout(timeout):
                value = await action()
        except asyncio.CancelledError:
            log(name, "cancelled", started)
            raise
        except TimeoutError:
            log(name, "failed", started, "reason=timeout")
            raise WorkflowTimeout(name) from None
        except WorkflowError as exc:
            log(name, "failed", started, f"reason={exc.code}")
            raise
        except Exception as exc:
            # 记录异常类型，避免将模型服务原始响应或凭据写入日志。
            log(name, "failed", started, f"reason={type(exc).__name__}")
            raise WorkflowError(
                "stage_failed", name, f"{name} 阶段执行失败，请查看服务端阶段日志"
            ) from None
        detail = ""
        if isinstance(value, SearchBundle):
            detail = f"search_status={value.status} sources={len(value.sources)}"
        log(name, "completed", started, detail)
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
    except WorkflowTimeout:
        log("workflow", "failed", overall_started, f"reason=stage_timeout stage_at_failure={current_stage}")
        raise
    except TimeoutError:
        log("workflow", "failed", overall_started, f"reason=overall_timeout stage_at_failure={current_stage}")
        raise WorkflowTimeout(current_stage) from None
    except asyncio.CancelledError:
        log("workflow", "cancelled", overall_started)
        raise
    except WorkflowError as exc:
        log("workflow", "failed", overall_started, f"reason={exc.code}")
        raise
    log("workflow", "completed", overall_started)
    return WorkflowResult(result=report, plan=plan, search=bundle)
