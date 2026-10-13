import asyncio
import sys

from research.planner_agent import create_plan


async def main():
    query = (
        sys.argv[1]
        if len(sys.argv) > 1
        else "研究 Go 服务中 Redis 缓存与 MySQL 的一致性问题"
    )

    plan = await create_plan(query)

    print("\n生成的研究计划：")
    print(plan.model_dump_json(indent=2))


if __name__ == "__main__":
    asyncio.run(main())
