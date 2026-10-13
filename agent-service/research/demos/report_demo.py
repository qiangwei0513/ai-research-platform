import asyncio
import sys

from research.planner_agent import create_plan
from research.searcher import execute_search_plan
from research.writer_agent import write_report


async def main():
    query = (
        sys.argv[1]
        if len(sys.argv) > 1
        else "研究 Go context 如何实现超时控制和取消传播"##默认问题
    )

    plan = await create_plan(query)

    bundle = await execute_search_plan(plan)

    report = await write_report(query, plan, bundle)

    print("\n" + report)


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except TimeoutError:
        print(
            "研究阶段超时，请稍后重试",
            file=sys.stderr,
        )
        raise SystemExit(1)
    except ValueError as exc:
        print(
            f"无法生成报告：{exc}",
            file=sys.stderr,
        )
        raise SystemExit(1)
