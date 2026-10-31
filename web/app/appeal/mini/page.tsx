"use client";

import { useEffect, useRef, useState } from "react";
import { loadTurnstile, loadHCaptcha } from "@/components/dual-captcha-verify-client";
import { telegramWebApp } from "@/lib/telegram";

type Case = { id: number; group_title: string; action: string; result: string; explanation: string; summary: string; created_at: string; appeal_status: string | null };
type Appeal = { id: number; case_id: number; status: string; reason: string; reference_url: string; created_at: string; public_note?: string; execution_result?: string };
type Challenge = { token: string; turnstile_sitekey: string; hcaptcha_sitekey: string; expires_in: number };
const actions: Record<string, string> = { delete: "删除消息", warn: "警告", delete_warn: "删除消息并警告", delete_and_warn: "删除消息并警告", mute: "禁言", mute_5m: "禁言", mute_1h: "禁言", delete_mute: "删除消息并禁言", ban: "封禁", delete_ban: "删除消息并封禁", kick: "移出群聊", unban: "解封请求", unmute: "解除禁言" };
const results: Record<string, string> = {executed:"已执行",partial:"部分处理或状态待确认",failed:"状态未确认",reversed:"已撤销"};
const actionLabel = (c: Case) => c.result === 'partial' || c.result === 'failed' ? `原拟处理：${actions[c.action] || '群规则处理'}` : (actions[c.action] || '群规则处理');
const statuses: Record<string, string> = {pending:"待审核", approved:"已通过", upheld:"维持原处理", executing:"审核通过，正在处理", failed:"审核通过，处理待重试", manual_review:"审核通过，需人工核查", completed:"审核通过，处理完成"};
const date = (v: string) => new Date(v).toLocaleString("zh-CN");

