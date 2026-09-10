"""Host inbound media, replied media, and source pending queue capacity/serial."""
from __future__ import annotations

import asyncio
import json

import pytest
from sqlalchemy import select

from bot.db.models import GroupMessageArchive
from bot.handlers import group
from clawguard_native.host import NativeHost
from test_native_behaviors import ScriptedBroker, response, settings
from test_native_host import event


def photo_event(message_id=1, *, caption="@cg_test_bot 这张图怎么配", file_id="photo-current", reply=None, text=None):
    payload = event(message_id, text=caption)
    message = payload["message"]
    if text is None:
        message.pop("text", None)
        message["caption"] = caption
        message["caption_entities"] = [{"type": "mention", "offset": 0, "length": len("@cg_test_bot")}]
    else:
        message["text"] = text
    message["photo"] = [{"file_id": file_id, "file_unique_id": "uniq-" + file_id, "width": 64, "height": 64, "file_size": 24}]
    if reply:
        message["reply_to_message"] = reply
    return payload


def voice_event(message_id=3):
    payload = event(message_id, text="@cg_test_bot")
    message = payload["message"]
    message.pop("text", None)
    message["voice"] = {"file_id": "voice-1", "file_unique_id": "uniq-voice", "duration": 2, "mime_type": "audio/ogg", "file_size": 12}
    message["reply_to_message"] = {
        "message_id": 900, "date": 1,
        "chat": {"id": -123, "type": "supergroup", "title": "隔离群"},
        "from": {"id": 99, "is_bot": True, "first_name": "ClawGuard", "username": "cg_test_bot"},
        "text": "上一句",
    }
    return payload


@pytest.mark.asyncio
async def test_inbound_photo_runs_source_vision_then_pending_reply(tmp_path):
    roles = []

    def vision_then_chat(payload):
        roles.append(payload.get("role"))
        if payload.get("role") == "vision":
            content = payload["messages"][0]["content"]
            assert any(part.get("type") == "image_url" for part in content)
            return response("出口准备图")
        return response("按图说明 nftables")

    broker = ScriptedBroker([vision_then_chat, vision_then_chat])
    host = NativeHost(tmp_path, broker, settings())
    try:
        result = await host.event(photo_event())
        assert result.get("queued") == 1
        await group.flush_pending_inbound_batches()
        assert roles[0] == "vision"
        sends = [v for op, v in broker.calls if op == "telegram" and v["method"] == "sendMessage"]
        assert sends and "nftables" in sends[-1]["data"]["text"]
        files = [v for op, v in broker.calls if op == "telegram" and v["method"] == "getFile"]
        assert files and files[0]["data"]["file_id"] == "photo-current"
        assert any(op == "download" for op, _ in broker.calls)
        scope = host.scopes[(-123, 0)]
        async with scope.sessions() as session:
            rows = (await session.execute(select(GroupMessageArchive))).scalars().all()
            assert any(row.telegram_message_id == 1 and "出口准备图" in row.content and "[image-vision]" in row.content for row in rows)
    finally:
        await host.close()


@pytest.mark.asyncio
async def test_replied_photo_is_enriched_for_current_text_turn(tmp_path):
    roles = []

    def script(payload):
        roles.append(payload.get("role"))
        if payload.get("role") == "vision":
            return response("被回复的旧图")
        dumped = json.dumps(payload["messages"], ensure_ascii=False)
        assert "被回复的旧图" in dumped
        return response("按旧图继续")

    broker = ScriptedBroker([script, script])
    host = NativeHost(tmp_path, broker, settings())
    try:
        reply = {
            "message_id": 9,
            "date": 1,
            "chat": {"id": -123, "type": "supergroup", "title": "隔离群"},
            "from": {"id": 8, "is_bot": False, "first_name": "前人"},
            "photo": [{"file_id": "photo-replied", "file_unique_id": "uniq-old", "width": 32, "height": 32, "file_size": 24}],
            "caption": "旧图",
        }
        asked = event(2, text="@cg_test_bot 这个呢")
        asked["message"]["reply_to_message"] = reply
        await host.event(asked)
        await group.flush_pending_inbound_batches()
        assert "vision" in roles
        files = [v for op, v in broker.calls if op == "telegram" and v["method"] == "getFile"]
        assert any(v["data"]["file_id"] == "photo-replied" for v in files)
        sends = [v for op, v in broker.calls if op == "telegram" and v["method"] == "sendMessage"]
        assert sends and "按旧图继续" in sends[-1]["data"]["text"]
    finally:
        await host.close()


