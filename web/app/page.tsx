"use client";

import { useCallback, useEffect, useState } from "react";
import { ExternalLink, LoaderCircle, RefreshCw, Shield } from "lucide-react";
import { apiFetch } from "@/lib/api";
import { Button } from "@/components/ui/button";

type WebChallengeResponse = {
  challenge_id: string;
  code: string;
  bot_username: string;
  tg_deep_link: string;
  expires_in: number;
};

type WebChallengeState = WebChallengeResponse & {
  expiresAtMs: number;
};

function safeRedirectTarget(raw?: string): string {
  const trimmed = (raw ?? "").trim();
  if (!trimmed.startsWith("/") || trimmed.startsWith("//") || trimmed.includes("\\")) {
    return "/dashboard";
  }
  return trimmed;
}

export default function HomePage() {
  const [checking, setChecking] = useState(true);
  const [challenge, setChallenge] = useState<WebChallengeState | null>(null);
  const [starting, setStarting] = useState(false);
  const [redirecting, setRedirecting] = useState(false);
  const [error, setError] = useState("");
  const [remainingSeconds, setRemainingSeconds] = useState(0);

  useEffect(() => {
    apiFetch<{ admin: unknown }>("/api/auth/me")
      .then(() => {
        window.location.replace("/dashboard");
      })
      .catch(() => setChecking(false));
  }, []);

  const startChallenge = useCallback(async () => {
    setStarting(true);
    setError("");
    try {
      const created = await apiFetch<WebChallengeResponse>("/api/auth/web-challenge", {
        method: "POST",
        body: "{}",
      });
      const ttlSeconds = created.expires_in > 0 ? created.expires_in : 300;
      const expiresAtMs = Date.now() + ttlSeconds * 1000;
      setChallenge({
        ...created,
        expiresAtMs,
      });
      setRemainingSeconds(ttlSeconds);
    } catch (err) {
      setError(err instanceof Error ? err.message : "发起登录请求失败");
    } finally {
      setStarting(false);
    }
  }, []);

  // Countdown timer depends only on the challenge identity & deadline, never on remainingSeconds.
  useEffect(() => {
    if (!challenge) return;
    const challengeExpiresAt = challenge.expiresAtMs;

    const updateRemaining = () => {
      const left = Math.max(0, Math.ceil((challengeExpiresAt - Date.now()) / 1000));
      setRemainingSeconds(left);
    };

    updateRemaining();
    const timer = window.setInterval(updateRemaining, 1000);
    return () => window.clearInterval(timer);
  }, [challenge]);

  // Poll loop is completely decoupled from remainingSeconds ticks so countdown updates never cancel in-flight polls.
  useEffect(() => {
    if (!challenge) return;
    const challengeId = challenge.challenge_id;
    const challengeExpiresAt = challenge.expiresAtMs;

    let stopped = false;
    let inFlight = false;
    let timerId: number | undefined;

    const clearScheduledTimer = () => {
      if (timerId !== undefined) {
        window.clearTimeout(timerId);
        timerId = undefined;
      }
    };

    const scheduleNext = (delayMs: number) => {
      if (stopped) return;
      clearScheduledTimer();
      timerId = window.setTimeout(() => {
        void pollOnce();
      }, delayMs);
    };

    const verifyExistingSession = async (): Promise<boolean> => {
      try {
        const meRes = await fetch("/api/auth/me", {
          method: "GET",
          credentials: "include",
          cache: "no-store",
        });
        return meRes.ok;
      } catch {
        return false;
      }
    };

    const navigateToConsole = (target?: string) => {
      stopped = true;
      clearScheduledTimer();
      setRedirecting(true);
      window.location.replace(safeRedirectTarget(target));
    };

    const pollOnce = async () => {
      if (stopped || inFlight) return;
      if (Date.now() >= challengeExpiresAt) {
        stopped = true;
        setRemainingSeconds(0);
        return;
      }

      inFlight = true;
      let nextDelayMs = 1500;
      try {
        const res = await fetch("/api/auth/web-poll", {
          method: "POST",
          credentials: "include",
          cache: "no-store",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ challenge_id: challengeId }),
        });

        if (stopped) return;

        if (res.ok) {
          const payload = (await res.json().catch(() => ({}))) as {
            status?: string;
            redirect_to?: string;
          };
          if (stopped) return;
          if (payload.status === "approved") {
            navigateToConsole(payload.redirect_to);
            return;
          }
        } else if (res.status === 401 || res.status === 410 || res.status === 403) {
          // Recheck /api/auth/me first in case an earlier poll already minted cg_admin.
          if (await verifyExistingSession()) {
            navigateToConsole("/dashboard");
            return;
          }
          const payload = (await res.json().catch(() => ({}))) as { error?: string };
          if (!stopped) {
            stopped = true;
            clearScheduledTimer();
            setChallenge(null);
            setError(payload.error || "登录验证已失效，请重新发起");
          }
          return;
        } else if (res.status === 429) {
          nextDelayMs = 3000;
        }
      } catch {
        // Transient network hiccup while switching apps should not wipe the active code.
        nextDelayMs = 2000;
      } finally {
        inFlight = false;
        if (!stopped && Date.now() < challengeExpiresAt) {
          scheduleNext(nextDelayMs);
        }
      }
    };

    const handleVisibilityOrFocus = () => {
      if (stopped || document.visibilityState === "hidden") return;
      const left = Math.max(0, Math.ceil((challengeExpiresAt - Date.now()) / 1000));
      setRemainingSeconds(left);
      if (left > 0 && !inFlight) {
        clearScheduledTimer();
        void pollOnce();
      }
    };

    window.addEventListener("focus", handleVisibilityOrFocus);
    document.addEventListener("visibilitychange", handleVisibilityOrFocus);
    scheduleNext(1000);

    return () => {
      stopped = true;
      clearScheduledTimer();
      window.removeEventListener("focus", handleVisibilityOrFocus);
      document.removeEventListener("visibilitychange", handleVisibilityOrFocus);
    };
  }, [challenge]);

  if (checking || redirecting) {
    return (
      <main className="flex min-h-screen items-center justify-center">
        <div className="flex items-center gap-2 text-sm text-[var(--text-muted)]">
          <LoaderCircle className="h-4 w-4 animate-spin text-[var(--accent)]" />
          <span>{redirecting ? "验证已通过，正在进入控制台…" : "Loading…"}</span>
        </div>
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
