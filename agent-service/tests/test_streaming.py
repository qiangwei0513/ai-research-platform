import asyncio
import json

import httpx
import pytest
from starlette.requests import ClientDisconnect

import main
from research import searcher, workflow
from research.progress import emit_progress, progress_sink
from research.schemas import SearchItem, SearchPlan, SearchSource, SearchTaskResult
from research.streaming import ResearchStream, research_frames


@pytest.mark.asyncio
async def test_real_workflow_progress_counts_finished_searches(monkeypatch):
    async def plan(query):
        return SearchPlan(goal=query, searches=[SearchItem(query=str(i), reason="test") for i in range(3)])

    async def search(item):
        await asyncio.sleep(0.002 * int(item.query))
        if item.query == "1":
            raise RuntimeError("one search failed")
        return SearchTaskResult(query=item.query, reason="test", status="success"), [
            SearchSource(title="source", url="https://example.test", snippets=["evidence"], queries=[item.query])]

    async def write(*args):
        return "# final report"

    monkeypatch.setattr(workflow, "create_plan", plan)
    monkeypatch.setattr(searcher, "run_search_task", search)
    monkeypatch.setattr(workflow, "write_report", write)
    async with httpx.AsyncClient(transport=httpx.ASGITransport(app=main.app), base_url="http://test") as client:
        response = await client.post("/research", json={"query": "q", "mode": "research"}, headers={"Accept": "application/x-ndjson"})
    assert response.headers["content-type"].startswith("application/x-ndjson")
    frames = [json.loads(line) for line in response.text.splitlines()]
    progress = [f["data"] for f in frames if f["type"] == "progress"]
    assert [(p["stage"], p["completed"]) for p in progress] == [
        ("planner", 0), ("planner", 1), ("search", 0), ("search", 1),
        ("search", 2), ("search", 3), ("writer", 0), ("writer", 1)]
    assert frames[-1]["type"] == "result"
    assert frames[-1]["data"]["search"]["status"] == "partial"
    assert progress_sink.get() is None


@pytest.mark.asyncio
async def test_stream_failure_has_terminal_error_and_no_fake_completion(monkeypatch):
    async def fail(query):
        raise RuntimeError("private provider error")

    monkeypatch.setattr(main, "run_research", fail)
    frames = [json.loads(x) async for x in research_frames(main.execute_research, main.ResearchRequest(query="q"))]
    assert [f["type"] for f in frames] == ["progress", "error"]
    assert frames[-1]["status"] == 500
    assert "private provider" not in json.dumps(frames)


@pytest.mark.asyncio
async def test_parallel_streams_keep_context_and_payloads_separate():
    async def execute(payload):
        await emit_progress(payload, 0, 1)
        await asyncio.sleep(0)
        return main.ResearchResponse(result=payload, mode="chat")

    async def collect(value):
        return [json.loads(x) async for x in research_frames(execute, value)]

    a, b = await asyncio.gather(collect("a"), collect("b"))
    assert a[0]["data"]["stage"] == "a"
    assert b[0]["data"]["stage"] == "b"
    assert progress_sink.get() is None


@pytest.mark.asyncio
@pytest.mark.parametrize("spec", ["2.3", "2.4"])
async def test_disconnect_closes_producer_even_while_yielding(spec):
    entered, finished, disconnected = asyncio.Event(), asyncio.Event(), asyncio.Event()

    async def execute(_):
        entered.set()
        try:
            await asyncio.Event().wait()
        finally:
            finished.set()

    async def receive():
        await disconnected.wait()
        return {"type": "http.disconnect"}

    async def send(message):
        if message["type"] == "http.response.body":
            disconnected.set()
            if spec == "2.4":
                raise OSError("socket closed")

    stream = ResearchStream(research_frames(execute, None, heartbeat_seconds=0.005))
    try:
        await asyncio.wait_for(stream({"type": "http", "asgi": {"spec_version": spec}}, receive, send), 1)
    except ClientDisconnect:
        assert spec == "2.4"
    assert entered.is_set() and finished.is_set()


@pytest.mark.asyncio
async def test_slow_reader_backpressure_is_bounded_and_cancellable():
    produced = 0
    stopped = asyncio.Event()

    async def execute(_):
        nonlocal produced
        try:
            for i in range(1000):
                await emit_progress("chat", i, 1000)
                produced += 1
        finally:
            stopped.set()

    frames = research_frames(execute, None)
    await anext(frames)
    await asyncio.sleep(0.01)
    assert produced <= 17
    await frames.aclose()
    assert stopped.is_set()
