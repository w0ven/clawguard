"""Source group settings/secret transactions; no alternate model provider pool."""
from dataclasses import asdict,replace

from bot.services.api_model_query import (get_api_model_query_config,set_api_model_query_config,
    normalize_api_model_query_base_url,replace_group_api_model_query_secret,clear_group_api_model_query_secret,
    group_api_model_query_secret_exists,api_model_query_tool_enabled)
from bot.services.at_reply import is_at_reply_enabled
from bot.services.doubao_tts import normalize_tts_mode,is_tts_tool_enabled
from bot.services.proactive import get_cooldown_task_state,is_cooldown_task_enabled
from bot.services.skills import SkillService


def effective_group(values,settings,llm):
    values=dict(values or {})
    task=get_cooldown_task_state(values)
    task['enabled']=is_cooldown_task_enabled(values,default_enabled=settings.bot.proactive_default_enabled)
    values['scheduled_tasks']={**values.get('scheduled_tasks',{}),'cooldown_topic':task}
    values['at_reply_mode']=is_at_reply_enabled(values)
    values['tts_mode']=normalize_tts_mode(values)
    values['mute_all_replies']=bool(values.get('mute_all_replies',False))
    values['cg_config_revision']=int(values.get('cg_config_revision') or 0)
    values.pop('allow_api_model_query',None)
    values['api_model_query']=asdict(get_api_model_query_config(values))
    service=SkillService(llm,settings=settings,default_sticker_file_ids=settings.skill_sticker_file_ids.split(','))
    selected=service._selected_skills(allow_tts=is_tts_tool_enabled(values['tts_mode']),allow_api_model_query=api_model_query_tool_enabled(values))
    readonly={'conversation_recall','wiki_query','websearch','webfetch','mihomo_doc','routeros_doc','bilibili_search','weibo_search','movie_info'}
    skills=[{'name':name,'description':skill.description,'enabled':True,'read_only':name in readonly} for name,skill in selected.items()]
    return {'settings':values,'skills':skills,'tools':skills,'server_bound_scope':True,
        'write_tools':[item['name'] for item in skills if not item['read_only']],
        'engine':'native','disabled_moderation_skills':['rule_manage','vote_ban']}


async def update_api_settings(session,values,patch,*,group_id,operator_id,master_key):
    if set(patch)-{'enabled','base_url','http_timeout_sec','check_timeout_sec','api_key','clear_api_key'}:
        raise ValueError('unsupported API query configuration')
    config=get_api_model_query_config(values)
    updates={}
    if 'enabled' in patch:
        if not isinstance(patch['enabled'],bool):raise ValueError('API query enabled must be boolean')
        updates['enabled']=patch['enabled']
    if 'base_url' in patch:updates['base_url']=normalize_api_model_query_base_url(patch['base_url'])
    for key,maximum in [('http_timeout_sec',300),('check_timeout_sec',600)]:
        if key in patch:
            number=float(patch[key])
            if not 1<=number<=maximum:raise ValueError('API query timeout outside source bounds')
            updates[key]=number
    changed=False
    if patch.get('clear_api_key'):
        await clear_group_api_model_query_secret(session,group_id=group_id);changed=True
    if patch.get('api_key'):
        await replace_group_api_model_query_secret(session,group_id=group_id,api_key=patch['api_key'],master_key=master_key,updated_by=operator_id);changed=True
    await session.flush()
    updates['api_key_configured']=await group_api_model_query_secret_exists(session,group_id)
    updates['secret_version']=config.secret_version+int(changed)
    config=replace(config,**updates)
    if config.enabled and (not config.base_url or not config.api_key_configured):
        raise ValueError('开启模型API查询前须填写Base URL和API Key')
    return set_api_model_query_config(values,config)
