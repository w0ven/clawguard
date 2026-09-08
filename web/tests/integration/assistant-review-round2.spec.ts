// Formal Round2 regression; helpers adapted from the preserved independent probe.
import {test as base,expect,type Page,type TestInfo} from '@playwright/test';
import fs from 'node:fs';
const session=JSON.parse(fs.readFileSync(process.env.CG_BROWSER_IT_SESSION!,'utf8'));
const out=process.env.CG_BROWSER_IT_OUT!;
const G='/api/admin/assistant/global';const pp=(chat=-88001)=>`/api/admin/groups/${chat}/assistant/model-pool`;
const h={'X-CSRF-Token':session.csrf};const data=(name:string,v:any)=>fs.writeFileSync(`${out}/${name}.json`,JSON.stringify(v,null,2));
const test=base.extend<{journal:any}>({journal:[async({page}:{page:Page},use:(value:any)=>Promise<void>,info:TestInfo)=>{
 const j:any={requests:[],api:[],pageErrors:[],failed:[],externalBlocked:[]};const pending:Promise<void>[]=[];
 page.on('pageerror',e=>j.pageErrors.push(e.message));page.on('request',r=>{if(new URL(r.url()).pathname.startsWith('/api/'))j.requests.push({path:new URL(r.url()).pathname,method:r.method()});});
 page.on('requestfailed',r=>j.failed.push({path:new URL(r.url()).pathname,error:r.failure()?.errorText}));
 page.on('response',r=>{if(new URL(r.url()).pathname.startsWith('/api/'))pending.push((async()=>{let body;try{body=await r.json();}catch{return;}j.api.push({path:new URL(r.url()).pathname,method:r.request().method(),status:r.status(),request:r.request().postData(),body});})());});
 await page.route('**/*',r=>{if(new URL(r.request().url()).hostname==='127.0.0.1')return r.continue();j.externalBlocked.push(r.request().url());return r.fulfill({body:'',contentType:'application/javascript'});});
 await use(j);await Promise.all(pending);data(info.title.split(' ')[0]+'-journal',j);expect.soft(j.pageErrors).toEqual([]);
},{auto:true}]});
async function login(page:Page){await page.context().addCookies([{name:'cg_admin',value:session.ownerToken,url:session.baseURL,httpOnly:true},{name:'cg_csrf',value:session.csrf,url:session.baseURL}]);}
async function api(page:Page,path:string){const r=await page.request.get(path);expect(r.status(),await r.text()).toBe(200);return r.json();}
async function control(page:Page,path:string){const r=await page.request.post(session.controlURL+path,{headers:{'X-Browser-Fixture':session.controlKey}});expect(r.status(),await r.text()).toBe(200);return r.json();}
async function snapshot(page:Page){const r=await page.request.get(session.controlURL+'/snapshot',{headers:{'X-Browser-Fixture':session.controlKey}});expect(r.status()).toBe(200);return r.json();}
function globalWrite(v:any){const {app_key_configured,access_key_configured,...tts}=v.tts;return {expected_version:v.version,model_roles:v.model_roles,bot:v.bot,tts,stickers:v.stickers};}
async function setGlobal(page:Page,roles:any){await login(page);const b=globalWrite(await api(page,G));b.model_roles=roles;const r=await page.request.put(G,{headers:h,data:b});expect(r.status(),await r.text()).toBe(200);return r.json();}
const single={model_ref:'browser:tools',fallbacks:[],strategy:'weighted',model_options:{'browser:tools':{weight:8,max_concurrency:2,timeout_ms:12000,cooldown_duration_sec:30}}};
async function baseline(page:Page){await setGlobal(page,{main:{...single,fallbacks:['browser:tools2','browser:tools3']}});const p=await api(page,pp());const endpoints=p.config.endpoints.map((e:any,i:number)=>({...e,weight:i===0?7:3}));const r=await page.request.put(pp(),{headers:h,data:{strategy:'weighted',inherit_global:false,expected_version:p.version,task_assignments:{chat:p.config.task_assignments.chat},endpoints,max_queue_depth:10,max_queue_wait_sec:15}});expect(r.status(),await r.text()).toBe(200);}
async function open(page:Page,chat=-88001){await login(page);await page.goto(`/groups/${chat}/assistant`);await page.getByRole('button',{name:'全局',exact:true}).click();await expect(page.getByLabel('全局负载策略')).toBeVisible();}
async function reload(page:Page){await page.reload();await page.getByRole('button',{name:'全局',exact:true}).click();await expect(page.getByLabel('全局负载策略')).toBeVisible();}
async function save(page:Page,path:string,label:string,status=200){const waiting=page.waitForResponse(r=>new URL(r.url()).pathname===path&&r.request().method()==='PUT');const prompts=path===G&&status===200?page.waitForResponse(r=>new URL(r.url()).pathname==='/api/admin/assistant/prompts'&&r.request().method()==='PUT'):null;await page.getByRole('button',{name:label,exact:true}).click();const r=await waiting;expect(r.status(),await r.text()).toBe(status);if(prompts)expect((await prompts).status()).toBe(200);return r.json();}
const card=(page:Page,title:string)=>page.locator('.glass-panel').filter({has:page.getByRole('heading',{name:title,exact:true})}).first();
const group=(page:Page)=>card(page,'助手共享模型负载');const roles=(page:Page)=>card(page,'模型角色');
async function shot(page:Page,name:string,title?:string){fs.mkdirSync(`${out}/screenshots`,{recursive:true});if(title)await card(page,title).screenshot({path:`${out}/screenshots/${name}.png`});else await page.screenshot({path:`${out}/screenshots/${name}.png`,fullPage:true});}
function refs(p:any,task='chat',key='config'){const c=p[key],a=c.task_assignments[task];const ids=typeof a==='string'?[a]:[a?.primary,...(a?.backups??[])];return ids.filter(Boolean).map(id=>c.endpoints.find((e:any)=>e.id===id)?.model_ref);}
const writes=(j:any)=>j.requests.filter((r:any)=>!['GET','HEAD'].includes(r.method));
async function expand(page:Page,task:string){const d=group(page).locator('details').filter({has:page.locator('summary').filter({hasText:`${task}路由`})});if(!await d.getAttribute('open').then(v=>v!==null))await d.locator('summary').click();}
async function addLearning(page:Page){await expand(page,'学习');await expect(page.getByLabel('学习模型 1',{exact:true})).toHaveValue('browser:text');for(const [i,ref] of ['browser:tools2','browser:tools3'].entries()){await page.getByRole('button',{name:'添加学习备用模型',exact:true}).click();await page.getByLabel(`学习模型 ${i+2}`,{exact:true}).selectOption(ref);}}
async function invariants(page:Page,chat:number){return {snapshot:await snapshot(page),global:await api(page,G),registry:await api(page,'/api/admin/llm/models'),prompts:await api(page,`/api/admin/groups/${chat}/assistant/prompts`),globalPrompts:await api(page,'/api/admin/assistant/prompts')};}
function unchangedOutsidePool(before:any,after:any){for(const key of ['global','registry','prompts','globalPrompts'])expect(after[key]).toEqual(before[key]);for(const key of ['policies','groups','global_config','memories','versions','conflicts','messages'])expect(after.snapshot[key]).toEqual(before.snapshot[key]);}