export default function AppealMiniPage() {
 const [init, setInit] = useState("");
 const [caseID, setCaseID] = useState<number | null>(null);
 const [items, setItems] = useState<Case[]>([]); const [offset,setOffset]=useState(0); const [more,setMore]=useState(false);
 const [item,setItem]=useState<Case | null>(null); const [appeal,setAppeal]=useState<Appeal | null>(null);
 const [reason,setReason]=useState(""); const [link,setLink]=useState("");
 const [challenge,setChallenge]=useState<Challenge | null>(null); const [cf,setCF]=useState(""); const [hc,setHC]=useState("");
 const [error,setError]=useState(""); const [busy,setBusy]=useState(false);
 const cfRef=useRef<HTMLDivElement>(null); const hcRef=useRef<HTMLDivElement>(null); const startCase=useRef<number | null>(null);
 useEffect(() => {
  const u=new URL(window.location.href); const id=u.searchParams.get("case");
  if (id && /^[1-9]\d*$/.test(id) && Number.isSafeInteger(Number(id))) startCase.current=Number(id);
  u.searchParams.delete("case"); window.history.replaceState(window.history.state,"",u.pathname+u.search+u.hash);
  let disposed=false; let cleanup=()=>{};
  const script=document.createElement("script"); script.src="https://telegram.org/js/telegram-web-app.js";
  script.onload=()=>{ if(disposed)return; const app=telegramWebApp(); if(!app?.initData){setError("无法验证当前账号，请本人从机器人私聊打开。");return;}
   const update=()=>{document.documentElement.style.setProperty("--mini-safe-top",`${Math.max(0,app.safeAreaInset?.top??0)+Math.max(0,app.contentSafeAreaInset?.top??0)}px`);document.documentElement.style.setProperty("--mini-safe-bottom",`${Math.max(0,app.safeAreaInset?.bottom??0)+Math.max(0,app.contentSafeAreaInset?.bottom??0)}px`)};
   app.onEvent?.("safeAreaChanged",update); app.onEvent?.("contentSafeAreaChanged",update); cleanup=()=>{app.offEvent?.("safeAreaChanged",update);app.offEvent?.("contentSafeAreaChanged",update)}; app.ready();app.expand();update();setInit(app.initData);setCaseID(startCase.current);
  }; script.onerror=()=>{if(!disposed)setError("页面加载失败，请返回私聊重试。");};document.head.appendChild(script);
  return ()=>{disposed=true;cleanup();script.remove();document.documentElement.style.removeProperty("--mini-safe-top");document.documentElement.style.removeProperty("--mini-safe-bottom")};
 },[]);
 async function request(path:string, data:object) {
  const r=await fetch(`/api/appeal/mini/${path}`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({init_data:init,...data}),cache:"no-store"});
  const b=await r.json(); if(!r.ok) throw new Error(b.error||"请求失败，请稍后重试"); return b;
 }
 useEffect(()=>{ if(!init)return; let cancelled=false; setError("");setItem(null);setAppeal(null);setChallenge(null);
  if(caseID===null){request("list",{offset}).then(b=>{if(!cancelled){setItems(b.cases);setMore(b.has_more)}}).catch(e=>{if(!cancelled)setError(e.message)});}
  else {request("case",{case_id:caseID}).then(b=>{if(!cancelled){setItem(b.case);setAppeal(b.appeal)}}).catch(e=>{if(!cancelled)setError(e.message)});}
  return ()=>{cancelled=true};
 // request uses init, which is tracked here.
 // eslint-disable-next-line react-hooks/exhaustive-deps
 },[init,caseID,offset]);
 useEffect(()=>{if(!challenge)return;let cancel=false;let cfID="",hcID="";setCF("");setHC("");
  Promise.all([loadTurnstile(),loadHCaptcha()]).then(()=>{if(cancel)return;if(!window.turnstile||!window.hcaptcha||!cfRef.current||!hcRef.current)throw new Error("验证组件暂不可用");
   const fail=()=>{setCF("");setHC("");setError("验证已过期或失败，请刷新验证；已填写内容会保留。")};
   cfID=window.turnstile.render(cfRef.current,{sitekey:challenge.turnstile_sitekey,action:"clawguard_dual",cData:challenge.token,theme:"auto",callback:setCF,"error-callback":fail,"expired-callback":fail});
   hcID=window.hcaptcha.render(hcRef.current,{sitekey:challenge.hcaptcha_sitekey,callback:setHC,"error-callback":fail,"expired-callback":fail});
  }).catch(e=>{if(!cancel)setError(e.message)});
  const timer=window.setTimeout(()=>{setCF("");setHC("");setError("验证已过期，请刷新验证；已填写内容会保留。")},challenge.expires_in*1000);
  return ()=>{cancel=true;clearTimeout(timer);if(cfID)window.turnstile?.remove(cfID);if(hcID)window.hcaptcha?.remove(hcID)};
 },[challenge]);
 async function begin(){if(!caseID||busy)return;setBusy(true);setError("");setChallenge(null);try{const b=await request("challenge",{case_id:caseID});if(b.appeal)setAppeal(b.appeal);else setChallenge(b as Challenge)}catch(e){setError(e instanceof Error?e.message:"暂不可用")}finally{setBusy(false)}}
 async function submit(){if(!caseID||!challenge||!cf||!hc||busy)return;setBusy(true);setError("");try{const b=await request("submit",{case_id:caseID,token:challenge.token,reason,reference_url:link,cf_response:cf,h_response:hc});setAppeal(b.appeal);setChallenge(null)}catch(e){setCF("");setHC("");setError(e instanceof Error?e.message:"提交失败，请刷新验证")}finally{setBusy(false)}}
 const button="rounded-lg bg-emerald-700 px-4 py-3 text-white disabled:opacity-50";
 return <main className="mx-auto min-h-screen max-w-xl space-y-4 bg-[#edf2ef] px-4 py-6 text-[#18342f]" style={{paddingTop:"max(1.5rem, var(--mini-safe-top, 0px))",paddingBottom:"max(1.5rem, var(--mini-safe-bottom, 0px))"}}>
  <header><h1 className="text-xl font-semibold">{caseID===null?"我的处理记录":appeal?"申诉进度":"提交申诉"}</h1>{caseID!==null&&<button className="mt-2 text-emerald-700" onClick={()=>{setCaseID(null);setReason("");setLink("")}}>‹ 返回处理记录</button>}</header>
  {error&&<p role="alert" className="rounded-lg bg-red-50 p-3 text-red-800">{error}</p>}
  {!init&&!error&&<p>正在加载…</p>}
  {init&&caseID===null&&<><p className="text-sm">仅显示你自己的处理记录，不需要仍在群里。</p>{items.map(c=><button key={c.id} onClick={()=>setCaseID(c.id)} className="block w-full rounded-lg border bg-white p-4 text-left"><strong>CASE-{c.id} · {c.group_title || "群聊"}</strong><p>{actionLabel(c)} · {results[c.result]||"状态待确认"}</p><p className="text-sm">{date(c.created_at)} {c.appeal_status&&` · ${statuses[c.appeal_status]||"处理中"}`}</p></button>)}{items.length===0&&<p>暂无处理记录</p>}<div className="flex gap-3"><button className={button} disabled={offset===0} onClick={()=>setOffset(Math.max(0,offset-20))}>上一页</button><button className={button} disabled={!more} onClick={()=>setOffset(offset+20)}>下一页</button></div></>}
  {caseID!==null&&item&&<><section className="space-y-2 rounded-lg border bg-white p-4"><strong>CASE-{item.id} · {item.group_title || "群聊"}</strong><p>{actionLabel(item)} · {results[item.result]||"状态待确认"}</p><p>记录时间：{date(item.created_at)}</p>{item.explanation&&<p>说明：{item.explanation}</p>}{item.summary&&<p>原消息摘要：{item.summary}</p>}</section>
   {appeal?<section className="space-y-2 rounded-lg border bg-white p-4"><h2 className="font-semibold">✓ 申诉已提交</h2><p>申诉编号：APL-{appeal.id}</p><p>关联案件：CASE-{appeal.case_id}</p><p>提交时间：{date(appeal.created_at)}</p><p>当前状态：{statuses[appeal.status]||"处理中"}</p>{appeal.execution_result&&<p>处理结果：{appeal.execution_result}</p>}{appeal.public_note&&<p>审核说明：{appeal.public_note}</p>}<p>申诉理由：{appeal.reason}</p><p>提交申诉不代表自动解除处罚。</p></section>:!challenge?<section className="space-y-3 rounded-lg border bg-white p-4"><label className="block font-semibold" htmlFor="reason">申诉理由（必填）</label><textarea id="reason" className="w-full rounded border p-2" rows={6} maxLength={2000} value={reason} onChange={e=>setReason(e.target.value)} /><label className="block" htmlFor="link">补充参考链接（选填）</label><input id="link" type="url" maxLength={2048} className="w-full rounded border p-2" value={link} onChange={e=>setLink(e.target.value)} /><p className="text-sm">提交申诉不代表自动解除处罚，需管理员人工审核。同一处理已有申诉时不重复提交。</p><button className={button} disabled={!reason.trim()||busy} onClick={()=>void begin()}>下一步：安全验证</button></section>:<section className="space-y-3 rounded-lg border bg-white p-4"><h2 className="font-semibold">安全验证</h2><p>你填写的理由已保留。提交前需完成以下两项验证。</p><div ref={cfRef} className="min-h-[65px]"/><div ref={hcRef} className="min-h-[65px]"/><p className="text-sm">如验证过期可刷新，填写的内容不会丢失。</p><div className="flex flex-wrap gap-2"><button className={button} disabled={!cf||!hc||busy} onClick={()=>void submit()}>提交申诉</button><button className="rounded border px-4" onClick={()=>void begin()} disabled={busy}>刷新验证</button><button className="rounded border px-4" onClick={()=>{setChallenge(null);setError("")}}>返回修改理由</button></div></section>}
  </>}
 </main>;
}
