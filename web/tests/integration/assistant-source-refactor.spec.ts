import {test,expect,type Page} from '@playwright/test';
import fs from 'node:fs';
const session=JSON.parse(fs.readFileSync(process.env.CG_BROWSER_IT_SESSION!,'utf8'));
const out=process.env.CG_BROWSER_IT_OUT!;
const path=(chat=-88001)=>`/api/admin/groups/${chat}/assistant/model-pool`;
const headers={'X-CSRF-Token':session.csrf};
async function open(page:Page,chat=-88001){
 await page.context().addCookies([{name:'cg_admin',value:session.ownerToken,url:session.baseURL,httpOnly:true},{name:'cg_csrf',value:session.csrf,url:session.baseURL}]);
 await page.route('**/*',r=>new URL(r.request().url()).hostname==='127.0.0.1'?r.continue():r.fulfill({body:'',contentType:'application/javascript'}));
 await page.goto(`/groups/${chat}/assistant`);await page.getByRole('button',{name:'全局',exact:true}).click();await expect(page.getByLabel('全局负载策略')).toBeVisible();
}
async function save(page:Page,url:string,label:string,status=200){const response=page.waitForResponse(r=>new URL(r.url()).pathname===url&&r.request().method()==='PUT');await page.getByRole('button',{name:label,exact:true}).click();const r=await response;expect(r.status(),await r.text()).toBe(status);return r.json();}
async function snapshot(page:Page){const r=await page.request.get(session.controlURL+'/snapshot',{headers:{'X-Browser-Fixture':session.controlKey}});expect(r.status()).toBe(200);return r.json();}
function refs(pool:any){const a=pool.config.task_assignments.chat;return [a.primary,...(a.backups??[])].map(id=>pool.config.endpoints.find((e:any)=>e.id===id).model_ref);}
test('source desktop real PG/API multi-backup weights inheritance CAS and separate drafts',async({page})=>{
 const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message));await open(page);const before=await snapshot(page);
 await page.getByLabel('全局负载策略').selectOption('weighted');await page.getByLabel('主模型模型 1',{exact:true}).selectOption('browser:tools');
 await page.getByRole('button',{name:'添加主模型备用模型',exact:true}).click();await page.getByLabel('主模型模型 2',{exact:true}).selectOption('browser:tools2');
 await page.getByRole('button',{name:'添加主模型备用模型',exact:true}).click();await page.getByLabel('主模型模型 3',{exact:true}).selectOption('browser:tools3');
 await page.getByLabel('主模型权重 1',{exact:true}).fill('5');await page.getByLabel('主模型权重 2',{exact:true}).fill('3');await page.getByLabel('主模型权重 3',{exact:true}).fill('2');
 await page.getByLabel('主模型上移 3',{exact:true}).click();await expect(page.getByLabel('主模型模型 2',{exact:true})).toHaveValue('browser:tools3');await page.getByLabel('主模型下移 2',{exact:true}).click();
 const global=await save(page,'/api/admin/assistant/global','保存全局设置');expect(global.model_roles.main).toMatchObject({model_ref:'browser:tools',fallbacks:['browser:tools2','browser:tools3'],strategy:'weighted'});
 expect(global.bot.inbound_merge_window_sec).toBe(0.4);expect(global.tts.enabled).toBe(false);
 await page.reload();await page.getByRole('button',{name:'全局',exact:true}).click();await expect(page.getByLabel('本群模型配置来源')).toHaveValue('global');
 let pool=await (await page.request.get(path())).json();expect(pool.source).toBe('global');expect(refs(pool)).toEqual(['browser:tools','browser:tools2','browser:tools3']);
 await page.getByLabel('本群模型配置来源').selectOption('custom');await page.getByLabel('聊天上移 3',{exact:true}).click();await expect(page.getByLabel('聊天模型 2',{exact:true})).toHaveValue('browser:tools3');await expect(page.getByLabel('聊天权重 2',{exact:true})).toHaveValue('2');
 await page.getByLabel('聊天权重 1',{exact:true}).fill('7');await page.getByLabel('主模型权重 1',{exact:true}).fill('9');
 pool=await save(page,path(),'保存本群负载');expect(pool.source).toBe('group');expect(refs(pool)).toEqual(['browser:tools','browser:tools3','browser:tools2']);
 expect((await (await page.request.get('/api/admin/assistant/global')).json()).model_roles.main.model_options['browser:tools'].weight).toBe(5);await expect(page.getByLabel('主模型权重 1',{exact:true})).toHaveValue('9');await page.getByRole('button',{name:'取消全局修改'}).click();
 await page.getByLabel('本群模型配置来源').selectOption('global');await page.getByLabel('本群模型配置来源').selectOption('custom');await expect(page.getByLabel('聊天权重 1',{exact:true})).toHaveValue('7');
 await page.getByLabel('聊天权重 1',{exact:true}).fill('8');
 const concurrent=await page.request.put(path(),{headers,data:{...pool.saved_config,expected_version:pool.version}});expect(concurrent.status(),await concurrent.text()).toBe(200);
 await save(page,path(),'保存本群负载',409);await expect(page.getByLabel('聊天权重 1',{exact:true})).toHaveValue('8');await expect(page.getByRole('alert').filter({hasText:'草稿已保留'})).toBeVisible();
 fs.mkdirSync(`${out}/screenshots`,{recursive:true});await page.screenshot({path:`${out}/screenshots/source-desktop-cas-draft.png`,fullPage:true});
 await page.getByRole('button',{name:'取消负载修改'}).click();await page.reload();await page.getByRole('button',{name:'全局',exact:true}).click();
 await page.getByLabel('本群模型配置来源').selectOption('global');pool=await save(page,path(),'保存本群负载');expect(pool.source).toBe('global');expect(refs(pool)).toEqual(['browser:tools','browser:tools2','browser:tools3']);expect(pool.saved_config.endpoints.some((e:any)=>e.weight===7)).toBe(true);
 await page.reload();await page.getByRole('button',{name:'全局',exact:true}).click();await page.getByLabel('本群模型配置来源').selectOption('custom');await expect(page.getByLabel('聊天权重 1',{exact:true})).toHaveValue('7');await page.getByRole('button',{name:'取消负载修改'}).click();
 // Old policy API updates the effective chain; unchanged policy fields cannot override a newer pool.
 const overview=await (await page.request.get('/api/admin/groups/-88001/assistant')).json();const {chat_id,version,created_at,updated_at,updated_by,...body}=overview.policy;
 let legacy=await page.request.put('/api/admin/groups/-88001/assistant',{headers,data:{...body,expected_version:version,chat_model_ref:'browser:tools3'}});expect(legacy.status(),await legacy.text()).toBe(200);
 const changed=await (await page.request.get(path())).json();expect(refs(changed)[0]).toBe('browser:tools3');
 const after=await snapshot(page);expect(after.groups).toEqual(before.groups);expect(after.global_config).toEqual(before.global_config);expect(after.policies[0]).toMatchObject({chat_enabled:false,learning_enabled:false,tts_mode:'off'});
 fs.writeFileSync(`${out}/source-api-evidence.json`,JSON.stringify({global,restored:pool,legacy:changed,moderation_preserved:true},null,2));expect(errors).toEqual([]);
});
test('source mobile 390 Chinese multi-add reorder remove cancel and inheritance',async({page})=>{
 await page.setViewportSize({width:390,height:844});await open(page,-88002);await page.getByLabel('本群模型配置来源').selectOption('custom');
 await page.getByLabel('本群负载策略').selectOption('primary-overflow');
 await page.getByLabel('聊天移除 3',{exact:true}).click();await page.getByLabel('聊天移除 2',{exact:true}).click();
 await page.getByRole('button',{name:'添加聊天备用模型'}).click();await page.getByLabel('聊天模型 2',{exact:true}).selectOption('browser:tools2');
 await page.getByRole('button',{name:'添加聊天备用模型'}).click();await page.getByLabel('聊天模型 3',{exact:true}).selectOption('browser:tools3');await page.getByLabel('聊天上移 3',{exact:true}).click();
 let pool=await save(page,path(-88002),'保存本群负载');expect(pool.strategy).toBe('primary-overflow');expect(refs(pool)).toEqual(['browser:tools','browser:tools3','browser:tools2']);
 await page.getByLabel('本群负载策略').selectOption('weighted');await page.getByLabel('聊天权重 2',{exact:true}).fill('6');
 const dialogs:string[]=[];page.on('dialog',async d=>{dialogs.push(d.message());await d.dismiss();});await page.getByRole('button',{name:'开始使用',exact:true}).click();await expect(page.getByLabel('聊天权重 2',{exact:true})).toHaveValue('6');expect(dialogs.length).toBeGreaterThan(0);
 await page.getByRole('button',{name:'取消负载修改'}).click();await expect(page.getByLabel('本群负载策略')).toHaveValue('primary-overflow');
 await page.getByLabel('本群模型配置来源').selectOption('global');pool=await save(page,path(-88002),'保存本群负载');expect(pool.source).toBe('global');
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);await page.screenshot({path:`${out}/screenshots/source-mobile-inheritance.png`,fullPage:true});
});
