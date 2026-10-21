"""Request-local progress channel; ordinary JSON/CLI calls incur no I/O."""
from contextvars import ContextVar
from collections.abc import Awaitable, Callable

ProgressSink = Callable[[dict], Awaitable[None]]
progress_sink: ContextVar[ProgressSink | None] = ContextVar("progress_sink", default=None)


async def emit_progress(stage: str, completed: int, total: int) -> None:
    sink = progress_sink.get()
    if sink is not None:
        await sink({"stage": stage, "completed": completed, "total": total})
