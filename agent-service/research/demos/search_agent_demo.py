import asyncio
import json
import sys

from research.schemas import SearchItem
from research.search_agent import run_search_task


async def main():
    item = SearchItem(
        query=sys.argv[1] if len(sys.argv) > 1 else "site:go.dev context cancellation",
        reason="查找与该搜索词相关的公开资料并总结关键内容",
    )
    task, sources = await run_search_task(item)
    print(json.dumps({
        "task": task.model_dump(),
        "sources": [source.model_dump() for source in sources],
    }, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except (RuntimeError, ValueError) as exc:
        print(f"搜索测试失败：{exc}", file=sys.stderr)
        raise SystemExit(1)
