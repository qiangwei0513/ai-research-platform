import asyncio
import sys
from time import perf_counter

from research.planner import create_plan
from research.searcher import execute_search_plan


async def main():
    query = (
        sys.argv[1]
        if len(sys.argv) > 1
        else "研究 Go HTTP 服务中 context 超时与取消的使用方式"
    )

    plan = await create_plan(query)

    print("\n搜索计划：")
    print(plan.model_dump_json(indent=2))

    started_at = perf_counter()

    bundle = await execute_search_plan(plan, concurrency=2)

    print(f"\n搜索阶段耗时：{perf_counter() - started_at:.2f} 秒")
    print("\n汇总资料：")
    print(bundle.model_dump_json(indent=2))

    if not bundle.sources:
        print("\n没有获得可供撰写报告的资料。")

    if bundle.status == "failed":
        raise SystemExit(1)


if __name__ == "__main__":
    asyncio.run(main())