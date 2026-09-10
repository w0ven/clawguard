"""Offline migration against the real CG SQL migrations and native source ORM."""
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import sqlite3

import asyncpg
import pytest
import pytest_asyncio
from sqlalchemy import select

from clawguard_native.migration import export_snapshot, import_snapshot, inspect_snapshot, verify_snapshot, check_dsn, backup_domain,restore_domain,verify_domain_backup
from clawguard_native.wiki import WikiStore
from clawguard_native.host import NativeHost
from bot.db.models import Group, GroupPermanentMemory, GroupMessageArchive
from test_native_host import FakeBroker

ROOT = Path(__file__).resolve().parents[2]
BUNDLE = ROOT/'native/knowledge/wiki-20260910.json'
GROUPS = [-101,-102]


@pytest_asyncio.fixture
async def postgres():
    dsn = os.environ.get('CG_NATIVE_MIGRATION_TEST_DATABASE_URL')
    if not dsn:
        pytest.skip('requires disposable local cg_native_migration_test database')
    check_dsn(dsn,False)
    assert dsn.split('/',3)[-1].split('?')[0] == 'cg_native_migration_test'
    db = await asyncpg.connect(dsn)
    try:
        # The runner owns this fresh throwaway database, never an existing PG.
        assert not await db.fetchval("SELECT to_regclass('public.groups')")
        for path in sorted((ROOT/'bot/migrations').glob('*.sql')):
            await db.execute(path.read_text().split('-- +goose Down')[0])
        await db.execute('CREATE TABLE goose_db_version(version_id bigint, is_applied boolean)')
        await db.execute('INSERT INTO goose_db_version VALUES(36,true)')
        for group in GROUPS:
            await db.execute("INSERT INTO groups(chat_id,title,type) VALUES($1,'fixture','supergroup')",group)
            await db.execute("INSERT INTO authorized_groups(chat_id) VALUES($1)",group)
            await db.execute("INSERT INTO group_assistant_policies(chat_id,chat_enabled,learning_enabled,mimic_target_user_id,mimic_profile_text,mimic_sample_count,mimic_distilled_at_count,version) VALUES($1,true,true,258605875,'切换时最新原画像',14,14,10)",group)
        await db.execute("INSERT INTO group_assistant_style_samples(chat_id,user_id,content) VALUES(-101,258605875,$1)",'完整风格样本'*200)
        for mid,topic,approved,delivered,expiry in [(1,0,True,True,1),(2,17,True,True,1),(3,0,False,True,1),(4,0,True,False,1),(5,0,True,True,-1),(6,0,True,True,1)]:
            await db.execute("INSERT INTO group_assistant_messages(chat_id,telegram_message_id,thread_id,role,text,content_hash,approved,delivered,expires_at) VALUES(-101,$1,$2,'user',$3,$4,$5,$6,now()+$7*interval '1 day')",mid,topic,f'原文{mid}',f'hash-{mid}',approved,delivered,expiry)
        for index,authority,scope,forgotten,source_mid in [(1,'admin_base','long_term',False,None),(2,'learned_fact','long_term',False,1),(3,'admin_base','today',False,None),(4,'learned_fact','long_term',True,6)]:
            await db.execute("INSERT INTO group_assistant_memories(chat_id,subject,content,memory_type,authority_level,valid_scope,source_type,source_message_id,expires_at,dedupe_hash,forgotten_at) VALUES(-101,$1,$2,'base',$3,$4,$3,$5,now()+interval '10 days',$1,CASE WHEN $6 THEN now() ELSE NULL END)",str(index),f'记忆{index}',authority,scope,source_mid,forgotten)
        await db.execute('UPDATE group_assistant_memories SET source_operator_id=7')
        await db.execute("INSERT INTO group_assistant_sticker_samples(chat_id,file_id,source_message_id,query,seen_count,sent_count) VALUES(-101,'real-fixture-id',2,'贴纸别名',3,1),(-101,'unverifiable-id',999,'保留但不激活',1,0)")
        yield dsn,db
    finally:
        await db.close()


def sql(data,topic,query):
    with sqlite3.connect(data/'scopes'/'-101'/f'{topic}.sqlite3') as db:
        return db.execute(query).fetchall()


