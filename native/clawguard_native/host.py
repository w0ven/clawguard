"""Assistant-only entry. No Dispatcher, polling, webhook or SGB __main__.

CG submits only approved events, plus ordered invalidations. Source helpers
perform enrichment; source pending worker, services and delivery own the turn.
"""
from __future__ import annotations

import asyncio
import hashlib
import json
import os
from contextvars import Context
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any

from aiogram import Bot
from aiogram.client.default import DefaultBotProperties
from aiogram.types import CallbackQuery, Message, User
from sqlalchemy import delete, select, text

from bot.config import Settings
from bot.db.engine import init_db
from bot.db.models import Group, GroupMessageArchive, GroupContextSummary, MessageVector, AuthorizedGroup, GroupApiModelQuerySecret
from bot.handlers import group as source
from bot.services import memory_holder
from bot.services.archive_vector import SQLiteArchiveVectorRecallProvider
from .memory import MemoryService
from bot.services.proactive import note_group_activity
from .proactive import ProactiveTopicService
from bot.services.speech_style import SpeechStyleService
from bot.services.sticker_library import sticker_library
from bot.services.update_completion import UpdateCompletionReceipt, bind_update_completion, reset_update_completion
from bot.services.telegram_cleanup import TelegramCleanupScheduler
from bot.utils.telegram import configure_telegram_cleanup_scheduler
from .cleanup import ScopedCleanup
from bot.utils.bot_identity import set_bot_identity
from bot.utils.telegram import extract_message_text, has_explicit_bot_mention, is_bot_mentioned, is_reply_message, is_reply_to_bot, mentions_other_user

from .boundary import Broker, BrokerSession, Execution, LLMService, execution


@dataclass
class Scope:
    group_id: int
    topic_id: int
    engine: Any
    sessions: Any
    memory: MemoryService
    llm: LLMService
    bot: Bot
    settings: Settings
    cleanup: TelegramCleanupScheduler
    grant: str
    maintenance: list[asyncio.Task]


