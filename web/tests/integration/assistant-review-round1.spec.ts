import {test as base,expect,type Page,type TestInfo} from '@playwright/test';
import fs from 'node:fs';
const session=JSON.parse(fs.readFileSync(process.env.CG_BROWSER_IT_SESSION!,'utf8'));
const out=process.env.CG_BROWSER_IT_OUT!;
const G='/api/admin/assistant/global';
const poolPath=(chat=-88001)=>`/api/admin/groups/${chat}/assistant/model-pool`;
const headers={'X-CSRF-Token':session.csrf};
const putJSON=(v:any)=>{const {app_key_configured,access_key_configured,...tts}=v.tts;return {expected_version:v.version,model_roles:v.model_roles,bot:v.bot,tts,stickers:v.stickers};};
const data=(name:string,v:any)=>fs.writeFileSync(`${out}/${name}.json`,JSON.stringify(v,null,2));
const test=base.extend<{journal:any}>({journal:[async({page}:{page:Page},use:(journal:any)=>Promise<void>,info:TestInfo)=>{
 const j:any={api:[],pageErrors:[],requestFailures:[],externalBlocked:[]};const pending:Promise<void>[]=[];
 page.on('pageerror',e=>j.pageErrors.push(e.message));page.on('requestfailed',r=>j.requestFailures.push({path:new URL(r.url()).pathname,error:r.failure()?.errorText}));
 page.on('response',r=>{if(new URL(r.url()).pathname.startsWith('/api/'))pending.push((async()=>{let body;try{body=await r.json();}catch{return;}j.api.push({path:new URL(r.url()).pathname,method:r.request().method(),status:r.status(),request:r.request().postData(),body});})());});
 await page.route('**/*',r=>{if(new URL(r.request().url()).hostname==='127.0.0.1')return r.continue();j.externalBlocked.push(r.request().url());return r.fulfill({body:'',contentType:'application/javascript'});});
 await use(j);await Promise.all(pending);data(info.title.split(' ')[0]+'-journal',j);expect.soft(j.pageErrors).toEqual([]);
},{auto:true}]});
async function open(page:Page,chat=-88001){await page.context().addCookies([{name:'cg_admin',value:session.ownerToken,url:session.baseURL,httpOnly:true},{name:'cg_csrf',value:session.csrf,url:session.baseURL}]);await page.goto(`/groups/${chat}/assistant`);await page.getByRole('button',{name:'全局',exact:true}).click();await expect(page.getByLabel('全局负载策略')).toBeVisible();await expect(page.getByLabel('主模型模型 1',{exact:true}).locator('option[value="browser:tools"]')).toHaveCount(1);}
async function reopen(page:Page,chat=-88001){await page.reload();await page.getByRole('button',{name:'全局',exact:true}).click();await expect(page.getByLabel('全局负载策略')).toBeVisible();}
async function save(page:Page,path:string,label:string,status=200){const waiting=page.waitForResponse(r=>new URL(r.url()).pathname===path&&r.request().method()==='PUT');const prompts=path===G&&status===200?page.waitForResponse(r=>new URL(r.url()).pathname==='/api/admin/assistant/prompts'&&r.request().method()==='PUT'):null;await page.getByRole('button',{name:label,exact:true}).click();const r=await waiting;expect(r.status(),await r.text()).toBe(status);if(prompts)expect((await prompts).status()).toBe(200);return r.json();}
async function api(page:Page,path:string){const r=await page.request.get(path);expect(r.status(),await r.text()).toBe(200);return r.json();}
async function snapshot(page:Page){const r=await page.request.get(session.controlURL+'/snapshot',{headers:{'X-Browser-Fixture':session.controlKey}});expect(r.status()).toBe(200);return r.json();}
const card=(page:Page,title:string)=>page.locator('.glass-panel').filter({has:page.getByRole('heading',{name:title,exact:true})}).first();
async function shot(page:Page,name:string,title?:string){fs.mkdirSync(`${out}/screenshots`,{recursive:true});if(title)await card(page,title).screenshot({path:`${out}/screenshots/${name}.png`});else await page.screenshot({path:`${out}/screenshots/${name}.png`,fullPage:true});}
function chain(p:any,task='chat'){const a=p.config.task_assignments[task];return [a.primary,...a.backups].filter(Boolean).map(id=>p.config.endpoints.find((e:any)=>e.id===id)?.model_ref);}
async function editRole(page:Page,label:string,refs:string[]){await page.getByLabel(`${label}模型 1`,{exact:true}).selectOption(refs[0]);for(let i=1;i<refs.length;i++){await page.getByRole('button',{name:`添加${label}备用模型`,exact:true}).click();await page.getByLabel(`${label}模型 ${i+1}`,{exact:true}).selectOption(refs[i]);}}

