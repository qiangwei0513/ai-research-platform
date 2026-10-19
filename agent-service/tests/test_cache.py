import asyncio
import os
from uuid import uuid4

import httpx
import pytest
from redis.asyncio import Redis

from research import web_search
from research.cache import SearchCache, bypass_search_cache, needs_fresh_search, search_cache_key


@pytest.fixture(autouse=True)
def isolated_config(monkeypatch):
    # 永远不使用开发者 .env 中的搜索凭据或 Redis。
    monkeypatch.setenv("TAVILY_API_KEY", "test-only-unused")
    monkeypatch.setenv("REDIS_URL", os.getenv("TEST_REDIS_URL", ""))
    monkeypatch.setenv("CACHE_PREFIX", f"test-{uuid4().hex}")
    monkeypatch.setenv("SEARCH_CACHE_TTL_SECONDS", "900")
    token = bypass_search_cache.set(False)
    yield
    bypass_search_cache.reset(token)


@pytest.fixture
def provider(monkeypatch):
    state = {"calls": 0, "status": 200, "data": {"results": [{"title": "Official", "url": "https://example.test/doc", "content": "Source content"}]}}
    original = httpx.AsyncClient

    def respond(request):
        state["calls"] += 1
        return httpx.Response(state["status"], json=state["data"])

    monkeypatch.setattr(web_search.httpx, "AsyncClient", lambda **kwargs: original(transport=httpx.MockTransport(respond), **kwargs))
    return state


def require_redis():
    if not os.getenv("TEST_REDIS_URL"):
        pytest.skip("set TEST_REDIS_URL to isolated test Redis")


def test_key_parameters_and_normalization():
    assert search_cache_key("  Go context  ", {"max_results": 3}) == search_cache_key("Go context", {"max_results": 3})
    assert search_cache_key("cafe\u0301", {}) == search_cache_key("café", {})
    assert search_cache_key("Go context", {"max_results": 3}) != search_cache_key("Go context", {"max_results": 2})
    assert search_cache_key('"a  b"', {}) != search_cache_key('"a b"', {})
    assert "private query" not in search_cache_key("private query", {})


@pytest.mark.parametrize("query", ["今天有什么新闻", "最新 Go 版本", "实时汇率", "current weather", "latest research"])
def test_freshness_detection(query):
    assert needs_fresh_search(query)


@pytest.mark.asyncio
async def test_cache_hit_and_expiry(provider, monkeypatch):
    require_redis()
    monkeypatch.setenv("SEARCH_CACHE_TTL_SECONDS", "1")
    first = await web_search.fetch_web_results("Go context")
    second = await web_search.fetch_web_results(" Go context ")
    assert first["cache_status"] == "miss"
    assert second["cache_status"] == "hit"
    assert first["fetched_at"] == second["fetched_at"]
    assert provider["calls"] == 1
    await asyncio.sleep(1.1)
    third = await web_search.fetch_web_results("Go context")
    assert third["cache_status"] == "miss"
    assert provider["calls"] == 2


@pytest.mark.asyncio
async def test_fresh_bypasses_existing_entry(provider):
    require_redis()
    await web_search.fetch_web_results("Go context")
    assert (await web_search.fetch_web_results("Go context", fresh=True))["cache_status"] == "bypass"
    token = bypass_search_cache.set(True)
    try:
        assert (await web_search.fetch_web_results("Go context"))["cache_status"] == "bypass"
    finally:
        bypass_search_cache.reset(token)
    assert provider["calls"] == 3
    assert (await web_search.fetch_web_results("Go context"))["cache_status"] == "hit"


@pytest.mark.asyncio
async def test_empty_and_failed_results_not_cached(provider):
    require_redis()
    provider["status"] = 503
    for _ in range(2):
        with pytest.raises(RuntimeError, match="HTTP 503"):
            await web_search.fetch_web_results("failed query")
    provider["status"] = 200
    provider["data"] = {"results": []}
    for _ in range(2):
        assert (await web_search.fetch_web_results("empty query"))["count"] == 0
    assert provider["calls"] == 4


@pytest.mark.asyncio
async def test_corrupt_cache_falls_back(provider):
    require_redis()
    params = {"search_depth": "basic", "max_results": 3, "include_answer": False, "include_raw_content": False, "snippet_limit": 1500}
    async with Redis.from_url(os.environ["TEST_REDIS_URL"]) as client:
        await client.set(search_cache_key("Go context", params), "broken-json", ex=10)
    result = await web_search.fetch_web_results("Go context")
    assert result["count"] == 1
    assert provider["calls"] == 1


@pytest.mark.asyncio
async def test_redis_failure_uses_provider(provider, monkeypatch):
    monkeypatch.setenv("REDIS_URL", "redis://127.0.0.1:1/0")
    result = await asyncio.wait_for(web_search.fetch_web_results("Go context"), timeout=1.5)
    assert result["cache_status"] == "degraded"
    assert result["count"] == 1 and provider["calls"] == 1
    assert await SearchCache().health() == "unavailable"


@pytest.mark.asyncio
async def test_disabled_cache_and_invalid_provider(provider, monkeypatch):
    monkeypatch.setenv("SEARCH_CACHE_TTL_SECONDS", "0")
    assert (await web_search.fetch_web_results("Go context"))["cache_status"] == "disabled"
    provider["data"] = {"error": "invalid response"}
    with pytest.raises(RuntimeError, match="无效结果结构"):
        await web_search.fetch_web_results("Go context")


@pytest.mark.asyncio
async def test_invalid_redis_configuration_degrades(provider, monkeypatch):
    monkeypatch.setenv("REDIS_URL", "invalid-protocol://secret@hidden")
    assert (await web_search.fetch_web_results("Go context"))["cache_status"] == "degraded"


@pytest.mark.asyncio
async def test_no_credentials_does_not_read_cache(provider, monkeypatch):
    monkeypatch.delenv("TAVILY_API_KEY")
    with pytest.raises(RuntimeError, match="未配置"):
        await web_search.fetch_web_results("Go context")
    assert provider["calls"] == 0
