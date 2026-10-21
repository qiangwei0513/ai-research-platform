"""Bounded NDJSON transport used only between the Worker and Agent service."""
import asyncio
import json

import anyio
from fastapi import HTTPException
from starlette.responses import StreamingResponse

from research.progress import progress_sink


async def research_frames(execute, payload, heartbeat_seconds=5):
    queue = asyncio.Queue(maxsize=16)

    async def progress(data):
        await queue.put({"type": "progress", "data": data})

    async def produce():
        token = progress_sink.set(progress)
        try:
            result = await execute(payload)
            await queue.put({"type": "result", "data": result.model_dump(mode="json", exclude_none=True)})
        except HTTPException as exc:
            await queue.put({"type": "error", "status": exc.status_code, "detail": exc.detail})
        except Exception:
            await queue.put({"type": "error", "status": 500, "detail": {"code": "upstream_failed"}})
        finally:
            progress_sink.reset(token)

    producer = asyncio.create_task(produce())
    try:
        while True:
            try:
                frame = await asyncio.wait_for(queue.get(), heartbeat_seconds)
            except TimeoutError:
                frame = {"type": "heartbeat"}
            yield json.dumps(frame, ensure_ascii=False, separators=(",", ":")) + "\n"
            if frame["type"] in ("result", "error"):
                return
    finally:
        producer.cancel()
        # Starlette may cancel its entire AnyIO task group on disconnect.
        with anyio.CancelScope(shield=True):
            await asyncio.gather(producer, return_exceptions=True)


class ResearchStream(StreamingResponse):
    async def __call__(self, scope, receive, send):
        try:
            await super().__call__(scope, receive, send)
        finally:
            # ASGI >=2.4 may fail send() while the generator is suspended at yield.
            # Close it explicitly so that the producer cannot outlive its caller.
            with anyio.CancelScope(shield=True):
                await self.body_iterator.aclose()
