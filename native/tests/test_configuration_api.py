import asyncio
import json
import sqlite3

import httpx
import pytest
from sqlalchemy import select

from bot.config import Settings
from bot.db.models import Group,GroupApiModelQuerySecret
from bot.services.api_model_query import load_group_api_model_query_connection
from bot.utils.project_info import build_bot_project_info_context
from clawguard_native.app import create_app
from test_native_host import FakeBroker


async def test_private_api_source_config_secrets_effective_group_and_cas(tmp_path,monkeypatch):
    monkeypatch.setenv('CONFIG_MASTER_KEY','isolated-test-master-key-not-a-deployment-secret')
    class Broker(FakeBroker):
        async def call(self,op,payload,*,scope=None):
            if op=='bootstrap':return {'scopes':[],'bot_user':{'id':99,'is_bot':True,'first_name':'ClawGuard'}}
            return await super().call(op,payload,scope=scope)
    broker=Broker();secret='local-control-authentication-secret-only'
    app=create_app(data=tmp_path,broker=broker,secret=secret)
    async with app.router.lifespan_context(app):
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app),base_url='http://isolated') as http:
            assert (await http.post('/config/read',json={})).status_code==401
            http.headers['Authorization']='Bearer '+secret
            baseline=(await http.post('/config/read',json={})).json()
            assert baseline['automatic_fact_learning'] is False
            assert baseline['bot']['memory_recent_messages']==500
            assert baseline['bot']['proactive_quiet_hours_end']==9
            changed=await http.post('/config/write',json={'revision':baseline['revision'],'bot':{'memory_recent_messages':1800,'memory_retention_days':90},
                'music':{'default_source':'netease'},'movie_info':{'enabled':True,'tmdb_read_access_token':'fixture-movie-secret'},'stickers':{'fallback_file_ids':['fixture-file']}})
            assert changed.status_code==200,changed.text
            config=changed.json()
            assert 'fixture-movie-secret' not in changed.text
            assert config['movie_secrets_configured']['tmdb_read_access_token']
            assert not config['movie_info']['tmdb_read_access_token']
            assert 'fixture-movie-secret' not in (tmp_path/'control.sqlite3').read_bytes().decode('utf-8','ignore')
            assert app.state.host.settings.movie_info_tmdb_read_access_token=='fixture-movie-secret'
            assert app.state.host.settings.music_api_default_source=='netease'
            assert app.state.host.settings.skill_sticker_file_ids=='fixture-file'
            assert app.state.host.settings.bot.memory_recent_messages==1800
            stale=await http.post('/config/write',json={'revision':baseline['revision'],'bot':{'memory_recent_messages':500}})
            assert stale.status_code==400
            group={'group_id':-123,'background_grant':'root-background'}
            actual=(await http.post('/groups/read',json=group)).json()
            assert actual['settings']['scheduled_tasks']['cooldown_topic']['enabled']==app.state.host.settings.bot.proactive_default_enabled
            assert 'memory_manage' in [item['name'] for item in actual['skills']]
            assert 'movie_info' in [item['name'] for item in actual['skills']]
            assert 'rule_manage' not in [item['name'] for item in actual['skills']]
            assert 'api_model_query' not in [item['name'] for item in actual['skills']]
            changed=await http.post('/groups/write',json={**group,'revision':0,'operator_id':7,'settings':{
                'api_model_query':{'enabled':True,'base_url':'https://fixture.invalid/v1','api_key':'fixture-group-key','http_timeout_sec':21},
                'mimic_target':{'user_id':258605875,'user_name':'保留对象'}}})
            assert changed.status_code==200,changed.text
            assert 'fixture-group-key' not in changed.text
            assert 'api_model_query' in [item['name'] for item in changed.json()['skills']]
            root=app.state.host.scopes[(-123,0)]
            async with root.sessions() as session:
                row=await session.get(Group,-123)
                values=dict(row.settings);values['speech_style']={**values['speech_style'],'profile_text':'不被设置保存覆盖的画像','sample_count':14,'distilled_at_count':14}
                row.settings=values;await session.commit()
            changed=await http.post('/groups/write',json={**group,'revision':1,'operator_id':7,'settings':{'at_reply_mode':True}})
            assert changed.status_code==200,changed.text
            assert changed.json()['settings']['speech_style']['profile_text']=='不被设置保存覆盖的画像'
            topic=await app.state.host.scope(-123,17,'topic-background')
            await app.state.host.sync_topic_settings(topic)
            async with topic.sessions() as session:
                connection=await load_group_api_model_query_connection(session,group_id=-123,master_key=topic.settings.config_master_key)
                assert connection.api_key=='fixture-group-key' and connection.ready
                row=await session.get(GroupApiModelQuerySecret,-123)
                assert row.ciphertext!='fixture-group-key' and row.updated_by==7
            cleared=await http.post('/groups/write',json={**group,'revision':2,'operator_id':7,'settings':{'api_model_query':{'enabled':False,'clear_api_key':True}}})
            assert cleared.status_code==200,cleared.text
            assert not cleared.json()['settings']['api_model_query']['api_key_configured']
            await app.state.host.sync_topic_settings(topic)
            async with topic.sessions() as session:
                assert await session.get(GroupApiModelQuerySecret,-123) is None
            assert not [1 for op,_ in broker.calls if op in {'model','telegram'}]
    identity=build_bot_project_info_context()
    assert 'project_name: ClawGuard' in identity and '82c3703daba218b36255132c9bf51ebc444c6480' in identity
    assert 'https://github.com/Hamster-Prime/Smart_Group_Bot' in identity and 'MIT License' in identity
