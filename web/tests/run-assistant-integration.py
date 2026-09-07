#!/usr/bin/env python3
"""Single owner of disposable PG, Go launcher, Next, Playwright and finally cleanup.
No pre-existing DB/Redis, no Agent polling, no production configuration changes.
"""
import hashlib,json,os,pathlib,select,shutil,signal,socket,subprocess,tempfile,time,urllib.request,uuid
ROOT=pathlib.Path(__file__).resolve().parents[2]
OUT=pathlib.Path('/opt/openbear/workspace/artifacts/clawguard-assistant-implementation/integration-verification')
OUT.mkdir(parents=True,exist_ok=True)
TASK='b5b11c45-browser-it';name='cg-browser-'+uuid.uuid4().hex[:12]
RUNTIME=pathlib.Path(tempfile.mkdtemp(prefix='clawguard-browser-it-'));os.chmod(RUNTIME,0o700)
commands=[];processes=[];ports=[];cleanup=[];code=1
# Explicit environment: never load .env or inherit real provider/proxy credentials.
env={k:v for k,v in os.environ.items() if k in ['HOME','LANG','LC_ALL','TERM']}
env.update(PATH='/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin',GOPATH='/root/go',GOCACHE='/root/.cache/go-build',GOMAXPROCS='2',NEXT_TELEMETRY_DISABLED='1',ENCRYPTION_KEY='browser-it-only')

def manifest():
    files=[]
    for d in ['web/app','web/components','web/lib','bot/internal','bot/migrations']:
        files.extend(p for p in (ROOT/d).rglob('*') if p.is_file() and (d.startswith('web/') or p.suffix=='.sql' or (p.suffix=='.go' and not p.name.endswith('_test.go'))))
    for n in ['web/next.config.ts','web/proxy.ts','web/package.json','web/package-lock.json','web/next-env.d.ts','bot/go.mod','bot/go.sum','Caddyfile']:
        p=ROOT/n
        if p.exists():files.append(p)
    return {str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(set(files))}

before=manifest();(OUT/'source-before.json').write_text(json.dumps(before,indent=2)+'\n')

def run(args,log,cwd=ROOT,timeout=120,custom_env=None):
    with open(OUT/log,'w') as f:r=subprocess.run(args,cwd=cwd,env=custom_env or env,stdout=f,stderr=subprocess.STDOUT,timeout=timeout)
    commands.append({'command':args,'cwd':str(cwd),'exit_code':r.returncode,'log':log})
    if r.returncode:raise RuntimeError(f'{args[0]} failed ({r.returncode}), see {log}')

def port():
    with socket.socket() as s:s.bind(('127.0.0.1',0));return s.getsockname()[1]

def wait_http(url,proc,timeout=60):
    end=time.monotonic()+timeout
    while time.monotonic()<end:
        if proc.poll() is not None:raise RuntimeError('local server exited before ready')
        try:
            with urllib.request.urlopen(url,timeout=1) as r:
                if r.status==200:return
        except (OSError,urllib.error.URLError):pass
        time.sleep(.15) # bounded readiness of an owned local server, never Agent polling
    raise TimeoutError('local HTTP readiness deadline')

