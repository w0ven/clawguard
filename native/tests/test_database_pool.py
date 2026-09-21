"""Regression coverage for the production many-topic thread exhaustion."""
import asyncio
import threading

import pytest
from sqlalchemy import text
from sqlalchemy.exc import OperationalError

from clawguard_native import database_pool


def sqlite_workers():
    return sum(
        getattr(getattr(thread, "_target", None), "__name__", "") == "_connection_worker_thread"
        for thread in threading.enumerate()
    )


@pytest.mark.asyncio
async def test_many_topic_databases_do_not_retain_workers(tmp_path):
    from bot.db import engine as source

    database_pool.install_native_pool()
    baseline = sqlite_workers()
    engines = [
        source.create_async_engine(f"sqlite+aiosqlite:///{tmp_path / f'{topic}.sqlite3'}")
        for topic in range(96)
    ]
    budget = database_pool.connection_budget()
    try:
        async def use_database(engine):
            async with engine.begin() as connection:
                await connection.execute(text("CREATE TABLE IF NOT EXISTS example (value INTEGER)"))
                await connection.execute(text("INSERT INTO example VALUES (7)"))
                assert (await connection.execute(text("SELECT value FROM example"))).scalar() == 7
                await asyncio.sleep(0.002)

        await asyncio.gather(*(use_database(engine) for engine in engines))
        assert budget.active == 0
        assert budget.waiting == 0
        assert budget.peak <= database_pool.MAX_CONNECTIONS
        # Every engine remains alive, as with bootstrap's topic cache. Threads
        # must still be released when sessions return their connections.
        await asyncio.sleep(0.05)
        assert sqlite_workers() <= baseline + 2
    finally:
        await asyncio.gather(*(engine.dispose() for engine in engines))


@pytest.mark.asyncio
async def test_cancelled_waiter_and_failed_connection_return_budget(tmp_path):
    from bot.db import engine as source

    database_pool.install_native_pool()
    budget = database_pool.ConnectionBudget(limit=1)
    setattr(asyncio.get_running_loop(), database_pool._BUDGET_ATTRIBUTE, budget)
    engine = source.create_async_engine(f"sqlite+aiosqlite:///{tmp_path / 'one.sqlite3'}")
    failed = source.create_async_engine(f"sqlite+aiosqlite:///{tmp_path / 'absent' / 'fail.sqlite3'}")
    try:
        async with engine.connect() as first:
            async def wait_for_connection():
                async with engine.connect():
                    pytest.fail("second connection escaped the shared budget")

            waiter = asyncio.create_task(wait_for_connection())
            async with asyncio.timeout(2):
                while budget.waiting == 0:
                    await asyncio.sleep(0)
            waiter.cancel()
            with pytest.raises(asyncio.CancelledError):
                await waiter
            assert budget.active == 1
            assert budget.waiting == 0
            # Invalidating a connection must not lose the pool's ownership slot.
            await first.invalidate()
        assert budget.active == 0
        with pytest.raises(OperationalError):
            async with failed.connect():
                pass
        assert budget.active == 0
        async with engine.connect() as connection:
            assert (await connection.execute(text("SELECT 1"))).scalar() == 1
        assert budget.active == 0
    finally:
        await engine.dispose()
        await failed.dispose()


@pytest.mark.asyncio
async def test_scope_initialization_failure_releases_resources(tmp_path, monkeypatch):
    from bot.config import Settings
    from clawguard_native.host import NativeHost
    from clawguard_native.memory import MemoryService
    from test_native_host import FakeBroker

    async def fail_bootstrap(self):
        raise RuntimeError("injected scope bootstrap failure")

    stopped = []
    shutdown = MemoryService.shutdown

    async def record_shutdown(self):
        stopped.append(self)
        await shutdown(self)

    monkeypatch.setattr(MemoryService, "bootstrap", fail_bootstrap)
    monkeypatch.setattr(MemoryService, "shutdown", record_shutdown)
    host = NativeHost(tmp_path, FakeBroker(), Settings(_env_file=None))
    try:
        with pytest.raises(RuntimeError, match="injected scope bootstrap failure"):
            await host.scope(-123, 1, "fixture-grant")
        assert not host.scopes
        assert stopped, "failed scope must shut down its partially initialized memory service"
        assert database_pool.connection_budget().active == 0
    finally:
        await host.close()
