"use client";

import { useEffect, useRef, useState } from "react";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { apiFetch } from "@/lib/api";
import { useDirtyGuard } from "@/components/dirty-guard";

const labels: Record<string,string> = {
 parse_mode:"文本格式",disable_link_preview:"关闭链接预览",inbound_debounce_seconds:"入站合并窗口（秒）",
 reply_batch_timeout_seconds:"回复总时限（秒）",enable_typing:"显示正在输入",enable_streaming:"渐进展示回复",
 stream_chunk_size:"每次展示字符数",stream_edit_interval_sec:"编辑间隔（秒）",auto_delete_seconds:"助手消息保留（秒，0为不自动删除）",
 auto_delete_categories:"自动删除类别（reply / media / proactive）",auto_delete_category_seconds:"各类别保留秒数（JSON）",
 auto_delete_category_mode:"各类别删除方式（timer / button，JSON）",decision_context_items:"决策历史条数",
 max_context_tokens:"上下文 token 预算",max_output_tokens:"上下文预算中的输出预留",
 memory_recent_messages:"热窗口消息数",memory_retention_days:"聊天原文保留天数",
 memory_archive_max_messages_per_group:"每群每topic档案上限",memory_recall_enabled:"按问题召回旧聊天",
 memory_recall_max_results:"索引卡片候选数",memory_automatic_compaction:"后台持久摘要",
 proactive_default_enabled:"已授权助手群默认冷群话题",proactive_idle_minutes:"冷群空闲分钟数",
 proactive_jitter_minutes:"额外随机等待分钟数",proactive_check_interval_seconds:"冷群检查间隔（秒）",
 proactive_quiet_hours_start:"本地静默开始小时",proactive_quiet_hours_end:"本地静默结束小时",
 proactive_retry_minutes:"源保留字段：失败重试分钟（源未接通）",
};
const promptLabels: Record<string,string> = {
 persona:"人格",casual:"日常对话",decision:"回复决策",manage_intent:"永久记忆意图识别",
 compress:"历史压缩",skill_tools:"技能轮任务",sticker_decision:"贴纸决策",reply_mode:"发送模式",
 proactive_topic:"冷群话题",style_distill:"学语气提炼",
};
const externalLabels:Record<string,string>={enabled:"启用",http_timeout_sec:"请求超时（秒）",base_url:"服务地址",default_source:"默认来源",stable_sources:"稳定来源（每行一个）",fallback_file_ids:"后备贴纸file_id（每行一个）",max_results:"最多结果数",default_language:"默认语言",default_region:"默认地区",tmdb_read_access_token:"TMDB只读令牌",imdb_data_set_id:"IMDb数据集ID",imdb_revision_id:"IMDb版本ID",imdb_asset_id:"IMDb资源ID",imdb_api_key:"IMDb API Key",imdb_aws_access_key_id:"IMDb AWS Access Key ID",imdb_aws_secret_access_key:"IMDb AWS Secret Access Key",imdb_aws_session_token:"IMDb AWS Session Token"};
type Schema = { type?:string; minimum?:number; maximum?:number };
type NativeConfig = {
 engine:string; revision:number; bot:Record<string,unknown>; prompts:Record<string,string>;
 schema:{properties:Record<string,Schema>}; source_unwired:Record<string,string>;
 music:Record<string,unknown>;movie_info:Record<string,unknown>;stickers:Record<string,unknown>;
 extra_schema:Record<string,{properties:Record<string,Schema>}>;movie_secret_fields:string[];movie_secrets_configured:Record<string,boolean>;secret_storage_ready:boolean;clear_movie_secrets?:string[];
};

