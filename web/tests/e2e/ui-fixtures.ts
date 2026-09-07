// TEST-ONLY, NON-PRODUCTION DATA. All API requests intercepted before network.
import { test as base, expect, type Page, type Locator } from '@playwright/test';
export const policy = {verify:{enabled:true,method:'button',timeout_seconds:120,fail_action:'kick',delete_join_message:true,welcome_message:{enabled:true,template:'Fixture welcome {user_name}',rules_link:'https://example.test/rules',delete_after_seconds:60,parse_mode:'html'},check_profile:false,profile_check_mode:'off'},ai:{message_rules:'Fixture message rules',bio_rules:'Fixture bio rules'},fixture_unknown:{preserve:'yes'}};
export const group = {chat_id:-1001,title:'Fixture 测试群 · 非生产',type:'supergroup',member_count:128,config:{fixture_unknown:{preserve:'yes'}},created_at:'2026-09-01T00:00:00Z'};
const provider = {id:1,key:'fixture',name:'fixture',label:'Fixture Provider',type:'openai',base_url:'https://example.test/v1',api_key_set:false,api_key_hint:'',timeout_ms:30000,extra_headers:{},enabled:true,models:[{key:'test-model',label:'Fixture Model'}]};
const model = {id:1,provider_id:1,provider_key:'fixture',model_key:'test-model',ref:'fixture:test-model',label:'Fixture Model',api_format:'openai_chat',enabled:true,supports_vision:false,supports_json:true,supports_tools:false,capability_tags:['moderation'],priority:100,meta:null,probe_enabled:false,probe_interval_seconds:120};
const join = {enabled:true,join_threshold:10,join_window_seconds:60,protection_duration_seconds:600,temporary_ban_seconds:600,admin_notify_interval_seconds:60,max_pending_verifications:100,telegram_failure_cooldown_seconds:60};
const status = {state:'normal',recent_joins:0,pending_verifications:0,intercepted:0,last_intercepted:0,deferred_cleanup_task_count:0};
export type Mock = {writes:Array<{path:string,method:string,body:any}>,errors:string[],console:string[],external:string[],unknown:string[],delay:number,status:number};
export const test = base.extend<{mock:Mock}>({mock:[async({page},use,testInfo)=>{
 const mock:Mock={writes:[],errors:[],console:[],external:[],unknown:[],delay:0,status:200};
 page.on('pageerror',e=>mock.errors.push(e.message));
 page.on('console',m=>{if(m.type()==='error')mock.console.push(m.text())});
 page.on('requestfailed',r=>mock.external.push(`${r.url()} ${r.failure()?.errorText}`));
 await page.route('**/*',async route=>{
  const req=route.request(),url=new URL(req.url()),p=url.pathname;
  if(url.hostname!=='127.0.0.1') {mock.external.push('intercepted '+req.url());return route.fulfill({contentType:'application/javascript',body:''});}
  if(!p.startsWith('/api/'))return route.continue();
  let data:any;
  if(p==='/api/auth/me' && !(await page.context().cookies()).some(c=>c.name==='cg_admin'))return route.fulfill({status:401,json:{error:'Fixture unauthenticated'}});
  if(req.method()!=='GET') {
   const body=req.postDataJSON();mock.writes.push({path:p,method:req.method(),body});
   if(mock.delay)await new Promise(r=>setTimeout(r,mock.delay));
   if(mock.status>=400)return route.fulfill({status:mock.status,json:{error:`Fixture ${mock.status} rejected`}});
   if(p.endsWith('/config'))data={group:{...group,config:body},merged_policy:{...policy,...body}};
   else if(p==='/api/admin/global-config')data={config:body.config,version:8};
   else if(p.endsWith('/join-protection'))data={join_protection:body,status};
   else data={ok:true};
  } else if(p==='/api/auth/me') data={admin:{id:1,telegram_id:1,role:'super_admin',display_name:'Fixture Admin'}};
  else if(p==='/api/admin/groups')data={groups:[group,{...group,chat_id:-1002,title:'Fixture 第二群'}]};
  else if(/^\/api\/admin\/groups\/-\d+$/.test(p))data={group:p.endsWith('-1002')?{...group,chat_id:-1002,title:'Fixture 第二群'}:group,merged_policy:policy};
  else if(p.endsWith('/scheduled-messages'))data={scheduled_messages:[],limit:20};
  else if(p.endsWith('/join-protection'))data={join_protection:join,defaults:join,status};
  else if(p==='/api/admin/global-config')data={config:policy,version:7};
  else if(p==='/api/admin/llm/providers'||p==='/api/admin/ai-providers')data={providers:[provider]};
  else if(p==='/api/admin/llm/models')data={models:[model]};
  else if(p==='/api/admin/llm/stats')data={stats:[]};
  else if(p==='/api/admin/adkiller')data={api_key_set:false,api_key_hint:''};
  else if(p==='/api/admin/stats')data={groups_count:2,active_verifications:3,today_violations:4};
  else if(p==='/api/admin/system-state')data={state:{ai_paused:false,actions_paused:false,frozen:false}};
  else if(p==='/api/admin/health')data={db_latency_ms:2,redis_latency_ms:1,join_cleanup_dead:0,backup_seconds_ago:60,verification_worker_seconds_ago:1,join_recovery_worker_seconds_ago:1,active_pending:3,due_cleanup:0,retrying_cleanup:0,webhook_seconds_ago:1};
  else if(p==='/api/admin/events')data={events:[],total:0,has_more:false};
  else if(p==='/api/admin/violations')data={violations:[]};
  else if(p==='/api/admin/audit')data={audit:[]};
  else if(p==='/api/admin/profile-check-logs')data={items:[],total:0};
  else {mock.unknown.push(p);data={};}
  return route.fulfill({json:data});
 });
 await use(mock);
 await testInfo.attach('browser-and-api-log',{body:JSON.stringify(mock,null,2),contentType:'application/json'});
 expect(mock.errors,'full-cycle pageerror').toEqual([]);
 expect(mock.unknown,'unmapped API fixtures').toEqual([]);
 const unexpected=mock.console.filter(x=>!/^Failed to load resource: the server responded with a status of (401|409|500)/.test(x));
 expect(unexpected,'unexpected consoleerror').toEqual([]);
 },{auto:true}]});