test('R2-legacy learning-only multiple backups preserve inheritance, conflict/network drafts and independent cancel',async({page,journal})=>{
 const chat=-88003;await setGlobal(page,{main:{...single,fallbacks:['browser:tools2','browser:tools3']}});await open(page,chat);
 const raw=await api(page,pp(chat)),before=await invariants(page,chat);expect(raw.source).toBe('legacy_group');expect(Object.keys(raw.saved_config.task_assignments)).toEqual(['chat']);expect(raw.config.task_assignments.learning.backups).toBeNull();expect(raw.inherited_tasks).toEqual(['decision','vision','compress','vector']);
 await addLearning(page);await page.getByLabel('主模型权重 1',{exact:true}).fill('31');
 // A separate real writer bumps the row version, then the UI must fail honestly.
 const bumped=await page.request.put(pp(chat),{headers:h,data:{...raw.saved_config,strategy:raw.strategy,expected_version:raw.version}});expect(bumped.status(),await bumped.text()).toBe(200);
 await save(page,pp(chat),'保存本群负载',409);await expect(page.getByLabel('学习模型 3',{exact:true})).toHaveValue('browser:tools3');await expect(page.getByLabel('主模型权重 1',{exact:true})).toHaveValue('31');
 const conflictDB=await api(page,pp(chat));expect(refs(conflictDB,'learning')).toEqual(['browser:text']);
 await page.route('**'+pp(chat),r=>r.request().method()==='PUT'?r.abort('failed'):r.continue());await page.getByRole('button',{name:'保存本群负载',exact:true}).click();await expect(group(page).getByRole('alert')).toBeVisible();await expect(page.getByLabel('学习模型 3',{exact:true})).toHaveValue('browser:tools3');await page.unroute('**'+pp(chat));
 await shot(page,'legacy-409-network-drafts','助手共享模型负载');await page.getByRole('button',{name:'取消负载修改',exact:true}).click();await expect(page.getByLabel('学习模型 2',{exact:true})).toHaveCount(0);await expect(page.getByLabel('主模型权重 1',{exact:true})).toHaveValue('31');
 await page.getByRole('button',{name:'取消全局修改',exact:true}).click();await reload(page);await addLearning(page);
 for(const task of ['视觉','向量']){await expand(page,task);await expect(page.getByLabel(`${task}路由来源`)).toHaveValue('inherit');}
 const saved=await save(page,pp(chat),'保存本群负载');expect(saved.source).toBe('group');expect(refs(saved,'learning')).toEqual(['browser:text','browser:tools2','browser:tools3']);expect(saved.saved_config.task_assignments.chat).toEqual(raw.saved_config.task_assignments.chat);
 for(const task of ['vision','vector']){expect(saved.saved_config.task_assignments[task].primary).toBe('');expect(saved.saved_config.task_assignments[task].backups).toEqual([]);expect(refs(saved,task)).toEqual(refs(saved,'chat'));}
 for(const key of ['temperature','max_tokens','strategy'])expect(saved.saved_config.task_assignments.learning[key]).toEqual(raw.config.task_assignments.learning[key]);
 await reload(page);await expand(page,'学习');await expect(page.getByLabel('学习模型 3',{exact:true})).toHaveValue('browser:tools3');expect((await api(page,pp(chat))).saved_config).toEqual(saved.saved_config);
 await page.setViewportSize({width:390,height:844});await shot(page,'legacy-learning-multiple-persisted-mobile','助手共享模型负载');expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBe(390);
 // Restoring global and reopening custom retains the saved learning draft.
 await page.getByLabel('本群模型配置来源').selectOption('global');await save(page,pp(chat),'保存本群负载');await reload(page);await page.getByLabel('本群模型配置来源').selectOption('custom');await expand(page,'学习');await expect(page.getByLabel('学习模型 3',{exact:true})).toHaveValue('browser:tools3');await page.getByRole('button',{name:'取消负载修改',exact:true}).click();await expect(page.getByLabel('本群模型配置来源')).toHaveValue('global');
 const after=await invariants(page,chat);unchangedOutsidePool(before,after);const row=after.snapshot.pools.find((p:any)=>p.chat_id===chat);expect(row.config.task_assignments.learning).toEqual(saved.saved_config.task_assignments.learning);
 expect(writes(journal).filter((r:any)=>r.path!==pp(chat))).toEqual([]);data('R2-legacy-evidence',{raw,before,conflictDB,saved,after,persistedRow:row});
});