class NativeHost:
    def __init__(self, data: Path, broker: Broker, settings: Settings | None = None):
        from .migration import domain_lock
        self._domain_lock = domain_lock(data)
        self._domain_lock.__enter__()
        self.activation_required = False
        try:
            manifest_path = data/"migration-manifest.json"
            if manifest_path.exists():
                manifest = json.loads(manifest_path.read_text())
                if manifest.get("state") != "prepared":
                    raise RuntimeError("native migration is incomplete; resume import before starting")
                if not manifest.get("activated_at"):
                    self.activation_required = bool(manifest.get("production"))
                    if not self.activation_required:
                        manifest["activated_at"] = datetime.now().astimezone().isoformat()
                        manifest_path.write_text(json.dumps(manifest,ensure_ascii=False)+"\n")
            elif os.environ.get("NATIVE_REQUIRE_MIGRATION") == "true":
                raise RuntimeError("native migration manifest required; refusing an empty production domain")
        except BaseException:
            self._domain_lock.__exit__(None,None,None)
            raise
        self.data = data
        self.broker = broker
        # Never read a deployment .env containing CG's provider/TG credentials.
        self.settings = settings or Settings(_env_file=None)
        self.settings.moderation.enabled = False
        self.scopes: dict[tuple[int,int], Scope] = {}
        self.scope_lock = asyncio.Lock()
        self.closing = False
        self.completions: dict[str, asyncio.Task] = {}
        self.input_locks: dict[tuple[int,int], asyncio.Lock] = {}
        configure_telegram_cleanup_scheduler(ScopedCleanup())

    async def activate(self):
        from .sqlite_tuning import apply_sqlite_tuning
        self._sqlite_tuning = apply_sqlite_tuning()
        if not self.activation_required:return
        path=self.data/"migration-manifest.json"
        manifest=json.loads(path.read_text())
        await self.broker.call("migration-check",{"profiles":manifest["profiles"]})
        manifest["activated_at"]=datetime.now().astimezone().isoformat()
        temporary=self.data/".activation-manifest.next"
        temporary.write_text(json.dumps(manifest,ensure_ascii=False)+"\n");temporary.chmod(0o600)
        temporary.replace(path)
        self.activation_required=False

    async def scope(self, group_id: int, topic_id: int, background_grant: str) -> Scope:
        if self.activation_required:raise RuntimeError("latest migration profile verification required before activation")
        key = (group_id,topic_id)
        async with self.scope_lock:
            if key in self.scopes:
                scope = self.scopes[key]
                scope.grant = background_grant
                scope.bot.session.scope.grant = background_grant
                return scope
            if group_id >= 0 or topic_id < 0:
                raise ValueError("invalid group/topic")
            path = self.data / "scopes" / str(group_id) / f"{topic_id}.sqlite3"
            engine, sessions = await init_db(f"sqlite+aiosqlite:///{path}")
            ctx = Execution(self.broker,group_id,topic_id,background_grant)
            settings = self.settings.model_copy(deep=True)
            llm = LLMService(settings.bot.main_model,settings.bot.decision_model,
                settings.bot.compress_model,vision=settings.bot.vision_model,
                embed=settings.bot.embed_model,max_context_tokens=settings.bot.max_context_tokens,bound=ctx)
            vectors = SQLiteArchiveVectorRecallProvider(session_factory=sessions,llm=llm,
                retention_days=self.settings.bot.memory_retention_days)
            memory = MemoryService(self.settings.bot,llm,session_factory=sessions,vector_recall_provider=vectors)
            bot = Bot("1:NATIVE_BROKER_NO_TELEGRAM_TOKEN",session=BrokerSession(ctx),
                      default=DefaultBotProperties(parse_mode=self.settings.bot.parse_mode))
            cleanup = TelegramCleanupScheduler(bot=bot,session_factory=sessions)
            memory.cg_cleanup = cleanup
            scope = Scope(group_id,topic_id,engine,sessions,memory,llm,bot,settings,cleanup,background_grant,[])
            async with engine.begin() as conn:
                await conn.execute(text("""CREATE TABLE IF NOT EXISTS cg_source_state (
                    message_id INTEGER PRIMARY KEY, revision TEXT NOT NULL,
                    valid INTEGER NOT NULL, forgotten INTEGER NOT NULL DEFAULT 0,
                    was_archived INTEGER NOT NULL DEFAULT 0)"""))
                columns = (await conn.execute(text("PRAGMA table_info(cg_source_state)"))).all()
                if not any(row[1] == "was_archived" for row in columns):
                    await conn.execute(text("ALTER TABLE cg_source_state ADD COLUMN was_archived INTEGER NOT NULL DEFAULT 0"))
                    await conn.execute(text("UPDATE cg_source_state SET was_archived=1 WHERE EXISTS (SELECT 1 FROM group_message_archive a WHERE a.telegram_message_id=cg_source_state.message_id)"))
                await conn.execute(text("CREATE TABLE IF NOT EXISTS cg_turns (turn TEXT PRIMARY KEY, status TEXT NOT NULL)"))
            async with sessions() as session:
                if await session.get(Group,group_id) is None:
                    session.add(Group(id=group_id,settings={}))
                await session.commit()
            await memory.bootstrap()
            try:
                # Source scheduler spawns its worker here. Do not inherit the
                # previous HTTP event's actor/turn; use this Bot's bound scope.
                await asyncio.create_task(cleanup.start(),context=Context())
            except BaseException:
                await cleanup.stop(timeout_seconds=1)
                await memory.shutdown()
                await engine.dispose()
                raise
            self.scopes[key] = scope
            memory.cg_refresh_settings = lambda: self.sync_topic_settings(scope)
            return scope

    def bind(self, scope: Scope, grant: str | None = None, turn: str = ""):
        memory_holder.bind(scope.memory)
        execution.set(Execution(self.broker,scope.group_id,scope.topic_id,grant,turn) if grant else scope.bot.session.scope)

    async def configure_group(self, group_id: int, *, background_grant: str, values: dict, revision: int, operator_id: int = 0):
        from bot.services.group_settings import acquire_group_settings_write_intent
        from bot.services.speech_style import set_style_target
        scope = await self.scope(group_id,0,background_grant)
        allowed = {"at_reply_mode", "tts_mode", "api_model_query", "mute_all_replies",
                   "mimic_target", "cooldown_topic"}
        if set(values) - allowed:
            raise ValueError("unsupported assistant group settings")
        for key in ("at_reply_mode","mute_all_replies"):
            if key in values and not isinstance(values[key],bool):
                raise ValueError(key+" must be boolean")
        if "tts_mode" in values and values["tts_mode"] not in {"off","on","always"}:
            raise ValueError("invalid TTS mode")
        async with scope.sessions() as session:
            await acquire_group_settings_write_intent(session,group_id)
            row = await session.get(Group,group_id,populate_existing=True)
            current = dict(row.settings or {})
            if int(current.get("cg_config_revision",0)) != revision:
                raise ValueError("group configuration changed; refresh before saving")
            if "api_model_query" in values:
                from .group_config import update_api_settings
                current=await update_api_settings(session,current,values["api_model_query"],group_id=group_id,operator_id=operator_id,master_key=scope.settings.config_master_key)
            if "mimic_target" in values:
                target = values["mimic_target"]
                user_id = int(target["user_id"])
                if user_id < 0:
                    raise ValueError("invalid mimic target")
                # This is an explicit retarget request; source resets its
                # counters/profile. Unrelated saves never write speech_style.
                current = set_style_target(current,user_id=user_id,user_name=str(target.get("user_name", "")))
            if "cooldown_topic" in values:
                update = values["cooldown_topic"]
                if set(update)-{"enabled","task_brief"} or ("enabled" in update and not isinstance(update["enabled"],bool)):
                    raise ValueError("invalid proactive settings")
                if len(str(update.get("task_brief",""))) > 2000:
                    raise ValueError("proactive brief exceeds 2000 characters")
                tasks = dict(current.get("scheduled_tasks") or {})
                tasks["cooldown_topic"] = {**dict(tasks.get("cooldown_topic") or {}),**update}
                current["scheduled_tasks"] = tasks
            current.update({key:value for key,value in values.items() if key not in {"mimic_target","cooldown_topic","api_model_query"}})
            current["cg_config_revision"] = revision+1
            row.settings = current
            await session.commit()
        return current

    async def sync_topic_settings(self, scope: Scope):
        if not scope.topic_id:
            return
        primary = self.scopes.get((scope.group_id,0))
        if primary is None:
            raise RuntimeError("primary group settings scope is not initialized")
        async with primary.sessions() as session:
            row = await session.get(Group,scope.group_id)
            values = dict(row.settings or {})
            secret=await session.get(GroupApiModelQuerySecret,scope.group_id)
            projected={"ciphertext":secret.ciphertext,"updated_by":secret.updated_by,"updated_at":secret.updated_at} if secret else None
        async with scope.sessions() as session:
            row = await session.get(Group,scope.group_id)
            row.settings = values
            secret=await session.get(GroupApiModelQuerySecret,scope.group_id)
            if projected:
                if secret is None:session.add(GroupApiModelQuerySecret(group_id=scope.group_id,**projected))
                else:
                    for key,value in projected.items():setattr(secret,key,value)
            elif secret is not None:await session.delete(secret)
            await session.commit()

    async def event(self, event: dict) -> dict:
        key = (int(event["group_id"]),int(event.get("topic_id",0)))
        lock = self.input_locks.setdefault(key,asyncio.Lock())
        async with lock:
            token = bind_update_completion(None)
            transport_token = execution.set(None)
            memory_token = memory_holder.bind(None)
            try:
                return await self._event(event)
            finally:
                reset_update_completion(token)
                execution.reset(transport_token)
                memory_holder._scoped_instance.reset(memory_token)

    async def _event(self, event: dict) -> dict:
        if self.closing:
            raise RuntimeError("native assistant is shutting down")
        group_id, topic_id = int(event["group_id"]),int(event.get("topic_id",0))
        primary = await self.scope(group_id,0,event.get("group_background_grant",event["background_grant"]))
        scope = await self.scope(group_id,topic_id,event["background_grant"])
        self.bind(scope,event["grant"],event["turn"])
        self.apply_scope_models(scope,event.get("models",{}))
        await self.sync_topic_settings(scope)
        raw = dict(event["message"])
        if int(raw["chat"]["id"]) != group_id or int(raw.get("message_thread_id") or 0) != topic_id:
            raise PermissionError("event scope mismatch")
        message_id = int(raw["message_id"])
        revision = str(event["revision"])
        async with scope.sessions() as session:
            old = (await session.execute(text("SELECT * FROM cg_source_state WHERE message_id=:id"),{"id":message_id})).mappings().first()
        retry = bool(old and old["revision"] == revision)
        if old and old["forgotten"]:
            return {"accepted":True,"duplicate":True,"done":True}
        if retry:
            async with scope.sessions() as session:
                status = (await session.execute(text("SELECT status FROM cg_turns WHERE turn=:turn"),{"turn":event["turn"]})).scalar_one_or_none()
            active = self.completions.get(event["turn"])
            if status == "done" or (active is not None and not active.done()):
                return {"accepted":True,"duplicate":True,"done":status == "done"}
            # After a crash consult CG's durable delivery ledger before retry.
            consumed = await self.broker.call("turn-status",{},scope=execution.get())
            if consumed["consumed"]:
                await self._finish_event(scope,event,True)
                return {"accepted":True,"duplicate":True,"done":True}
        if event.get("kind") in {"invalidate","forget","edit"}:
            await self.invalidate(scope,message_id,revision,forgotten=event["kind"] == "forget")
            if event["kind"] != "edit" or (not event.get("known_origin") and (not old or not old["was_archived"])):
                return {"accepted":True,"invalidated":True,"done":True}
        elif not event.get("approved"):
            raise PermissionError("assistant accepts approved events only")
        # Reply data is supplied by CG only if its original source is still
        # approved and in this exact topic. Native never fetches arbitrary history.
        reply = raw.get("reply_to_message")
        if reply and (int(reply["chat"]["id"]) != group_id or int(reply.get("message_thread_id") or 0) != topic_id):
            raise PermissionError("cross-topic reply context")
        raw.update(cg_grant=event["grant"],cg_turn=event["turn"])
        message = Message.model_validate(raw,context={"bot":scope.bot})
        me = User.model_validate(event["bot_user"])
        scope.bot._me = me
        set_bot_identity(user_id=me.id,username=me.username or "",display_name=me.full_name)
        text_value,msg_type = extract_message_text(message)
        if not text_value:
            return {"accepted":True,"empty":True,"done":True}
        sender = source._resolve_sender_identity(message)
        owner,admin = bool(event.get("is_owner")),bool(event.get("is_admin"))
        if not event.get("chat_enabled"):
            # Independent mimic opt-in is not permission to archive the group.
            if not retry and msg_type == "text" and not sender.is_chat:
                async with primary.sessions() as session:
                    await SpeechStyleService(scope.llm).collect_sample(session,group_id=group_id,user_id=sender.actor_id,text=text_value)
                    await session.commit()
            async with scope.sessions() as session:
                await session.execute(text("INSERT INTO cg_source_state(message_id,revision,valid,was_archived) VALUES (:id,:rev,0,0) ON CONFLICT(message_id) DO UPDATE SET revision=:rev"),{"id":message_id,"rev":revision})
                await session.commit()
            await self._finish_event(scope,event,True)
            return {"accepted":True,"done":True,"style_only":True}
        command = (message.text or "").split(" ",1)[0].split("@",1)
        if command[0] == "/lm" and (len(command)==1 or command[1].lower()==(me.username or "").lower()):
            from bot.handlers.commands import cmd_lm
            command_settings=scope.settings.model_copy(deep=True)
            command_settings.super_admin_id=sender.actor_id if owner else 0
            async with scope.sessions() as session:
                await session.execute(text("INSERT INTO cg_source_state(message_id,revision,valid,was_archived) VALUES(:id,:rev,1,0) ON CONFLICT(message_id) DO UPDATE SET revision=:rev,valid=1"),{"id":message_id,"rev":revision})
                await session.commit()
                await cmd_lm(message,session,command_settings,scope.sessions)
            await self._finish_event(scope,event,True)
            return {"accepted":True,"done":True,"command":"lm"}
        receipt = UpdateCompletionReceipt()
        bind_update_completion(receipt)
        raw_bot_sender = bool(message.from_user and message.from_user.is_bot and not source._uses_sender_chat_identity(message))
        if event.get("kind") != "edit" and not retry:
            note_group_activity(group_id)
            async with primary.sessions() as session:
                await source._record_group_activity_cas(session,group_id=group_id,
                    title=message.chat.title or "",settings=self.settings,session_factory=primary.sessions)
        # Original source archive enrichment helpers run, AFTER CG approval.
        enriched,vision = text_value,""
        if event.get("kind") != "edit" and not raw_bot_sender and msg_type not in {"video","video_note"}:
            enriched,vision = await source._append_image_context(message,scope.llm,text_value,msg_type)
            reply_context = await source._build_reply_context_for_llm(message,scope.llm)
            if reply_context:
                enriched += "\n"+reply_context
        await scope.memory.archive_message(group_id,"user",enriched,message_id=str(message_id),
            created_at=message.date,message_type=msg_type,
            **source._message_archive_metadata(message,sender_identity=sender,raw_text=text_value,
                derived_text=vision,sender_is_owner=owner,sender_is_tg_admin=admin))
        async with scope.sessions() as session:
            await session.execute(text("""INSERT INTO cg_source_state(message_id,revision,valid,was_archived) VALUES (:id,:rev,1,1)
                ON CONFLICT(message_id) DO UPDATE SET revision=:rev,valid=1,was_archived=1"""),{"id":message_id,"rev":revision})
            if msg_type == "sticker":
                await sticker_library.learn_from_message(session,group_id,message,vision_description=vision)
            await session.commit()
        if event.get("kind") == "edit":
            # Source edits update retained data, never generate another reply.
            return {"accepted":True,"edited":True,"done":True}
        if raw_bot_sender or msg_type in {"video","video_note"}:
            await self._watch_receipt(scope,event,receipt)
            return {"accepted":True,"media_bypass":True}
        async with primary.sessions() as session:
            if not retry and msg_type == "text" and not sender.is_chat:
                await SpeechStyleService(scope.llm).collect_sample(session,group_id=group_id,
                    user_id=sender.actor_id,text=text_value)
                await session.commit()
        if msg_type == "video_caption":
            await self._watch_receipt(scope,event,receipt)
            return {"accepted":True,"media_bypass":True}
        display = source.re.sub(r"\s+"," ",sender.display_name).strip()[:160].replace("[","［").replace("]","］")
        user_tag = (f"id:{sender.actor_id} username:{'@'+sender.username if sender.username else '(none)'} "
                    f"is_owner:{'yes' if owner else 'no'} is_tg_admin:{'yes' if admin else 'no'} "
                    f"trusted_source:{'tg_admin' if admin else 'none'} name:{display}")
        memory_entry = ""
        if msg_type != "contact":
            memory_entry = f"[{user_tag}] {enriched}"
            await scope.memory.add_message(group_id,"user",memory_entry,user_id=sender.actor_id,
                sender_name=sender.display_name,message_type=msg_type,message_id=str(message_id),
                created_at=message.date,defer_persistence=True,persist_archive=False)
            source._schedule_memory_compaction(scope.memory,group_id)
        if event.get("keyword_replied"):
            await self._watch_receipt(scope,event,receipt)
            return {"accepted":True,"indexed":True}
        is_reply = is_reply_message(message)
        reply_bot = is_reply_to_bot(message,me.username or "",me.id)
        item = source._PendingReplyItem(message=message,group_id=group_id,user_id=sender.actor_id,
            input_text=enriched,msg_type=msg_type,sender_username=sender.username,
            sender_is_owner=owner,sender_is_tg_admin=admin,user_tag=user_tag,
            explicit_mention=has_explicit_bot_mention(message,me.username or "",me.id),
            mentioned=is_bot_mentioned(message,me.username or "",me.id),is_reply=is_reply,
            reply_to_bot=reply_bot,reply_to_other=is_reply and not reply_bot,
            mention_other=mentions_other_user(message,me.username or "",me.id),memory_entry=memory_entry)
        receipt.defer()
        item.update_completion = receipt
        await self._watch_receipt(scope,event,receipt)
        try:
            count,delay = await source._enqueue_pending_reply(item,scope.settings)
        except source._PendingReplyQueueFull:
            handled = not source._is_strong_pending_reply_signal(item)
            if not handled:
                try:
                    handled = bool(await source._await_hard_deadline(source.send_reply(message,"当前请求较多，请稍后再试。",delivery_mode="reply",
                        reply_to_message_id=message_id,stream=False,
                        disable_link_preview=self.settings.bot.disable_link_preview),timeout_seconds=8.0))
                except Exception:
                    handled = False
            receipt.finish(handled)
            if not handled:
                raise
            return {"accepted":True,"overloaded":True}
        except BaseException:
            receipt.finish(False)
            raise
        return {"accepted":True,"queued":count,"delay":delay}

    async def _watch_receipt(self,scope: Scope,event: dict,receipt: UpdateCompletionReceipt):
        async with scope.sessions() as session:
            await session.execute(text("INSERT INTO cg_turns VALUES (:turn,'queued') ON CONFLICT(turn) DO UPDATE SET status='queued'"),{"turn":event["turn"]})
            await session.commit()
        self.completions[event["turn"]] = asyncio.create_task(self._complete_receipt(scope,event,receipt))

    async def _complete_receipt(self,scope: Scope,event: dict,receipt: UpdateCompletionReceipt):
        succeeded = await receipt.wait()
        await self._finish_event(scope,event,succeeded)

    async def _finish_event(self,scope: Scope,event: dict,succeeded: bool):
        ctx = Execution(self.broker,scope.group_id,scope.topic_id,event["grant"],event["turn"])
        await self.broker.call("finish",{"succeeded":succeeded},scope=ctx)
        async with scope.sessions() as session:
            await session.execute(text("INSERT INTO cg_turns VALUES (:turn,:status) ON CONFLICT(turn) DO UPDATE SET status=:status"),
                {"turn":event["turn"],"status":"done" if succeeded else "retry"})
            await session.commit()

    async def invalidate(self, scope: Scope, message_id: int, revision: str, *, forgotten: bool):
        # Cancel the scope's in-flight generation before removing its inputs.
        # No "latest user message" gate: unrelated new speakers never cancel.
        tasks = []
        async with source._PENDING_REPLY_LOCK:
            for key,state in list(source._PENDING_REPLY_BATCHES.items()):
                if key[0] == scope.group_id and key[2] == scope.topic_id and state.task:
                    state.task.cancel()
                    tasks.append(state.task)
        compact = source._MEMORY_COMPACT_TASKS.get((id(scope.memory),scope.group_id))
        if compact is not None:
            compact.cancel()
            tasks.append(compact)
        if tasks:
            await asyncio.gather(*tasks,return_exceptions=True)
        if not await scope.memory.flush_pending_writes():
            raise RuntimeError("cannot invalidate until source write-behind drains")
        async with scope.sessions() as session:
            await session.execute(delete(GroupMessageArchive).where(GroupMessageArchive.group_id == scope.group_id,
                GroupMessageArchive.telegram_message_id == message_id))
            await session.execute(delete(MessageVector).where(MessageVector.group_id == scope.group_id,
                MessageVector.message_id == f"{scope.group_id}:{message_id}"))
            await session.execute(delete(GroupContextSummary).where(GroupContextSummary.group_id == scope.group_id))
            await session.execute(text("""INSERT INTO cg_source_state(message_id,revision,valid,forgotten) VALUES (:id,:rev,0,:forgot)
                ON CONFLICT(message_id) DO UPDATE SET revision=:rev,valid=0,forgotten=MAX(forgotten,:forgot)"""),
                {"id":message_id,"rev":revision,"forgot":int(forgotten)})
            await session.commit()
        scope.memory._history_loaded.discard(scope.group_id)
        scope.memory._summary_cache.pop(scope.group_id,None)
        await scope.memory._ensure_history_loaded(scope.group_id)

    def apply_scope_models(self,scope: Scope,models: dict):
        for role in ("main","decision","compress","vision","embed"):
            if role in models:
                model = getattr(scope.settings.bot,role+"_model")
                model.model = models[role]["model"]
                for field in ("max_tokens","temperature","timeout_sec"):
                    if field in models[role] and hasattr(model,field):setattr(model,field,models[role][field])
                if role == "embed":
                    scope.llm.embedding_space = models[role]["space_id"]
        scope.llm.reconfigure(scope.settings.bot.main_model,scope.settings.bot.decision_model,
            scope.settings.bot.compress_model,vision=scope.settings.bot.vision_model,
            embed=scope.settings.bot.embed_model,max_context_tokens=scope.settings.bot.max_context_tokens)

    async def refresh_bootstrap(self):
        values = await self.broker.call("bootstrap",{})
        for key,value in values.get("tts",{}).items():
            if key == "ready":
                self.settings._cg_tts_ready = bool(value)
            else:
                setattr(self.settings,"doubao_tts_"+key,value)
        user = User.model_validate(values["bot_user"])
        set_bot_identity(user_id=user.id,username=user.username or "",display_name=user.full_name)
        authorized = set()
        for record in values["scopes"]:
            group_id,topic_id = record["group_id"],record["topic_id"]
            scope = await self.scope(group_id,topic_id,record["background_grant"])
            authorized.add((group_id,topic_id))
            for key in self.settings.__class__.model_fields:
                if key.startswith("doubao_tts_"):
                    setattr(scope.settings,key,getattr(self.settings,key))
            scope.settings._cg_tts_ready = getattr(self.settings,"_cg_tts_ready",False)
            scope.bot._me = user
            self.apply_scope_models(scope,record["models"])
            async with scope.sessions() as session:
                row = await session.get(AuthorizedGroup,group_id)
                if row is None:
                    row = AuthorizedGroup(group_id=group_id,bot_present=record["chat_enabled"])
                    session.add(row)
                else:
                    row.bot_present = record["chat_enabled"]
                await session.commit()
            await self.start_background(scope)
        for key,scope in self.scopes.items():
            if key not in authorized:
                for task in scope.maintenance:
                    task.cancel()
                await asyncio.gather(*scope.maintenance,return_exceptions=True)
                scope.maintenance.clear()
                async with scope.sessions() as session:
                    row = await session.get(AuthorizedGroup,scope.group_id)
                    if row is not None:
                        row.bot_present = False
                        await session.commit()

    async def callback(self,event):
        transport_token=execution.set(None)
        memory_token=memory_holder.bind(None)
        try:return await self._callback(event)
        finally:
            execution.reset(transport_token)
            memory_holder._scoped_instance.reset(memory_token)

    async def _callback(self,event):
        from bot.handlers.commands import on_memory_list_paging,on_memory_delete
        from bot.utils.telegram import DELETE_BUTTON_CALLBACK_DATA
        group_id,topic_id=int(event["group_id"]),int(event.get("topic_id") or 0)
        scope=await self.scope(group_id,topic_id,event["background_grant"])
        raw=event["callback"]
        message=raw.get("message") or {}
        if int(message.get("chat",{}).get("id",0))!=group_id or int(message.get("message_thread_id") or 0)!=topic_id:
            raise PermissionError("callback scope mismatch")
        data=raw.get("data","")
        if not (data.startswith("lml:") or data.startswith("lmd:") or data==DELETE_BUTTON_CALLBACK_DATA):
            raise PermissionError("callback not owned by native assistant")
        self.bind(scope,event["grant"],event["turn"])
        callback=CallbackQuery.model_validate(raw,context={"bot":scope.bot})
        settings=scope.settings.model_copy(deep=True)
        settings.super_admin_id=callback.from_user.id if event.get("is_owner") else 0
        async with scope.sessions() as session:
            handler=on_memory_list_paging if data.startswith("lml:") else on_memory_delete if data.startswith("lmd:") else source.on_delete_button
            await handler(callback,settings,session)
        return {"done":True}

    async def bootstrap_loop(self):
        import logging
        while not self.closing:
            try:
                await self.refresh_bootstrap()
            except asyncio.CancelledError:
                raise
            except Exception:
                logging.getLogger(__name__).exception("native bootstrap refresh failed")
            await asyncio.sleep(30)

    async def start_background(self, scope: Scope):
        if scope.maintenance:
            return
        self.bind(scope)
        scope.memory._vector_recall_provider.start()
        scope.maintenance.append(asyncio.create_task(scope.memory.run_archive_maintenance()))
        if scope.topic_id == 0:
            proactive = ProactiveTopicService(settings=scope.settings,bot=scope.bot,memory=scope.memory,
                session_factory=scope.sessions,llm=scope.llm)
            scope.maintenance.append(asyncio.create_task(proactive.run_forever()))

    async def close(self):
        self.closing = True
        await source.flush_pending_inbound_batches()
        if self.completions:
            await asyncio.gather(*self.completions.values(),return_exceptions=True)
        for scope in self.scopes.values():
            for task in scope.maintenance:
                task.cancel()
            await asyncio.gather(*scope.maintenance,return_exceptions=True)
            await scope.cleanup.stop()
            await scope.memory.shutdown()
            await scope.engine.dispose()
        configure_telegram_cleanup_scheduler(None)
        await self.broker.close()
        self._domain_lock.__exit__(None,None,None)
