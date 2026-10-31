"use client";

import { useEffect, useState } from "react";
import { DualCaptchaVerifyClient } from "@/components/dual-captcha-verify-client";
import { telegramWebApp } from "@/lib/telegram";

export default function MiniVerificationPage() {
  const [state, setState] = useState<{ token: string; data: string } | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    const token = window.location.hash.slice(1);
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
  if (error) return <main className="p-6 text-center" role="alert">{error}</main>;
  if (!state) return <main className="p-6 text-center">正在加载验证…</main>;
  return <DualCaptchaVerifyClient token={state.token} initData={state.data} />;
}