test('R2-explicit legacy equal-chain child and custom learning parameters remain explicit',async({page})=>{
 const chat=-88005;await setGlobal(page,{main:{...single,fallbacks:['browser:tools2','browser:tools3']}});await open(page,chat);const raw=await api(page,pp(chat)),before=await invariants(page,chat);expect(raw.source).toBe('legacy_group');expect(raw.saved_config.task_assignments.decision).toEqual(raw.saved_config.task_assignments.chat);expect(raw.inherited_tasks).not.toContain('decision');
 await expand(page,'决策');await expect(page.getByLabel('决策路由来源')).toHaveValue('custom');await addLearning(page);const saved=await save(page,pp(chat),'保存本群负载');expect(saved.saved_config.task_assignments.decision).toEqual(raw.saved_config.task_assignments.decision);
 const old=raw.config.task_assignments.learning,now=saved.saved_config.task_assignments.learning;for(const key of ['temperature','max_tokens','strategy'])expect(now[key]).toEqual(old[key]);const oldEp=raw.config.endpoints.find((e:any)=>e.id===old.primary),newEp=saved.config.endpoints.find((e:any)=>e.id===now.primary);for(const key of ['model_ref','weight','max_concurrency','timeout_ms','cooldown_duration_sec'])expect(newEp[key]).toEqual(oldEp[key]);
 await reload(page);await expand(page,'决策');await expect(page.getByLabel('决策路由来源')).toHaveValue('custom');await expand(page,'学习');await expect(page.getByLabel('学习模型 3',{exact:true})).toHaveValue('browser:tools3');const after=await invariants(page,chat);unchangedOutsidePool(before,after);await shot(page,'explicit-same-chain-and-learning-parameters','助手共享模型负载');data('R2-explicit-evidence',{raw,saved,before,after});
});

test('R2-global projected inherited and explicit equal-model routes keep distinct provenance',async({page})=>{
 const chat=-88004;await setGlobal(page,{main:single,decision:single});await open(page,chat);const raw=await api(page,pp(chat));expect(raw.source).toBe('global');expect(raw.inherited_tasks).not.toContain('decision');expect(refs(raw,'decision')).toEqual(refs(raw,'chat'));await page.getByLabel('本群模型配置来源').selectOption('custom');await expand(page,'决策');await expect(page.getByLabel('决策路由来源')).toHaveValue('custom');await expand(page,'向量');await expect(page.getByLabel('向量路由来源')).toHaveValue('inherit');const saved=await save(page,pp(chat),'保存本群负载');expect(saved.inherited_tasks).not.toContain('decision');expect(refs(saved,'decision')).toEqual(refs(raw,'decision'));await reload(page);await expand(page,'决策');await expect(page.getByLabel('决策路由来源')).toHaveValue('custom');data('R2-global-evidence',{raw,saved,reloaded:await api(page,pp(chat))});
});


