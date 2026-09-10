"""Repeatable offline PG -> native import; never clears PG or invokes a model.

Production export/verify requires the new CG application in explicit paused
mode. Import requires the native unit stopped, so live native truth is never
overwritten by a legacy snapshot. Every original export is retained verbatim.
"""
from __future__ import annotations

import argparse
import asyncio
from collections import Counter
from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import tempfile
from urllib.parse import urlparse

import aiohttp
import asyncpg
from sqlalchemy import delete, select, text, update

from bot.config import Settings
from bot.db.engine import init_db
from bot.db.models import (Group, GroupMessageArchive, MessageVector, GroupPermanentMemory,
                           SpeechStyleSample, StickerLibraryRecord)
from bot.services.llm import LLMService
from bot.services.memory import MemoryService
from bot.utils.security import clean_multiline_text
from bot.utils.timezone import to_shanghai_naive

from . import SOURCE_COMMIT
from .configuration import Configuration
from .wiki import WikiStore

TARGETS = [-1002699516772,-1003974339921]
TABLES = {
    "groups": "SELECT chat_id,title,type,enabled FROM groups WHERE chat_id=ANY($1) ORDER BY chat_id",
    "authorized_groups": "SELECT * FROM authorized_groups WHERE chat_id=ANY($1) ORDER BY chat_id",
    "group_assistant_policies": "SELECT * FROM group_assistant_policies WHERE chat_id=ANY($1) ORDER BY chat_id",
    "group_assistant_pools": "SELECT * FROM group_assistant_pools WHERE chat_id=ANY($1) ORDER BY chat_id",
    "group_assistant_global_settings": "SELECT * FROM group_assistant_global_settings ORDER BY id",
    "group_assistant_prompt_overrides": "SELECT * FROM group_assistant_prompt_overrides WHERE chat_id=ANY($1) OR chat_id=0 ORDER BY chat_id,prompt_key",
    "group_assistant_messages": "SELECT * FROM group_assistant_messages WHERE chat_id=ANY($1) ORDER BY created_at,id",
    "group_assistant_memories": "SELECT * FROM group_assistant_memories WHERE chat_id=ANY($1) ORDER BY id",
    "group_assistant_memory_versions": "SELECT v.* FROM group_assistant_memory_versions v JOIN group_assistant_memories m ON m.id=v.memory_id WHERE m.chat_id=ANY($1) ORDER BY v.id",
    "group_assistant_conflicts": "SELECT * FROM group_assistant_conflicts WHERE chat_id=ANY($1) ORDER BY id",
    "group_assistant_style_samples": "SELECT * FROM group_assistant_style_samples WHERE chat_id=ANY($1) ORDER BY created_at,id",
    "group_assistant_sticker_samples": "SELECT * FROM group_assistant_sticker_samples WHERE chat_id=ANY($1) ORDER BY id",
}


def packed(value):
    return json.dumps(value,ensure_ascii=False,sort_keys=True,separators=(",",":"))


def digest(value):
    return hashlib.sha256(packed(value).encode()).hexdigest()


def utc(value):
    if not value:
        return None
    parsed = datetime.fromisoformat(str(value).replace("Z","+00:00"))
    return parsed.replace(tzinfo=timezone.utc) if parsed.tzinfo is None else parsed.astimezone(timezone.utc)


def local(value):
    parsed = utc(value)
    return to_shanghai_naive(parsed) if parsed else None


