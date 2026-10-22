"""Request-local correlation, observed usage and bounded model execution."""
import json
import logging
import os
import re
from contextvars import ContextVar
from dataclasses import dataclass, field
from time import perf_counter
from uuid import uuid4

from agents import ModelSettings, RunConfig, Runner, set_default_openai_client, set_tracing_export_api_key
from openai import AsyncOpenAI

logger = logging.getLogger("research.telemetry")
logger.setLevel(logging.INFO)
if not logger.handlers:
    handler = logging.StreamHandler()
    handler.setFormatter(logging.Formatter("%(message)s"))
    logger.addHandler(handler)
logger.propagate = False


def setting(name, default, minimum=1, maximum=10000):
    try:
        number = int(os.getenv(name, str(default)))
        if minimum <= number <= maximum:
            return number
    except ValueError:
        pass
    return default


@dataclass
class Measurement:
    request_id: str = field(default_factory=lambda: uuid4().hex)
    task_id: str = ""
    attempt: int = 0
    model_runs: int = 0
    model_requests: int = 0
    input_tokens: int = 0
    output_tokens: int = 0
    reported: bool = False
    incomplete: bool = False
    search_calls: int = 0
    search_cache_hits: int = 0
    search_cache_misses: int = 0
    search_errors: int = 0

    def payload(self):
        return {
            "model": os.getenv("RESEARCH_MODEL", "deepseek-flash"),
            "model_runs": self.model_runs, "model_requests": self.model_requests,
            "input_tokens": self.input_tokens if self.reported else None,
            "output_tokens": self.output_tokens if self.reported else None,
            "usage_complete": self.reported and not self.incomplete,
            "search_calls": self.search_calls, "search_cache_hits": self.search_cache_hits,
            "search_cache_misses": self.search_cache_misses, "search_errors": self.search_errors,
        }


measurement: ContextVar[Measurement | None] = ContextVar("measurement", default=None)


def log_event(event, **fields):
    state = measurement.get()
    logger.info(json.dumps({"event": event, "request_id": state.request_id if state else "",
                           "task_id": state.task_id if state else "", "attempt": state.attempt if state else 0,
                           **fields}, ensure_ascii=False, separators=(",", ":")))


def increment(field):
    state = measurement.get()
    if state is not None:
        setattr(state, field, getattr(state, field) + 1)


_client = None
_tracing = False


def configure_client():
    global _client, _tracing
    if _client is not None:
        return
    # The model provider and the trace exporter never share a configured key.
    _client = AsyncOpenAI(api_key=os.getenv("OPENAI_API_KEY", "missing"),
                         base_url=os.getenv("OPENAI_BASE_URL") or None, max_retries=1, timeout=60)
    set_default_openai_client(_client, use_for_tracing=False)
    trace_key = os.getenv("OPENAI_TRACING_API_KEY", "")
    _tracing = os.getenv("ENABLE_OPENAI_TRACING", "0") == "1" and bool(trace_key)
    if _tracing:
        set_tracing_export_api_key(trace_key)


async def run_agent(agent, prompt, **kwargs):
    configure_client()
    state = measurement.get()
    if state:
        state.model_runs += 1
    started = perf_counter()
    config = RunConfig(
        model=os.getenv("RESEARCH_MODEL", "deepseek-flash"),
        model_settings=ModelSettings(max_tokens=setting("MODEL_MAX_OUTPUT_TOKENS", 10000, 256, 16384)),
        tracing_disabled=not _tracing, trace_include_sensitive_data=False,
        workflow_name="ai-research-platform", group_id=(state.task_id or state.request_id) if state else None,
    )
    try:
        result = await Runner.run(agent, prompt, run_config=config, **kwargs)
    except BaseException as exc:
        if state:
            state.incomplete = True
        log_event("model_failed", stage=agent.name, error_type=type(exc).__name__, duration_ms=round((perf_counter()-started)*1000))
        raise
    if state:
        responses = getattr(result, "raw_responses", [])
        if not responses:
            state.incomplete = True
        for response in responses:
            usage = getattr(response, "usage", None)
            if usage is None:
                state.incomplete = True
                continue
            state.model_requests += max(0, getattr(usage, "requests", 0) or 0)
            inputs, outputs = getattr(usage, "input_tokens", 0) or 0, getattr(usage, "output_tokens", 0) or 0
            # SDK defaults missing provider fields to zero. All-zero usage is
            # deliberately treated as unavailable, not a free model request.
            if inputs + outputs > 0:
                state.reported = True
                state.input_tokens += inputs
                state.output_tokens += outputs
            else:
                state.incomplete = True
    log_event("model_finished", stage=agent.name, duration_ms=round((perf_counter()-started)*1000))
    return result


class RequestTelemetry:
    """Pure ASGI: the slot and ContextVar live until an NDJSON stream is closed."""
    def __init__(self, app):
        self.app = app
        self.active = 0

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http" or scope["path"] != "/research":
            return await self.app(scope, receive, send)
        headers = dict(scope.get("headers", []))
        supplied = headers.get(b"x-request-id", b"").decode("ascii", "ignore")
        task = headers.get(b"x-task-id", b"").decode("ascii", "ignore")
        attempt = headers.get(b"x-task-attempt", b"0").decode("ascii", "ignore")
        state = Measurement(request_id=supplied if re.fullmatch(r"[a-zA-Z0-9_-]{1,64}", supplied) else uuid4().hex,
                            task_id=task if re.fullmatch(r"[0-9a-f]{32}", task) else "",
                            attempt=int(attempt) if attempt in {"1", "2", "3"} else 0)
        token = measurement.set(state)
        if self.active >= setting("AGENT_MAX_CONCURRENCY", 4, 1, 32):
            from starlette.responses import JSONResponse
            response = JSONResponse({"detail": {"code": "agent_busy", "message": "研究执行槽已满"}}, 429, headers={"Retry-After": "5"})
            try:
                return await response(scope, receive, send)
            finally:
                measurement.reset(token)
        self.active += 1
        started = perf_counter()
        try:
            log_event("research_started")
            await self.app(scope, receive, send)
        finally:
            log_event("research_closed", duration_ms=round((perf_counter()-started)*1000), usage=state.payload())
            self.active -= 1
            measurement.reset(token)
