import asyncio
import json
import os
from dataclasses import dataclass, field

import httpx
from agents import RunContextWrapper, function_tool


@dataclass
class WebSearchContext:
    """每次 Search Agent 独立保存工具实测结果，不与其他搜索共享。"""

    calls: int = 0
    results: list[dict] = field(default_factory=list)


async def fetch_web_results(query: str) -> dict:
    """请求搜索 API，返回整理后的网页资料。"""
    query = query.strip()
    if not query:
        raise ValueError("搜索词不能为空")

    api_key = os.getenv("TAVILY_API_KEY")
    if not api_key:
        raise RuntimeError("未配置 TAVILY_API_KEY")

    print(f"[tool] search_web 开始执行，搜索词={query!r}", flush=True)

    try:
        # 为整个搜索操作设置 25 秒期限。
        async with asyncio.timeout(25):
            async with httpx.AsyncClient(timeout=20) as client:
                response = await client.post(
                    "https://api.tavily.com/search",
                    headers={
                        "Authorization": f"Bearer {api_key}",
                    },
                    json={
                        "query": query,
                        "search_depth": "basic",
                        "max_results": 3,
                        "include_answer": False,
                        "include_raw_content": False,
                    },
                )

                response.raise_for_status()
                data = response.json()

    except (TimeoutError, httpx.TimeoutException):
        raise RuntimeError("联网搜索超时，请稍后重试") from None
    except httpx.HTTPStatusError as exc:
        code = exc.response.status_code
        raise RuntimeError(f"搜索服务返回 HTTP {code}") from None
    except httpx.RequestError:
        raise RuntimeError("无法连接搜索服务，请检查网络") from None
    except ValueError:
        raise RuntimeError("搜索服务返回了无效 JSON") from None

    results = []

    for item in data["results"]:
        results.append(
            {
                "title": item["title"],
                "url": item["url"],
                "content": item["content"][:1500],
            }
        )

    print(
        f"[tool] search_web 执行完成，找到 {len(results)} 条结果",
        flush=True,
    )

    for item in results:
        print(f"[source] {item['url']}", flush=True)

    return {
        "query": query,
        "count": len(results),
        "results": results,
    }


@function_tool(failure_error_function=None)
async def search_web(ctx: RunContextWrapper, query: str) -> str:
    """联网搜索公开网页，返回标题、来源链接和内容片段。

    Args:
        query: 适合搜索引擎的查询词，可以包含主题、技术名称或网站限定。
    """
    context = ctx.context
    if isinstance(context, WebSearchContext):
        if context.calls >= 1:
            raise RuntimeError("每个搜索任务最多调用一次 Tavily")
        # 在 await 之前计数，防止同时发起多个工具调用时绕过上限。
        context.calls += 1

    data = await fetch_web_results(query)
    output = json.dumps(data, ensure_ascii=False)
    if isinstance(context, WebSearchContext):
        context.results.append(data)
    return output
