import asyncio

import httpx
import pytest

import main
from research import workflow
from research.cache import bypass_search_cache
from research.schemas import SearchBundle, SearchItem, SearchPlan, SearchSource, SearchTaskResult


@pytest.fixture
def stages(monkeypatch):
    observed = []

    async def plan(query):
        observed.append(bypass_search_cache.get())
        return SearchPlan(goal=query, searches=[SearchItem(query="query 1", reason="a"), SearchItem(query="query 2", reason="b")])

    async def search(plan, concurrency):
        observed.append(bypass_search_cache.get())
        return SearchBundle(goal=plan.goal, status="completed", tasks=[SearchTaskResult(query="query 1", reason="a", status="success", source_urls=["https://example.test"])], sources=[SearchSource(title="source", url="https://example.test", snippets=["text"], queries=["query 1"])])

    async def write(query, plan, bundle):
        return "# report"

    monkeypatch.setattr(workflow, "create_plan", plan)
    monkeypatch.setattr(workflow, "execute_search_plan", search)
    monkeypatch.setattr(workflow, "write_report", write)
    return observed


@pytest.mark.asyncio
async def test_workflow_stage_records_and_context_reset(stages):
    outcome = await workflow.run_research_workflow("normal query", fresh=True)
    assert stages == [True, True]
    assert [r.stage for r in outcome.runs] == ["planner", "search", "writer"]
    assert all(r.status == "succeeded" and r.finished_at >= r.started_at for r in outcome.runs)
    assert not bypass_search_cache.get()


@pytest.mark.asyncio
async def test_failed_stage_records(stages, monkeypatch):
    async def fail(*args, **kwargs):
        raise RuntimeError("secret provider response")
    monkeypatch.setattr(workflow, "write_report", fail)
    with pytest.raises(workflow.WorkflowError) as exc:
        await workflow.run_research_workflow("normal query")
    assert exc.value.runs[-1].status == "failed"
    assert exc.value.runs[-1].stage == "writer"
    assert "secret" not in str(exc.value)


@pytest.mark.asyncio
async def test_total_timeout_records(stages, monkeypatch):
    async def slow(*args, **kwargs):
        await asyncio.sleep(1)
    monkeypatch.setattr(workflow, "RESEARCH_TIMEOUT", 0.01)
    monkeypatch.setattr(workflow, "write_report", slow)
    with pytest.raises(workflow.WorkflowTimeout) as exc:
        await workflow.run_research_workflow("normal query", fresh=True)
    assert exc.value.stage == "writer"
    assert len(exc.value.runs) == 3
    assert not bypass_search_cache.get()


@pytest.mark.asyncio
async def test_http_modes_include_records(stages, monkeypatch):
    async def answer(query):
        assert bypass_search_cache.get()
        return "answer"
    monkeypatch.setattr(main, "run_research", answer)
    async with httpx.AsyncClient(transport=httpx.ASGITransport(app=main.app), base_url="http://test") as client:
        chat = await client.post("/research", json={"query": "normal", "fresh": True})
        assert chat.status_code == 200
        assert chat.json()["runs"][0]["stage"] == "chat"
        research = await client.post("/research", json={"query": "最新资料", "mode": "research"})
        assert research.status_code == 200
        assert len(research.json()["runs"]) == 3
        assert stages == [True, True]
    assert not bypass_search_cache.get()
