"""Credential-free HTTP response adapter for the UNMODIFIED source TTS parser.

SGB constructs payloads, dynamic emotions, splitting and fallback. CG injects
its existing credentials into one bounded HTTP attempt, returning raw frames.
"""
from __future__ import annotations

import base64
from contextlib import asynccontextmanager
from .boundary import execution


class Content:
    def __init__(self,data):
        self.data = data

    async def read(self,size=-1):
        value = self.data if size < 0 else self.data[:size]
        self.data = self.data[len(value):]
        return value

    async def iter_any(self):
        while self.data:
            yield await self.read(65536)


class Response:
    def __init__(self,result):
        self.status = result["status"]
        self.headers = result.get("headers",{})
        self.charset = "utf-8"
        self.content = Content(base64.b64decode(result["body"],validate=True))


class Client:
    @asynccontextmanager
    async def post(self,url,*,headers,json):
        # The URL and credential headers assembled by SGB are NEVER sent as
        # routing parameters. CG's server-side configured endpoint is sole owner.
        ctx = execution.get()
        result = await ctx.broker.call("tts",{"payload":json,"request_id":headers["X-Api-Request-Id"]})
        yield Response(result)


@asynccontextmanager
async def tts_client_session(*,timeout):
    yield Client()
