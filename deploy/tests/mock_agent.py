"""Test-only upstream substitutes. The real HTTP/workflow/NDJSON code still runs.

Mounted only by compose.smoke.yaml; excluded from production images.
"""
import asyncio

import main
from research import searcher, workflow
from research.schemas import SearchItem, SearchPlan, SearchSource, SearchTaskResult


async def plan(query):
    await asyncio.sleep(0.1)
    return SearchPlan(goal=query, searches=[
        SearchItem(query=f"V7 source {i}", reason="container smoke test")
        for i in range(3)
    ])


async def search(item):
    await asyncio.sleep(0.2)
    url = f"https://example.test/source/{item.query[-1]}"
    return (
        SearchTaskResult(query=item.query, reason=item.reason, status="success", source_urls=[url]),
        [SearchSource(title=item.query, url=url, snippets=["V7 test evidence."], queries=[item.query])],
    )


async def write(query, plan, bundle):
    await asyncio.sleep(0.1)
    return "# V7 container report\n\nVolumes preserve reports. [1]\n\n[1] https://example.test/source/0"


async def chat(query):
    return "V7 container chat OK"


workflow.create_plan = plan
searcher.run_search_task = search
workflow.write_report = write
main.run_research = chat
app = main.app
