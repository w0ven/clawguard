"""Run the SAME pinned upstream fixtures against pristine or integrated source.

No live model/TG/network. This is source behavior evidence, not a substitute for
CG authorization/HTTP/UI integration tests. Approved identity/topic/broker seams
are covered separately. Upstream files are read-only and are not rewritten.
"""
import argparse
import importlib
import json
import os
from pathlib import Path
import socket
import sys
import tempfile

parser=argparse.ArgumentParser()
parser.add_argument('mode',choices=['pristine','native'])
parser.add_argument('--source',type=Path,required=True)
parser.add_argument('--output',type=Path,required=True)
args=parser.parse_args()
root=Path(__file__).resolve().parents[1]
args.source=args.source.resolve();args.output=args.output.resolve();args.output.mkdir(parents=True,exist_ok=True)
os.environ['LITELLM_MODE']='PRODUCTION'
os.environ['LITELLM_LOCAL_MODEL_COST_MAP']='True'
os.environ['HF_HUB_OFFLINE']='1'
os.environ['PYTHONDONTWRITEBYTECODE']='1'
sys.dont_write_bytecode=True
sys.path.insert(0,str(root))
sys.path.insert(0,str(args.source if args.mode=='pristine' else root/'vendor/sgb'))

nodes=[
 'test_pending_reply_debounce.py::PendingReplyDebounceTests',
 'test_reply_output.py::ReplyOutputParserTests','test_reply_mode_batch.py',
 'test_group_reply_targets.py','test_memory_archive_unit.py','test_memory_recall_skill.py',
 'test_memory_archive_integration.py','test_archive_vector_provider.py',
 'test_proactive.py::ProactiveStateTests','test_proactive.py::ProactiveDeliveryTests',
 'test_skill_service.py::SkillServiceFollowupSuppressionTests',
 'test_skill_service.py::SkillProgressCallbackTests','test_skill_service.py::SkillExecutionBoundaryTests',
 'test_telegram_send.py::TelegramMarkdownOutputTests','test_telegram_send.py::TelegramMarkdownDeliveryTests',
 'test_webfetch_skill.py','test_music_search_skill.py','test_mihomo_doc_skill.py',
 'test_routeros_doc_skill.py','test_movie_info_skill.py','test_send_sticker_skill.py',
]
forbidden=[]
connect=socket.socket.connect
connect_ex=socket.socket.connect_ex
resolve=socket.getaddrinfo

def deny_connect(self,address):
    if self.family in (socket.AF_INET,socket.AF_INET6):
        forbidden.append(('connect',str(address)));raise RuntimeError('source replay forbids network')
    return connect(self,address)

def deny_connect_ex(self,address):
    if self.family in (socket.AF_INET,socket.AF_INET6):
        forbidden.append(('connect_ex',str(address)));raise RuntimeError('source replay forbids network')
    return connect_ex(self,address)

def deny_resolve(host,*a,**kw):
    if host not in {'localhost','127.0.0.1','::1',None}:
        forbidden.append(('dns',str(host)));raise RuntimeError('source replay forbids DNS')
    return resolve(host,*a,**kw)
socket.socket.connect=deny_connect;socket.socket.connect_ex=deny_connect_ex;socket.getaddrinfo=deny_resolve

import pytest
class Evidence:
    def __init__(self):self.results=[]
    def pytest_runtest_logreport(self,report):
        if report.when=='call' or report.failed:self.results.append({'nodeid':report.nodeid,'when':report.when,'outcome':report.outcome})
plugin=Evidence()
with tempfile.TemporaryDirectory(prefix='source-replay-',dir=args.output) as work:
    os.environ['TMPDIR']=work;tempfile.tempdir=work;os.chdir(work)
    module=importlib.import_module('bot.handlers.group')
    expected=args.source if args.mode=='pristine' else root/'vendor/sgb'
    assert Path(module.__file__).resolve().is_relative_to(expected),module.__file__
    print('EXECUTED_SOURCE',args.mode,module.__file__,flush=True)
    result=pytest.main(['-v','-p','no:cacheprovider','--import-mode=importlib',*[(str(args.source/'tests')+'/'+node) for node in nodes]],plugins=[plugin])
(args.output/(args.mode+'-results.json')).write_text(json.dumps({'mode':args.mode,'module':module.__file__,'results':plugin.results,'network_attempts':forbidden},ensure_ascii=False,indent=2))
if forbidden:
    print('FORBIDDEN_NETWORK_ATTEMPTS',json.dumps(forbidden));sys.exit(3)
sys.exit(result)
