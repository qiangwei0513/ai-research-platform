"""可选 Redis 缓存：失败只影响性能，不改变搜索是否能够执行。"""

import asyncio
import hashlib
import json
import logging
import os
import re
import unicodedata
from contextvars import ContextVar

from redis.asyncio import Redis
from redis.backoff import NoBackoff
from redis.retry import Retry

logger = logging.getLogger(__name__)
bypass_search_cache: ContextVar[bool] = ContextVar("bypass_search_cache", default=False)


def needs_fresh_search(query: str) -> bool:
    """保守识别时效性问题；调用方仍可用 fresh=true 明确绕过。"""
    return bool(re.search(
        r"最新|实时|即时|今天|今日|目前|当前|现在|刚刚|新闻|股价|汇率|天气|票价|"
        r"\b(latest|current|today|tonight|now|news|live|real[- ]?time|weather|stock price)\b",
        query, re.IGNORECASE,
    ))


def normalize_query(query: str) -> str:
    # 保留大小写与引号内空格，避免改变代码标识符或精确短语搜索的语义。
    return unicodedata.normalize("NFC", query).strip()


def search_cache_key(query: str, parameters: dict) -> str:
    payload = {"provider": "tavily", "query": normalize_query(query), **parameters}
    digest = hashlib.sha256(json.dumps(payload, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    return f"{os.getenv('CACHE_PREFIX', 'arp')}:search:v1:{digest}"


class SearchCache:
    def __init__(self):
        self.url = os.getenv("REDIS_URL", "").strip()
        try:
            self.ttl = int(os.getenv("SEARCH_CACHE_TTL_SECONDS", "900"))
            if not 0 <= self.ttl <= 86400:
                raise ValueError
        except ValueError:
            logger.warning("[cache] invalid TTL; search cache disabled")
            self.ttl = 0

    @property
    def enabled(self) -> bool:
        return bool(self.url) and self.ttl > 0

    async def _command(self, operation: str, *args, **kwargs):
        try:
            # 每个作用域显式关闭连接，HTTP 与多次 asyncio.run 的 CLI 演示均可使用。
            async with asyncio.timeout(0.25):
                async with Redis.from_url(
                    self.url, decode_responses=True,
                    socket_connect_timeout=0.2, socket_timeout=0.2,
                    retry=Retry(NoBackoff(), 0),
                ) as client:
                    return await getattr(client, operation)(*args, **kwargs), True
        except Exception as exc:
            # 只记录异常类型；不输出带密码的 Redis URL 或查询内容。
            logger.warning("[cache] redis %s degraded error_type=%s", operation, type(exc).__name__)
            return None, False

    async def get(self, key: str) -> tuple[dict | None, str]:
        if not self.enabled:
            return None, "disabled"
        raw, ok = await self._command("get", key)
        if not ok:
            return None, "degraded"
        if raw is None:
            return None, "miss"
        try:
            value = json.loads(raw)
            if not isinstance(value, dict) or not isinstance(value.get("fetched_at"), str):
                raise ValueError
            hits = value.get("results")
            if not isinstance(hits, list) or not 1 <= len(hits) <= 3:
                raise ValueError
            if any(not isinstance(hit, dict) or any(not isinstance(hit.get(k), str) for k in ("title", "url", "content")) for hit in hits):
                raise ValueError
            return value, "hit"
        except (ValueError, TypeError):
            logger.warning("[cache] invalid search entry; fetching origin")
            return None, "miss"

    async def set(self, key: str, value: dict) -> None:
        if self.enabled and value.get("results"):
            await self._command("set", key, json.dumps(value, ensure_ascii=False), ex=self.ttl)

    async def health(self) -> str:
        if not self.url:
            return "disabled"
        _, ok = await self._command("ping")
        return "ok" if ok else "unavailable"
