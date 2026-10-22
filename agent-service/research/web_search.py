import asyncio
import json
import os
from collections import deque
from time import monotonic
from weakref import WeakKeyDictionary
from dataclasses import dataclass, field
from datetime import datetime, timezone

import httpx
from agents import RunContextWrapper, function_tool

from research.cache import SearchCache, bypass_search_cache, needs_fresh_search, normalize_query, search_cache_key
from research.telemetry import increment, log_event, measurement, setting


_limits = WeakKeyDictionary()


def search_limits():
    # A process normally has one loop; separate test/CLI loops need separate locks.
    loop = asyncio.get_running_loop()
    if loop not in _limits:
        _limits[loop] = (asyncio.Semaphore(setting("SEARCH_MAX_CONCURRENCY", 4, 1, 16)), deque())
    return _limits[loop]


async def request_search(api_key, query, parameters):
    semaphore, recent = search_limits()
    async with asyncio.timeout(25):
        async with semaphore:
            async with httpx.AsyncClient(timeout=10) as client:
                for attempt in range(2):
                    now = monotonic()
                    while recent and recent[0] <= now - 60:
                        recent.popleft()
                    state = measurement.get()
                    if len(recent) >= setting("SEARCH_CALLS_PER_MINUTE", 120, 1, 1000):
                        raise RuntimeError("搜索服务本分钟额度已用完")
                    if state and state.search_calls >= setting("SEARCH_CALLS_PER_TASK", 6, 1, 12):
                        raise RuntimeError("本次研究搜索额度已用完")
                    # Count before awaiting: concurrent tools cannot overspend a budget.
                    recent.append(now)
                    increment("search_calls")
                    try:
                        async with client.stream("POST", "https://api.tavily.com/search",
                                                 headers={"Authorization": f"Bearer {api_key}"},
                                                 json={"query": query, **parameters}) as response:
                            response.raise_for_status()
                            body = bytearray()
                            async for chunk in response.aiter_bytes():
                                body.extend(chunk)
                                if len(body) > 262144:
                                    raise RuntimeError("搜索响应超过大小限制")
                            return json.loads(body)
                    except httpx.HTTPStatusError as exc:
                        if attempt or (exc.response.status_code != 429 and exc.response.status_code < 500):
                            raise
                    except (httpx.TimeoutException, httpx.RequestError):
                        if attempt:
                            raise
                    await asyncio.sleep(0.25)


@dataclass
class WebSearchContext:
    """每次 Search Agent 独立保存工具实测结果，不与其他搜索共享。"""

    calls: int = 0
    results: list[dict] = field(default_factory=list)


async def fetch_web_results(query: str, *, fresh: bool = False) -> dict:
    """请求搜索 API，返回整理后的网页资料。"""
    query = normalize_query(query)
    if not query:
        raise ValueError("搜索词不能为空")

    api_key = os.getenv("TAVILY_API_KEY")
    if not api_key:
        raise RuntimeError("未配置 TAVILY_API_KEY")

    parameters = {
        "search_depth": "basic", "max_results": 3,
        "include_answer": False, "include_raw_content": False,
    }
    cache = SearchCache()
    key = search_cache_key(query, {**parameters, "snippet_limit": 1500})
    bypass = fresh or bypass_search_cache.get() or needs_fresh_search(query)
    cached, cache_status = (None, "bypass") if bypass else await cache.get(key)
    if cached is not None:
        increment("search_cache_hits")
        log_event("search_cache", status="hit")
        return {"query": query, "count": len(cached["results"]), **cached, "cache_status": "hit"}

    increment("search_cache_misses")
    log_event("search_started", cache_status=cache_status)

    try:
        data = await request_search(api_key, query, parameters)

    except (TimeoutError, httpx.TimeoutException):
        increment("search_errors")
        raise RuntimeError("联网搜索超时，请稍后重试") from None
    except httpx.HTTPStatusError as exc:
        increment("search_errors")
        code = exc.response.status_code
        raise RuntimeError(f"搜索服务返回 HTTP {code}") from None
    except httpx.RequestError:
        increment("search_errors")
        raise RuntimeError("无法连接搜索服务，请检查网络") from None
    except ValueError:
        increment("search_errors")
        raise RuntimeError("搜索服务返回了无效 JSON") from None
    except RuntimeError:
        increment("search_errors")
        raise

    results = []

    if not isinstance(data, dict) or not isinstance(data.get("results"), list):
        increment("search_errors")
        raise RuntimeError("搜索服务返回了无效结果结构")
    for item in data["results"][:3]:
        if not isinstance(item, dict) or any(not isinstance(item.get(k), str) for k in ("title", "url", "content")):
            increment("search_errors")
            raise RuntimeError("搜索服务返回了无效来源结构")
        results.append(
            {
                "title": item["title"][:300],
                "url": item["url"],
                "content": item["content"][:1500],
            }
        )

    fetched_at = datetime.now(timezone.utc).isoformat()
    if not bypass:
        await cache.set(key, {"results": results, "fetched_at": fetched_at})
    log_event("search_finished", count=len(results), cache_status=cache_status)
    return {
        "query": query,
        "count": len(results),
        "results": results,
        "cache_status": cache_status,
        "fetched_at": fetched_at,
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
