// TEST ONLY: round3 final API attachment + current web DTO. Strict mock, NOT backend integration.
import {test as base, expect, type Page} from '@playwright/test';
import {policy as guardPolicy, login} from './ui-fixtures';
export {expect, login};
export const groups = [-1001,-1002].map((id,i)=>({chat_id:id,title:`Fixture 助手${i?'乙':'甲'}群 · 非线上`,enabled:true,type:'supergroup',member_count:12,config:{},created_at:'2026-09-01T00:00:00Z'}));
export const tools=['knowledge_query','conversation_recall','webfetch_readonly'];
export function defaultPolicy(chat_id=-1001){return {chat_id,version:0,chat_enabled:false,learning_enabled:false,trigger_mode:'mention_or_reply',followup_window_sec:300,max_followup_turns:5,chat_model_ref:'',learning_model_ref:'',temperature:0.3,system_prompt:'',history_limit:30,retention_days:7,collection_policy:'history_7d_and_long_term_summary',tool_allowlist:[...tools],allow_domains:[],max_queue_depth:10,max_queue_wait_sec:15,proactive_interject_enabled:false,proactive_cold_topic_enabled:false,cold_topic_idle_minutes:180,cold_topic_quiet_start:0,cold_topic_quiet_end:8,mimic_target_user_id:0,mimic_target_user_name:'',mimic_profile_text:'',mimic_sample_count:0,mimic_distilled_at_count:0};}
export const models=[['tools',true,true,'declared'],['backup',true,true,'declared'],['text',false,true,'unspecified'],['no-tools',false,true,'unsupported'],['disabled',true,false,'declared']].map(([key,supports_tools,enabled,tool_capability],i)=>({id:i+1,provider_id:1,provider_key:'fixture',model_key:key,ref:`fixture:${key}`,label:`Fixture ${key}`,api_format:'openai_chat',enabled,supports_tools,tools_declared:tool_capability==='declared'?true:undefined,supports_tools_declared:tool_capability==='declared'?true:undefined,tool_capability, supports_vision:false,supports_json:true,capability_tags:[],priority:i}));
export function configuredPool(){return {version:3,strategy:'primary-overflow',config:{max_queue_depth:10,max_queue_wait_sec:15,task_assignments:{chat:{primary:'ep-main',backups:['ep-backup']},learning:{primary:'ep-text',backups:[]}},endpoints:[['ep-main','tools','primary'],['ep-backup','backup','backup'],['ep-text','text','primary']].map(([id,model,role],i)=>({id,name:`Fixture ${id}`,model_ref:`fixture:${model}`,role,priority:i,max_concurrency:3,timeout_ms:15000,cooldown_duration_sec:30,supports_tools:model!=='text'}))}};}
export function memory(id=1,chat_id=-1001):any {return {id,chat_id,subject:`Fixture ${chat_id} 事实${id}`,content:`Fixture 本群事实内容 ${id}`,memory_type:'base',authority_level:'admin_base',valid_scope:'long_term',source:{source_type:'admin_base',source_chat_id:chat_id,operator_id:7,operator_name:'Fixture 管理员',snippet:'Fixture 来源摘要',verified:'server_verified',currently_verified:true},expires_at:'2099-01-01T00:00:00Z',active:true,version:2,created_at:'2026-09-01T00:00:00Z',updated_at:'2026-09-01T00:00:00Z'};}
export type Rule={match:RegExp,method?:string,status?:number,body?:any,delay?:number,wait?:Promise<void>,once?:boolean};
export type Harness={writes:any[],requests:any[],errors:string[],console:any[],external:string[],unknown:string[],injected:any[],failures:any[],rules:Rule[],policies:Record<string,any>,pools:Record<string,any>,memories:Record<string,any[]>,conflicts:Record<string,any[]>,status:Record<string,any>,dispatches:Record<string,any[]>};
export const test=base.extend<{api:Harness}>({api:[async({page},use,info)=>{
 let unauthorized=false;
 const api:Harness={writes:[],requests:[],errors:[],console:[],external:[],unknown:[],injected:[],failures:[],rules:[],policies:{},pools:{},memories:{},conflicts:{},status:{},dispatches:{}};
 for(const {chat_id:id} of groups){
  api.policies[id]=defaultPolicy(id); api.pools[id]={version:0,strategy:'primary-overflow',config:{task_assignments:{chat:{primary:'',backups:[]},learning:{primary:'',backups:[]}},endpoints:[],max_queue_depth:10,max_queue_wait_sec:15}};
  api.memories[id]=[memory(1,id),{...memory(2,id),memory_type:'learned',authority_level:'learned_fact',source:{source_type:'telegram_message',source_message_id:88,source_chat_id:id,snippet:'Fixture 成员原文',verified:'unknown',currently_verified:false}},{...memory(3,id),expires_at:'2000-01-01T00:00:00Z'},{...memory(4,id),active:false},{...memory(5,id),memory_type:'pending',authority_level:'unknown',source:{},expires_at:''}];
  api.conflicts[id]=[1,2].map(n=>({id:n,chat_id:id,memory_id:1,subject:`Fixture 冲突${n}`,candidate_content:'Fixture 待审核候选',candidate_scope:'current_group',candidate_authority:'learned_fact',source:{type:'telegram_message',message_id:89,source_chat_id:id,snippet:'Fixture 原始候选引文'},status:'pending',created_at:'2026-09-01T00:00:00Z'}));
  api.status[id]={chat_id:id,active_strategy:'primary-overflow',endpoints_status:[],queue_depth:0,last_dispatch_event:null,remote_quota_note:'unknown'};api.dispatches[id]=[];
 }
 page.on('pageerror',e=>api.errors.push(e.message));page.on('console',m=>{if(m.type()==='error')api.console.push({text:m.text(),location:m.location()});});page.on('requestfailed',r=>api.failures.push({url:r.url(),error:r.failure()?.errorText}));
 await page.route('**/*',async route=>{
  const req=route.request(),url=new URL(req.url()),path=url.pathname,method=req.method(),full=path+url.search;
  if(url.hostname!=='127.0.0.1'){api.external.push(req.url());return route.fulfill({contentType:'application/javascript',body:''});}
  if(!path.startsWith('/api/'))return route.continue();
  const body=method==='GET'?undefined:req.postDataJSON();api.requests.push({path:full,method,body});if(method!=='GET')api.writes.push({path,method,body,csrf:req.headers()['x-csrf-token']});
  const rule=api.rules.find(r=>(!r.method||r.method===method)&&r.match.test(full));
  if(rule){if(rule.once)api.rules.splice(api.rules.indexOf(rule),1);if(rule.delay)await new Promise(r=>setTimeout(r,rule.delay));if(rule.wait)await rule.wait;if(rule.status||rule.body!==undefined){const status=rule.status??200;if(status===401)unauthorized=true;if(status>=400)api.injected.push({path:full,status});return route.fulfill({status,json:rule.body??{error:`Fixture injected ${status}`}});}}
  const reject=(status:number,error:string)=>{api.injected.push({path:full,status,kind:'strict-contract',error});return route.fulfill({status,json:{error:`Fixture contract: ${error}`}});};
  let data:any,known=true;
  if(path==='/api/auth/me'&&unauthorized){api.injected.push({path,status:401});return route.fulfill({status:401,json:{error:'Fixture expired session'}});}
  if(path==='/api/auth/me')data={admin:{id:7,telegram_id:7,role:'super_admin',display_name:'Fixture 验证管理员'}};
  else if(path==='/api/admin/groups')data={groups};
  else if(path==='/api/admin/llm/models')data={models};
  else if(/^\/api\/admin\/groups\/-\d+$/.test(path))data={group:groups.find(g=>g.chat_id===Number(path.split('/').at(-1))),merged_policy:guardPolicy};
  else if(/\/assistant(?:\/|$)/.test(path)){
   const [,id,suffix='']=path.match(/\/groups\/(-\d+)\/assistant(.*)/)??[];const p=api.policies[id],pool=api.pools[id];
   if(!p)return reject(404,'missing group');
   const mid=Number(suffix.split('/')[2]);const stored=api.memories[id].find(m=>m.id===mid&&m.chat_id===Number(id));
   if(/^\/memories\/\d+(?:\/(?:versions|forget))?$/.test(suffix)&&!stored)return reject(404,'memory or versions missing/foreign');
   if((suffix==='/memories'&&method==='POST')||(/^\/memories\/\d+$/.test(suffix)&&method==='PUT')){
    if(Object.hasOwn(body,'source_message_id'))return reject(400,'client source_message_id forbidden');
    const allowed=['subject','content','valid_scope','expires_at','source_snippet',method==='POST'?'source_type':'expected_version'];
    if(Object.keys(body).some(k=>!allowed.includes(k)))return reject(400,'client identity or unexpected source fields forbidden');
    if(method==='POST'&&body.source_type!=='admin_base')return reject(400,'create source_type must be admin_base');
    if(!['today','this_week','this_month','current_group','long_term','weekly','retention_window'].includes(body.valid_scope))return reject(400,'unknown valid_scope');
    if(!body.subject?.trim()||!body.content?.trim())return reject(400,'subject and content required');
    if(method==='PUT'&&body.expected_version!==stored.version)return reject(409,'memory version changed');
    if(method==='PUT'&&(stored.source.source_message_id||stored.source.message_id)&&stored.source.currently_verified!==true)return reject(409,'stored Telegram source invalid or hash unverifiable');
   }
   const overview=()=>{const selected=models.find(m=>m.ref===p.chat_model_ref);const declared=selected?.tool_capability==='declared';const blockers=!p.chat_model_ref?['还不能回复：请先选择聊天模型。']:!selected?['当前聊天模型不可用']:!declared?['这个模型还没声明能调用技能，请去模型管理打开「支持工具调用」声明，或换一个已声明的模型。']:[];return {policy:p,model_pool:pool,defaults:{disabled:true,retention_days:7,history_retention:'7 days',remote_quota:'unknown'},readiness:{can_chat:Boolean(p.chat_enabled&&declared),blockers,chat_primary_model_ref:p.chat_model_ref||'',tools_declared:declared}};};
   if(!suffix&&method==='GET')data=overview();
   else if(!suffix&&method==='PUT'){if(body.expected_version!==p.version)return reject(409,'policy version changed');const selected=models.find(m=>m.ref===body.chat_model_ref);if(body.chat_enabled&&(!body.chat_model_ref||!selected?.enabled||selected.tool_capability!=='declared'))return reject(400,'还不能启用聊天，请先选择已声明能调用技能的聊天模型。');api.policies[id]={...p,...body,version:p.version+1};delete api.policies[id].expected_version;if(body.chat_model_ref&&(!pool.config.task_assignments.chat?.primary)){const endpoint={id:'ep-auto-chat',name:'Fixture 自动主模型',model_ref:body.chat_model_ref,role:'primary',priority:0,max_concurrency:3,timeout_ms:15000,cooldown_duration_sec:30,supports_tools:true};pool.config.endpoints=[...pool.config.endpoints.filter((e: {id:string})=>e.id!=='ep-auto-chat'),endpoint];pool.config.task_assignments.chat={primary:'ep-auto-chat',backups:[]};}if(body.learning_model_ref&&(!pool.config.task_assignments.learning?.primary)){const learningSelected=models.find(m=>m.ref===body.learning_model_ref);const endpoint={id:'ep-auto-learning',name:'Fixture 自动学习模型',model_ref:body.learning_model_ref,role:'primary',priority:1,max_concurrency:3,timeout_ms:15000,cooldown_duration_sec:30,supports_tools:Boolean(learningSelected?.supports_tools)};pool.config.endpoints=[...pool.config.endpoints.filter((e: {id:string})=>e.id!=='ep-auto-learning'),endpoint];pool.config.task_assignments.learning={primary:'ep-auto-learning',backups:[]};}data={policy:api.policies[id]};}
   else if(suffix==='/model-pool'&&method==='GET')data=pool;
   else if(suffix==='/model-pool'&&method==='PUT'){
    if(body.expected_version!==pool.version)return reject(409,'pool version changed');
    for(const task of ['chat','learning']){const a=body.task_assignments?.[task];if(!a?.primary||!Array.isArray(a.backups))return reject(400,'assignment missing');
     const ids=[a.primary,...a.backups];if(new Set(ids).size!==ids.length)return reject(400,'duplicate assignment');
     for(const eid of ids){const ep=body.endpoints?.find((e:any)=>e.id===eid),model=models.find(m=>m.ref===ep?.model_ref);if(!model?.enabled||(task==='chat'&&!model.supports_tools))return reject(400,'incompatible endpoint capability');}}
    const {expected_version,strategy,...config}=body;api.pools[id]={version:expected_version+1,strategy,config};data=api.pools[id];}
   else if(suffix==='/status'&&method==='GET')data=api.status[id];
   else if(suffix==='/dispatches'&&method==='GET')data={dispatches:api.dispatches[id]};
   else if(suffix==='/tools'&&method==='GET')data={chat_id:Number(id),tools:tools.map(name=>({name,read_only:true,enabled:p.tool_allowlist.includes(name),scope:'server-bound-current-group',allow_domains:p.allow_domains})),write_tools:[],server_bound_scope:true};
   else if(suffix==='/recent-senders'&&method==='GET')data={chat_id:Number(id),senders:[{user_id:7,user_name:'Fixture 成员',message_count:12,last_seen_at:'2026-09-01T00:00:00Z'},{user_id:8,user_name:'Fixture 另一成员',message_count:5,last_seen_at:'2026-09-01T01:00:00Z'}]};
   else if(suffix==='/memories'&&method==='GET'){let list=api.memories[id];if(url.searchParams.get('include_inactive')!=='true')list=list.filter(m=>m.active&&(!m.expires_at||Date.parse(m.expires_at)>Date.now()));const q=url.searchParams.get('q');if(q)list=list.filter(m=>(m.subject+m.content).includes(q));data={chat_id:Number(id),memories:list,retention_notice:'Fixture 原文7天，长期仅提炼事实'};}
   else if(suffix==='/memories'&&method==='POST'){const m={...memory(6,Number(id)),...body,version:1,source:{source_type:'admin_base',source_chat_id:Number(id),operator_id:7,operator_name:'Fixture 管理员',currently_verified:true,snippet:body.source_snippet},authority_level:'admin_base'};api.memories[id].push(m);data={memory:m};}
   else if(/^\/memories\/\d+\/versions$/.test(suffix)&&method==='GET'){const m=api.memories[id].find(m=>m.id===Number(suffix.split('/')[2]));data={versions:[{id:1,memory_id:m.id,version:1,content:'Fixture 旧版本事实',memory_type:m.memory_type,authority_level:m.authority_level,valid_scope:m.valid_scope,source_type:m.source.source_type,source_snippet:m.source.snippet,change_kind:'create',created_at:m.created_at}]};}
   else if(/^\/memories\/\d+\/forget$/.test(suffix)&&method==='POST'){const m=api.memories[id].find(m=>m.id===Number(suffix.split('/')[2]));m.active=false;data={forgotten:true,remote_telegram_deleted:false,notice:'只移出本地召回'};}
   else if(/^\/memories\/\d+$/.test(suffix)){const m=api.memories[id].find(m=>m.id===Number(suffix.split('/')[2]));if(method==='PUT'){const {expected_version,...fields}=body;Object.assign(m,fields,{version:m.version+1,memory_type:'base',authority_level:'admin_explicit',source:{...m.source,source_type:'admin_explicit_correction',operator_id:7,operator_name:'Fixture 管理员',snippet:body.source_snippet}});}data={memory:m};}
   else if(suffix==='/conflicts'&&method==='GET')data={conflicts:api.conflicts[id]};
   else if(/^\/conflicts\/\d+\/resolve$/.test(suffix)&&method==='POST'){
    const cid=Number(suffix.split('/')[2]),conflict=api.conflicts[id].find(c=>c.id===cid);if(!conflict)return reject(404,'conflict missing');
    if(body.accept===false){if(Object.keys(body).join()!=='accept')return reject(400,'reject payload must only contain accept:false');}
    else if(body.accept===true){
     if(Object.keys(body).sort().join()!=='accept,expected_memory_version,resolution_mode'||body.resolution_mode!=='admin_explicit_correction'||!Number.isInteger(body.expected_memory_version))return reject(400,'accept requires exact explicit correction DTO');
     const target=api.memories[id].find(m=>m.id===conflict.memory_id&&m.chat_id===Number(id));
     if(!target||!target.active||(target.expires_at&&Date.parse(target.expires_at)<=Date.now())||target.version!==body.expected_memory_version)return reject(409,'conflict target inactive expired or version changed');
     Object.assign(target,{content:conflict.candidate_content,version:target.version+1,authority_level:'admin_explicit',memory_type:'base',source:{source_type:'admin_conflict_accept',source_message_id:conflict.source.source_message_id??conflict.source.message_id,source_chat_id:conflict.source.source_chat_id??conflict.source.chat_id,operator_id:7,operator_name:'Fixture 管理员',snippet:conflict.source.snippet,currently_verified:true}});
    }else return reject(400,'accept boolean required');
    data={conflict:{...conflict,status:body.accept?'accepted':'rejected'}};api.conflicts[id]=api.conflicts[id].filter(c=>c.id!==cid);
   }
   else if(suffix==='/history'&&method==='GET')data={chat_id:Number(id),history:[{id:1,chat_id:Number(id),thread_id:Number(url.searchParams.get('thread_id')||11),telegram_message_id:42,sender_id:Number(url.searchParams.get('sender_id')||7),sender_name:'Fixture 成员',role:'user',text:`Fixture ${id} 保留期历史`,approved:true,delivered:true,expires_at:'2099-01-01T00:00:00Z',source:{type:'telegram_message',id:'42'},created_at:'2026-09-01T00:00:00Z'}],retention_days:p.retention_days,expired_auto_removed:true};
   else known=false;
  }else if(path==='/api/admin/stats')data={groups_count:2,active_verifications:0,today_violations:0};
  else if(path==='/api/admin/system-state')data={state:{ai_paused:false,actions_paused:false,frozen:false}};
  else if(path==='/api/admin/health')data={db_latency_ms:0,redis_latency_ms:0};
  else if(path==='/api/admin/audit')data={audit:[]};
  else if(path==='/api/admin/profile-check-logs')data={items:[],total:0};
  else if(path==='/api/admin/events')data={events:[],total:0,has_more:false};
  else if(path.endsWith('/join-protection'))data={join_protection:{enabled:false},defaults:{},status:{}};
  else known=false;
  if(!known){api.unknown.push(`${method} ${full}`);return route.fulfill({status:501,json:{error:'UNMAPPED TEST API'}});}
  return route.fulfill({json:data});
 });
 await use(api);
 await info.attach('assistant-browser-api-log',{body:JSON.stringify(api,(k,v)=>k==='wait'?undefined:v,2),contentType:'application/json'});
 expect.soft(api.errors,'full-cycle JS exceptions').toEqual([]);expect.soft(api.unknown,'unmapped API').toEqual([]);
 const unexpected=api.console.filter(m=>!(/^Failed to load resource: the server responded with a status of (400|401|403|404|409|500)/.test(m.text)&&api.injected.some(i=>m.text.includes(String(i.status))&&m.location.url.endsWith(i.path))));
 expect.soft(unexpected,'unexpected console errors (injected HTTP separated)').toEqual([]);
 },{auto:true}]});
export async function openAssistant(page:Page){await login(page);await page.context().addCookies([{name:'cg_csrf',value:'fixture-csrf',domain:'127.0.0.1',path:'/'}]);await page.goto('/groups/-1001/assistant');await expect(page.getByText('实际状态',{exact:true})).toBeVisible();await expect(page.getByLabel('切换群组')).toBeVisible();}
const sectionAliases:Record<string,string>={'总览':'开始使用','聊天与学习':'怎么说话','记忆中心':'群记忆','模型池':'模型与负载（高级）','只读技能':'能查什么'};
export async function tab(page:Page,name:string){await page.getByRole('button',{name:sectionAliases[name]??name,exact:true}).click();}
export async function memoryTab(page:Page){await tab(page,'群记忆');await expect(page.getByText('Fixture -1001 事实1',{exact:true})).toBeVisible();}
export function deferred(){let release!:()=>void;const wait=new Promise<void>(r=>release=r);return {wait,release};}
