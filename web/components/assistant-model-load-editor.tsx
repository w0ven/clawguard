"use client";

import { useEffect, useState } from "react";
import { useDirtyGuard } from "@/components/dirty-guard";
import { GuardedLink } from "@/components/guarded-link";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { saveAssistantPool, normalizeAssistantPoolConfig, type AssistantModelLoadOptions, type AssistantPool, type AssistantPoolConfig, type RegistryModel } from "@/lib/group-assistant";

export const assistantTaskLabels: Record<string,string> = { main:"主模型",chat:"聊天",learning:"学习",decision:"决策",vision:"视觉",compress:"压缩",vector:"向量" };
export function assistantModelUnsupported(model: RegistryModel, task: string): string {
  if (!model.enabled) return "模型已停用";
  if ((task === "main" || task === "chat") && !model.supports_tools) return "未声明技能调用能力";
  if (task === "vision" && !model.supports_vision) return "未声明视觉能力";
  if (task === "vector" && !(model.capability_tags ?? []).some(t => ["embedding","embeddings"].includes(t.toLowerCase()))) return "未声明 embedding 能力";
  return "";
}
export function modelLoadDefaults(timeout=12000): AssistantModelLoadOptions { return { weight:1,max_concurrency:2,timeout_ms:timeout,cooldown_duration_sec:30 }; }
export function ModelChainEditor({task,refs,options,models,registryError,weighted,onChange,onOptions,timeout=12000}:{task:string;refs:string[];options:Record<string,AssistantModelLoadOptions>;models:RegistryModel[];registryError?:string|null;weighted:boolean;onChange:(refs:string[])=>void;onOptions:(value:Record<string,AssistantModelLoadOptions>)=>void;timeout?:number}) {
  const label=assistantTaskLabels[task] ?? task;
  const items=refs.length?refs:[""];
  const compatible=models.filter(model=>!assistantModelUnsupported(model,task));
  const unavailable=registryError || (models.length===0?"现有模型目录为空，暂无可用模型。":compatible.length===0?`当前没有可用于${label}的已启用模型，请核实相应能力声明。`:"");
  return <div className="min-w-0 space-y-3">
    {unavailable&&<p role="note" className="text-sm text-[var(--warning)]">{unavailable} 请到<GuardedLink href="/llm" className="underline">现有模型管理</GuardedLink>添加、启用或核实模型；目录加载失败时可稍后刷新重试，已有草稿不会自动保存。</p>}
    {items.map((ref,index)=>{
      const load={...modelLoadDefaults(timeout),...options[ref]};
      const model=models.find(m=>m.ref===ref);
      const problem=ref ? (model?assistantModelUnsupported(model,task):"模型目录暂不可用或引用已失效") : "";
      const update=(key:keyof AssistantModelLoadOptions,value:number)=>onOptions({...options,[ref]:{...load,[key]:value}});
      const move=(direction:number)=>{const next=[...items];[next[index],next[index+direction]]=[next[index+direction],next[index]];onChange(next);};
      return <div key={`${task}-${index}`} className="min-w-0 space-y-2 rounded-xl border border-[var(--border)] p-3">
        <label className="block text-xs text-[var(--text-muted)]">{label}{index===0?"主模型":`备用 ${index}`}
          <Select aria-label={`${label}模型 ${index+1}`} value={ref} onChange={e=>{const next=[...items];next[index]=e.target.value;onChange(next);}}>
            <option value="">请选择现有模型</option>
            {ref&&!model&&<option value={ref}>{ref}（当前不可用）</option>}
            {models.map(m=>{const reason=assistantModelUnsupported(m,task);return <option key={m.ref} value={m.ref} disabled={Boolean(reason)||items.some((r,i)=>i!==index&&r===m.ref)}>{m.label||m.ref}{reason?` · ${reason}`:""}</option>;})}
          </Select>
        </label>
        {problem&&<p role="note" className="text-xs text-[var(--warning)]">{problem}；请在模型管理核实能力，不会自动声明支持。</p>}
        {ref&&<div className="grid grid-cols-2 gap-2 lg:grid-cols-4">
          {weighted&&<label className="text-xs">权重<Input aria-label={`${label}权重 ${index+1}`} type="number" min={1} max={1000} value={load.weight} onChange={e=>update("weight",Number(e.target.value))}/></label>}
          <label className="text-xs">共享并发上限<Input aria-label={`${label}并发 ${index+1}`} type="number" min={1} max={100} value={load.max_concurrency} onChange={e=>update("max_concurrency",Number(e.target.value))}/></label>
          <label className="text-xs">超时（毫秒）<Input aria-label={`${label}端点超时 ${index+1}`} type="number" min={1000} max={120000} step={1000} value={load.timeout_ms} onChange={e=>update("timeout_ms",Number(e.target.value))}/></label>
          <label className="text-xs">冷却（秒）<Input aria-label={`${label}冷却 ${index+1}`} type="number" min={1} max={3600} value={load.cooldown_duration_sec} onChange={e=>update("cooldown_duration_sec",Number(e.target.value))}/></label>
        </div>}
        <div className="flex flex-wrap gap-2">
          <Button type="button" variant="secondary" size="sm" disabled={index===0} aria-label={`${label}上移 ${index+1}`} onClick={()=>move(-1)}>上移</Button>
          <Button type="button" variant="secondary" size="sm" disabled={index===items.length-1} aria-label={`${label}下移 ${index+1}`} onClick={()=>move(1)}>下移</Button>
          <Button type="button" variant="ghost" size="sm" aria-label={`${label}移除 ${index+1}`} onClick={()=>onChange(items.filter((_,i)=>i!==index))}>移除</Button>
        </div>
      </div>;
    })}
    <Button type="button" variant="secondary" size="sm" disabled={items.length>=16||Boolean(registryError)||compatible.length===0||items.some(ref=>!ref)} onClick={()=>onChange([...items,""])}>添加{label}备用模型</Button>
  </div>;
}

