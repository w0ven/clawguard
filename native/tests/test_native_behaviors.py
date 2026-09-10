import asyncio
import base64
import json
from pathlib import Path

import pytest
from aiogram.types import Message
from sqlalchemy import select

from bot.config import Settings
from bot.db.models import GroupMessageArchive, GroupPermanentMemory, SpeechStyleSample, Group
from bot.handlers import group
from bot.services.doubao_tts import DoubaoTTSService
from clawguard_native.host import NativeHost
from clawguard_native import wiki as wiki_module
from clawguard_native.wiki import WikiStore
from test_native_host import FakeBroker, event


def response(text="",calls=None):
    message={"role":"assistant","content":text}
    if calls: message["tool_calls"]=calls
    return {"content":text,"response":{"choices":[{"index":0,"message":message,"finish_reason":"tool_calls" if calls else "stop"}]}}


def tool(name,args,index=1):
    return {"id":f"call-{index}","type":"function","function":{"name":name,"arguments":json.dumps(args,ensure_ascii=False)}}


class ScriptedBroker(FakeBroker):
    def __init__(self,responses):
        super().__init__()
        self.responses=list(responses)
        self.requests=[]
    async def call(self,op,payload,*,scope=None):
        if op=="model":
            self.calls.append((op,payload));self.requests.append(payload)
            assert self.responses,"unexpected extra model round"
            value=self.responses.pop(0)
            if callable(value): value=value(payload)
            if asyncio.iscoroutine(value): value=await value
            return value
        return await super().call(op,payload,scope=scope)


def settings():
    s=Settings(_env_file=None)
    s.bot.inbound_debounce_seconds=0
    s.bot.enable_streaming=False
    return s


@pytest.mark.asyncio
async def test_wiki_database_to_source_tool_transcript_to_reply(tmp_path,monkeypatch):
    bundle=json.loads((Path(__file__).parents[1]/"knowledge/wiki-20260910.json").read_text())
    store=WikiStore(tmp_path/"wiki.sqlite3")
    store.import_bundle(bundle,group_ids=[-123,-456],batch="reviewed")
    monkeypatch.setattr(wiki_module,"wiki_store",store)
    doc=next(d for d in bundle["sources"] if d["canonical_url"].endswith("/nftables-port-forwarding"))
    def answer(payload):
        result=json.loads(next(m["content"] for m in payload["messages"] if m["role"]=="tool"))
        source=result["payload"]["documents"][0]
        assert source["body_markdown"]==doc["body_markdown"]
        assert source["body_sha256"]==doc["body_sha256"]
        assert "flush ruleset" in source["body_markdown"] and "EOF" in source["body_markdown"]
        assert result["payload"]["is_live"] is False
        return response("先确认出口可用，按原文完整步骤配置，注意覆盖防火墙规则的风险。\n"+doc["canonical_url"])
    broker=ScriptedBroker([response(calls=[tool("wiki_query",{"url":doc["canonical_url"],"product":"po0"})]),answer])
    host=NativeHost(tmp_path/"domain",broker,settings())
    try:
        await host.event(event(text="@cg_test_bot Po0 nftables 手动转发怎么配置？"))
        await group.flush_pending_inbound_batches()
        assert len(broker.requests)==2
        sends=[v for op,v in broker.calls if op=="telegram" and v["method"]=="sendMessage"]
        assert len(sends)==1 and doc["canonical_url"] in sends[0]["data"]["text"]
        async with host.scopes[(-123,0)].sessions() as session:
            outputs=(await session.execute(select(GroupMessageArchive).where(GroupMessageArchive.role=="assistant"))).scalars().all()
            assert any(doc["canonical_url"] in row.content for row in outputs)
    finally: await host.close()


@pytest.mark.asyncio
async def test_source_stops_same_response_after_first_media_success(tmp_path):
    broker=ScriptedBroker([response(calls=[tool("send_sticker",{"sticker_file_id":"file-A"}),tool("send_sticker",{"sticker_file_id":"file-B"},2)])])
    host=NativeHost(tmp_path,broker,settings())
    try:
        await host.event(event(text="@cg_test_bot 发一张贴纸"))
        await group.flush_pending_inbound_batches()
        media=[v for op,v in broker.calls if op=="telegram" and v["method"]=="sendSticker"]
        assert len(media)==1 and media[0]["data"]["sticker"]=="file-A"
        assert len(broker.requests)==1
        assert not any(op=="telegram" and v["method"]=="sendMessage" for op,v in broker.calls)
    finally: await host.close()


@pytest.mark.asyncio
async def test_direct_multibubble_reference_normalization_and_numeric_parser(tmp_path):
    raw=json.dumps({"schema":"smart-group-bot.reply.v2","messages":[
        {"text":"第一泡","mode":"reply","reply_to":"latest_input"},
        {"text":"第二泡","mode":"reply","reply_to":"latest_input"},7]},ensure_ascii=False)
    broker=ScriptedBroker([response(raw)])
    host=NativeHost(tmp_path,broker,settings())
    try:
        await host.event(event())
        await group.flush_pending_inbound_batches()
        sends=[v["data"] for op,v in broker.calls if op=="telegram" and v["method"]=="sendMessage"]
        assert [s["text"] for s in sends]==["第一泡","第二泡","7"]
        assert str(sends[0].get("reply_to_message_id"))=="1"
        assert not sends[1].get("reply_to_message_id")
        assert len(broker.requests)==1,"direct mode must not invoke ReplyMode model"
    finally: await host.close()


