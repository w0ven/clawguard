"""Only /lm's authorization transport: CG owns current group/admin grants.

The source command, parser, memory skill, list renderer and transactions run
unchanged. No native delegated-admin database becomes an alternate authority.
"""
from .boundary import execution


async def is_group_admin_authorized(session,group_id,user_id):
    ctx=execution.get()
    if ctx.group_id!=int(group_id) or not ctx.turn:
        return False
    try:
        return bool(await ctx.broker.call('authorize-memory',{}))
    except (RuntimeError,PermissionError):
        return False


async def is_group_authorized(session,group_id):
    # /lm is admin-only; checking both here also prevents stale local grants.
    return await is_group_admin_authorized(session,group_id,0)


async def ensure_group_authorized(message,session,settings,**kwargs):
    if await is_group_authorized(session,message.chat.id):
        return True
    await message.answer('仅已授权群的管理员可管理永久记忆。')
    return False


async def ensure_group_admin_permission(message,session,settings,**kwargs):
    return await ensure_group_authorized(message,session,settings,**kwargs)
