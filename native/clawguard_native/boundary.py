"""Only connection/identity seams; the SGB business services remain the execution owner.

A scope is (real Telegram group, topic). Each has its own native SQLite memory
and vector domain, keeping SGB's real `group:telegram_message_id` keys intact.
No bot token, provider key, arbitrary URL or moderation operation crosses here.
"""
from __future__ import annotations

import base64
from contextvars import Context, ContextVar
from dataclasses import dataclass
from typing import Any

import aiohttp
from aiogram.client.session.base import BaseSession
from aiogram.exceptions import TelegramNetworkError
from .purpose import purpose
from aiogram.types import InputFile
from litellm import ModelResponse

from bot.services.llm import LLMService as SourceLLM, EmbeddingBatchResult


@dataclass
class Execution:
    broker: "Broker"
    group_id: int
    topic_id: int
    grant: str
    turn: str = ""


execution: ContextVar[Execution] = ContextVar("cg_assistant_execution")


def bind_message(message: Any) -> None:
    old = execution.get()
    extra = message.model_extra or {}
    execution.set(Execution(old.broker, old.group_id, old.topic_id,
                            str(extra["cg_grant"]), str(extra["cg_turn"])))


def native_context() -> Context:
    # SGB intentionally starts detached workers with an empty Context. Carry
    # only our transport/data scope, not a stale source update-completion owner.
    from bot.services import memory_holder
    context = Context()
    current = execution.get(None)
    if current is not None:
        context.run(execution.set,current)
    memory = memory_holder.get_optional()
    if memory is not None:
        context.run(memory_holder.bind,memory)
    context.run(purpose.set,purpose.get())
    return context


async def prepare_batch(message: Any) -> None:
    bind_message(message)
    from bot.services import memory_holder
    refresh = getattr(memory_holder.get(), "cg_refresh_settings", None)
    if refresh is not None:
        await refresh()


class Broker:
    def __init__(self, url: str, secret: str):
        if not secret or len(secret) < 32:
            raise ValueError("native broker secret must contain at least 32 characters")
        self.url = url.rstrip("/")
        self.secret = secret
        self.http: aiohttp.ClientSession | None = None

    async def call(self, operation: str, payload: dict, *, scope: Execution | None = None) -> Any:
        ctx = scope or execution.get(None)
        if operation in {"bootstrap","migration-check"}:ctx=None
        if ctx is None and operation not in {"bootstrap","migration-check"}:
            raise PermissionError("missing server-bound scope")
        if self.http is None:
            self.http = aiohttp.ClientSession(timeout=aiohttp.ClientTimeout(total=120))
        body = ({"group_id": ctx.group_id, "topic_id": ctx.topic_id,
                 "grant": ctx.grant, "turn": ctx.turn, "payload": payload} if ctx else payload)
        # Exactly one HTTP attempt. Neither TG nor a model/tool side effect is
        # replayed here. The CG model lease owns permitted provider fallback.
        async with self.http.post(f"{self.url}/{operation}", json=body,
                                  headers={"Authorization": f"Bearer {self.secret}"}) as response:
            if response.status != 200:
                raise RuntimeError(f"assistant broker {operation}: HTTP {response.status}")
            data = await response.json()
            if not data.get("ok"):
                raise RuntimeError(str(data.get("error", "broker unavailable")))
            return data["result"]

    async def close(self):
        if self.http is not None:
            await self.http.close()
            self.http = None


