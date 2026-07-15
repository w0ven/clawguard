"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { Shield, LoaderCircle, TriangleAlert } from "lucide-react";
import { telegramWebApp } from "@/lib/telegram";

export default function MiniAppEntryPage() {
  const router = useRouter();
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    async function authenticate() {
      const app = telegramWebApp();
      if (!app?.initData) {
        setError("请从 Telegram Bot 内打开此管理面板");
        return;
      }
      app.ready();
      app.expand();
      try {
        const response = await fetch("/api/auth/miniapp", {
          method: "POST",
          credentials: "include",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ init_data: app.initData }),
        });
        if (!response.ok) {
          const payload = (await response.json().catch(() => ({}))) as { error?: string };
          throw new Error(payload.error || "Mini App 登录失败");
        }
        const payload = (await response.json()) as { redirect_to?: string };
        if (cancelled) return;
        sessionStorage.setItem("cg_miniapp", "1");
        app.HapticFeedback?.notificationOccurred("success");
        router.replace(payload.redirect_to || "/dashboard");
      } catch (authError) {
        if (!cancelled) {
          app.HapticFeedback?.notificationOccurred("error");
          setError(authError instanceof Error ? authError.message : "Mini App 登录失败");
        }
      }
    }
    void authenticate();
    return () => {
      cancelled = true;
    };
  }, [router]);

  return (
    <main className="miniapp-entry flex min-h-screen items-center justify-center px-6 text-center">
      <div className="flex max-w-xs flex-col items-center gap-4">
        <div className="flex h-12 w-12 items-center justify-center rounded-md bg-[var(--accent)] text-white">
          <Shield className="h-6 w-6" />
        </div>
        <h1 className="text-xl font-semibold">ClawGuard</h1>
        {error ? (
          <div className="flex items-start gap-2 text-sm text-[var(--danger)]">
            <TriangleAlert className="mt-0.5 h-4 w-4 shrink-0" />
            <span>{error}</span>
          </div>
        ) : (
          <div className="flex items-center gap-2 text-sm text-[var(--text-muted)]">
            <LoaderCircle className="h-4 w-4 animate-spin" />
            正在验证管理员身份
          </div>
        )}
      </div>
    </main>
  );
}
