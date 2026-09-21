"""Bound SQLite worker threads across every native assistant topic.

aiosqlite owns a thread for each open connection. The upstream per-database
queue pool is appropriate for one database, but retaining it for every topic
exhausts the container's PID limit. Native uses short-lived connections and a
shared, cancellation-safe budget instead; vendored database code is unchanged.
"""
from __future__ import annotations

import asyncio
import functools
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from sqlalchemy.engine import make_url
from sqlalchemy.exc import TimeoutError as PoolTimeoutError
from sqlalchemy.pool import NullPool
from sqlalchemy.util.concurrency import await_only

MAX_CONNECTIONS = 32
ACQUIRE_TIMEOUT_SECONDS = 5.0
_SLOT_KEY = "clawguard_native_connection_budget"


@dataclass
class ConnectionBudget:
    limit: int = MAX_CONNECTIONS
    active: int = 0
    waiting: int = 0
    peak: int = 0
    semaphore: asyncio.Semaphore = field(init=False)

    def __post_init__(self) -> None:
        self.semaphore = asyncio.Semaphore(self.limit)

    async def acquire(self) -> None:
        self.waiting += 1
        try:
            async with asyncio.timeout(ACQUIRE_TIMEOUT_SECONDS):
                await self.semaphore.acquire()
        except TimeoutError as exc:
            raise PoolTimeoutError("native SQLite connection budget exhausted") from exc
        finally:
            self.waiting -= 1
        self.active += 1
        self.peak = max(self.peak, self.active)

    def release(self) -> None:
        self.active -= 1
        self.semaphore.release()


_BUDGET_ATTRIBUTE = "_clawguard_native_connection_budget"


def connection_budget() -> ConnectionBudget:
    loop = asyncio.get_running_loop()
    budget = getattr(loop, _BUDGET_ATTRIBUTE, None)
    if budget is None:
        budget = ConnectionBudget()
        setattr(loop, _BUDGET_ATTRIBUTE, budget)
    return budget


class NativeSQLitePool(NullPool):
    """Close returned connections, with one shared budget for all topic pools."""

    _is_asyncio = True

    def _do_get(self):
        budget = connection_budget()
        await_only(budget.acquire())
        try:
            record = self._create_connection()
        except BaseException:
            budget.release()
            raise
        # record_info survives connection invalidation/reconnect, unlike info.
        record.record_info[_SLOT_KEY] = budget
        return record

    def _do_return_conn(self, record):
        try:
            super()._do_return_conn(record)
        finally:
            budget = record.record_info.pop(_SLOT_KEY, None)
            if budget is not None:
                budget.release()


def install_native_pool() -> None:
    """Adapt the source engine factory before any scope database is opened."""
    from bot.db import engine as source

    factory = source.create_async_engine
    if getattr(factory, "_clawguard_native_pool", False):
        return

    @functools.wraps(factory)
    def create_engine(url, **kwargs):
        parsed = make_url(url)
        if parsed.drivername == "sqlite+aiosqlite" and parsed.database not in {None, "", ":memory:"}:
            for key in ("pool_size", "max_overflow", "pool_timeout", "pool_use_lifo"):
                kwargs.pop(key, None)
            kwargs["poolclass"] = NativeSQLitePool
        return factory(url, **kwargs)

    create_engine._clawguard_native_pool = True
    source.create_async_engine = create_engine


def resource_snapshot() -> dict[str, Any]:
    budget = connection_budget()
    result: dict[str, Any] = {
        "sqlite_connections": budget.active,
        "sqlite_connection_limit": budget.limit,
        "sqlite_waiters": budget.waiting,
        "sqlite_connection_peak": budget.peak,
        "thread_headroom_ok": True,
    }
    for root in (Path("/sys/fs/cgroup"), Path("/sys/fs/cgroup/pids")):
        try:
            current = int((root / "pids.current").read_text().strip())
            maximum = (root / "pids.max").read_text().strip()
        except (OSError, ValueError):
            continue
        result["pids_current"] = current
        if maximum != "max":
            try:
                limit = int(maximum)
            except ValueError:
                break
            result["pids_limit"] = limit
            result["thread_headroom_ok"] = current < limit - min(16, max(1, limit // 8))
        break
    return result
