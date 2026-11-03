"use client";

import { useEffect, useRef, useState } from "react";
import { Shield, LoaderCircle, AlertCircle } from "lucide-react";
import { DualCaptchaVerifyClient } from "@/components/dual-captcha-verify-client";
import { telegramWebApp } from "@/lib/telegram";

export default function MiniVerificationPage() {
  const [state, setState] = useState<{ token: string; data: string } | null>(null);
  const [error, setError] = useState("");
  const task = useRef<string | null>(null);
  useEffect(() => {
    if (task.current === null) {
      const url = new URL(window.location.href);
      task.current = url.searchParams.get("task") ?? "";
      url.searchParams.delete("task");
      window.history.replaceState(window.history.state, "", url.pathname + url.search + url.hash);
    }
    const token = task.current;
    let disposed = false;
    let unsubscribe = () => {};
    const script = document.createElement("script");
    script.src = "https://telegram.org/js/telegram-web-app.js";
    script.onload = () => {
      if (disposed) return;
      const app = telegramWebApp();
      if (!token || !app?.initData) { setError("无法验证当前账号，请本人从机器人私聊打开。"); return; }
      const updateInsets = () => {
        const top = Math.max(0, app.safeAreaInset?.top ?? 0) + Math.max(0, app.contentSafeAreaInset?.top ?? 0);
        const bottom = Math.max(0, app.safeAreaInset?.bottom ?? 0) + Math.max(0, app.contentSafeAreaInset?.bottom ?? 0);
        document.documentElement.style.setProperty("--mini-safe-top", `${top}px`);
        document.documentElement.style.setProperty("--mini-safe-bottom", `${bottom}px`);
      };
      app.onEvent?.("safeAreaChanged", updateInsets);
      app.onEvent?.("contentSafeAreaChanged", updateInsets);
      unsubscribe = () => { app.offEvent?.("safeAreaChanged", updateInsets); app.offEvent?.("contentSafeAreaChanged", updateInsets); };
      app.ready(); app.expand(); updateInsets();
      setState({ token, data: app.initData });
    };
    script.onerror = () => { if (!disposed) setError("验证页面加载失败，请返回私聊重试。"); };
    document.head.appendChild(script);
    return () => { disposed = true; unsubscribe(); script.remove(); document.documentElement.style.removeProperty("--mini-safe-top"); document.documentElement.style.removeProperty("--mini-safe-bottom"); };
  }, []);
  if (error) {
    return (
      <main className="flex min-h-screen items-center justify-center p-6 text-center" role="alert">
        <div className="glass-panel flex max-w-sm flex-col items-center gap-3 rounded-3xl border border-[var(--border)] p-6 shadow-xl">
          <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-[var(--danger-soft)] text-[var(--danger)]">
            <AlertCircle className="h-6 w-6" />
          </div>
          <p className="text-sm font-medium text-[var(--danger)]">{error}</p>
        </div>
      </main>
    );
  }
  if (!state) {
    return (
      <main className="flex min-h-screen items-center justify-center p-6 text-center">
        <div className="glass-panel flex max-w-sm flex-col items-center gap-3 rounded-3xl border border-[var(--border)] p-6 shadow-xl">
          <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-[var(--accent)] text-white">
            <Shield className="h-6 w-6" />
          </div>
          <div className="flex items-center gap-2 text-sm text-[var(--text-muted)]">
            <LoaderCircle className="h-4 w-4 animate-spin" />
            <span>正在加载验证…</span>
          </div>
        </div>
      </main>
    );
  }
  return <DualCaptchaVerifyClient token={state.token} initData={state.data} />;
}
