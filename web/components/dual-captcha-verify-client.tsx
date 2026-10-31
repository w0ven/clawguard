"use client";

import { useEffect, useRef, useState } from "react";
import { Shield } from "lucide-react";
import { Button } from "@/components/ui/button";
import { telegramWebApp } from "@/lib/telegram";

declare global {
  interface Window {
    __clawguardHcaptchaOnload?: () => void;
    hcaptcha?: {
      render: (container: HTMLElement, options: {
        sitekey: string;
        callback: (token: string) => void;
        "error-callback": () => void;
        "expired-callback": () => void;
      }) => string;
      remove: (id: string) => void;
    };
  }
}

const cfScript = "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
const hScript = "https://js.hcaptcha.com/1/api.js?onload=__clawguardHcaptchaOnload&render=explicit";
let hCaptchaLoading: Promise<void> | null = null;
let turnstileLoading: Promise<void> | null = null;

type SiteKeys = { turnstile_sitekey: string; hcaptcha_sitekey: string; group_title?: string; user_id?: number; username?: string | null; expires_at?: string; remaining_ms?: number; recovery_pending?: boolean };

function loadScript(url: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const script = document.createElement("script");
    const timeout = window.setTimeout(() => { script.remove(); reject(new Error("验证组件加载超时")); }, 15000);
    script.src = url;
    script.async = true;
    script.defer = true;
    script.onload = () => { window.clearTimeout(timeout); resolve(); };
    script.onerror = () => { window.clearTimeout(timeout); script.remove(); reject(new Error("验证组件加载失败")); };
    document.head.appendChild(script);
  });
}

export function loadTurnstile(): Promise<void> {
  if (turnstileLoading) return turnstileLoading;
  if (window.turnstile) return Promise.resolve();
  turnstileLoading = loadScript(cfScript).catch((error) => { turnstileLoading = null; throw error; });
  return turnstileLoading;
}

export function loadHCaptcha(): Promise<void> {
  if (hCaptchaLoading) return hCaptchaLoading;
  if (window.hcaptcha) return Promise.resolve();
  hCaptchaLoading = new Promise<void>((resolve, reject) => {
    const script = document.createElement("script");
    const timeout = window.setTimeout(() => {
      script.remove();
      delete window.__clawguardHcaptchaOnload;
      reject(new Error("hCaptcha 组件加载超时"));
    }, 15000);
    window.__clawguardHcaptchaOnload = () => {
      window.clearTimeout(timeout);
      delete window.__clawguardHcaptchaOnload;
      resolve();
    };
    script.src = hScript;
    script.async = true;
    script.defer = true;
    script.onerror = () => {
      window.clearTimeout(timeout);
      delete window.__clawguardHcaptchaOnload;
      script.remove();
      reject(new Error("hCaptcha 组件加载失败"));
    };
    document.head.appendChild(script);
  }).catch((error) => { hCaptchaLoading = null; throw error; });
  return hCaptchaLoading;
}

