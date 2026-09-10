"""Stable transport identity for the source's durable proactive activity claim."""
from bot.services.proactive import ProactiveTopicService as SourceProactive, get_cooldown_task_state
from .boundary import execution, Execution


class ProactiveTopicService(SourceProactive):
    async def _run_group_cooldown_task(self,group_id,*,now):
        original = execution.get()
        state = get_cooldown_task_state(await self._load_group_settings(group_id))
        revision = int(state.get("activity_revision",0))
        token = execution.set(Execution(original.broker,group_id,original.topic_id,original.grant,
                                        f"proactive:{group_id}:{original.topic_id}:{revision}"))
        try:
            return await super()._run_group_cooldown_task(group_id,now=now)
        finally:
            execution.reset(token)
