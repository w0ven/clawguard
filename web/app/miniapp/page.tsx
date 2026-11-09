"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { CheckCircle2, KeyRound, LoaderCircle, Shield, TriangleAlert } from "lucide-react";
import { telegramWebApp } from "@/lib/telegram";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

function safeNextPath(raw: string | null): string {
  if (!raw) return "/dashboard";
  const trimmed = raw.trim();
  if (!trimmed.startsWith("/") || trimmed.startsWith("//") || trimmed.includes("\\")) {
    return "/dashboard";
  }
  return trimmed;
}

export default function MiniAppEntryPage() {
  const router = useRouter();
  const [error, setError] = useState("");
  const [approveMode, setApproveMode] = useState(false);
  const [approveCode, setApproveCode] = useState("");
  const [approving, setApproving] = useState(false);
  const [approvedDone, setApprovedDone] = useState(false);

  useEffect(() => {
    let cancelled = false;
    const params = new URLSearchParams(window.location.search);
    const approveParam = (params.get("approve") ?? "").trim().toUpperCase();
    const nextPath = safeNextPath(params.get("next"));

    const app = telegramWebApp();
    if (!app?.initData) {
      setError("请从 Telegram Bot 内打开此管理面板");
      return;
    }
    app.ready();
    app.expand();

    if (approveParam) {
      setApproveMode(true);
      setApproveCode(approveParam);
      return;
    }

    async function authenticate() {
      try {
        const response = await fetch("/api/auth/miniapp", {
          method: "POST",
          credentials: "include",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ init_data: app!.initData }),
        });
        if (!response.ok) {
          const payload = (await response.json().catch(() => ({}))) as { error?: string };
          throw new Error(payload.error || "Mini App 登录失败");
        }
        const payload = (await response.json()) as { redirect_to?: string };
        if (cancelled) return;
        sessionStorage.setItem("cg_miniapp", "1");
        app!.HapticFeedback?.notificationOccurred("success");
        router.replace(nextPath !== "/dashboard" ? nextPath : payload.redirect_to || "/dashboard");
      } catch (authError) {
        if (!cancelled) {
          app!.HapticFeedback?.notificationOccurred("error");
          setError(authError instanceof Error ? authError.message : "Mini App 登录失败");
        }
      }
    }
    void authenticate();
    return () => {
      cancelled = true;
    };
  }, [router]);

  async function submitApproval() {
    const app = telegramWebApp();
    if (!app?.initData) {
      setError("请从 Telegram Bot 内打开此管理面板");
      return;
    }
    const cleanCode = approveCode.replace(/[-\s]/g, "").toUpperCase();
    if (cleanCode.length !== 6) {
      setError("请输入浏览器上显示的 6 位核对码");
      return;
    }
    setApproving(true);
    setError("");
    try {
      const response = await fetch("/api/auth/web-approve", {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          code: cleanCode,
          init_data: app.initData,
        }),
      });
      const payload = (await response.json().catch(() => ({}))) as { error?: string };
      if (!response.ok) {
        throw new Error(payload.error || "批准登录失败");
      }
      app.HapticFeedback?.notificationOccurred("success");
      setApprovedDone(true);
    } catch (approveErr) {
      app.HapticFeedback?.notificationOccurred("error");
      setError(approveErr instanceof Error ? approveErr.message : "批准登录失败");
    } finally {
      setApproving(false);
    }
  }

  return (
    <main className="miniapp-entry flex min-h-screen items-center justify-center px-6 text-center">
      <div className="glass-panel flex w-full max-w-sm flex-col items-center gap-4 rounded-3xl border border-[var(--border)] p-8 shadow-xl">
        <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-[var(--accent)] text-white shadow-md">
          <Shield className="h-7 w-7" />
        </div>
        <div>
          <p className="text-[10px] font-semibold uppercase tracking-[0.18em] text-[var(--accent)]">
            ClawGuard Control Plane
          </p>
          <h1 className="mt-1 text-xl font-semibold">ClawGuard</h1>
        </div>

        {error && (
          <div className="flex w-full items-start justify-center gap-2 rounded-2xl bg-[var(--danger-soft)] px-4 py-3 text-sm text-[var(--danger)]">
            <TriangleAlert className="mt-0.5 h-4 w-4 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        {approveMode ? (
          approvedDone ? (
            <div className="flex w-full flex-col items-center gap-3">
              <div className="flex w-full items-center justify-center gap-2 rounded-2xl bg-[var(--surface-2)] px-4 py-3 text-sm font-medium text-[var(--success)]">
                <CheckCircle2 className="h-4 w-4 shrink-0" />
                <span>已批准浏览器登录，请返回浏览器操作</span>
              </div>
              <Button variant="secondary" className="w-full justify-center" onClick={() => telegramWebApp()?.close?.()}>
                关闭窗口
              </Button>
            </div>
          ) : (
            <div className="flex w-full flex-col items-center gap-3">
              <p className="text-xs leading-relaxed text-[var(--text-muted)]">
                请核对电脑浏览器上显示的 6 位安全码一致后再批准登录：
              </p>
              <Input
                value={approveCode}
                onChange={(e) => setApproveCode(e.target.value.toUpperCase())}
                placeholder="6 位核对码"
                className="text-center font-mono text-lg tracking-[0.24em]"
                maxLength={8}
              />
              <Button
                className="w-full justify-center"
                onClick={() => void submitApproval()}
                disabled={approving}
              >
                <KeyRound className="h-4 w-4" />
                {approving ? "正在签名验证…" : "确认批准此浏览器登录"}
              </Button>
            </div>
          )
        ) : !error ? (
          <div className="flex flex-col items-center gap-3">
            <div className="flex items-center gap-2 rounded-2xl bg-[var(--surface-2)] px-4 py-2.5 text-sm text-[var(--text-muted)]">
              <LoaderCircle className="h-4 w-4 animate-spin text-[var(--accent)]" />
              正在验证管理员身份
            </div>
            <button
              type="button"
              onClick={() => setApproveMode(true)}
              className="text-xs text-[var(--text-subtle)] underline hover:text-[var(--text)]"
            >
              改为批准电脑浏览器登录
            </button>
          </div>
        ) : (
          telegramWebApp()?.initData && (
            <Button variant="secondary" size="sm" onClick={() => { setError(""); setApproveMode(true); }}>
              批准电脑浏览器登录
            </Button>
          )
        )}
      </div>
    </main>
  );
}