export function NativeGlobalSettings() {
 const [value,setValue]=useState<NativeConfig|null>(null);
 const [snapshot,setSnapshot]=useState<NativeConfig|null>(null);
 const [jsonFields,setJSONFields]=useState<Record<string,string>>({});
 const [error,setError]=useState(""); const [notice,setNotice]=useState(""); const [saving,setSaving]=useState(false);
 const dirty=JSON.stringify(value)!==JSON.stringify(snapshot)||Object.entries(jsonFields).some(([k,v])=>v!==JSON.stringify(snapshot?.bot[k],null,2));
 useDirtyGuard(dirty,"原生助手全局设置尚未保存，确定离开吗？","native-assistant-global");
 function load(){setError("");void apiFetch<NativeConfig>("/api/admin/assistant/native").then(v=>{setValue(v);setSnapshot(v);setJSONFields(Object.fromEntries(Object.entries(v.bot??{}).filter(([,x])=>typeof x==="object").map(([k,x])=>[k,JSON.stringify(x,null,2)])))}).catch(e=>setError(String(e.message)))}
 useEffect(load,[]);
 async function save(){if(!value)return;setSaving(true);setError("");try{
  const bot={...value.bot};for(const [key,text] of Object.entries(jsonFields)){bot[key]=JSON.parse(text)}
  const v=await apiFetch<NativeConfig>("/api/admin/assistant/native",{method:"PUT",body:JSON.stringify({revision:value.revision,bot,prompts:value.prompts,music:value.music,movie_info:value.movie_info,stickers:value.stickers,clear_movie_secrets:value.clear_movie_secrets??[]})});
  setValue(v);setSnapshot(v);setJSONFields(Object.fromEntries(Object.entries(v.bot).filter(([,x])=>typeof x==="object").map(([k,x])=>[k,JSON.stringify(x,null,2)])));setNotice("原生配置已保存并应用");
 }catch(e){setError(e instanceof Error?e.message:"保存失败")}finally{setSaving(false)}}
 if(!value)return <Card><CardBody><p>{error||"正在读取原生有效配置…"}</p><Button onClick={load} variant="secondary">重新读取原生配置</Button></CardBody></Card>;
 if(value.engine!=="native")return null;
 return <div className="space-y-4" data-testid="native-global-settings">
  <Card><CardHeader><CardTitle>原生 SGB 行为</CardTitle><CardDescription>实际运行固定源版本 82c3703；普通聊天不自动抽取事实。永久 Wiki 和管理员记忆不受聊天过期策略影响。模型角色、主备/权重与密钥仍在原入口设置。</CardDescription></CardHeader>
   <CardBody className="grid gap-4 md:grid-cols-2">
    {Object.entries(value.bot).map(([key,current])=>{const schema=value.schema.properties[key]??{};return <label className="block space-y-2 text-sm" key={key}>
     <span>{labels[key]??key}</span>
     {typeof current==="boolean"?<Switch aria-label={labels[key]??key} checked={current} onCheckedChange={v=>setValue({...value,bot:{...value.bot,[key]:v}})}/>
      :typeof current==="number"?<Input aria-label={labels[key]??key} type="number" min={schema.minimum} max={schema.maximum} step={schema.type==="integer"?1:0.1} value={current} onChange={e=>setValue({...value,bot:{...value.bot,[key]:Number(e.target.value)}})}/>
      :typeof current==="object"?<Textarea aria-label={labels[key]??key} rows={3} value={jsonFields[key]??""} onChange={e=>setJSONFields({...jsonFields,[key]:e.target.value})}/>
      :<Input aria-label={labels[key]??key} value={String(current)} onChange={e=>setValue({...value,bot:{...value.bot,[key]:e.target.value}})}/>}
     {value.source_unwired[key]&&<p className="text-xs text-[var(--warning)]">{value.source_unwired[key]}</p>}
    </label>})}
   </CardBody>
  </Card>
  <Card><CardHeader><CardTitle>源外部技能连接</CardTitle><CardDescription>按源条件注册；未配置或服务失败不会伪称成功。密钥只写入加密存储，留空保留，清除须明确操作；不改变CG模型目录或群管。</CardDescription></CardHeader><CardBody className="space-y-5">
   {(["music","movie_info","stickers"] as const).map(namespace=><section key={namespace} className="space-y-3"><h3 className="font-medium">{{music:"音乐服务",movie_info:"影片资料",stickers:"贴纸后备库"}[namespace]}</h3><div className="grid gap-3 md:grid-cols-2">
    {Object.entries(value[namespace]??{}).map(([key,current])=>{const secret=namespace==="movie_info"&&value.movie_secret_fields.includes(key);const label=externalLabels[key]??key;const set=(next:unknown)=>setValue({...value,[namespace]:{...value[namespace],[key]:next}});return <label className="block space-y-1 text-sm" key={key}><span>{label}</span>
     {typeof current==="boolean"?<Switch aria-label={`${namespace} ${label}`} checked={current} onCheckedChange={set}/>:Array.isArray(current)?<Textarea aria-label={`${namespace} ${label}`} rows={2} value={current.join("\n")} onChange={e=>set(e.target.value.split(/[\n,]/).map(s=>s.trim()).filter(Boolean))}/>:<Input aria-label={`${namespace} ${label}`} type={secret?"password":typeof current==="number"?"number":"text"} autoComplete={secret?"new-password":"off"} value={String(current)} placeholder={secret&&value.movie_secrets_configured[key]?"已配置，留空保留":""} onChange={e=>set(typeof current==="number"?Number(e.target.value):e.target.value)}/>}
     {secret&&value.movie_secrets_configured[key]&&<Button type="button" variant="ghost" size="sm" onClick={()=>setValue({...value,clear_movie_secrets:[...(value.clear_movie_secrets??[]),key],movie_info:{...value.movie_info,[key]:""}})}>{value.clear_movie_secrets?.includes(key)?"保存时清除":"清除此密钥"}</Button>}
    </label>})}
   </div></section>)}
   {!value.secret_storage_ready&&<p className="text-xs text-[var(--warning)]">未配置原生密钥加密主密钥；服务端将拒绝保存影片/API查询密钥，请由发布者配置CONFIG_MASTER_KEY。</p>}
  </CardBody></Card>
  <Card><CardHeader><CardTitle>原生 Prompt</CardTitle><CardDescription>分别进入源服务组装，不混入旧 Go Prompt。清空恢复固定源默认；不改变技能权限或群管授权。</CardDescription></CardHeader><CardBody className="space-y-3">
   {Object.entries(value.prompts).map(([key,text])=><label className="block space-y-2 text-sm" key={key}>{promptLabels[key]??key}<Textarea aria-label={`原生${promptLabels[key]??key}Prompt`} rows={5} value={text} onChange={e=>setValue({...value,prompts:{...value.prompts,[key]:e.target.value}})}/></label>)}
  </CardBody></Card>
  {error&&<p role="alert" className="text-sm text-[var(--danger)]">{error}</p>}{notice&&<p role="status" className="text-sm text-[var(--success)]">{notice}</p>}
  <div className="flex gap-2"><Button onClick={()=>void save()} disabled={saving||!dirty}>保存原生全局配置</Button><Button variant="secondary" onClick={load} disabled={saving}>重新读取</Button></div>
 </div>;
}

