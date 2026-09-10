"""Vector/budget isolation, admin revoke-before-write, and group-management denial."""
from __future__ import annotations

import json

import pytest
from bot.config import Settings
from bot.handlers import group
from clawguard_native.host import NativeHost
from test_native_behaviors import ScriptedBroker, response, settings, tool
from test_commands_cleanup import CommandBroker
from test_native_host import event


@pytest.mark.asyncio
async def test_source_context_budget_is_consumed_not_capped_at_12000(tmp_path):
    seen = []

    def capture(payload):
        seen.append(payload)
        return response("预算已接通")

    s = settings()
    s.bot.max_context_tokens = 256000
    s.bot.max_output_tokens = 2048
    broker = ScriptedBroker([capture])
    host = NativeHost(tmp_path, broker, s)
    try:
        scope = await host.scope(-123, 0, "background")
        assert scope.llm.max_context_tokens == 256000
        await host.event(event())
        await group.flush_pending_inbound_batches()
        assert seen and seen[0]["context_tokens"] == 256000
        assert seen[0]["max_tokens"] == s.bot.main_model.max_tokens
    finally:
        await host.close()


@pytest.mark.asyncio
async def test_memory_manage_tool_rechecks_admin_immediately_before_write(tmp_path):
    def admin_event(message_id, text):
        payload = event(message_id, text=text)
        payload["is_admin"] = True
        payload["is_owner"] = True
        return payload

    intent = response(json.dumps({"intent": "memory_manage", "memory_action": "add", "memory_content": "管理员永久原文"}, ensure_ascii=False))
    class AdminBroker(CommandBroker):
        async def call(self, op, payload, *, scope=None):
            if op == "telegram" and payload["method"] == "getChatMember":
                self.calls.append((op, payload))
                return {"status": "administrator", "user": {"id": int(payload["data"]["user_id"]), "is_bot": False, "first_name": "管理员"}}
            if op == "model":
                if payload.get("tools"):
                    self.responses.append(response(calls=[tool("memory_manage", {"request_text": "请记住管理员永久原文"})]))
                else:
                    self.responses.append(intent)
            return await super().call(op, payload, scope=scope)

    broker = AdminBroker([])
    host = NativeHost(tmp_path, broker, settings())
    try:
        broker.admin = False
        await host.event(admin_event(1, "@cg_test_bot 请记住管理员永久原文"))
        await group.flush_pending_inbound_batches()
        scope = host.scopes[(-123, 0)]
        assert await scope.memory.list_permanent_memories(-123, limit=20) == []
        assert any(op == "authorize-memory" for op, _ in broker.calls)
        broker.admin = True
        await host.event(admin_event(2, "@cg_test_bot 请记住管理员永久原文"))
        await group.flush_pending_inbound_batches()
        memories = await scope.memory.list_permanent_memories(-123, limit=20)
        assert len(memories) == 1 and memories[0].content == "管理员永久原文"
    finally:
        await host.close()


@pytest.mark.asyncio
async def test_native_host_rejects_moderation_skills_and_does_not_register_them(tmp_path):
    from bot.services.skills.service import SkillService

    s = Settings(_env_file=None)
    names = set(SkillService(None, settings=s).available_skill_names())
    assert "memory_manage" in names
    assert "wiki_query" in names
    assert "rule_manage" not in names
    assert "vote_ban" not in names
    broker = CommandBroker()
    host = NativeHost(tmp_path, broker, settings())
    try:
        with pytest.raises(PermissionError, match="not owned"):
            from test_commands_cleanup import callback
            await host.callback(callback("vote_ban:start"))
    finally:
        await host.close()
