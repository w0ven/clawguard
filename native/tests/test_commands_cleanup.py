import asyncio
from datetime import timedelta
import json

import pytest
from sqlalchemy import select, update

from bot.db.models import GroupMessageArchive, TelegramDeleteJob
from bot.handlers import group
from bot.utils.timezone import now_shanghai_naive
from clawguard_native.boundary import Execution,execution
from clawguard_native.host import NativeHost
from test_native_behaviors import ScriptedBroker,response,settings
from test_native_host import event


class CommandBroker(ScriptedBroker):
    def __init__(self,responses=()):
        super().__init__(responses)
        self.admin=True
        self.transport=[]
    async def call(self,op,payload,*,scope=None):
        if op=='authorize-memory':
            self.calls.append((op,payload))
            if not self.admin: raise PermissionError('current admin required')
            return True
        if op=='telegram':
            self.transport.append((payload['method'],scope))
            if payload['method']=='answerCallbackQuery':
                self.calls.append((op,payload));return True
        return await super().call(op,payload,scope=scope)


def callback(data,topic=0):
    msg=event(topic=topic)['message']
    msg['message_id']=901
    msg['from']={'id':99,'is_bot':True,'first_name':'ClawGuard'}
    return {'group_id':-123,'topic_id':topic,'grant':'callback-grant','background_grant':'background',
        'turn':'callback:one','callback':{'id':'one','from':{'id':7,'is_bot':False,'first_name':'管理员'},
            'data':data,'chat_instance':'fixture','message':msg}}


async def test_real_lm_intent_list_paging_delete_and_revoke(tmp_path):
    fixed=response(json.dumps({'intent':'memory_manage','memory_action':'add','memory_content':'管理员永久原文'},ensure_ascii=False))
    broker=CommandBroker([fixed])
    host=NativeHost(tmp_path,broker,settings())
    try:
        result=await host.event(event(text='/lm add 管理员永久原文'))
        assert result['command']=='lm'
        scope=host.scopes[(-123,0)]
        memories=await scope.memory.list_permanent_memories(-123,limit=200)
        assert len(memories)==1 and memories[0].content=='管理员永久原文'
        assert memories[0].created_by==7
        assert len(broker.requests)==1
        assert len([1 for op,_ in broker.calls if op=='authorize-memory'])>=3
        # /lm does not pass through ordinary pending chat or archive commands.
        async with scope.sessions() as session:
            assert not (await session.execute(select(GroupMessageArchive))).scalars().all()
        await host.event(event(2,text='/lm'))
        sends=[v for op,v in broker.calls if op=='telegram' and v['method']=='sendMessage']
        assert 'lmd:' in json.dumps(sends[-1]['data']['reply_markup'])
        await host.callback(callback('lml:0'))
        assert any(method=='editMessageText' for method,_ in broker.transport)
        broker.admin=False
        await host.callback(callback(f'lmd:{memories[0].id}:0'))
        assert len(await scope.memory.list_permanent_memories(-123))==1
        broker.admin=True
        await host.callback(callback(f'lmd:{memories[0].id}:0'))
        assert not await scope.memory.list_permanent_memories(-123)
        with pytest.raises(PermissionError,match='not owned'):
            await host.callback(callback('rule_manage:delete'))
    finally:
        await host.close()


async def test_source_cleanup_persists_restart_with_bound_topic_not_stale_turn(tmp_path):
    broker=CommandBroker()
    host=NativeHost(tmp_path,broker,settings())
    scope=await host.scope(-123,17,'background-17')
    await scope.cleanup.enqueue_durable(chat_id=-123,message_id=908,due_at=now_shanghai_naive()+timedelta(hours=1))
    await host.close()
    assert not any(method=='deleteMessage' for method,_ in broker.transport)
    # The scheduler must use its own background context even if created while
    # another request context happens to be present on the calling task.
    token=execution.set(Execution(broker,-999,8,'wrong','stale-turn'))
    restarted=NativeHost(tmp_path,broker,settings())
    try:
        scope=await restarted.scope(-123,17,'new-background-17')
        async with scope.sessions() as session:
            jobs=(await session.execute(select(TelegramDeleteJob))).scalars().all()
            assert len(jobs)==1 and jobs[0].message_id==908
            await session.execute(update(TelegramDeleteJob).values(due_at=now_shanghai_naive()-timedelta(seconds=1)))
            await session.commit()
        for _ in range(60):
            if any(method=='deleteMessage' for method,_ in broker.transport):break
            await asyncio.sleep(.05)
        deletes=[ctx for method,ctx in broker.transport if method=='deleteMessage']
        assert len(deletes)==1
        assert (deletes[0].group_id,deletes[0].topic_id,deletes[0].turn,deletes[0].grant)==(-123,17,'','new-background-17')
        assert not broker.requests
    finally:
        await restarted.close()
        execution.reset(token)
