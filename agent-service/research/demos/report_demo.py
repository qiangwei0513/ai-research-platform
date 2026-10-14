import asyncio
import sys

from research.workflow import WorkflowError, run_research_workflow


async def main():
    query = (
        sys.argv[1]
        if len(sys.argv) > 1
        else "研究 Go context 如何实现超时控制和取消传播"##默认问题
    )

    outcome = await run_research_workflow(query)
    print("\n搜索计划：")
    print(outcome.plan.model_dump_json(indent=2))
    print("\n" + outcome.result)


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except TimeoutError:
        print(
            "研究阶段超时，请稍后重试",
            file=sys.stderr,
        )
        raise SystemExit(1)
    except (ValueError, WorkflowError) as exc:
        print(
            f"无法生成报告：{exc}",
            file=sys.stderr,
        )
        raise SystemExit(1)
