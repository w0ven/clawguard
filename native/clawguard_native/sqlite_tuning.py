"""Production SQLite write-lock tuning for the native assistant host.

Source Smart_Group_Bot keeps one write lock per event loop plus a 5 second
acquisition timeout. That is correct for one small database, but a deployment
that serves many topic databases from a single loop starves: one slow turn
(an LLM call inside a pending-reply batch) holds the only writer slot and
every unrelated topic's write aborts after exactly 5 seconds, losing the
message instead of queueing it.

This module re-tunes runtime behaviour without editing vendored source:

1. Timeout is raised so an ordinary slow turn no longer aborts others.
2. The writer slot is sharded per database file, so unrelated topics no
   longer serialise against each other. Within one database the source
   semantics are preserved: priority ordering and a single writer.
3. A periodic WAL checkpoint bounds write-ahead logs, because production
   reports ``journal_size_limit=-1`` (unlimited) on open scope databases.

Only methods on the source session class are rebound at runtime. Source files
stay byte identical, preserving the "identical to SGB source" requirement.
"""
from __future__ import annotations

import asyncio
import logging
import os
import time
from typing import Any

from sqlalchemy.exc import OperationalError

log = logging.getLogger(__name__)

DEFAULT_WRITE_LOCK_TIMEOUT_SECONDS = 30.0
DEFAULT_WAL_CHECKPOINT_INTERVAL_SECONDS = 300.0
DEFAULT_WAL_AUTOCKPOINT_PAGES = 512
DEFAULT_JOURNAL_SIZE_LIMIT_BYTES = 16 * 1024 * 1024

_APPLIED = False


def _env_float(name: str, default: float) -> float:
    try:
        value = float(os.environ.get(name, ""))
    except (TypeError, ValueError):
        return default
    return value if value > 0 else default


def _shard_key(session: Any) -> str:
    """Identity of the database a session writes to."""
    try:
        bind = session.sync_session.get_bind()
    except Exception:
        bind = getattr(getattr(session, "sync_session", None), "bind", None)
    database = getattr(getattr(bind, "url", None), "database", None)
    return str(database) if database else "shared"


def _loop_buckets() -> tuple[dict[str, Any], dict[str, dict[str, Any]]]:
    """Per-loop sharded lock and owner registries."""
    loop = asyncio.get_running_loop()
    lock_key = "_cg_native_sqlite_write_locks"
    owner_key = "_cg_native_sqlite_write_lock_owners"
    locks = getattr(loop, lock_key, None)
    owners = getattr(loop, owner_key, None)
    if locks is None:
        locks = {}
        setattr(loop, lock_key, locks)
    if owners is None:
        owners = {}
        setattr(loop, owner_key, owners)
    return locks, owners