export {expect};
export async function login(page:Page){await page.context().addCookies([{name:'cg_admin',value:'fixture-only',domain:'127.0.0.1',path:'/'}]);}
export async function openGroup(page:Page){await login(page);await page.goto('/groups/-1001');await expect(page.getByRole('heading',{name:group.title})).toBeVisible();}
export function field(page:Page,label:string):Locator{return page.getByText(label,{exact:true}).locator('xpath=ancestor::div[contains(@class,"grid gap-3 px-3") or contains(@class,"glass-panel")][1]');}
export async function draftGroup(page:Page){await page.getByRole('button',{name:'展开详细设置（4 项）'}).first().click();const card=field(page,'超时时长 (秒)');await card.getByRole('checkbox').uncheck();await card.locator('input[type=number]').fill('321');return card.locator('input[type=number]');}
export async function theme(page:Page,value:string){await page.getByRole('combobox',{name:'界面主题'}).filter({visible:true}).first().selectOption(value);await expect(page.locator('html')).toHaveAttribute('data-theme',value);}
export function dialogs(page:Page,accept=false){const messages:string[]=[];page.on('dialog',async d=>{messages.push(d.message());if(accept)await d.accept();else await d.dismiss();});return messages;}
export async function mini(page:Page){await page.addInitScript(()=>{sessionStorage.setItem('cg_miniapp','1');const callbacks=new Set<()=>void>();(window as any).__miniBack=()=>callbacks.forEach(cb=>cb());(window as any).Telegram={WebApp:{initData:'fixture',ready(){},expand(){},close(){},BackButton:{show(){},hide(){},onClick(cb:()=>void){callbacks.add(cb)},offClick(cb:()=>void){callbacks.delete(cb)}}}};});}
