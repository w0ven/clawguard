import httpx
import pytest

from clawguard_native import database_pool
from clawguard_native.app import create_app
from test_native_host import FakeBroker


@pytest.mark.asyncio
async def test_health_checks_thread_headroom_and_real_database(tmp_path, monkeypatch):
    app = create_app(data=tmp_path, broker=FakeBroker(), secret="local-fixture-" * 4)
    host = app.state.host
    try:
        await host.scope(-123, 0, "fixture-grant")
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://native.test") as client:
            response = await client.get("/healthz")
            assert response.status_code == 200
            assert response.json()["resources"]["database_readable"] is True
            real_snapshot = database_pool.resource_snapshot

            def exhausted():
                return {**real_snapshot(), "pids_current": 256, "pids_limit": 256, "thread_headroom_ok": False}

            monkeypatch.setattr(database_pool, "resource_snapshot", exhausted)
            response = await client.get("/healthz")
            assert response.status_code == 503
            assert response.json()["ok"] is False
    finally:
        await host.close()


@pytest.mark.asyncio
async def test_health_detects_database_starvation_without_leaking_waiter(tmp_path):
    import asyncio

    app = create_app(data=tmp_path, broker=FakeBroker(), secret="local-fixture-" * 4)
    host = app.state.host
    try:
        scope = await host.scope(-123, 0, "fixture-grant")
        # Stop the independent cleanup reader so the only waiter in this
        # scenario is the health probe whose cancellation we are testing.
        await scope.cleanup.stop(timeout_seconds=1)
        budget = database_pool.ConnectionBudget(limit=1)
        setattr(asyncio.get_running_loop(), database_pool._BUDGET_ATTRIBUTE, budget)
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://native.test") as client:
            async with scope.engine.connect():
                response = await client.get("/healthz")
                assert response.status_code == 503
                assert response.json()["resources"]["database_readable"] is False
                assert budget.waiting == 0
            response = await client.get("/healthz")
            assert response.status_code == 200
    finally:
        await host.close()