@pytest.mark.asyncio
async def test_pure_voice_uses_source_placeholder_and_is_queued(tmp_path):
    broker = ScriptedBroker([response("收到语音占位")])
    host = NativeHost(tmp_path, broker, settings())
    try:
        result = await host.event(voice_event())
        assert result.get("queued") == 1
        await group.flush_pending_inbound_batches()
        models = [payload for op, payload in broker.calls if op == "model"]
        assert models
        dumped = json.dumps(models, ensure_ascii=False)
        assert "[voice]" in dumped
        sends = [v for op, v in broker.calls if op == "telegram" and v["method"] == "sendMessage"]
        assert sends and sends[-1]["data"]["text"] == "收到语音占位"
    finally:
        await host.close()


@pytest.mark.asyncio
async def test_pending_queue_keeps_original_items_and_rejects_overflow(tmp_path, monkeypatch):
    monkeypatch.setattr(group, "_PENDING_REPLY_MAX_ITEMS_PER_SENDER", 2)
    monkeypatch.setattr(group, "_PENDING_REPLY_MAX_SENDERS", 2)
    s = settings()
    s.bot.inbound_debounce_seconds = 8
    broker = ScriptedBroker([response("合并后回答"), response("第二人")])
    host = NativeHost(tmp_path, broker, s)
    try:
        first = await host.event(event(1, text="@cg_test_bot 一"))
        assert first["queued"] == 1
        second = await host.event(event(2, text="@cg_test_bot 二"))
        assert second["queued"] == 2
        third = await host.event(event(3, text="@cg_test_bot 三"))
        assert third.get("overloaded") is True
        key = group._pending_batch_key(-123, 7, 0)
        async with group._PENDING_REPLY_LOCK:
            items = list(group._PENDING_REPLY_BATCHES[key].items)
        assert [item.message.message_id for item in items] == [1, 2]
        other = event(4, text="@cg_test_bot 别人")
        other["message"]["from"]["id"] = 8
        other_result = await host.event(other)
        assert other_result["queued"] == 1
        await group.flush_pending_inbound_batches()
        texts = [v["data"]["text"] for op, v in broker.calls if op == "telegram" and v["method"] == "sendMessage"]
        assert "合并后回答" in texts and "第二人" in texts
        assert not any("三" in text for text in texts)
    finally:
        await host.close()


@pytest.mark.asyncio
async def test_same_sender_second_batch_waits_for_in_flight_turn(tmp_path):
    order = []
    first_started = asyncio.Event()
    release_first = asyncio.Event()

    async def first_hold(_payload):
        order.append("first-start")
        first_started.set()
        await release_first.wait()
        order.append("first-end")
        return response("A完成")

    broker = ScriptedBroker([first_hold, response("A2完成")])
    host = NativeHost(tmp_path, broker, settings())
    try:
        await host.event(event(1, text="@cg_test_bot A1"))
        await asyncio.wait_for(first_started.wait(), 2)
        await host.event(event(2, text="@cg_test_bot A2"))
        await asyncio.sleep(0.2)
        assert order == ["first-start"]
        release_first.set()
        await group.flush_pending_inbound_batches()
        assert order == ["first-start", "first-end"]
        texts = [v["data"]["text"] for op, v in broker.calls if op == "telegram" and v["method"] == "sendMessage"]
        assert texts == ["A完成", "A2完成"] or set(texts) == {"A完成", "A2完成"}
    finally:
        release_first.set()
        await host.close()