// Regression copies of the original FE-01/02/03/04 probes. Product assertions retained.
test.beforeAll(async({playwright})=>{
 const request=await playwright.request.newContext({baseURL:session.baseURL,extraHTTPHeaders:{Cookie:`cg_admin=${session.ownerToken}; cg_csrf=${session.csrf}`,'X-CSRF-Token':session.csrf}});
 const g=await (await request.get(G)).json();const body=putJSON(g);
 body.model_roles={main:{model_ref:'browser:tools',fallbacks:['browser:tools2','browser:tools3'],strategy:'weighted',model_options:{'browser:tools':{weight:8,max_concurrency:2,timeout_ms:12000,cooldown_duration_sec:30}}}};
 let r=await request.put(G,{data:body});expect(r.status(),await r.text()).toBe(200);
 const p=await (await request.get(poolPath())).json();const endpoints=p.config.endpoints.map((e:any,i:number)=>({...e,weight:i===0?7:3}));
 r=await request.put(poolPath(),{data:{strategy:'weighted',inherit_global:false,expected_version:p.version,task_assignments:{chat:p.config.task_assignments.chat},endpoints,max_queue_depth:10,max_queue_wait_sec:15}});expect(r.status(),await r.text()).toBe(200);
 await request.dispose();
});
test('F04 group dirty guards tab group link and browser history',async({page})=>{
 await open(page);await page.getByLabel('聊天权重 1',{exact:true}).fill('21');const messages:string[]=[];page.on('dialog',async d=>{messages.push(d.message());await d.dismiss();});
 await page.getByRole('button',{name:'开始使用',exact:true}).click();await expect(page.getByLabel('聊天权重 1',{exact:true})).toHaveValue('21');
 await page.getByLabel('切换群组').selectOption('-88002');await expect(page).toHaveURL(/-88001\/assistant$/);await page.getByRole('link',{name:'查看现有群审核策略（只读链接）'}).click();await expect(page).toHaveURL(/-88001\/assistant$/);await page.evaluate(()=>history.back());await expect.poll(()=>messages.length).toBeGreaterThanOrEqual(4);await expect(page.getByLabel('聊天权重 1',{exact:true})).toHaveValue('21');data('F04-evidence',{messages});await page.getByRole('button',{name:'取消负载修改'}).click();
});
test('F05 global-only dirty tab change must warn and retain on dismiss',async({page})=>{
 await open(page);const before=await api(page,G);await page.getByLabel('主模型权重 1',{exact:true}).fill('31');const messages:string[]=[];page.on('dialog',async d=>{messages.push(d.message());await d.dismiss();});await page.getByRole('button',{name:'回复与媒体',exact:true}).click();const stillGlobal=await page.getByLabel('主模型权重 1',{exact:true}).count();if(!stillGlobal)await page.getByRole('button',{name:'全局',exact:true}).click();await expect(page.getByLabel('全局负载策略')).toBeVisible();data('F05-evidence',{messages,valueAfter:returnValue(await page.getByLabel('主模型权重 1',{exact:true}).inputValue()),savedWeight:before.model_roles.main.model_options['browser:tools'].weight});await shot(page,'05-global-draft-after-tab-loss','模型角色');expect.soft(messages,'合同要求全局草稿离开提醒').not.toEqual([]);await expect(page.getByLabel('主模型权重 1',{exact:true}),'拒绝离开应保留31').toHaveValue('31');
});
test('F06 workspace refresh must guard global and group unsaved drafts',async({page})=>{
 await open(page);await page.getByLabel('聊天权重 1',{exact:true}).fill('41');await page.getByLabel('主模型权重 1',{exact:true}).fill('42');const messages:string[]=[];page.on('dialog',async d=>{messages.push(d.message());await d.dismiss();});await page.getByRole('button',{name:'刷新',exact:true}).first().click();await expect(page.getByLabel('全局负载策略')).toBeVisible();const values={group:await page.getByLabel('聊天权重 1',{exact:true}).inputValue(),global:await page.getByLabel('主模型权重 1',{exact:true}).inputValue()};data('F06-evidence',{messages,values});await shot(page,'06-refresh-draft-loss','助手共享模型负载');expect.soft(messages,'刷新应提醒两个独立草稿').not.toEqual([]);expect(values).toEqual({group:'41',global:'42'});
});
test('F07 legacy source preserves enabled learning prompts and moderation',async({page})=>{
 await open(page,-88003);const before=await snapshot(page),registry=await api(page,'/api/admin/llm/models'),prompts=await api(page,'/api/admin/groups/-88003/assistant/prompts');let p=await api(page,poolPath(-88003));expect(p.source).toBe('legacy_group');expect(chain(p)).toEqual(['browser:tools','browser:tools2']);await expect(card(page,'助手共享模型负载')).toContainText('旧群显式池（兼容覆盖）');await shot(page,'07-legacy-compatibility','助手共享模型负载');await page.getByRole('button',{name:'回复与媒体',exact:true}).click();await expect(page.getByRole('switch',{name:'启用聊天',exact:true})).toBeChecked();await expect(page.getByRole('switch',{name:'启用普通群聊自动学习'})).toBeChecked();await page.getByRole('button',{name:'主动与风格',exact:true}).click();await shot(page,'07-existing-style-switches');await page.getByRole('button',{name:'全局',exact:true}).click();await page.getByText('备用模型、排队与回答随机程度',{exact:true}).click();await expect(page.getByLabel('默认系统指令')).toHaveValue('旧群显式Prompt保全 [ACTIVE_PERSONA]');await page.getByLabel('本群模型配置来源').selectOption('global');p=await save(page,poolPath(-88003),'保存本群负载');expect(p.source).toBe('global');const after=await snapshot(page);expect(after.policies).toEqual(before.policies);expect(after.groups).toEqual(before.groups);expect(after.global_config).toEqual(before.global_config);expect(await api(page,'/api/admin/llm/models')).toEqual(registry);expect(await api(page,'/api/admin/groups/-88003/assistant/prompts')).toEqual(prompts);data('F07-evidence',{before,after,p,prompts,registryUnchanged:true});
});
function returnValue(v:any){return v;}
test('F12 ordinary global single main without backup must remain usable after UI save',async({page})=>{
 await page.context().addCookies([{name:'cg_admin',value:session.ownerToken,url:session.baseURL,httpOnly:true},{name:'cg_csrf',value:session.csrf,url:session.baseURL}]);const current=await api(page,G);const clear=putJSON(current);clear.model_roles={};let r=await page.request.put(G,{headers,data:clear});expect(r.status(),await r.text()).toBe(200);
 await open(page,-88004);await page.getByLabel('主模型模型 1',{exact:true}).selectOption('browser:tools');const saved=await save(page,G,'保存全局设置');const actual=await api(page,poolPath(-88004));await page.waitForTimeout(300);data('F12-evidence',{saved,actual});await shot(page,'12-single-main-after-save-crash');await expect(page.getByLabel('全局负载策略'),'只配置一个主模型也必须保留可用编辑入口').toBeVisible();
});
test('RFE01 all single-role nullable arrays reload continue editing at 390px',async({page})=>{
 await page.setViewportSize({width:390,height:844});await open(page,-88004);
 const roles=card(page,'模型角色');for(const label of ['决策','视觉','压缩','向量'])await roles.getByLabel(`${label}模型 1`,{exact:true}).selectOption('browser:tools2');
 await save(page,G,'保存全局设置');const raw=await api(page,poolPath(-88004));
 for(const task of ['chat','learning','decision','vision','compress','vector'])expect(raw.config.task_assignments[task].backups??[]).toEqual([]);
 await reopen(page,-88004);for(const label of ['主模型','决策','视觉','压缩','向量'])await expect(roles.getByLabel(`${label}模型 1`,{exact:true})).toHaveValue(label==='主模型'?'browser:tools':'browser:tools2');
 await page.getByLabel('本群模型配置来源').selectOption('custom');await page.getByText('学习路由 · 继承聊天主角色',{exact:true}).click();await page.getByLabel('学习路由来源').selectOption('custom');await page.getByLabel('学习模型 1',{exact:true}).selectOption('browser:text');
 await save(page,poolPath(-88004),'保存本群负载');await reopen(page,-88004);await page.getByText('学习路由',{exact:true}).click();await expect(page.getByLabel('学习模型 1',{exact:true})).toHaveValue('browser:text');
 await page.getByRole('button',{name:'添加学习备用模型',exact:true}).click();await page.getByLabel('学习模型 2',{exact:true}).selectOption('browser:tools2');await save(page,poolPath(-88004),'保存本群负载');
 await page.getByLabel('学习移除 2',{exact:true}).click();await save(page,poolPath(-88004),'保存本群负载');await reopen(page,-88004);
 expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);data('RFE01-evidence',{raw,singleRoleReloadEditable:true});await shot(page,'round1-single-roles-mobile');
});
test('RFE04 registry GET failure has Chinese reason management link and disabled add',async({page})=>{
 await page.route('**/api/admin/llm/models',route=>route.abort('failed'));
 await open(page,-88004);const c=card(page,'助手共享模型负载');await expect(c).toContainText('模型清单暂时加载失败');await expect(c.getByRole('link',{name:'现有模型管理'}).first()).toHaveAttribute('href','/llm');
 await expect(c.getByRole('button',{name:'添加聊天备用模型',exact:true})).toBeDisabled();await expect(card(page,'模型角色')).toContainText('模型清单暂时加载失败');
 data('RFE04-error-evidence',{registryGETAborted:true,realCatalogRemains:await api(page,'/api/admin/llm/models')});await shot(page,'round1-registry-error','助手共享模型负载');
});
test('RFE04 real registry all disabled has no available candidates and cannot add',async({page})=>{
 await open(page,-88004);let r=await page.request.post(session.controlURL+'/disable-ui-catalog',{headers:{'X-Browser-Fixture':session.controlKey}});expect(r.status(),await r.text()).toBe(200);await reopen(page,-88004);
 const c=card(page,'助手共享模型负载');await expect(c).toContainText('当前没有可用于聊天的已启用模型');await expect(c.getByRole('button',{name:'添加聊天备用模型',exact:true})).toBeDisabled();await expect(c.getByRole('link',{name:'现有模型管理'}).first()).toHaveAttribute('href','/llm');
 data('RFE04-disabled-evidence',{registry:await api(page,'/api/admin/llm/models')});await shot(page,'round1-no-compatible-model','助手共享模型负载');
});
test('F11 real empty registry must explain no available models next to add entry',async({page})=>{
 await page.context().addCookies([{name:'cg_admin',value:session.ownerToken,url:session.baseURL,httpOnly:true},{name:'cg_csrf',value:session.csrf,url:session.baseURL}]);const current=await api(page,G);const clear=putJSON(current);clear.model_roles={};let r=await page.request.put(G,{headers,data:clear});expect(r.status(),await r.text()).toBe(200);
 r=await page.request.post(session.controlURL+'/empty-ui-catalog',{headers:{'X-Browser-Fixture':session.controlKey}});expect(r.status(),await r.text()).toBe(200);const registry=await api(page,'/api/admin/llm/models');expect(registry.models??[]).toEqual([]);
 await page.goto('/groups/-88004/assistant');await page.getByRole('button',{name:'全局',exact:true}).click();await expect(page.getByLabel('全局负载策略')).toBeVisible();await page.getByLabel('本群模型配置来源').selectOption('custom');const c=card(page,'助手共享模型负载');const text=await c.innerText();data('F11-evidence',{registryResponseInjected:false,registry,text,modelOptions:await page.getByLabel('聊天模型 1',{exact:true}).locator('option').allTextContents()});await shot(page,'11-real-empty-registry-no-explanation','助手共享模型负载');expect(text,'空目录需要明确中文解释，不能只给空选择器').toMatch(/暂无可用模型|没有可用模型|无可用模型|模型清单.*失败|模型目录.*不可用/);
});
