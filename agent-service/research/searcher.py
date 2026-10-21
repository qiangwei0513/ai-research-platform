import asyncio

from research.schemas import (
    SearchBundle,
    SearchItem,
    SearchPlan,
    SearchSource,
    SearchTaskResult,
)
from research.search_agent import run_search_task
from research.progress import emit_progress


async def execute_search_plan(
    plan: SearchPlan,
    concurrency: int = 2,
) -> SearchBundle:
    if not 2 <= len(plan.searches) <= 3:
        raise ValueError("搜索计划必须包含 2～3 个任务")

    if concurrency < 1:
        raise ValueError("并发数必须大于 0")

    # 控制这一次研究中同时执行的搜索数量。
    semaphore = asyncio.Semaphore(concurrency)
    completed = 0
    progress_lock = asyncio.Lock()
    await emit_progress("search", 0, len(plan.searches))

    async def search_one(item: SearchItem):
        nonlocal completed
        async with semaphore:
            print(f"[searcher] 开始搜索：{item.query}", flush=True)

            try:
                task, sources = await run_search_task(item)

                print(
                    f"[searcher] 搜索结束：{item.query}，"
                    f"状态={task.status}",
                    flush=True,
                )

            except Exception as exc:
                # 一个搜索失败，保留错误，让其他搜索继续。
                print(
                    f"[searcher] 搜索失败：{item.query}，原因={exc}",
                    flush=True,
                )

                task = SearchTaskResult(
                    query=item.query,
                    reason=item.reason,
                    status="failed",
                    error=str(exc),
                )

                sources = []
            # Count finished sub-tasks, including failed/empty searches. Serialise
            # emission too: queue backpressure must not reorder 1/3 and 2/3.
            async with progress_lock:
                completed += 1
                await emit_progress("search", completed, len(plan.searches))
            return task, sources

    # 同时调度任务，并等待它们全部结束。
    outcomes = await asyncio.gather(
        *(search_one(item) for item in plan.searches)
    )

    tasks = []
    sources_by_url: dict[str, SearchSource] = {}

    # 按 URL 合并资料。
    for task, sources in outcomes:
        tasks.append(task)

        for source in sources:
            existing = sources_by_url.get(source.url)

            if existing is None:
                sources_by_url[source.url] = source
                continue

            for query in source.queries:
                if query not in existing.queries:
                    existing.queries.append(query)

            for snippet in source.snippets:
                if snippet not in existing.snippets:
                    existing.snippets.append(snippet)

    merged_sources = list(sources_by_url.values())
    failed_count = sum(task.status == "failed" for task in tasks)

    if failed_count == len(tasks):
        status = "failed"
    elif failed_count > 0:
        status = "partial"
    elif not merged_sources:
        status = "empty"
    else:
        status = "completed"

    print(
        f"[searcher] 汇总完成：状态={status}，"
        f"失败任务={failed_count}，"
        f"去重后来源={len(merged_sources)}",
        flush=True,
    )

    return SearchBundle(
        goal=plan.goal,
        status=status,
        tasks=tasks,
        sources=merged_sources,
    )