const tasks=["chat","learning","decision","vision","compress","vector"];
function draftPool(pool:AssistantPool):AssistantPoolConfig & {strategy:string} {
  const config=pool.source==="group"?pool.saved_config??pool.config:pool.source==="global"&&pool.saved_config?.endpoints?.length?pool.saved_config:pool.config;
  const draft=JSON.parse(JSON.stringify({...normalizeAssistantPoolConfig(config),inherit_global:pool.source==="global"||pool.config.inherit_global===true,strategy:config.strategy??pool.strategy,max_queue_depth:config.max_queue_depth??10,max_queue_wait_sec:config.max_queue_wait_sec??15}));
  if(config===pool.config){
    // The effective projection includes inherited routes. Only server-provided
    // provenance authorizes clearing them; equal chains may be truly explicit.
    for(const task of pool.inherited_tasks??[]){
      const a=draft.task_assignments[task];
      if(a)draft.task_assignments[task]={...a,primary:"",backups:[]};
    }
  }
  return draft;
}
function refsFor(cfg:AssistantPoolConfig,task:string):string[]{const a=cfg.task_assignments[task];return a?.primary?[a.primary,...(a.backups??[])].map(id=>cfg.endpoints.find(e=>e.id===id)?.model_ref??""):[];}
export function GroupAssistantPoolEditor({chatId,pool,models,registryError,onSaved}:{chatId:number;pool:AssistantPool;models:RegistryModel[];registryError?:string|null;onSaved:(pool:AssistantPool)=>void}) {
  const [draft,setDraft]=useState(()=>draftPool(pool));
  const [snapshot,setSnapshot]=useState(()=>JSON.stringify(draftPool(pool)));
  const [baseVersion,setBaseVersion]=useState(pool.version);
  const [saving,setSaving]=useState(false);
  const [error,setError]=useState<string|null>(null);
  const dirty=JSON.stringify(draft)!==snapshot;
  useDirtyGuard(dirty,"模型负载还有未保存草稿，确定离开吗？","assistant-pool");
  useEffect(()=>{const next=draftPool(pool);if(dirty){setError("当前生效负载已更新；未覆盖你的草稿，请核对后保存或取消。");return;}setDraft(next);setSnapshot(JSON.stringify(next));setBaseVersion(pool.version);setError(null);},[pool]);
  const updateChain=(task:string,refs:string[])=>setDraft(current=>{
    const previous=current.task_assignments[task]??{primary:"",backups:[]};
    const oldIDs=[previous.primary,...previous.backups];
    const unused=current.endpoints.filter(e=>!oldIDs.includes(e.id)||Object.entries(current.task_assignments).some(([key,a])=>key!==task&&[a.primary,...a.backups].includes(e.id)));
    const endpoints=refs.map((ref,i)=>{const existing=current.endpoints.find(e=>e.model_ref===ref&&oldIDs.includes(e.id));let id=`custom-${task}-${i}`;while(unused.some(e=>e.id===id))id+="x";return {...modelLoadDefaults(),...existing,id,model_ref:ref,name:ref,role:i===0?"primary":"backup",priority:i,supports_tools:false};});
    return {...current,endpoints:[...unused,...endpoints],task_assignments:{...current.task_assignments,[task]:{...previous,primary:endpoints[0]?.id??"",backups:endpoints.slice(1).map(e=>e.id)}}};
  });
  const save=async()=>{
    setSaving(true);setError(null);
    try {
      const payload=JSON.parse(JSON.stringify(draft)) as typeof draft;
      if(!payload.inherit_global){
        for(const task of tasks){const refs=refsFor(payload,task);if(refs.some(r=>!r))throw new Error("请完成所有已添加模型的选择，或移除空行。");if(new Set(refs).size!==refs.length)throw new Error("同一任务不能重复选择同一个模型。");}
        // Explicit subroles are never silently erased to make validation pass.
        const used=new Set(Object.values(payload.task_assignments).flatMap(a=>[a.primary,...a.backups]));
        payload.endpoints=payload.endpoints.filter(e=>used.has(e.id));
      }
      const saved=await saveAssistantPool(chatId,payload,baseVersion);const next=draftPool(saved);setDraft(next);setSnapshot(JSON.stringify(next));setBaseVersion(saved.version);onSaved(saved);
    }catch(e){setError(e instanceof Error?e.message:"保存失败，草稿已保留。");}finally{setSaving(false);}
  };
  const source=pool.source==="global"?"继承全局":pool.source==="legacy_group"?"旧群显式池（兼容覆盖）":"本群自定义";
  return <Card><CardHeader><CardTitle>助手共享模型负载</CardTitle><p className="text-sm text-[var(--text-muted)]">当前生效来源：{source} · {pool.strategy==="weighted"?"按权重分流":"主备优先"}</p><p className="break-words text-xs">当前聊天链：{refsFor(pool.config,"chat").join(" → ")||"未配置"}</p></CardHeader>
    <CardBody className="space-y-4"><fieldset disabled={saving} className="min-w-0 space-y-4">
      <label className="block text-sm">配置来源<Select aria-label="本群模型配置来源" value={draft.inherit_global?"global":"custom"} onChange={e=>setDraft({...draft,inherit_global:e.target.value==="global"})}><option value="global">继承全局</option><option value="custom">本群自定义</option></Select></label>
      {draft.inherit_global?<p className="text-sm text-[var(--text-muted)]">保存后使用全局角色与负载；已有本群模型配置保留，切回自定义可继续编辑。不修改聊天、学习、主动与语音开关。</p>:<>
        <label className="block text-sm">负载策略<Select aria-label="本群负载策略" value={draft.strategy} onChange={e=>setDraft({...draft,strategy:e.target.value,task_assignments:Object.fromEntries(Object.entries(draft.task_assignments).map(([key,a])=>[key,{...a,strategy:undefined}]))})}><option value="primary-overflow">主备优先</option><option value="weighted">按权重分流</option></Select></label>
        <p className="text-xs text-[var(--text-muted)]">只引用现有模型目录；同一模型跨群/跨角色共用本地并发上限（多个引用取较小值）。工具轮固定模型，远端配额未知。</p>
        {tasks.map(task=>{const refs=refsFor(draft,task);const inherited=task!=="chat"&&!refs.length;const options=Object.fromEntries(draft.endpoints.filter(e=>[draft.task_assignments[task]?.primary,...(draft.task_assignments[task]?.backups??[])].includes(e.id)).map(e=>[e.model_ref,{...modelLoadDefaults(),weight:e.weight??1,max_concurrency:e.max_concurrency,timeout_ms:e.timeout_ms,cooldown_duration_sec:e.cooldown_duration_sec}]));return <details key={task} open={task==="chat"} className="rounded-xl border border-[var(--border)] p-3"><summary className="cursor-pointer text-sm font-medium">{assistantTaskLabels[task]}路由 {inherited?"· 继承聊天主角色":""}</summary><div className="mt-3 space-y-3">
          {task!=="chat"&&<Select aria-label={`${assistantTaskLabels[task]}路由来源`} value={inherited?"inherit":"custom"} onChange={e=>updateChain(task,e.target.value==="inherit"?[]:[""])}><option value="inherit">继承聊天主角色（按能力筛选）</option><option value="custom">单独配置</option></Select>}
          {!inherited&&draft.task_assignments[task]?.strategy&&<p className="text-xs">此角色保存的策略：{draft.task_assignments[task].strategy==="weighted"?"按权重分流":"主备优先"}（更改本群负载策略可统一重设）</p>}
          {!inherited&&<ModelChainEditor task={task} registryError={registryError} refs={refs} options={options} models={models} weighted={(draft.task_assignments[task]?.strategy||draft.strategy)==="weighted"} onChange={r=>updateChain(task,r)} onOptions={next=>setDraft(current=>({...current,endpoints:current.endpoints.map(e=>[current.task_assignments[task]?.primary,...(current.task_assignments[task]?.backups??[])].includes(e.id)?{...e,...next[e.model_ref]}:e)}))}/>}
          {task==="vector"&&<p className="text-xs text-[var(--text-muted)]">没有可靠 embedding 声明时使用持久词法检索，不声称语义召回成功。</p>}
        </div></details>;})}
      </>}
    </fieldset>
    {error&&<p role="alert" className="text-sm text-[var(--danger)]">{error} 草稿已保留，可修改后重试。</p>}
    <div className="flex flex-wrap items-center gap-2"><Button type="button" disabled={saving||!dirty} onClick={()=>void save()}>{saving?"保存中…":"保存本群负载"}</Button><Button type="button" variant="secondary" disabled={saving||!dirty} onClick={()=>{const next=draftPool(pool);setDraft(next);setSnapshot(JSON.stringify(next));setBaseVersion(pool.version);setError(null);}}>取消负载修改</Button><span className="text-xs text-[var(--text-muted)]">{dirty?"未保存草稿，不影响当前生效路由":"已保存"}</span></div>
    </CardBody></Card>;
}
