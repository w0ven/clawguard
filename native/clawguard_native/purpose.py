"""Transport-only purpose metadata. Never changes source progress scheduling."""
from contextvars import ContextVar
from functools import wraps
from inspect import iscoroutinefunction

purpose: ContextVar[str] = ContextVar("cg_delivery_purpose",default="reply")


def progress_transport(cls):
    def decorate(method):
        @wraps(method)
        async def wrapped(*args,**kwargs):
            token = purpose.set("progress")
            try:
                return await method(*args,**kwargs)
            finally:
                purpose.reset(token)
        return wrapped
    for name,method in list(vars(cls).items()):
        if iscoroutinefunction(method):
            setattr(cls,name,decorate(method))
    return cls
