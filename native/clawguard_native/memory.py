"""Authorization seam around the real source memory transactions."""
from bot.services.memory import MemoryService as SourceMemory
from .boundary import execution


class MemoryService(SourceMemory):
    async def _authorize_write(self,group_id):
        ctx = execution.get(None)
        # Admin HTTP requests have already passed CG's group/role authorization.
        # Chat tools must revalidate their signed source and actor at mutation,
        # not just before the potentially long model round.
        if ctx is not None and ctx.turn:
            if group_id != ctx.group_id:
                raise PermissionError("memory write scope mismatch")
            await ctx.broker.call("authorize-memory",{})

    async def add_permanent_memory(self,group_id,content,**kwargs):
        await self._authorize_write(group_id)
        return await super().add_permanent_memory(group_id,content,**kwargs)

    async def replace_permanent_memory(self,group_id,*args,**kwargs):
        await self._authorize_write(group_id)
        return await super().replace_permanent_memory(group_id,*args,**kwargs)

    async def delete_permanent_memory(self,group_id,*args,**kwargs):
        await self._authorize_write(group_id)
        return await super().delete_permanent_memory(group_id,*args,**kwargs)

    async def clear_permanent_memory(self,group_id):
        await self._authorize_write(group_id)
        return await super().clear_permanent_memory(group_id)
