from datetime import datetime, timezone

import pytest
from sqlalchemy import select

from bot.config import Settings
from bot.db.models import GroupMessageArchive
from bot.handlers import group
from clawguard_native.host import NativeHost


class FakeBroker:
    def __init__(self):
        self.calls = []
        self.next_id = 900

    async def call(self, operation, payload, *, scope=None):
        self.calls.append((operation,payload))
        if operation == "finish":
            return True
        if operation == "turn-status":
            return {"consumed":False}
        if operation == "model":
            return {"content":"固定回答", "response":{"choices":[{"index":0,
                "message":{"role":"assistant","content":"固定回答"},"finish_reason":"stop"}]}}
        if operation == "embedding":
            texts = payload.get("texts") or [""]
            return {"vectors": [[0.1, 0.2, 0.3] for _ in texts], "space_id": "fixture-space", "model": "fixture-embed", "dimensions": 3}
        if operation == "download":
            import base64
            return {"base64": base64.b64encode(b"\x89PNG\r\n\x1a\nfixture-image-bytes").decode()}
        if operation == "telegram":
            method,data = payload["method"],payload["data"]
            if method == "getChatMember":
                return {"status":"member","user":{"id":int(data["user_id"]),"is_bot":False,"first_name":"成员"}}
            if method == "getFile":
                return {"file_id":data["file_id"],"file_unique_id":"uniq","file_path":"fixture-media","file_size":24}
            if method in {"sendChatAction","deleteMessage"}:
                return True
            self.next_id += 1
            return {"message_id":self.next_id,"date":int(datetime.now(timezone.utc).timestamp()),
                    "chat":{"id":scope.group_id if scope else -123,"type":"supergroup","title":"隔离群"},
                    "message_thread_id":scope.topic_id if scope and scope.topic_id else None,
                    "from":{"id":99,"is_bot":True,"first_name":"ClawGuard","username":"cg_test_bot"},
                    "text":data.get("text","")}
        raise AssertionError(operation)

    async def close(self):
        pass


def event(message_id=1, *, topic=0, text="@cg_test_bot 你好"):
    return {"group_id":-123,"topic_id":topic,"grant":"opaque-test-grant","background_grant":"opaque-background",
            "turn":f"test:{message_id}","revision":str(message_id),"approved":True,"chat_enabled":True,
            "bot_user":{"id":99,"is_bot":True,"first_name":"ClawGuard","username":"cg_test_bot"},
            "message":{"message_id":message_id,"date":int(datetime.now(timezone.utc).timestamp()),
                "chat":{"id":-123,"type":"supergroup","title":"隔离群"},
                "from":{"id":7,"is_bot":False,"first_name":"成员"},
                "message_thread_id":topic or None,"text":text}}


@pytest.mark.asyncio
async def test_real_pending_memory_reply(tmp_path):
    broker = FakeBroker()
    settings = Settings(_env_file=None)
    settings.bot.inbound_debounce_seconds = 0
    host = NativeHost(tmp_path,broker,settings)
    try:
        result = await host.event(event())
        assert result["queued"] == 1
        await group.flush_pending_inbound_batches()
        models = [payload for op,payload in broker.calls if op == "model"]
        sends = [payload for op,payload in broker.calls if op == "telegram" and payload["method"] == "sendMessage"]
        assert models, broker.calls
        assert sends and sends[-1]["data"]["text"] == "固定回答", broker.calls
        assert "casual" not in [m["role"] for m in models]
        scope = host.scopes[(-123,0)]
        async with scope.sessions() as session:
            rows = (await session.execute(select(GroupMessageArchive))).scalars().all()
            assert any(row.telegram_message_id == 1 and "你好" in row.content for row in rows)
            assert any(row.role == "assistant" and "固定回答" in row.content for row in rows)
    finally:
        await host.close()
