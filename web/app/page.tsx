"use client";

import { useCallback, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { ExternalLink, LoaderCircle, RefreshCw, Shield } from "lucide-react";
import { apiFetch } from "@/lib/api";
import { Button } from "@/components/ui/button";

type WebChallenge = {
  challenge_id: string;
  code: string;
  bot_username: string;
  tg_deep_link: string;
  expires_in: number;
};

export default function HomePage() {
  const router = useRouter();
  const [checking, setChecking] = useState(true);
  const [challenge, setChallenge] = useState<WebChallenge | null>(null);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState("");
  const [remainingSeconds, setRemainingSeconds] = useState(0);

  useEffect(() => {
    apiFetch<{ admin: unknown }>("/api/auth/me")
      .then(() => router.replace("/dashboard"))
      .catch(() => setChecking(false));
  }, [router]);

  const startChallenge = useCallback(async () => {
    setStarting(true);
    setError("");
    try {
      const created = await apiFetch<WebChallenge>("/api/auth/web-challenge", {
        method: "POST",
        body: "{}",
      });
      setChallenge(created);
      setRemainingSeconds(created.expires_in || 300);
    } catch (err) {
      setError(err instanceof Error ? err.message : "发起登录请求失败");
    } finally {
      setStarting(false);
    }
  }, []);

  useEffect(() => {
    if (!challenge || remainingSeconds <= 0) return;
    const timer = window.setInterval(() => {
      setRemainingSeconds((prev) => (prev > 0 ? prev - 1 : 0));
    }, 1000);
    return () => window.clearInterval(timer);
  }, [challenge, remainingSeconds]);

  useEffect(() => {
    if (!challenge || remainingSeconds <= 0) return;
    let cancelled = false;

    const timer = window.setInterval(async () => {
      try {
        const res = await apiFetch<{ status: string; redirect_to?: string }>(
          "/api/auth/web-poll",
          {
            method: "POST",
            body: JSON.stringify({ challenge_id: challenge.challenge_id }),
          },
        );
        if (!cancelled && res.status === "approved") {
          router.replace(res.redirect_to || "/dashboard");
        }
      } catch (err) {
        if (!cancelled) {
          setChallenge(null);
          setError(err instanceof Error ? err.message : "登录验证已失效，请重新发起");
        }
      }
    }, 2000);

    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [challenge, remainingSeconds, router]);

  if (checking) {
    return (
      <main className="flex min-h-screen items-center justify-center">
        <div className="text-sm text-[var(--text-muted)]">Loading…</div>
      </main>
    );
  }

  const expired = Boolean(challenge && remainingSeconds <= 0);

  return (
    <main className="flex min-h-screen items-center justify-center px-4">
      <div className="w-full max-w-sm">
        <div className="glass-panel flex flex-col items-center gap-5 rounded-2xl border border-[var(--border)] bg-[var(--surface)] p-6 shadow-sm">
          <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-[var(--accent)] text-white shadow-sm">
            <Shield className="h-6 w-6" strokeWidth={2.25} />
          </div>
          <div className="text-center">
            <h1 className="text-2xl font-semibold tracking-tight">ClawGuard</h1>
            <p className="mt-1.5 text-sm text-[var(--text-muted)]">
              Telegram 群管理控制台
            </p>
          </div>

          {error && (
            <div className="w-full rounded-xl border border-[var(--danger)]/30 bg-[var(--danger-soft)] px-3.5 py-2.5 text-center text-xs text-[var(--danger)]">
              {error}
            </div>
          )}

          {!challenge || expired ? (
            <div className="flex w-full flex-col items-center gap-3">
              <Button
                className="w-full justify-center"
                onClick={() => void startChallenge()}
                disabled={starting}
              >
                {starting ? (
                  <>
                    <LoaderCircle className="h-4 w-4 animate-spin" />
                    正在生成安全核对码…
                  </>
                ) : (
                  "通过 Telegram 客户端验证登录"
                )}
              </Button>
              <p className="text-center text-xs leading-relaxed text-[var(--text-subtle)]">
                采用 Telegram 官方非对称签名核验身份，即使 Bot Token 泄露也无法伪造登录。
              </p>
            </div>
          ) : (
            <div className="flex w-full flex-col items-center gap-4">
              <div className="w-full rounded-xl border border-[var(--border)] bg-[var(--surface-2)] px-4 py-3.5 text-center">
                <p className="text-[11px] uppercase tracking-wider text-[var(--text-muted)]">
                  安全登录核对码
                </p>
                <p className="mt-1 font-mono text-2xl font-bold tracking-[0.24em] text-[var(--text)]">
                  {challenge.code}
                </p>
                <p className="mt-1.5 text-[11px] text-[var(--text-subtle)]">
                  等待 Telegram 内确认 · 剩余 {remainingSeconds} 秒
                </p>
              </div>

              {challenge.tg_deep_link && (
                <a
                  href={challenge.tg_deep_link}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex h-10 w-full items-center justify-center gap-2 rounded-xl bg-[var(--accent)] px-4 text-sm font-medium text-white transition hover:opacity-90"
                >
                  打开 Telegram 确认登录
                  <ExternalLink className="h-4 w-4" />
                </a>
              )}

              <p className="text-center text-xs leading-relaxed text-[var(--text-muted)]">
                也可在手机 Telegram 打开机器人左下角「管理面板」，点击「批准浏览器登录」输入上方 6 位码。
              </p>

              <button
                type="button"
                onClick={() => void startChallenge()}
                className="inline-flex items-center gap-1.5 text-xs text-[var(--text-subtle)] hover:text-[var(--text)]"
              >
                <RefreshCw className="h-3.5 w-3.5" />
                换一个核对码
              </button>
            </div>
          )}

          <p className="text-xs text-[var(--text-subtle)]">仅限管理员访问</p>
        </div>
      </div>
    </main>
  );
}