export function DualCaptchaVerifyClient({ token, initData }: { token: string; initData?: string }) {
  const mini = initData !== undefined;
  const [details, setDetails] = useState<SiteKeys | null>(null);
  const [elapsed, setElapsed] = useState(0);
  const deadlineAnchor = useRef(0);
  useEffect(() => { if (!mini) return; const timer = window.setInterval(() => setElapsed(performance.now()), 1000); return () => window.clearInterval(timer); }, [mini]);
  const remaining = details?.remaining_ms !== undefined ? Math.max(0, Math.ceil((details.remaining_ms - Math.max(0, elapsed - deadlineAnchor.current)) / 1000)) : null;
  const cfContainer = useRef<HTMLDivElement>(null);
  const hContainer = useRef<HTMLDivElement>(null);
  const [attempt, setAttempt] = useState(0);
  const [cfToken, setCFToken] = useState("");
  const [hToken, setHToken] = useState("");
  const [ready, setReady] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [success, setSuccess] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    let cfID = "";
    let hID = "";
    setReady(false);
    setCFToken("");
    setHToken("");
    setError("");

    const fail = (message: string) => {
      if (cancelled) return;
      setCFToken("");
      setHToken("");
      setError(message);
    };
    async function initialize() {
      try {
        const requestStarted = performance.now();
        const response = await fetch(mini ? "/api/verify/mini/config" : "/api/verify/dual/config", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ token, ...(mini ? { init_data: initData } : {}) }), cache: "no-store",
        });
        if (!response.ok) {
          const body = await response.json().catch(() => ({})) as { error?: string };
          throw new Error(body.error || "验证暂不可用");
        }
        const keys = await response.json() as SiteKeys;
        deadlineAnchor.current = requestStarted;
        setElapsed(performance.now());
        setDetails(keys);
        if (mini && keys.recovery_pending) return;
        if (!keys.turnstile_sitekey || !keys.hcaptcha_sitekey) throw new Error("双重验证未配置");
        await Promise.all([
          loadTurnstile(),
          loadHCaptcha(),
        ]);
        if (cancelled || !window.turnstile || !window.hcaptcha || !cfContainer.current || !hContainer.current) throw new Error("验证组件不可用");
        cfID = window.turnstile.render(cfContainer.current, {
          sitekey: keys.turnstile_sitekey,
          action: "clawguard_dual",
          cData: token,
          theme: "auto",
          callback: setCFToken,
          "error-callback": () => fail("Cloudflare 验证失败，请刷新两项后重试"),
          "expired-callback": () => fail("Cloudflare 验证已过期，请刷新两项后重试"),
        });
        hID = window.hcaptcha.render(hContainer.current, {
          sitekey: keys.hcaptcha_sitekey,
          callback: setHToken,
          "error-callback": () => fail("hCaptcha 验证失败，请刷新两项后重试"),
          "expired-callback": () => fail("hCaptcha 验证已过期，请刷新两项后重试"),
        });
        setReady(true);
      } catch (e) { if (!cancelled) fail(e instanceof Error ? e.message : "验证组件加载失败"); }
    }
    void initialize();
    return () => {
      cancelled = true;
      if (cfID && window.turnstile) window.turnstile.remove(cfID);
      if (hID && window.hcaptcha) window.hcaptcha.remove(hID);
    };
  }, [token, attempt, mini, initData]);

  async function submit() {
    const recovery = mini && details?.recovery_pending;
    if ((!recovery && (!cfToken || !hToken)) || submitting) return;
    setSubmitting(true);
    try {
      const response = await fetch(recovery ? "/api/verify/mini/recover" : mini ? "/api/verify/mini/submit" : "/api/verify/dual/submit", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token, ...(recovery ? {} : { cf_response: cfToken, h_response: hToken }), ...(mini ? { init_data: initData } : {}) }),
      });
      if (!response.ok) {
        const body = await response.json().catch(() => ({})) as { error?: string };
        throw new Error(body.error || "验证失败，请刷新两项后重试");
      }
      setSuccess(true);
    } catch (e) {
      setCFToken(""); setHToken("");
      setError(e instanceof Error ? e.message : "验证失败，请刷新两项后重试");
    } finally { setSubmitting(false); }
  }

  return (
    <main className="flex min-h-screen items-center justify-center px-4 py-8" style={{ paddingTop: "max(2rem, env(safe-area-inset-top), var(--mini-safe-top, 0px))", paddingBottom: "max(2rem, env(safe-area-inset-bottom), var(--mini-safe-bottom, 0px))" }}>
      <div className="flex w-full max-w-sm flex-col items-center gap-5">
        <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-[var(--accent)] text-white"><Shield /></div>
        {success ? <div className="text-center"><h1 className="text-xl font-semibold">{mini ? "入群验证已完成" : "验证通过"}</h1><p className="mt-2 text-sm text-[var(--text-muted)]">本次入群验证限制已解除，其他未到期的限制不受影响。</p>{mini && <Button className="mt-4" onClick={() => telegramWebApp()?.close()}>关闭验证页</Button>}</div> : <>
          <div className="text-center"><h1 className="text-xl font-semibold">{mini ? "入群验证" : "双重人机验证"}</h1><p className="mt-2 text-sm text-[var(--text-muted)]">{details?.recovery_pending ? "验证已完成，请继续解除本次限制。" : "两项都完成后，即可提交验证。"}</p></div>
          {mini && details && <div className="w-full rounded-lg border p-3 text-sm"><strong>{details.group_title}</strong><p>验证账号：{details.username ? `@${details.username} ` : ""}(ID: {details.user_id})</p><p>剩余时间：{remaining !== null ? `${Math.floor(remaining / 60).toString().padStart(2, "0")}:${(remaining % 60).toString().padStart(2, "0")}` : "--:--"}</p><p className="text-[var(--text-muted)]">请使用本人 Telegram 账号操作。</p></div>}
          {!details?.recovery_pending && <div className="w-full space-y-3">
            <p className="text-sm">Cloudflare Turnstile：{cfToken ? "已完成" : "待完成"}</p><div ref={cfContainer} className="min-h-[65px]" />
            <p className="text-sm">hCaptcha：{hToken ? "已完成" : "待完成"}</p><div ref={hContainer} className="min-h-[65px]" />
          </div>}
          {mini && remaining === 0 && !details?.recovery_pending && <p role="alert" className="text-sm text-[var(--danger)]">入群验证已超时，本次验证已关闭。</p>}
          {error ? <><p role="alert" className="text-center text-sm text-[var(--danger)]">{error}</p><Button onClick={() => setAttempt((n) => n + 1)}>{details?.recovery_pending ? "重试" : "刷新两项重试"}</Button></> : <Button disabled={(!details?.recovery_pending && (!ready || !cfToken || !hToken)) || submitting || (mini && remaining === 0 && !details?.recovery_pending)} onClick={() => void submit()}>{submitting ? "正在处理…" : details?.recovery_pending ? "继续解除限制" : "提交验证"}</Button>}
        </>}
      </div>
    </main>
  );
}
