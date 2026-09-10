"""Source group settings/secret transactions; no alternate model provider pool."""
from dataclasses import asdict,replace

from bot.services.api_model_query import (get_api_model_query_config,set_api_model_query_config,
    normalize_api_model_query_base_url,replace_group_api_model_query_secret,clear_group_api_model_query_secret,
    group_api_model_query_secret_exists,api_model_query_tool_enabled)
from bot.services.at_reply import is_at_reply_enabled
from bot.services.doubao_tts import normalize_tts_mode,is_tts_tool_enabled
from bot.services.proactive import get_cooldown_task_state,is_cooldown_task_enabled
from bot.services.skills import SkillService


INTERJECTION_MODES = frozenset({"balanced", "engaged"})
DEFAULT_INTERJECTION_MODE = "balanced"

# ClawGuard adapter policy: this is deliberately request-scoped and is never
# written into the shared/source decision Prompt.  The source decision rules
# remain authoritative; this block only gives an explicitly opted-in group a
# narrower, more useful unsolicited-interjection preference.
_ENGAGED_INTERJECTION_POLICY = """[CLAWGUARD_GROUP_INTERJECTION_POLICY]
This trusted policy is active only when this group's exact interjection_mode is `engaged`. It supplements the active decision Prompt and never overrides it.
For an ordinary group message that does not directly address the bot, prefer `casual` when the bot can add a short, natural, specific, timely, non-repetitive contribution now. Do not wait for a mention. Do not answer every message: casual acknowledgements, low-value chatter, and messages with no concrete contribution remain `skip`. Use recent group context to avoid repeating the bot or interrupting a human exchange.
Prefer `casual` when the current message or recent context is about Po0, 5gpn, RFCHOST, KFCHOST, wiki tutorials, getting-started guides, airport/proxy/node/subscription, client configuration, troubleshooting, or another concrete how-to / error / product question the bot can teach or clarify. These are high-value teaching turns, not chatter.
Preserve every existing hard-skip rule. In particular, do not reply to pure emoji/sticker/GIF content without a clear question, a pure link with no comment or question, a clear two-person/private-style exchange, or a message with [MENTIONS_OTHER_USER]=yes when [IS_MENTIONED]=no and [IS_REPLY_TO_BOT]=no. Preserve the existing [IS_REPLY_TO_OTHER]=yes rule and its human-conversation safeguard; do not treat a reply to another member as an invitation to the bot unless the existing decision rules clearly identify a pivot toward the bot. Single-word acknowledgements that are not aimed at the bot remain `skip`.
Never change the mandatory behavior for an explicit bot mention or a reply to the bot. [SENDER_IS_OWNER] and [SENDER_IS_TG_ADMIN] are identity metadata only and must not lower the threshold. Do not implement or promise a fixed reply probability or frequency.
Keep the strict output contract: output exactly one lowercase word, only `skip` or `casual`, with no explanation or additional text."""


def normalize_interjection_mode(value: object) -> str:
    """Validate the persisted group-level interjection mode exactly."""
    if not isinstance(value, str) or value not in INTERJECTION_MODES:
        raise ValueError("invalid interjection mode")
    return value


def effective_interjection_mode(values: dict | None) -> str:
    """Return the group mode; a missing key intentionally means balanced."""
    values = values or {}
    if "interjection_mode" not in values:
        return DEFAULT_INTERJECTION_MODE
    return normalize_interjection_mode(values["interjection_mode"])


def decision_system_policy(interjection_mode: object) -> str:
    """Return only the fixed trusted policy for a validated group mode."""
    mode = normalize_interjection_mode(interjection_mode)
    return _ENGAGED_INTERJECTION_POLICY if mode == "engaged" else ""


def effective_group(values,settings,llm):
    values=dict(values or {})
    values['interjection_mode']=effective_interjection_mode(values)
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
