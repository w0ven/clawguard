"""Select the real source cleanup scheduler using the current topic data scope."""
from bot.services import memory_holder


class ScopedCleanup:
    def _scheduler(self):
        memory = memory_holder.get_optional()
        scheduler = getattr(memory,"cg_cleanup",None) if memory is not None else None
        if scheduler is None:
            raise RuntimeError("native topic cleanup scheduler is not initialized")
        return scheduler

    def enqueue(self,*,chat_id,message_id,due_at):
        return self._scheduler().enqueue(chat_id=chat_id,message_id=message_id,due_at=due_at)

    async def enqueue_durable(self,*,chat_id,message_id,due_at):
        return await self._scheduler().enqueue_durable(chat_id=chat_id,message_id=message_id,due_at=due_at)
