from datetime import datetime,timedelta
from zoneinfo import ZoneInfo
import time
import pytest


@pytest.fixture(autouse=True)
def local_zone(monkeypatch):
    with monkeypatch.context() as patch:
        patch.setenv('TZ','Asia/Shanghai');time.tzset()
        yield
    time.tzset()

from bot.db.models import Group,GroupMessageArchive
from bot.services import proactive as source
from sqlalchemy import select
from clawguard_native.host import NativeHost
from clawguard_native.proactive import ProactiveTopicService
from test_native_behaviors import ScriptedBroker,response,settings


async def test_real_proactive_brief_local_jitter_claim_and_restart(tmp_path,monkeypatch):
    now=datetime(2026,9,10,14,0,tzinfo=ZoneInfo('Asia/Shanghai'))
    monkeypatch.setattr(source,'_now_local',lambda:now)
    monkeypatch.setattr(source.random,'randint',lambda low,high:37*60)
    broker=ScriptedBroker([response('聊聊最近折腾的路由器配置？')])
    host=NativeHost(tmp_path,broker,settings())
    scope=await host.scope(-123,0,'root-background')
    initial=source.record_group_activity({},at=now-timedelta(hours=4),config=scope.settings.bot)
    initial['scheduled_tasks']['cooldown_topic']['enabled']=True
    initial['scheduled_tasks']['cooldown_topic']['task_brief']='只聊路由器，不谈其他话题'
    due=source.get_cooldown_task_state(initial)
    assert datetime.fromisoformat(due['next_run_at'])==now-timedelta(minutes=23)
    async with scope.sessions() as session:
        row=await session.get(Group,-123);row.settings=initial;await session.commit()
    host.bind(scope)
    service=ProactiveTopicService(settings=scope.settings,bot=scope.bot,memory=scope.memory,session_factory=scope.sessions,llm=scope.llm)
    try:
        assert await service._run_group_cooldown_task(-123,now=now)
        assert len(broker.requests)==1
        request=broker.requests[0]
        assert request['role']=='chat'
        assert request['messages'][-1]['content']=='只聊路由器，不谈其他话题'
        assert any('[CURRENT_TIME]' in message['content'] for message in request['messages'])
        sends=[v for op,v in broker.calls if op=='telegram' and v['method']=='sendMessage']
        assert len(sends)==1 and sends[0]['data']['text']=='聊聊最近折腾的路由器配置？'
        async with scope.sessions() as session:
            row=await session.get(Group,-123)
            persisted=source.get_cooldown_task_state(row.settings)
            assert persisted['processed_activity_revision']==persisted['activity_revision']
            assert 'delivery_claim' not in persisted
            archive=(await session.execute(select(GroupMessageArchive))).scalars().all()
            assert any(item.role=='assistant' and item.telegram_message_id==901 for item in archive)
    finally:await host.close()
    restarted=NativeHost(tmp_path,broker,settings())
    try:
        scope=await restarted.scope(-123,0,'new-root-background');restarted.bind(scope)
        service=ProactiveTopicService(settings=scope.settings,bot=scope.bot,memory=scope.memory,session_factory=scope.sessions,llm=scope.llm)
        assert not await service._run_group_cooldown_task(-123,now=now)
        assert len(broker.requests)==1
    finally:await restarted.close()


async def test_real_proactive_new_activity_during_generation_drops_send(tmp_path,monkeypatch):
    now=datetime(2026,9,10,14,0,tzinfo=ZoneInfo('Asia/Shanghai'))
    monkeypatch.setattr(source,'_now_local',lambda:now)
    async def model(_):
        async with scope.sessions() as session:
            row=await session.get(Group,-123)
            row.settings=source.record_group_activity(row.settings,at=now,config=scope.settings.bot)
            await session.commit()
        source.note_group_activity(-123)
        return response('此时不应发送')
    broker=ScriptedBroker([model]);host=NativeHost(tmp_path,broker,settings())
    try:
        scope=await host.scope(-123,0,'background');host.bind(scope)
        async with scope.sessions() as session:
            row=await session.get(Group,-123)
            initial=source.record_group_activity({},at=now-timedelta(hours=5),config=scope.settings.bot)
            initial['scheduled_tasks']['cooldown_topic']['enabled']=True
            row.settings=initial
            await session.commit()
        service=ProactiveTopicService(settings=scope.settings,bot=scope.bot,memory=scope.memory,session_factory=scope.sessions,llm=scope.llm)
        assert await service._run_group_cooldown_task(-123,now=now)
        assert len(broker.requests)==1
        assert not any(op=='telegram' and value['method']=='sendMessage' for op,value in broker.calls)
    finally:await host.close()