type GroupSettings = {
 cg_config_revision?:number; at_reply_mode?:boolean;tts_mode?:string;mute_all_replies?:boolean;
 api_model_query?:{enabled:boolean;base_url:string;http_timeout_sec:number;check_timeout_sec:number;api_key_configured:boolean};
 scheduled_tasks?:{cooldown_topic?:{enabled?:boolean;task_brief?:string}};
 speech_style?:{target_user_id?:number;target_user_name?:string;profile_text?:string;sample_count?:number;distilled_at_count?:number};
};
export function NativeGroupSettings({chatId,chatEnabled,onChatChange,onSaveChat,chatDirty}:{chatId:number;chatEnabled:boolean;onChatChange:(value:boolean)=>void;onSaveChat:()=>void;chatDirty:boolean}) {
 const [value,setValue]=useState<GroupSettings|null>(null);const [snapshot,setSnapshot]=useState<GroupSettings|null>(null);
 const [target,setTarget]=useState({user_id:0,user_name:""});const [error,setError]=useState("");const [notice,setNotice]=useState("");const [saving,setSaving]=useState(false);
 const targetDirty=target.user_id!==(snapshot?.speech_style?.target_user_id??0)||target.user_name!==(snapshot?.speech_style?.target_user_name??"");
 const [apiKey,setApiKey]=useState("");const [clearAPI,setClearAPI]=useState(false);
 const dirty=JSON.stringify(value)!==JSON.stringify(snapshot)||targetDirty||!!apiKey||clearAPI;
 useDirtyGuard(dirty,"原生群设置尚未保存，确定离开吗？",`native-group-${chatId}`);
 const path=`/api/admin/groups/${chatId}/assistant/native`;
 function load(){setError("");setApiKey("");setClearAPI(false);void apiFetch<{settings:GroupSettings}>(path).then(v=>{setValue(v.settings);setSnapshot(v.settings);setTarget({user_id:v.settings.speech_style?.target_user_id??0,user_name:v.settings.speech_style?.target_user_name??""})}).catch(e=>setError(e.message))}
 useEffect(load,[chatId]);
 async function save(){if(!value)return;setSaving(true);setError("");try{
  const settings:Record<string,unknown>={at_reply_mode:!!value.at_reply_mode,tts_mode:value.tts_mode??"off",mute_all_replies:!!value.mute_all_replies,cooldown_topic:{enabled:!!value.scheduled_tasks?.cooldown_topic?.enabled,task_brief:value.scheduled_tasks?.cooldown_topic?.task_brief??""}};
  if(targetDirty)settings.mimic_target=target;
  const api=value.api_model_query;
  if(api&&(JSON.stringify(api)!==JSON.stringify(snapshot?.api_model_query)||apiKey||clearAPI))settings.api_model_query={enabled:api.enabled,base_url:api.base_url,http_timeout_sec:api.http_timeout_sec,check_timeout_sec:api.check_timeout_sec,...(apiKey?{api_key:apiKey}:{}),...(clearAPI?{clear_api_key:true}:{})};
  const v=await apiFetch<{settings:GroupSettings}>(path,{method:"PUT",body:JSON.stringify({revision:value.cg_config_revision??0,settings})});
  setValue(v.settings);setSnapshot(v.settings);setTarget({user_id:v.settings.speech_style?.target_user_id??0,user_name:v.settings.speech_style?.target_user_name??""});setApiKey("");setClearAPI(false);setNotice("原生群设置已保存；未覆盖实时生成的画像和调度回执");
 }catch(e){setError(e instanceof Error?e.message:"保存失败")}finally{setSaving(false)}}
 if(!value)return <Card><CardBody><p>{error||"正在读取原生群配置…"}</p><Button variant="secondary" onClick={load}>重试</Button></CardBody></Card>;
 function setTask(patch:Record<string,unknown>){if(value)setValue({...value,scheduled_tasks:{...value.scheduled_tasks,cooldown_topic:{...value.scheduled_tasks?.cooldown_topic,...patch}}})}
 return <div className="space-y-4" data-testid="native-group-settings">
  <Card><CardHeader><CardTitle>原生回复与媒体</CardTitle><CardDescription>仅处理已审核且仍有效的消息，按 topic 隔离；不注册群规管理或投票封禁。普通聊天自动抽事实已停止，学语气仍由源 compress 角色提炼。</CardDescription></CardHeader><CardBody className="space-y-4">
   <label className="flex items-center justify-between text-sm">启用群聊天应答<Switch aria-label="启用原生聊天" checked={chatEnabled} onCheckedChange={onChatChange}/></label>
   <Button disabled={!chatDirty} onClick={onSaveChat}>保存聊天授权</Button>
   <label className="flex items-center justify-between text-sm">仅 @ 或回复机器人时应答<Switch aria-label="原生仅@回复" checked={!!value.at_reply_mode} onCheckedChange={v=>setValue({...value,at_reply_mode:v})}/></label>
   <label className="flex items-center justify-between text-sm">静默全部助手回复<Switch aria-label="原生静默" checked={!!value.mute_all_replies} onCheckedChange={v=>setValue({...value,mute_all_replies:v})}/></label>
   <label className="block text-sm">语音模式<Select aria-label="原生语音模式" value={value.tts_mode??"off"} onChange={e=>setValue({...value,tts_mode:e.target.value})}><option value="off">关闭</option><option value="on">按技能请求</option><option value="always">总是语音（失败按源退化）</option></Select></label>
   {value.api_model_query&&<div className="space-y-3 rounded-xl border border-[var(--border)] p-3"><label className="flex items-center justify-between text-sm">群独立 API 模型查询<Switch aria-label="原生API模型查询" checked={value.api_model_query.enabled} onCheckedChange={enabled=>setValue({...value,api_model_query:{...value.api_model_query!,enabled}})}/></label>
    <label className="block text-sm">OpenAI兼容Base URL<Input aria-label="群API查询地址" value={value.api_model_query.base_url} onChange={e=>setValue({...value,api_model_query:{...value.api_model_query!,base_url:e.target.value}})}/></label>
    <label className="block text-sm">API Key（只写，留空保留）<Input aria-label="群API查询密钥" type="password" autoComplete="new-password" value={apiKey} placeholder={value.api_model_query.api_key_configured?"已配置，留空保留":"未配置"} onChange={e=>setApiKey(e.target.value)}/></label>
    <div className="grid gap-2 md:grid-cols-2"><label className="text-sm">列表超时（秒）<Input aria-label="群API列表超时" type="number" min={1} max={300} value={value.api_model_query.http_timeout_sec} onChange={e=>setValue({...value,api_model_query:{...value.api_model_query!,http_timeout_sec:Number(e.target.value)}})}/></label><label className="text-sm">测活超时（秒）<Input aria-label="群API测活超时" type="number" min={1} max={600} value={value.api_model_query.check_timeout_sec} onChange={e=>setValue({...value,api_model_query:{...value.api_model_query!,check_timeout_sec:Number(e.target.value)}})}/></label></div>
    <Button type="button" variant="secondary" onClick={()=>{setClearAPI(true);setApiKey("");setValue({...value,api_model_query:{...value.api_model_query!,enabled:false}})}}>{clearAPI?"保存时停用并清除密钥":"停用并清除API查询密钥"}</Button><p className="text-xs text-[var(--text-muted)]">仅向该群提供源list_models/check_model技能，不替换ClawGuard共享模型池；测活会按用户明确请求发起模型测试。</p>
   </div>}
   <p className="text-xs text-[var(--text-muted)]">音乐、网页搜索、官方文档和媒体技能按源条件注册；服务未配置或不可用会如实失败，不伪称执行成功。模型调度与豆包凭据仍使用全局现有配置。</p>
  </CardBody></Card>
  <Card><CardHeader><CardTitle>原生主动与学语气</CardTitle><CardDescription>时间段、随机等待与上下文预算使用全局源设置。修改学习对象会按源规则重置该对象的画像和计数；普通参数保存不覆盖画像。</CardDescription></CardHeader><CardBody className="space-y-3">
   <label className="flex items-center justify-between text-sm">冷群找话题<Switch aria-label="原生冷群话题" checked={!!value.scheduled_tasks?.cooldown_topic?.enabled} onCheckedChange={v=>setTask({enabled:v})}/></label>
   <label className="block text-sm">主动任务简述<Textarea aria-label="原生主动任务简述" rows={3} maxLength={2000} value={value.scheduled_tasks?.cooldown_topic?.task_brief??""} onChange={e=>setTask({task_brief:e.target.value})}/></label>
   <div className="grid gap-3 md:grid-cols-2"><label className="text-sm">学习对象 ID<Input aria-label="原生学习对象ID" type="number" min={0} value={target.user_id} onChange={e=>setTarget({...target,user_id:Number(e.target.value)})}/></label><label className="text-sm">对象名称<Input aria-label="原生学习对象名称" value={target.user_name} onChange={e=>setTarget({...target,user_name:e.target.value})}/></label></div>
   <p className="text-xs text-[var(--text-muted)]">已采样 {value.speech_style?.sample_count??0} / 1000；上次提炼计数 {value.speech_style?.distilled_at_count??0}。目标 0 表示停止新采样。</p>
   <Textarea aria-label="原生当前画像" rows={5} readOnly value={value.speech_style?.profile_text??""}/>
  </CardBody></Card>
  {error&&<p role="alert">{error}</p>}{notice&&<p role="status">{notice}</p>}
  <div className="flex gap-2"><Button onClick={()=>void save()} disabled={!dirty||saving}>保存原生群配置</Button><Button variant="secondary" onClick={load} disabled={saving}>读取最新画像与配置</Button></div>
 </div>;
}

