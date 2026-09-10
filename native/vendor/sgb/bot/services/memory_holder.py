"""Global memory service holder — initialized at startup, imported by handlers."""
from __future__ import annotations

from typing import TYPE_CHECKING
from contextvars import ContextVar

if TYPE_CHECKING:
    from bot.services.memory import MemoryService

_instance: MemoryService | None = None
# CG boundary: one source service/database per group/topic, selected by host.
_scoped_instance: ContextVar[MemoryService | None] = ContextVar("native_memory", default=None)


def bind(mem: MemoryService):
    return _scoped_instance.set(mem)


def init(mem: MemoryService) -> None:
    global _instance
    _instance = mem


def get() -> MemoryService:
    instance = _scoped_instance.get() or _instance
    assert instance is not None, "MemoryService not initialized"
    return instance


def get_optional() -> MemoryService | None:
    """Return the service when startup completed, else ``None`` for dry handlers/tests."""

    return _scoped_instance.get() or _instance
