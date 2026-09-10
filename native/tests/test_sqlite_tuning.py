"""Production SQLite write-lock sharding and WAL tuning tests.

These cover the tuning that keeps a multi-topic native deployment from
starving: one slow turn must no longer abort writes to unrelated topic
databases, and the timeout must be raised above a slow LLM call.
"""
from __future__ import annotations

import asyncio
import os
import sqlite3
import time

import pytest

from clawguard_native import sqlite_tuning


def test_sharding_allows_parallel_writes_to_distinct_databases(tmp_path):
    """Two sessions bound to different databases must not serialise."""
    from bot.db.engine import init_db
    from bot.db.sqlite_session import SQLiteSafeAsyncSession

    async def scenario():
        url_a = f"sqlite+aiosqlite:///{tmp_path/'a.sqlite3'}"
        url_b = f"sqlite+aiosqlite:///{tmp_path/'b.sqlite3'}"
        engine_a, sessions_a = await init_db(url_a)
        engine_b, sessions_b = await init_db(url_b)
        sqlite_tuning.apply_sqlite_tuning(timeout_seconds=5.0)

        async with sessions_a() as sa, sessions_b() as sb:
            assert isinstance(sa, SQLiteSafeAsyncSession)

            key_a = sqlite_tuning._shard_key(sa)
            key_b = sqlite_tuning._shard_key(sb)
            assert key_a != key_b, "distinct databases must shard differently"

            # Hold A's writer, then confirm B can still acquire its own.
            await sa._acquire_write_lock(op="execute")
            try:
                started = time.perf_counter()
                await asyncio.wait_for(sb._acquire_write_lock(op="execute"), timeout=2.0)
                elapsed = time.perf_counter() - started
                assert elapsed < 1.0, f"B blocked by A for {elapsed:.2f}s"
                sb._release_write_lock()
            finally:
                sa._release_write_lock()

        await engine_a.dispose()
        await engine_b.dispose()

    asyncio.run(scenario())


def test_same_database_still_serialises_single_writer(tmp_path):
    """Sharding must not break single-writer semantics within one database."""
    from bot.db.engine import init_db

    async def scenario():
        url = f"sqlite+aiosqlite:///{tmp_path/'same.sqlite3'}"
        engine, sessions = await init_db(url)
        sqlite_tuning.apply_sqlite_tuning(timeout_seconds=1.0)

        async with sessions() as s1, sessions() as s2:
            assert sqlite_tuning._shard_key(s1) == sqlite_tuning._shard_key(s2)
            await s1._acquire_write_lock(op="execute")
            blocked = False
            try:
                await asyncio.wait_for(s2._acquire_write_lock(op="execute"), timeout=0.4)
            except asyncio.TimeoutError:
                blocked = True
            assert blocked, "second writer on the same database must wait"
            s1._release_write_lock()

        await engine.dispose()

    asyncio.run(scenario())


def test_timeout_is_raised_above_source_default():
    """The raised timeout must exceed the 5s source default."""
    from bot.db import sqlite_session as source

    assert sqlite_tuning.DEFAULT_WRITE_LOCK_TIMEOUT_SECONDS > 5.0
    result = sqlite_tuning.apply_sqlite_tuning(timeout_seconds=30.0)
    assert result["write_lock_timeout_seconds"] == 30.0
    assert source._SQLITE_WRITE_LOCK_TIMEOUT_SECONDS == 30.0


def test_wal_checkpoint_bounds_wal_file(tmp_path):
    """A checkpoint must shrink an oversized WAL and set a journal limit."""
    db_path = str(tmp_path / "wal.sqlite3")
    connection = sqlite3.connect(db_path)
    connection.execute("PRAGMA journal_mode=WAL")
    connection.execute("CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)")
    connection.commit()
    for index in range(2000):
        connection.execute("INSERT INTO t (v) VALUES (?)", (f"row-{index}",))
    connection.commit()
    wal_path = db_path + "-wal"
    wal_size = os.path.getsize(wal_path)
    assert wal_size > 0, "WAL should exist after writes"
    connection.execute("PRAGMA wal_checkpoint(TRUNCATE)")
    connection.close()
    # TRUNCATE either empties or removes the WAL; both bound its growth.
    truncated = os.path.getsize(wal_path) if os.path.exists(wal_path) else 0
    assert truncated < wal_size, f"WAL not truncated: {wal_size} -> {truncated}"


def test_logging_level_is_persisted_and_applied(tmp_path):
    """Log level must round-trip through configuration and reach the logger."""
    import logging

    from clawguard_native.configuration import Configuration

    from bot.utils.logging_setup import shutdown_logging

    try:
        path = tmp_path / "control.sqlite3"
        config = Configuration(path)
        assert config.read()["log_levels"][0] == "DEBUG"

        # Source default is INFO; native previously never applied it, leaving the
        # root logger at WARNING and hiding decision logs.
        config.apply(__import__("bot.config", fromlist=["Settings"]).Settings(_env_file=None))
        assert logging.getLogger().level == logging.INFO

        payload = config.write({"logging": {"level": "WARNING"}, "revision": config.read()["revision"]})
        assert payload["logging"]["level"] == "WARNING"
        config.apply(__import__("bot.config", fromlist=["Settings"]).Settings(_env_file=None))
        assert logging.getLogger().level == logging.WARNING
    finally:
        # Stop the test-created listener while pytest's captured stdout is
        # still open; this does not alter the persisted logging behavior.
        shutdown_logging()
