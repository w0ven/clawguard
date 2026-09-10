"""Private authenticated HTTP entry; deliberately no Telegram update receiver."""
from __future__ import annotations

import hmac
import asyncio
import os
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse

from .boundary import Broker
from .configuration import Configuration
from .host import NativeHost
from . import SOURCE_COMMIT, wiki


def create_app(*, data: Path | None = None, broker=None, secret: str | None = None):
    data = data or Path(os.environ.get("NATIVE_DATA_DIR","/data"))
    secret = secret or os.environ.get("ASSISTANT_BROKER_SECRET","")
    if len(secret) < 32:
        raise ValueError("ASSISTANT_BROKER_SECRET requires at least 32 characters")
    broker = broker or Broker(os.environ["CG_BROKER_URL"],secret)
    host = NativeHost(data,broker)
    configuration = Configuration(data/"control.sqlite3")
    configuration.apply(host.settings)
    wiki.wiki_store = wiki.WikiStore(data/"wiki.sqlite3")

    @asynccontextmanager
    async def lifespan(app):
        try:
            await host.activate()
        except BaseException:
            await host.close()
            raise
        background = asyncio.create_task(host.bootstrap_loop(),name="cg-native-bootstrap")
        try:
            yield
        finally:
            background.cancel()
            await asyncio.gather(background,return_exceptions=True)
            await host.close()

    app = FastAPI(docs_url=None,redoc_url=None,openapi_url=None,lifespan=lifespan)
    app.state.host = host
    app.state.configuration = configuration

    @app.middleware("http")
    async def authorize(request: Request,call_next):
        if request.url.path != "/healthz":
            supplied = request.headers.get("authorization","")
            if not hmac.compare_digest(supplied,"Bearer "+secret):
                return JSONResponse({"error":"unauthorized"},status_code=401)
            if int(request.headers.get("content-length","0")) > 32*1024*1024:
                return JSONResponse({"error":"request too large"},status_code=413)
        try:
            return await call_next(request)
        except (ValueError,PermissionError,KeyError) as exc:
            return JSONResponse({"error":str(exc)},status_code=400)

    @app.get("/healthz")
    async def health():
        return {"ok":not host.closing,"engine":"native","source_commit":SOURCE_COMMIT}

    @app.post("/events")
    async def event(request: Request):
        return await host.event(await request.json())

    @app.post("/callbacks")
    async def callback(request: Request):
        return await host.callback(await request.json())

    @app.post("/sources/check")
    async def source_check(request: Request):
        from datetime import timedelta
        from sqlalchemy import select,text
        from bot.db.models import GroupMessageArchive
        from bot.utils.timezone import now_shanghai_naive
        values=await request.json()
        scope=await host.scope(int(values["group_id"]),int(values.get("topic_id") or 0),values["background_grant"])
        message_id=int(values["message_id"])
        async with scope.sessions() as session:
            state=(await session.execute(text("SELECT valid,forgotten FROM cg_source_state WHERE message_id=:id"),{"id":message_id})).first()
            if state and (not state.valid or state.forgotten):return {"visible":False}
            row=(await session.execute(select(GroupMessageArchive.id).where(GroupMessageArchive.group_id==scope.group_id,
                GroupMessageArchive.telegram_message_id==message_id,GroupMessageArchive.ingested_at>=now_shanghai_naive()-timedelta(days=scope.settings.bot.memory_retention_days)).limit(1))).first()
            return {"visible":row is not None}

    @app.post("/config/read")
    async def config_read():
        return configuration.read()

    @app.post("/config/write")
    async def config_write(request: Request):
        value = configuration.write(await request.json())
        configuration.apply(host.settings)
        for scope in host.scopes.values():
            models = {role:{"model":getattr(scope.settings.bot,role+"_model").model,"space_id":scope.llm.embedding_space}
                      for role in ("main","decision","compress","vision","embed")}
            configuration.apply(scope.settings)
            host.apply_scope_models(scope,models)
            scope.memory.reconfigure(scope.settings.bot)
        return value

    @app.post("/groups/read")
    async def group_read(request: Request):
        from bot.db.models import Group
        values = await request.json()
        scope = await host.scope(int(values["group_id"]),0,values["background_grant"])
        async with scope.sessions() as session:
            row = await session.get(Group,scope.group_id)
            from .group_config import effective_group
            return effective_group(row.settings,scope.settings,scope.llm)

    @app.post("/groups/write")
    async def group_write(request: Request):
        values = await request.json()
        result = await host.configure_group(int(values["group_id"]),background_grant=values["background_grant"],values=values["settings"],revision=int(values["revision"]),operator_id=int(values["operator_id"]))
        from .group_config import effective_group
        scope=host.scopes[(int(values["group_id"]),0)]
        return effective_group(result,scope.settings,scope.llm)

    async def memory_scope(values):
        return await host.scope(int(values["group_id"]),int(values.get("topic_id") or 0),values["background_grant"])

    @app.post("/memory/list")
    async def memory_list(request: Request):
        scope = await memory_scope(await request.json())
        rows = await scope.memory.list_permanent_memories(scope.group_id,limit=200)
        return {"items":[{"id":row.id,"content":row.content,"created_by":row.created_by,
                          "created_at":row.created_at.isoformat(),"permanent":True} for row in rows]}

    @app.post("/memory/add")
    async def memory_add(request: Request):
        values = await request.json()
        scope = await memory_scope(values)
        row,created = await scope.memory.add_permanent_memory(scope.group_id,str(values["content"]),created_by=int(values["operator_id"]))
        if row is None:
            raise ValueError("永久记忆内容为空")
        return {"id":row.id,"created":created,"permanent":True}

    @app.post("/memory/replace")
    async def memory_replace(request: Request):
        values=await request.json()
        scope=await memory_scope(values)
        deleted,row,created=await scope.memory.replace_permanent_memory(scope.group_id,target="#"+str(int(values["id"])),new_content=str(values["content"]),created_by=int(values["operator_id"]))
        if row is None:raise ValueError("永久记忆内容为空")
        return {"id":row.id,"replaced":len(deleted),"created":created}

    @app.post("/memory/delete")
    async def memory_delete(request: Request):
        values = await request.json()
        scope = await memory_scope(values)
        deleted = await scope.memory.delete_permanent_memory(scope.group_id,"#"+str(int(values["id"])))
        return {"deleted":bool(deleted)}

    return app


def main():
    import uvicorn
    uvicorn.run(create_app(),host="0.0.0.0",port=int(os.environ.get("NATIVE_PORT","8091")),access_log=False)


if __name__ == "__main__":
    main()