type PermanentMemory={id:number;content:string;created_by:number;created_at:string};
export function NativePermanentMemories({chatId}:{chatId:number}) {
 const [items,setItems]=useState<PermanentMemory[]>([]);const [topic,setTopic]=useState(0);const [content,setContent]=useState("");const [error,setError]=useState("");const [editing,setEditing]=useState<number|null>(null);const [saving,setSaving]=useState(false);
 useDirtyGuard(!!content,"永久记忆草稿尚未保存，确定离开吗？",`native-permanent-${chatId}`);
 const path=`/api/admin/groups/${chatId}/assistant/native/memories`;
 const generation=useRef(0);
 function load(){const request=++generation.current;setItems([]);void apiFetch<{items:PermanentMemory[]}>(`${path}?topic_id=${topic}`).then(v=>{if(request===generation.current){setItems(v.items);setError("")}}).catch(e=>{if(request===generation.current)setError(e.message)})}
 useEffect(load,[chatId,topic]);
 async function add(){setSaving(true);try{await apiFetch(editing?`${path}/${editing}`:path,{method:editing?"PUT":"POST",body:JSON.stringify({topic_id:topic,content})});setContent("");setEditing(null);load()}catch(e){setError(e instanceof Error?e.message:"保存失败")}finally{setSaving(false)}}
 async function remove(id:number){if(!confirm("确认删除这条管理员永久记忆？不会清空其他记忆或Wiki。"))return;try{await apiFetch(`${path}/${id}?topic_id=${topic}`,{method:"DELETE"});load()}catch(e){setError(e instanceof Error?e.message:"删除失败")}}
 return <Card><CardHeader><CardTitle>管理员永久记忆</CardTitle><CardDescription>直接使用 SGB 永久记忆数据域，直到管理员更新或删除；旧自动学习记录不会自动升级。Wiki按授权来源全文检索，不作为短句塞入此列表。</CardDescription></CardHeader><CardBody className="space-y-3">
  <label className="block text-sm">Topic 编号（0为普通会话）<Input aria-label="永久记忆Topic" type="number" min={0} value={topic} disabled={saving} onChange={e=>{if(content&&!confirm("放弃当前永久记忆草稿并切换Topic？"))return;generation.current++;setItems([]);setEditing(null);setContent("");setTopic(Number(e.target.value))}}/></label>
  <Textarea aria-label="新增管理员永久记忆" value={content} onChange={e=>setContent(e.target.value)} rows={3}/><Button onClick={()=>void add()} disabled={!content.trim()||saving}>{editing?`更新永久记忆 #${editing}`:"添加永久记忆"}</Button>{editing&&<Button variant="ghost" onClick={()=>{setContent("");setEditing(null)}} disabled={saving}>取消编辑</Button>}
  {error&&<p role="alert">{error}</p>}
  {items.map(item=><div key={item.id} className="rounded-xl border border-[var(--border)] p-3"><p className="whitespace-pre-wrap text-sm">#{item.id} {item.content}</p><p className="mt-1 text-xs text-[var(--text-muted)]">管理员 {item.created_by} · {item.created_at} · 永久</p><Button variant="ghost" size="sm" disabled={saving} onClick={()=>{setEditing(item.id);setContent(item.content)}}>编辑永久记忆 #{item.id}</Button><Button variant="ghost" size="sm" disabled={saving} onClick={()=>void remove(item.id)}>删除永久记忆 #{item.id}</Button></div>)}
 </CardBody></Card>;
}

type NativeSkill={name:string;description:string;enabled:boolean;read_only:boolean};
export function NativeSkills({chatId}:{chatId:number}) {
 const [skills,setSkills]=useState<NativeSkill[]|null>(null);const [error,setError]=useState("");
 function load(){setError("");void apiFetch<{skills:NativeSkill[]}>(`/api/admin/groups/${chatId}/assistant/native`).then(v=>setSkills(v.skills)).catch(e=>setError(e.message))}
 useEffect(load,[chatId]);
 return <Card data-testid="native-skills"><CardHeader><CardTitle>原生可用技能</CardTitle><CardDescription>直接读取当前群源SkillService实际注册与模式选择；并非旧Go工具白名单。管理员永久记忆可写，媒体可投递；rule_manage和vote_ban不注册。外部服务可用性以实际执行为准。</CardDescription></CardHeader><CardBody className="space-y-3">
  {error&&<p role="alert">{error}</p>}{!skills&&!error&&<p>读取源技能…</p>}
  {skills?.map(skill=><div key={skill.name} className="rounded-xl border border-[var(--border)] p-3"><p className="font-medium">{skill.name} <span className="text-xs text-[var(--text-muted)]">{skill.read_only?"只读查询":"按源权限写入或投递"}</span></p><p className="mt-1 text-sm text-[var(--text-muted)]">{skill.description}</p></div>)}
  <p className="text-xs text-[var(--text-muted)]">影片资料须配置相应服务；语音须CG豆包连接就绪及群语音模式允许；API模型查询须本群地址与密钥。未满足条件的技能不会出现在模型工具定义中。Wiki只读且仅两群授权。</p><Button variant="secondary" onClick={load}>刷新原生技能</Button>
 </CardBody></Card>;
}