async def test_pg_native_migration_refresh_integrity_activation(postgres,tmp_path,monkeypatch):
    dsn,pg = postgres
    snapshot = tmp_path/'snapshot.jsonl'
    exported = await export_snapshot(dsn,snapshot,targets=GROUPS)
    before = snapshot.read_bytes()
    data=tmp_path/'native'
    # Fail after source ORM rows were written but before Wiki/ready commit.
    with monkeypatch.context() as patch:
        def fail(*args,**kwargs):raise RuntimeError('injected preparation failure')
        patch.setattr(WikiStore,'import_bundle',fail)
        with pytest.raises(RuntimeError,match='injected'):
            await import_snapshot(snapshot,data,wiki_bundle=BUNDLE)
    with pytest.raises(RuntimeError,match='incomplete'):
        NativeHost(data,FakeBroker())
    first = await import_snapshot(snapshot,data,wiki_bundle=BUNDLE)
    assert first['state']=='prepared'
    assert first['counts']['originals']==2
    assert first['counts']['explicit_permanent_memories']==1
    assert first['counts']['legacy_facts_not_promoted']==3
    assert first['counts']['stickers']==1
    assert first['profiles']['-101']['target_user_id']==258605875
    assert first['profiles']['-101']['profile_sha256']==hashlib.sha256('切换时最新原画像'.encode()).hexdigest()
    assert first['profiles']['-101']['legacy_policy_version']==10
    assert first['profiles']['-101']['distilled_at_count']==14
    assert sql(data,0,'SELECT content FROM group_permanent_memories') == [('记忆1',)]
    assert sql(data,0,'SELECT telegram_message_id FROM group_message_archive') == [(1,)]
    assert sql(data,17,'SELECT telegram_message_id FROM group_message_archive') == [(2,)]
    assert sql(data,0,'SELECT content FROM speech_style_samples') == [('完整风格样本'*200,)]
    assert sql(data,0,'SELECT forgotten,valid FROM cg_source_state WHERE message_id=6') == [(1,0)]
    assert snapshot.read_bytes()==before
    assert (await import_snapshot(snapshot,data,wiki_bundle=BUNDLE))['unchanged']
    assert (await verify_snapshot(dsn,snapshot))['verified']
    assert await pg.fetchval('SELECT count(*) FROM group_assistant_memories')==4
    assert await pg.fetchval('SELECT learning_enabled FROM group_assistant_policies WHERE chat_id=-101')
    # A new profile and approval invalidation must force a fresh final snapshot.
    await pg.execute("UPDATE group_assistant_policies SET version=11,mimic_profile_text='更新后的模型原画像' WHERE chat_id=-101")
    await pg.execute('UPDATE group_assistant_messages SET approved=false WHERE telegram_message_id=2')
    with pytest.raises(RuntimeError,match='changed'):
        await verify_snapshot(dsn,snapshot)
    current=tmp_path/'latest.jsonl'
    await export_snapshot(dsn,current,targets=GROUPS)
    latest=await import_snapshot(current,data,wiki_bundle=BUNDLE)
    assert latest['profiles']['-101']['legacy_policy_version']==11
    assert sql(data,17,'SELECT telegram_message_id FROM group_message_archive')==[]
    with pytest.raises(RuntimeError,match='older'):
        await import_snapshot(snapshot,data,wiki_bundle=BUNDLE)
    host = NativeHost(data,FakeBroker())
    try:
        with pytest.raises(RuntimeError,match='running'):
            await import_snapshot(current,data,wiki_bundle=BUNDLE)
        scope=await host.scope(-101,0,'test-background')
        async with scope.sessions() as session:
            row=await session.get(Group,-101)
            assert row.settings['speech_style']['profile_text']=='更新后的模型原画像'
        # The source lifecycle must not synthesize a new portrait at activation.
        assert not any(op=='model' for op,_ in host.broker.calls)
        await scope.memory.add_permanent_memory(-101,'切换后新增，回退不可丢',created_by=7)
        await scope.memory.delete_permanent_memory(-101,'记忆1')
        async with scope.sessions() as session:
            row=await session.get(Group,-101)
            state=dict(row.settings);state['speech_style']={**state['speech_style'],'profile_text':'原生运行期新画像','sample_count':15}
            row.settings=state;await session.commit()
        with pytest.raises(RuntimeError,match='running'):
            backup_domain(data,tmp_path/'running-backup')
    finally:
        await host.close()
    with pytest.raises(RuntimeError,match='authoritative'):
        await import_snapshot(snapshot,data,wiki_bundle=BUNDLE)
    assert (await import_snapshot(current,data,wiki_bundle=BUNDLE))['unchanged']
    backup=tmp_path/'post-switch-backup'
    assert backup_domain(data,backup)['files']>3
    assert verify_domain_backup(backup)['files']
    restored=tmp_path/'restored-native'
    restore_domain(backup,restored)
    assert sql(restored,0,'SELECT content FROM group_permanent_memories')==[('切换后新增，回退不可丢',)]
    restored_host=NativeHost(restored,FakeBroker())
    try:
        restored_scope=await restored_host.scope(-101,0,'restored-background')
        async with restored_scope.sessions() as session:
            row=await session.get(Group,-101)
            assert row.settings['speech_style']['profile_text']=='原生运行期新画像'
            assert row.settings['speech_style']['sample_count']==15
        assert WikiStore(restored/'wiki.sqlite3').retrieve(-101,query='Po0')['documents']
    finally:await restored_host.close()
    with pytest.raises(ValueError,match='empty'):
        restore_domain(backup,data)
    corrupt=tmp_path/'corrupt.jsonl'
    corrupt.write_bytes(before.replace('原文1'.encode(),'篡改1'.encode()))
    with pytest.raises(ValueError,match='hash'):
        inspect_snapshot(corrupt)


async def test_refuse_unsafe_target_and_empty_production_domain(tmp_path,monkeypatch):
    with pytest.raises(ValueError,match='non-task'):
        check_dsn('postgres://127.0.0.1/production',False)
    monkeypatch.setenv('NATIVE_REQUIRE_MIGRATION','true')
    with pytest.raises(RuntimeError,match='manifest required'):
        NativeHost(tmp_path,FakeBroker())
    monkeypatch.delenv('NATIVE_REQUIRE_MIGRATION')
    host=NativeHost(tmp_path,FakeBroker())
    await host.close()