def apply_sqlite_tuning(*, timeout_seconds: float | None = None) -> dict[str, Any]:
    """Shard the source write lock per database and raise its timeout."""
    global _APPLIED
    from bot.db import sqlite_session as source

    timeout = (
        _env_float("NATIVE_SQLITE_WRITE_LOCK_TIMEOUT", DEFAULT_WRITE_LOCK_TIMEOUT_SECONDS)
        if timeout_seconds is None
        else float(timeout_seconds)
    )

    # Keep the module constant in sync: the source embeds it in the error
    # message, and other readers may surface it in health snapshots.
    source._SQLITE_WRITE_LOCK_TIMEOUT_SECONDS = timeout

    if _APPLIED:
        return {
            "write_lock_timeout_seconds": timeout,
            "lock_sharding": "per-database",
            "already_applied": True,
        }

    lock_cls = source._PrioritySQLiteWriteLock
    info_seconds = source._SQLITE_WRITE_LOCK_INFO_SECONDS
    warn_seconds = source._SQLITE_WRITE_LOCK_WARN_SECONDS

    async def _acquire_write_lock(self, *, op: str) -> None:
        if not self._uses_sqlite() or self._sqlite_write_lock_held:
            return
        locks, owners = _loop_buckets()
        shard = _shard_key(self)
        lock = locks.get(shard)
        if lock is None:
            lock = lock_cls()
            locks[shard] = lock
        started = time.perf_counter()
        try:
            async with asyncio.timeout(timeout):
                await lock.acquire()
        except TimeoutError as exc:
            owner = owners.get(shard) or {}
            held_for_ms = 0
            acquired_at = owner.get("acquired_at")
            if isinstance(acquired_at, (int, float)):
                held_for_ms = max(0, int((time.perf_counter() - acquired_at) * 1000))
            log.error(
                "sqlite write lock timeout | shard=%s op=%s wait_ms=%d "
                "owner_session=%s owner_op=%s held_ms=%d",
                shard,
                op,
                int((time.perf_counter() - started) * 1000),
                owner.get("session_id", "unknown"),
                owner.get("op", "unknown"),
                held_for_ms,
            )
            raise OperationalError(
                "sqlite write lock acquire",
                None,
                TimeoutError(f"sqlite write lock timeout after {timeout:.1f}s"),
            ) from exc
        self._sqlite_write_lock_held = lock
        owners[shard] = {
            "session_id": id(self),
            "op": op,
            "acquired_at": time.perf_counter(),
            "priority": source.current_execution_priority().name.lower(),
        }
        waited_ms = int((time.perf_counter() - started) * 1000)
        if waited_ms >= int(warn_seconds * 1000):
            log.warning("sqlite write serialized | shard=%s op=%s wait_ms=%d", shard, op, waited_ms)
        elif waited_ms >= int(info_seconds * 1000):
            log.info("sqlite write serialized | shard=%s op=%s wait_ms=%d", shard, op, waited_ms)

    def _release_write_lock(self) -> None:
        if not self._sqlite_write_lock_held:
            return
        lock = self._sqlite_write_lock_held
        self._sqlite_write_lock_held = None
        _, owners = _loop_buckets()
        shard = _shard_key(self)
        owner = owners.get(shard)
        if owner and owner.get("session_id") == id(self):
            acquired_at = owner.get("acquired_at")
            held_ms = 0
            if isinstance(acquired_at, (int, float)):
                held_ms = max(0, int((time.perf_counter() - acquired_at) * 1000))
            if held_ms >= int(warn_seconds * 1000):
                log.warning(
                    "sqlite write lock released | shard=%s session=%s op=%s held_ms=%d",
                    shard, id(self), owner.get("op", "unknown"), held_ms,
                )
            elif held_ms >= int(info_seconds * 1000):
                log.info(
                    "sqlite write lock released | shard=%s session=%s op=%s held_ms=%d",
                    shard, id(self), owner.get("op", "unknown"), held_ms,
                )
            owners.pop(shard, None)
        lock.release()

    source.SQLiteSafeAsyncSession._acquire_write_lock = _acquire_write_lock
    source.SQLiteSafeAsyncSession._release_write_lock = _release_write_lock
    _APPLIED = True

    result = {
        "write_lock_timeout_seconds": timeout,
        "lock_sharding": "per-database",
    }
    log.info("sqlite tuning applied | %s", result)
    return result


async def run_wal_checkpoint_loop(
    scopes: Any,
    *,
    interval_seconds: float | None = None,
) -> None:
    """Periodically bound and truncate the WAL of every open scope database."""
    from sqlalchemy import text

    interval = (
        _env_float("NATIVE_WAL_CHECKPOINT_INTERVAL", DEFAULT_WAL_CHECKPOINT_INTERVAL_SECONDS)
        if interval_seconds is None
        else float(interval_seconds)
    )
    while True:
        await asyncio.sleep(interval)
        try:
            items = list(scopes.values())
        except Exception:
            items = []
        for scope in items:
            engine = getattr(scope, "engine", None)
            if engine is None:
                continue
            try:
                async with engine.begin() as conn:
                    await conn.execute(
                        text(f"PRAGMA wal_autocheckpoint={DEFAULT_WAL_AUTOCKPOINT_PAGES}")
                    )
                    await conn.execute(
                        text(f"PRAGMA journal_size_limit={DEFAULT_JOURNAL_SIZE_LIMIT_BYTES}")
                    )
                    await conn.execute(text("PRAGMA wal_checkpoint(TRUNCATE)"))
            except asyncio.CancelledError:
                raise
            except Exception:
                log.warning(
                    "wal checkpoint failed | group=%s topic=%s",
                    getattr(scope, "group_id", "?"),
                    getattr(scope, "topic_id", "?"),
                    exc_info=True,
                )