class LLMService(SourceLLM):
    """Retain source message builders/budgeting, replace ONLY provider dispatch.

    In particular chat/decision/generate/compress/vision_describe run their
    original implementations. Tool-loop pinning is owned by CG, not LiteLLM.
    """
    def __init__(self, *args, bound: Execution | None = None, **kwargs):
        super().__init__(*args, **kwargs)
        self.bound = bound
        self.embedding_space = ""

    def _scope(self) -> Execution:
        return execution.get(None) or self.bound  # type: ignore[return-value]

    async def _request(self, messages, *, label, cfg, tools=None):
        if label == "moderation":
            raise PermissionError("native assistant cannot dispatch moderation")
        scope = self._scope()
        if scope is None:
            raise PermissionError("missing server-bound assistant scope")
        return await scope.broker.call("model", {
            "role": "chat" if label in {"main", "skill"} else label,
            "messages": messages, "tools": tools or [],
            "max_tokens": cfg.max_tokens, "temperature": cfg.temperature,
            "timeout_sec": cfg.timeout_sec,
            "context_tokens": self.max_context_tokens,
        }, scope=scope)

    async def _chat_with_fallbacks(self, *, messages, candidates, label, preview_limit=80):
        if not candidates:
            return ""
        try:
            result = await self._request(messages, label=label, cfg=candidates[0])
            return str(result.get("content") or "")
        except (aiohttp.ClientError, TimeoutError, RuntimeError):
            return ""

    async def complete_with_tools(self, *, messages, tools, label="skill", cfg=None, preview_limit=80):
        try:
            result = await self._request(messages, label=label, cfg=cfg or self.main, tools=tools)
            return ModelResponse(**result["response"])
        except (aiohttp.ClientError, TimeoutError, RuntimeError):
            return None

    def primary_embedding_space_id(self):
        return self.embedding_space

    async def embed_primary_with_space(self, texts, *, total_deadline_sec=None):
        scope = self._scope()
        try:
            result = await scope.broker.call("embedding", {"texts": texts,
                "space_id": self.embedding_space, "timeout_sec": total_deadline_sec}, scope=scope)
            if not result or not result.get("vectors"):
                return None
            self.embedding_space = result["space_id"]
            return EmbeddingBatchResult(vectors=result["vectors"], space_id=result["space_id"],
                                        model=result["model"], dimensions=result["dimensions"])
        except (aiohttp.ClientError, TimeoutError, RuntimeError):
            return None

    async def embed(self, texts):
        result = await self.embed_primary_with_space(texts)
        return result.vectors if result else []


# The Go broker repeats this allowlist and enforces actual message ownership,
# current eligibility, scope, admin identity and issued media references.
TG_METHODS = frozenset({
    "getMe", "getChatMember", "getFile", "sendMessage", "sendRichMessage",
    "editMessageText", "editMessageReplyMarkup", "deleteMessage", "sendChatAction",
    "sendSticker", "sendVoice", "sendAudio", "sendPhoto", "sendDocument",
    "sendVideo", "sendAnimation", "sendMediaGroup", "answerCallbackQuery",
})


class BrokerSession(BaseSession):
    def __init__(self, scope: Execution):
        super().__init__()
        self.scope = scope

    async def close(self):
        pass  # Broker is application-owned, shared and closed once at shutdown.

    async def make_request(self, bot, method, timeout=None):
        name = method.__api_method__
        if name not in TG_METHODS:
            raise PermissionError(f"native Telegram method denied: {name}")
        ctx = execution.get(None) or self.scope
        files: dict[str, InputFile] = {}
        data = {}
        for key, value in method.model_dump(warnings=False).items():
            prepared = self.prepare_value(value, bot=bot, files=files)
            if prepared is not None:
                data[key] = prepared
        attachments = {}
        for key, file in files.items():
            chunks = bytearray()
            async for part in file.read(bot):
                chunks.extend(part)
                if len(chunks) > 20 * 1024 * 1024:
                    raise ValueError("native media upload exceeds 20 MiB")
            attachments[key] = {"filename": file.filename or "media",
                                "base64": base64.b64encode(chunks).decode()}
        try:
            result = await ctx.broker.call("telegram", {"method": name, "data": data,
                "files": attachments,"purpose":purpose.get()}, scope=ctx)
        except (aiohttp.ClientError,TimeoutError,RuntimeError) as exc:
            raise TelegramNetworkError(method=method,message="CG broker unavailable; delivery may be uncertain") from exc
        import json
        envelope = result["telegram_error"] if isinstance(result,dict) and "telegram_error" in result else {"ok":True,"result":result}
        response = self.check_response(bot=bot, method=method, status_code=int(envelope.get("error_code",200)),
                                       content=json.dumps(envelope))
        return response.result

    async def stream_content(self, url, headers=None, timeout=30, chunk_size=65536, raise_for_status=True):
        # Never fetch an aiogram-generated URL (it contains the dummy token).
        # getFile's file_path is a scope-bound handle issued by the broker.
        handle = url.split("/file/bot", 1)[-1].split("/", 1)[-1]
        result = await self.scope.broker.call("download", {"handle": handle}, scope=self.scope)
        data = base64.b64decode(result["base64"], validate=True)
        for offset in range(0, len(data), chunk_size):
            yield data[offset:offset + chunk_size]