@pytest.mark.asyncio
async def test_new_member_message_does_not_cancel_direct_reply(tmp_path):
    entered=asyncio.Event();release=asyncio.Event()
    async def delayed(payload):
        entered.set();await release.wait();return response("对第一位成员的回答")
    broker=ScriptedBroker([delayed,response("skip")])
    host=NativeHost(tmp_path,broker,settings())
    try:
        await host.event(event(1))
        await asyncio.wait_for(entered.wait(),2)
        other=event(2,text="别的成员补一句");other["message"]["from"]["id"]=8
        await host.event(other)
        release.set()
        await group.flush_pending_inbound_batches()
        assert any(op=="telegram" and "对第一位成员的回答" in v["data"].get("text","") for op,v in broker.calls)
    finally:
        release.set();await host.close()


@pytest.mark.asyncio
async def test_topic_forget_and_mimic_only_do_not_resurrect_or_expand_raw_visibility(tmp_path):
    broker=FakeBroker();host=NativeHost(tmp_path,broker,settings())
    try:
        first=event(1,topic=7,text="独有甲主题材料");first["keyword_replied"]=True
        await host.event(first)
        await asyncio.gather(*host.completions.values())
        await host.event(event(2,topic=8,text="乙主题"))
        await group.flush_pending_inbound_batches()
        assert not any("独有甲" in json.dumps(v,ensure_ascii=False) for op,v in broker.calls if op=="model")
        forgotten={**first,"kind":"forget","revision":"forgotten","turn":"forget:1"}
        await host.event(forgotten)
        await host.event({**first,"revision":"new-looking-hash","turn":"replay:1"})
        scope=host.scopes[(-123,7)]
        assert await scope.memory.recall_archive(-123,message_keys=["-123:1"])==[]
        async with scope.sessions() as session:
            assert not (await session.execute(select(GroupMessageArchive).where(GroupMessageArchive.telegram_message_id==1))).first()
        await host.configure_group(-123,background_grant="background",revision=0,values={"mimic_target":{"user_id":7,"user_name":"目标"}})
        sample=event(3,text="只有语气样本");sample["chat_enabled"]=False;sample["style_only"]=True
        await host.event(sample)
        root=host.scopes[(-123,0)]
        async with root.sessions() as session:
            assert len((await session.execute(select(SpeechStyleSample))).scalars().all())==1
            assert not (await session.execute(select(GroupMessageArchive).where(GroupMessageArchive.telegram_message_id==3))).first()
            assert not (await session.execute(select(GroupPermanentMemory))).first()
            state=(await session.get(Group,-123)).settings["speech_style"]
            assert state["sample_count"]==1
    finally: await host.close()


class TTSBroker(FakeBroker):
    def __init__(self,fail_at=0):super().__init__();self.payloads=[];self.fail_at=fail_at
    async def call(self,op,payload,*,scope=None):
        if op=="tts":
            self.payloads.append(payload["payload"])
            if self.fail_at==len(self.payloads):frames=[{"code":45000000,"message":"fixed upstream failure"}]
            else:frames=[{"code":0,"data":base64.b64encode(b"fixed-audio-bytes").decode()},{"code":20000000,"usage":{"text_words":1}}]
            return {"status":200,"headers":{},"body":base64.b64encode("".join(json.dumps(x) for x in frames).encode()).decode()}
        return await super().call(op,payload,scope=scope)


@pytest.mark.asyncio
async def test_long_tts_source_segments_partial_failure_and_dynamic_style(tmp_path):
    s=settings();s.doubao_tts_enabled=True;s._cg_tts_ready=True
    s.doubao_tts_speaker="fixture-speaker";s.doubao_tts_audio_format="mp3"
    broker=TTSBroker(fail_at=2);host=NativeHost(tmp_path,broker,s)
    try:
        scope=await host.scope(-123,0,"background");host.bind(scope,"grant","tts-test")
        msg=Message.model_validate(event()["message"],context={"bot":scope.bot})
        service=DoubaoTTSService(s)
        content="这是源长语音分段测试。"*65
        result=await service.send_message_tts_result(msg,content,emotion="happy",context="轻快地说明",speech_rate=12)
        assert len(result.requested_segments)>=2
        assert result.sent_segment_count==1 and not result.complete
        assert result.remaining_text and result.delivered_text
        assert len(broker.payloads)==2
        for payload in broker.payloads:
            params=payload["req_params"]
            assert len(params["text"])<=500
            assert params["audio_params"]["emotion"]=="happy"
            assert params["audio_params"]["speech_rate"]==12
            assert json.loads(params["additions"])["context_texts"]==["轻快地说明"]
        assert sum(op=="telegram" and v["method"]=="sendAudio" for op,v in broker.calls)==1
    finally: await host.close()