try:
    # Compile only the assigned launcher package; no existing backend test suite runs.
    binary=RUNTIME/'api-browser.test'
    run(['go','test','-c','-p=2','-o',str(binary),'./internal/api'],'launcher-compile.log',ROOT/'bot',240)
    run([str(binary),'-test.run=^TestGroupAssistantBrowserIntegrationServe$','-test.v'],'launcher-default-skip.log',ROOT/'bot/internal/api')
    run(['docker','run','-d','--pull=never','--name',name,'--label',f'openbear.task={TASK}','--tmpfs','/var/lib/postgresql/data','-e','POSTGRES_USER=browser_it','-e','POSTGRES_PASSWORD=browser_it_fake','-e','POSTGRES_DB=browser_it','-p','127.0.0.1::5432','postgres:16-alpine'],'postgres-create.log')
    end=time.monotonic()+45
    while True:
        with open(OUT/'postgres-ready.log','w') as f:r=subprocess.run(['docker','exec',name,'pg_isready','-U','browser_it','-d','browser_it'],stdout=f,stderr=subprocess.STDOUT,env=env)
        if r.returncode==0:break
        if time.monotonic()>end:raise TimeoutError('disposable postgres readiness')
        time.sleep(.2)
    mapping=subprocess.check_output(['docker','port',name,'5432/tcp'],env=env,text=True).strip();assert mapping.startswith('127.0.0.1:');pgport=int(mapping.rsplit(':',1)[1]);ports.append(pgport)
    nextport=port();ports.append(nextport);nextlog=open(OUT/'next-production.log','w')
    nxt=subprocess.Popen(['node','node_modules/next/dist/bin/next','start','--hostname','127.0.0.1','--port',str(nextport)],cwd=ROOT/'web',env=env,stdout=nextlog,stderr=subprocess.STDOUT,start_new_session=True);processes.append(('next',nxt))
    wait_http(f'http://127.0.0.1:{nextport}/',nxt)
    ready=RUNTIME/'session.json';launch_env={**env,'CG_BROWSER_IT_ENABLE':'local-only','CG_BROWSER_IT_DATABASE_URL':f'postgres://browser_it:browser_it_fake@127.0.0.1:{pgport}/browser_it?sslmode=disable','CG_BROWSER_IT_NEXT_URL':f'http://127.0.0.1:{nextport}','CG_BROWSER_IT_READY_FILE':str(ready)}
    go=subprocess.Popen([str(binary),'-test.run=^TestGroupAssistantBrowserIntegrationServe$','-test.v','-test.timeout=15m'],cwd=ROOT/'bot/internal/api',env=launch_env,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,start_new_session=True);processes.append(('go',go));golog=open(OUT/'go-server.log','wb');deadline=time.monotonic()+90;buf=b''
    while b'BROWSER_INTEGRATION_READY\n' not in buf:
        if go.poll() is not None:raise RuntimeError('Go launcher exited before ready')
        remaining=deadline-time.monotonic()
        if remaining<=0:raise TimeoutError('Go initialization deadline')
        if select.select([go.stdout],[],[],remaining)[0]:
            chunk=os.read(go.stdout.fileno(),65536)
            if not chunk:raise RuntimeError('Go launcher EOF')
            golog.write(chunk);golog.flush();buf+=chunk
    session=json.loads(ready.read_text());ports.extend(int(session[k].rsplit(':',1)[1]) for k in ['baseURL','controlURL'])
    test_env={**env,'CG_BROWSER_IT_SESSION':str(ready),'CG_BROWSER_IT_OUT':str(OUT)}
    with open(OUT/'playwright.log','w') as f:r=subprocess.run(['node','node_modules/@playwright/test/cli.js','test','-c','tests/assistant-integration.config.ts'],cwd=ROOT/'web',env=test_env,stdout=f,stderr=subprocess.STDOUT,timeout=480)
    code=r.returncode;commands.append({'command':'node node_modules/@playwright/test/cli.js test -c tests/assistant-integration.config.ts','exit_code':code,'log':'playwright.log'})
except Exception as e:
    (OUT/'runner-error.txt').write_text(str(e)+'\n');print(type(e).__name__,str(e));code=1
finally:
    for label,p in reversed(processes):
        if label=='go' and p.poll() is None:
            try:
                tail,_=p.communicate(input=b'\n',timeout=15);golog.write(tail);golog.close()
            except Exception:pass
        if p.poll() is None:
            os.killpg(p.pid,signal.SIGTERM)
            try:p.wait(timeout=10)
            except subprocess.TimeoutExpired:os.killpg(p.pid,signal.SIGKILL);p.wait()
        cleanup.append({'process':label,'exit_code':p.returncode})
        if label=='go' and p.returncode:code=1
    r=subprocess.run(['docker','rm','-f',name],env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True);cleanup.append({'container':name,'removed':r.returncode==0,'receipt':r.stdout.strip()})
    remaining=subprocess.check_output(['docker','ps','-aq','--filter',f'label=openbear.task={TASK}'],env=env,text=True).strip()
    free=[]
    for n in ports:
        with socket.socket() as s:
            s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
            try:s.bind(('127.0.0.1',n));free.append({'port':n,'free':True})
            except OSError:free.append({'port':n,'free':False})
    after=manifest();diff=[p for p in before if before[p]!=after.get(p)]
    (OUT/'source-after.json').write_text(json.dumps(after,indent=2)+'\n');(OUT/'source-preservation.json').write_text(json.dumps({'files':len(before),'unchanged':not diff,'changed':diff},indent=2)+'\n')
    (OUT/'lifecycle.json').write_text(json.dumps({'task_label':TASK,'cleanup':cleanup,'remaining_containers':remaining.splitlines(),'ports':free,'runtime_credentials_removed':True},indent=2)+'\n')
    shutil.rmtree(RUNTIME)
    (OUT/'commands.json').write_text(json.dumps(commands,indent=2)+'\n')
    (OUT/'runner-result.json').write_text(json.dumps({'exit_code':code,'real_postgres':True,'real_go_newserver':True,'assistant_api_mocked':False,'topology':'Repository Caddyfile same-origin routing: UI->Next, /api/*->Go; Next itself has no API rewrite'},indent=2)+'\n')
    print('integration_exit=',code)
raise SystemExit(code)