@contextmanager
def domain_lock(data: Path):
    data.mkdir(parents=True,exist_ok=True)
    descriptor = os.open(data/".native-domain.lock",os.O_CREAT|os.O_RDWR,0o600)
    try:
        try:
            fcntl.flock(descriptor,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise RuntimeError("native unit is running; stop ONLY that unit before importing") from exc
        yield
    finally:
        os.close(descriptor)


async def require_paused(control_url,secret):
    if not control_url or len(secret)<32:
        raise ValueError("production export requires CG control URL and broker authentication")
    async with aiohttp.ClientSession(timeout=aiohttp.ClientTimeout(total=10)) as http:
        async with http.post(control_url.rstrip("/")+"/state",json={},headers={"Authorization":"Bearer "+secret}) as response:
            if response.status!=200:
                raise RuntimeError("cannot verify CG assistant pause")
            value=await response.json()
            if value.get("engine")!="paused":
                raise RuntimeError("CG assistant must be explicitly paused before final snapshot")


def check_dsn(dsn,production):
    url=urlparse(dsn)
    if url.scheme not in {"postgres","postgresql"}:
        raise ValueError("PostgreSQL DSN required")
    if not production and (url.hostname!="127.0.0.1" or url.path not in {"/cg_native_test","/cg_native_migration_test"}):
        raise ValueError("refusing non-task database without explicit production flag")


async def export_snapshot(dsn: str,path: Path,*,targets=None,production=False,control_url="",secret=""):
    targets = list(TARGETS if targets is None else targets)
    check_dsn(dsn,production)
    if production:
        if sorted(targets)!=sorted(TARGETS):
            raise ValueError("production Wiki target groups must match the implementation contract")
        await require_paused(control_url,secret)
    connection=await asyncpg.connect(dsn,timeout=10)
    try:
        async with connection.transaction(isolation="repeatable_read",readonly=True):
            schema=await connection.fetchval("SELECT max(version_id) FROM goose_db_version WHERE is_applied")
            if schema is None or schema<35:
                raise ValueError("published assistant schema 35 or newer is required")
            groups=await connection.fetch("""SELECT a.chat_id FROM authorized_groups a
                LEFT JOIN group_assistant_policies p ON p.chat_id=a.chat_id
                WHERE a.enabled AND (p.chat_enabled OR p.learning_enabled OR p.mimic_target_user_id<>0 OR a.chat_id=ANY($1))
                ORDER BY a.chat_id""",targets)
            ids=[int(row["chat_id"]) for row in groups]
            if set(targets)-set(ids):
                raise ValueError("one or more target groups are not currently authorized")
            if production:
                current=await connection.fetchrow("SELECT version,mimic_target_user_id,mimic_profile_text FROM group_assistant_policies WHERE chat_id=-1002699516772")
                if current is None or current["version"]<10:
                    raise ValueError("refusing stale pre-distillation RFC policy; read the latest production state")
                if current["version"]==10 and (current["mimic_target_user_id"]!=258605875 or hashlib.sha256(current["mimic_profile_text"].encode()).hexdigest()!="ab5b669407c432cd937a46aa4ed494f2d40722d7c4c6b2e6f72b66499f68e212"):
                    raise ValueError("RFC v10 does not match the approved completed distillation")
            at=await connection.fetchval("SELECT transaction_timestamp()")
            header={"type":"header","format":1,"source_commit":SOURCE_COMMIT,"pg_schema":schema,
                    "exported_at":at.isoformat(),"groups":ids,"wiki_targets":targets,"production":production}
            path.parent.mkdir(parents=True,exist_ok=True)
            counts=Counter();hashing=hashlib.sha256()
            descriptor=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
            with os.fdopen(descriptor,"w",encoding="utf-8") as output:
                header_line=packed(header)+"\n"
                hashing.update(header_line.encode());output.write(header_line)
                for table,query in TABLES.items():
                    args=(ids,) if "$1" in query else ()
                    async for item in connection.cursor("SELECT row_to_json(t)::text AS row FROM ("+query+") t",*args,prefetch=200):
                        value={"type":"row","table":table,"row":json.loads(item["row"])}
                        line=packed(value)+"\n"
                        hashing.update(line.encode());output.write(line);counts[table]+=1
                trailer={"type":"manifest","data_sha256":hashing.hexdigest(),"counts":dict(counts)}
                output.write(packed(trailer)+"\n")
            if production:
                await require_paused(control_url,secret)
            return {**header,**trailer,"path":str(path)}
    finally:
        await connection.close()


def inspect_snapshot(path: Path):
    hashing=hashlib.sha256();counts=Counter();header=None;trailer=None
    with path.open(encoding="utf-8") as source:
        for line in source:
            value=json.loads(line)
            if trailer is not None:
                raise ValueError("data after snapshot trailer")
            if value["type"]=="header":
                if header is not None or counts:
                    raise ValueError("invalid snapshot header")
                header=value
                hashing.update(line.encode())
            elif value["type"]=="row":
                if header is None or value["table"] not in TABLES:
                    raise ValueError("unsupported snapshot record")
                hashing.update(line.encode());counts[value["table"]]+=1
            elif value["type"]=="manifest":
                trailer=value
            else:
                raise ValueError("unknown snapshot record")
    if header is None or trailer is None or header.get("source_commit")!=SOURCE_COMMIT:
        raise ValueError("incomplete or wrong-source snapshot")
    if hashing.hexdigest()!=trailer["data_sha256"] or dict(counts)!=trailer["counts"]:
        raise ValueError("snapshot hash/count mismatch")
    return header,trailer


def rows(path: Path,table: str):
    with path.open(encoding="utf-8") as source:
        for line in source:
            value=json.loads(line)
            if value.get("type")=="row" and value["table"]==table:
                yield value["row"]


async def verify_snapshot(dsn,path,*,production=False,control_url="",secret=""):
    header,trailer=inspect_snapshot(path)
    with tempfile.TemporaryDirectory(prefix="cg-native-verify-") as temp:
        current=await export_snapshot(dsn,Path(temp)/"current.jsonl",targets=header["wiki_targets"],
            production=production,control_url=control_url,secret=secret)
        # Transaction timestamps differ on every verification. Compare the
        # entire row stream and group/grant coverage rather than that timestamp.
        live_path=Path(temp)/"current.jsonl"
        def state_hash(source):
            h=hashlib.sha256()
            for line in source.read_text().splitlines():
                value=json.loads(line)
                if value["type"]=="row":h.update((line+"\n").encode())
            return h.hexdigest()
        if state_hash(live_path)!=state_hash(path) or current["groups"]!=header["groups"]:
            raise RuntimeError("legacy assistant data/config changed; export and import the latest paused snapshot")
    return {"verified":True,"data_sha256":trailer["data_sha256"]}


class ImportOnlyLLM(LLMService):
    async def _chat_with_fallbacks(self,**kwargs):
        raise RuntimeError("migration must not invoke an LLM")
    async def embed(self,*args,**kwargs):
        raise RuntimeError("migration must not invoke embeddings")


async def import_snapshot(path: Path,data: Path,*,wiki_bundle: Path):
    """Prepare off to the side; activate only a completely imported domain.

    A failed or interrupted preparation cannot leave untracked source rows in
    the next attempt. Old prepared files and exact PG snapshots are retained.
    """
    header,trailer=inspect_snapshot(path)
    batch=trailer["data_sha256"]
    with domain_lock(data):
        manifest_path=data/"migration-manifest.json"
        previous=json.loads(manifest_path.read_text()) if manifest_path.exists() else None
        if previous and previous.get("activated_at"):
            if previous["snapshot_sha256"]==batch:return {**previous,"unchanged":True}
            raise RuntimeError("native is authoritative now; refusing legacy snapshot overwrite")
        if previous and previous.get("state")=="prepared" and previous["snapshot_sha256"]==batch:
            return {**previous,"unchanged":True}
        if previous and utc(header["exported_at"])<utc(previous["exported_at"]):
            raise RuntimeError("refusing an older snapshot")
        if not previous and list((data/"scopes").glob("*/*.sqlite3")):
            raise RuntimeError("unowned native data exists; refusing to overwrite")
        if previous and previous.get("state")=="prepared":
            for group,profile in previous.get("profiles",{}).items():
                location=data/"scopes"/group/"0.sqlite3"
                if location.exists():
                    with sqlite3.connect(location) as db:
                        raw=db.execute('SELECT settings FROM groups WHERE id=?',(int(group),)).fetchone()
                    if raw and digest(json.loads(raw[0]).get("speech_style"))!=profile["state_sha256"]:
                        raise RuntimeError("native profile changed since preparation; refusing overwrite")
        snapshots=data/"migration"/"snapshots"
        snapshots.mkdir(parents=True,exist_ok=True)
        backup=snapshots/(batch+".jsonl")
        if not backup.exists():shutil.copyfile(path,backup);backup.chmod(0o600)
        manifest_path.write_text(packed({**(previous or {}),"state":"preparing","snapshot_sha256":batch,"exported_at":header["exported_at"]})+"\n")
        manifest_path.chmod(0o600)
        preparations=data/"migration"/"preparations"
        preparations.mkdir(parents=True,exist_ok=True)
        stage=Path(tempfile.mkdtemp(prefix=batch[:12]+"-",dir=preparations))
        for name in ("control.sqlite3","wiki.sqlite3"):
            original=data/name
            if original.exists():
                with sqlite3.connect(original) as source,sqlite3.connect(stage/name) as target:source.backup(target)
        forgotten_sources=set()
        for historical in snapshots.glob("*.jsonl"):
            for record in rows(historical,"group_assistant_memories"):
                if record.get("forgotten_at") and record.get("source_message_id"):
                    forgotten_sources.add((int(record.get("source_chat_id") or record["chat_id"]),int(record["source_message_id"])))
        result=await _build_import_snapshot(path,stage,wiki_bundle=wiki_bundle,forgotten_sources=forgotten_sources)
        result["production"]=bool(header.get("production"))
        retained=stage/"previous-preparation"
        retained.mkdir()
        for name in ("scopes","control.sqlite3","wiki.sqlite3"):
            original=data/name
            if original.exists():original.rename(retained/name)
            (stage/name).rename(original)
        # This is the only activation-readiness commit. A crash at any prior
        # point leaves `preparing`, so the host refuses to run partial truth.
        temporary=data/".migration-manifest.next"
        temporary.write_text(packed(result)+"\n");temporary.chmod(0o600)
        temporary.replace(manifest_path)
        return result


async def _build_import_snapshot(path: Path,data: Path,*,wiki_bundle: Path,forgotten_sources=()):
    header,trailer=inspect_snapshot(path)
    batch=trailer["data_sha256"]
    with domain_lock(data):
        manifest_path=data/"migration-manifest.json"
        previous=json.loads(manifest_path.read_text()) if manifest_path.exists() else None
        if previous and previous.get("activated_at"):
            if previous["snapshot_sha256"]==batch:
                return {**previous,"unchanged":True}
            raise RuntimeError("native is authoritative now; refusing legacy snapshot overwrite")
        if previous and previous["snapshot_sha256"]==batch and previous.get("state")=="prepared":
            return {**previous,"unchanged":True}
        if previous and utc(header["exported_at"])<utc(previous["exported_at"]):
            raise RuntimeError("refusing an older snapshot")
        if not previous and list((data/"scopes").glob("*/*.sqlite3")):
            raise RuntimeError("unowned native data exists; refusing to overwrite")
        backup=data/"migration"/"snapshots"/(batch+".jsonl")
        backup.parent.mkdir(parents=True,exist_ok=True)
        if not backup.exists():
            shutil.copyfile(path,backup);backup.chmod(0o600)
        manifest={"state":"preparing","source_commit":SOURCE_COMMIT,"snapshot_sha256":batch,
                  "exported_at":header["exported_at"],"groups":header["groups"],"profiles":dict((previous or {}).get("profiles",{})),"counts":{}}
        manifest_path.write_text(packed(manifest)+"\n");manifest_path.chmod(0o600)
        config=Configuration(data/"control.sqlite3")
        settings=Settings(_env_file=None)
        config.apply(settings)
        llm=ImportOnlyLLM(settings.bot.main_model,settings.bot.decision_model,settings.bot.compress_model)
        domains={}
        async def domain(group,topic=0):
            key=(int(group),int(topic or 0))
            if key in domains:return domains[key]
            location=data/"scopes"/str(key[0])/(str(key[1])+".sqlite3")
            engine,sessions=await init_db("sqlite+aiosqlite:///"+str(location))
            memory=MemoryService(settings.bot,llm,session_factory=sessions)
            async with engine.begin() as connection:
                await connection.execute(text("CREATE TABLE IF NOT EXISTS cg_migrated (kind TEXT NOT NULL,source_id TEXT NOT NULL,target TEXT NOT NULL,PRIMARY KEY(kind,source_id))"))
                await connection.execute(text("CREATE TABLE IF NOT EXISTS cg_source_state(message_id INTEGER PRIMARY KEY,revision TEXT NOT NULL,valid INTEGER NOT NULL,forgotten INTEGER NOT NULL DEFAULT 0,was_archived INTEGER NOT NULL DEFAULT 0)"))
            async with sessions() as session:
                old=(await session.execute(text("SELECT kind,target FROM cg_migrated"))).all()
                for kind,target in old:
                    if kind=="message":
                        await session.execute(delete(MessageVector).where(MessageVector.message_id==target))
                        await session.execute(delete(GroupMessageArchive).where(GroupMessageArchive.message_key==target))
                    elif kind=="memory":await session.execute(delete(GroupPermanentMemory).where(GroupPermanentMemory.id==int(target)))
                    elif kind=="sample":await session.execute(delete(SpeechStyleSample).where(SpeechStyleSample.id==int(target)))
                    elif kind=="sticker":await session.execute(delete(StickerLibraryRecord).where(StickerLibraryRecord.id==int(target)))
                await session.execute(text("DELETE FROM cg_migrated"))
                # Retain forgotten tombstones even across preparation retries.
                await session.execute(text("UPDATE cg_source_state SET valid=0"))
                if await session.get(Group,key[0]) is None:session.add(Group(id=key[0],settings={}))
                await session.commit()
            domains[key]=(engine,sessions,memory)
            return domains[key]
        async def mapped(sessions,kind,source_id,target):
            async with sessions() as session:
                await session.execute(text("INSERT OR REPLACE INTO cg_migrated VALUES (:kind,:source,:target)"),{"kind":kind,"source":str(source_id),"target":str(target)})
                await session.commit()
        counts=Counter()
        policies={int(row["chat_id"]):row for row in rows(path,"group_assistant_policies")}
        messages_by_id={}
        forgotten=set(forgotten_sources)
        for record in rows(path,"group_assistant_memories"):
            if record.get("forgotten_at") and record.get("source_message_id"):
                forgotten.add((int(record.get("source_chat_id") or record["chat_id"]),int(record["source_message_id"])))
        try:
            # A refreshed paused snapshot may remove a whole topic/group. Clear
            # its old mapped projections too; originals remain in each snapshot.
            if previous:
                for location in (data/"scopes").glob("*/*.sqlite3"):
                    await domain(int(location.parent.name),int(location.stem))
            for group in header["groups"]:
                _,sessions,_=await domain(group)
                policy=policies.get(group,{})
                profile={"target_user_id":int(policy.get("mimic_target_user_id") or 0),
                    "target_user_name":policy.get("mimic_target_user_name") or "",
                    "profile_text":policy.get("mimic_profile_text") or "",
                    "sample_count":int(policy.get("mimic_sample_count") or 0),
                    "distilled_at_count":int(policy.get("mimic_distilled_at_count") or 0)}
                async with sessions() as session:
                    row=await session.get(Group,group)
                    current=dict(row.settings or {})
                    old_profile=current.get("speech_style")
                    if previous and old_profile is not None:
                        expected=previous.get("profiles",{}).get(str(group),{}).get("state_sha256")
                        if expected and digest(old_profile)!=expected:
                            raise RuntimeError("native profile changed since preparation; refusing overwrite")
                    current["speech_style"]=profile
                    row.settings=current
                    await session.commit()
                manifest["profiles"][str(group)]={"state_sha256":digest(profile),"profile_sha256":hashlib.sha256(profile["profile_text"].encode()).hexdigest(),
                    "target_user_id":profile["target_user_id"],"sample_count":profile["sample_count"],"distilled_at_count":profile["distilled_at_count"],"legacy_policy_version":policy.get("version",0)}
            snapshot_at=utc(header["exported_at"])
            for record in rows(path,"group_assistant_messages"):
                group=int(record["chat_id"]);topic=int(record.get("thread_id") or 0);message_id=int(record["telegram_message_id"])
                messages_by_id[(group,message_id)]=(topic,record)
                _,sessions,memory=await domain(group,topic)
                forgotten_source=(group,message_id) in forgotten
                eligible=bool(record.get("approved") and record.get("delivered") and utc(record["expires_at"])>snapshot_at and not forgotten_source)
                async with sessions() as session:
                    await session.execute(text("INSERT INTO cg_source_state VALUES (:id,:revision,:valid,:forgotten,:archived) ON CONFLICT(message_id) DO UPDATE SET revision=:revision,valid=:valid,forgotten=MAX(forgotten,:forgotten),was_archived=MAX(was_archived,:archived)"),
                        {"id":message_id,"revision":record["content_hash"],"valid":int(eligible),"forgotten":int(forgotten_source),"archived":int(eligible)})
                    await session.commit()
                if not eligible:counts["inactive_originals_retained_in_snapshot"]+=1;continue
                await memory.add_message(group,record["role"],record["text"],user_id=int(record.get("sender_id") or 0),sender_name=record.get("sender_name") or "",
                    message_type="legacy_text",message_id=str(message_id),created_at=utc(record["created_at"]),
                    archive_metadata={"telegram_message_id":message_id,"message_thread_id":topic or None,"raw_text":record["text"],
                        "direction":"outbound" if record["role"]=="assistant" else "inbound","sender_kind":"bot" if record["role"]=="assistant" else "user",
                        "sender_display_name":record.get("sender_name") or "","extra_metadata":{"legacy_pg_id":record["id"],"legacy_source_type":record["source_type"],"snapshot_sha256":batch,"missing_original_media_metadata":True}})
                await mapped(sessions,"message",record["id"],f"{group}:{message_id}");counts["originals"]+=1
            for record in rows(path,"group_assistant_memories"):
                if not (record.get("active") and not record.get("forgotten_at") and utc(record["expires_at"])>snapshot_at and record.get("source_verified")=="verified"
                    and record["authority_level"] in {"admin_base","admin_explicit"} and record["source_type"] in {"admin_base","admin_explicit"}
                    and record.get("source_operator_id") and record["valid_scope"] in {"long_term","current_group"}):
                    counts["legacy_facts_not_promoted"]+=1;continue
                if record.get("source_message_id"):
                    source_key=(int(record.get("source_chat_id") or record["chat_id"]),int(record["source_message_id"]))
                    origin=messages_by_id.get(source_key)
                    if not origin or source_key in forgotten or not (origin[1]["approved"] and origin[1]["delivered"] and utc(origin[1]["expires_at"])>snapshot_at and origin[1]["content_hash"]==record.get("source_content_hash")):
                        counts["legacy_facts_not_promoted"]+=1;continue
                _,sessions,memory=await domain(record["chat_id"])
                row,_=await memory.add_permanent_memory(record["chat_id"],record["content"],created_by=int(record.get("source_operator_id") or 0))
                async with sessions() as session:
                    await session.execute(update(GroupPermanentMemory).where(GroupPermanentMemory.id==row.id).values(created_at=local(record.get("source_created_at") or record["created_at"]),updated_at=local(record["updated_at"])))
                    await session.commit()
                await mapped(sessions,"memory",record["id"],row.id);counts["explicit_permanent_memories"]+=1
            samples={}
            for record in rows(path,"group_assistant_style_samples"):
                group=record["chat_id"]
                if record["user_id"]==policies.get(group,{}).get("mimic_target_user_id"):
                    samples.setdefault(group,[]).append(record)
            for group,items in samples.items():
                _,sessions,_=await domain(group)
                for record in items[-200:]:
                    async with sessions() as session:
                        row=SpeechStyleSample(group_id=group,user_id=record["user_id"],content=record["content"],created_at=local(record["created_at"]))
                        session.add(row);await session.commit();await session.refresh(row)
                    await mapped(sessions,"sample",record["id"],row.id);counts["style_samples"]+=1
            for record in rows(path,"group_assistant_sticker_samples"):
                group=record["chat_id"];origin=messages_by_id.get((group,int(record.get("source_message_id") or 0)))
                if not origin or (group,int(record.get("source_message_id") or 0)) in forgotten:
                    counts["stickers_without_visible_source_retained_in_snapshot"]+=1;continue
                topic,message=origin
                if not(message["approved"] and message["delivered"] and utc(message["expires_at"])>snapshot_at):continue
                _,sessions,_=await domain(group,topic)
                async with sessions() as session:
                    row=StickerLibraryRecord(group_id=group,file_id=record["file_id"],description=record.get("query") or "",emoji=record.get("emoji") or "",set_name=record.get("set_name") or "",aliases=record.get("aliases") or [],seen_count=record["seen_count"],sent_count=record["sent_count"],source="legacy_pg",created_at=local(record["created_at"]),last_seen_at=local(record.get("last_seen_at") or record["created_at"]),last_sent_at=local(record.get("last_sent_at")))
                    session.add(row);await session.commit();await session.refresh(row)
                await mapped(sessions,"sticker",record["id"],row.id);counts["stickers"]+=1
            bundle=json.loads(wiki_bundle.read_text())
            wiki_result=WikiStore(data/"wiki.sqlite3").import_bundle(bundle,group_ids=header["wiki_targets"],batch="wiki-source-20260910")
            manifest.update(state="prepared",counts=dict(counts),wiki=wiki_result,prepared_at=datetime.now(timezone.utc).isoformat())
            manifest_path.write_text(packed(manifest)+"\n")
            return manifest
        finally:
            for engine,_,memory in domains.values():
                await memory.shutdown();await engine.dispose()


def backup_domain(data: Path,destination: Path):
    """Stopped-unit complete backup, including post-switch native-only truth."""
    data=data.resolve();destination=destination.resolve()
    if destination==data or data in destination.parents:
        raise ValueError("backup must be outside the live native directory")
    with domain_lock(data):
        if destination.exists():raise FileExistsError("backup destination already exists")
        if not (data/"migration-manifest.json").exists():
            raise ValueError("cannot back up an unprepared native domain")
        destination.mkdir(parents=True,mode=0o700)
        files={}
        for source in sorted(data.rglob("*")):
            if source.is_symlink():raise ValueError("unexpected symlink in native domain")
            if not source.is_file() or source.name==".native-domain.lock" or source.name.endswith(("-wal","-shm")):
                continue
            relative=str(source.relative_to(data))
            target=destination/relative
            target.parent.mkdir(parents=True,exist_ok=True,mode=0o700)
            if source.suffix==".sqlite3":
                # The SQLite backup API includes every committed WAL frame.
                with sqlite3.connect(source.as_uri()+"?mode=ro",uri=True) as original,sqlite3.connect(target) as copied:
                    original.backup(copied)
                    if copied.execute("PRAGMA integrity_check").fetchone()[0]!="ok":
                        raise RuntimeError("native SQLite backup failed integrity check")
            else:shutil.copyfile(source,target)
            target.chmod(0o600)
            files[relative]=hashlib.sha256(target.read_bytes()).hexdigest()
        manifest={"format":1,"source_commit":SOURCE_COMMIT,"created_at":datetime.now(timezone.utc).isoformat(),"files":files}
        target=destination/"domain-backup.json"
        target.write_text(packed(manifest)+"\n");target.chmod(0o600)
        return {"backup":str(destination),"files":len(files),"manifest_sha256":digest(manifest)}


def verify_domain_backup(backup: Path):
    manifest=json.loads((backup/"domain-backup.json").read_text())
    if manifest.get("format")!=1 or manifest.get("source_commit")!=SOURCE_COMMIT:
        raise ValueError("unsupported native backup")
    for name,expected in manifest["files"].items():
        path=backup/name
        if Path(name).is_absolute() or ".." in Path(name).parts or path.is_symlink() or not path.is_file():
            raise ValueError("invalid native backup path")
        if hashlib.sha256(path.read_bytes()).hexdigest()!=expected:
            raise ValueError("native backup hash mismatch: "+name)
    return manifest


def restore_domain(backup: Path,data: Path):
    """Restore into a NEW directory; never overwrite the latest active truth."""
    manifest=verify_domain_backup(backup)
    with domain_lock(data):
        if any(p.name!=".native-domain.lock" for p in data.iterdir()):
            raise ValueError("restore requires an empty destination; retain the current native directory")
        for name in manifest["files"]:
            target=data/name
            target.parent.mkdir(parents=True,exist_ok=True,mode=0o700)
            shutil.copyfile(backup/name,target);target.chmod(0o600)
    return {"restored":True,"files":len(manifest["files"]),"data":str(data)}


async def cli():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command",choices=["export","verify","import","inspect","backup-domain","restore-domain","verify-backup"])
    parser.add_argument("--snapshot",required=True,type=Path)
    parser.add_argument("--data",type=Path,default=Path("/data"))
    parser.add_argument("--wiki-bundle",type=Path,default=Path(__file__).parents[1]/"knowledge/wiki-20260910.json")
    parser.add_argument("--production",action="store_true")
    parser.add_argument("--control-url",default=os.environ.get("CG_BROKER_URL",""))
    args=parser.parse_args()
    if args.command=="backup-domain":result=backup_domain(args.data,args.snapshot)
    elif args.command=="restore-domain":result=restore_domain(args.snapshot,args.data)
    elif args.command=="verify-backup":result={"verified":True,"files":len(verify_domain_backup(args.snapshot)["files"])}
    elif args.command=="import":result=await import_snapshot(args.snapshot,args.data,wiki_bundle=args.wiki_bundle)
    elif args.command=="inspect":result=dict(zip(("header","manifest"),inspect_snapshot(args.snapshot)))
    else:
        dsn=os.environ["CG_MIGRATION_DATABASE_URL"]
        kwargs={"production":args.production,"control_url":args.control_url,"secret":os.environ.get("ASSISTANT_BROKER_SECRET","")}
        result=await (export_snapshot(dsn,args.snapshot,**kwargs) if args.command=="export" else verify_snapshot(dsn,args.snapshot,**kwargs))
    print(packed(result))


if __name__=="__main__":
    asyncio.run(cli())
