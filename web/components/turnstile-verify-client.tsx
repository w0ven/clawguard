"use client";

import { useEffect, useRef, useState } from "react";
import { Shield, CheckCircle2, XCircle } from "lucide-react";
import { Button } from "@/components/ui/button";

declare global {
  interface Window {
    turnstile?: {
      remove: (widgetId: string) => void;
      render: (
        container: HTMLElement,
        options: {
          sitekey: string;
          callback: (token: string) => void;
          "error-callback"?: () => void;
          "expired-callback"?: () => void;
          theme?: "light" | "dark" | "auto";
        },
      ) => string;
    };
  }
}

type Props = { token: string };
type Status = "idle" | "submitting" | "success" | "error";

const scriptURL = "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";

export function TurnstileVerifyClient({ token }: Props) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const widgetRef = useRef<string | null>(null);
  const [widgetNonce, setWidgetNonce] = useState(0);
  const [status, setStatus] = useState<Status>("idle");
  const [errorMessage, setErrorMessage] = useState("");

  const siteKey = process.env.NEXT_PUBLIC_TURNSTILE_SITE_KEY ?? "";

  useEffect(() => {
    if (!siteKey) {
      setStatus("error");
      setErrorMessage("服务器未配置 Turnstile 站点密钥");
      return;
    }

    let cancelled = false;

    function renderWidget() {
      if (cancelled || !containerRef.current || !window.turnstile) return;
      containerRef.current.innerHTML = "";
      widgetRef.current = window.turnstile.render(containerRef.current, {
        sitekey: siteKey,
        theme: "auto",
        callback: (cfToken) => {
          void submitVerification(cfToken);
        },
        "error-callback": () => {
          setStatus("error");
          setErrorMessage("验证组件加载异常");
        },
        "expired-callback": () => {
          setStatus("error");
          setErrorMessage("验证已过期");
        },
      });
    }

    async function ensureScript() {
      const existing = document.querySelector<HTMLScriptElement>(
        `script[src="${scriptURL}"]`,
      );
      if (existing) {
        if (window.turnstile) return renderWidget();
        existing.addEventListener("load", renderWidget, { once: true });
        return;
      }
      const script = document.createElement("script");
      script.src = scriptURL;
      script.async = true;
      script.defer = true;
      script.onload = renderWidget;
      script.onerror = () => {
        setStatus("error");
        setErrorMessage("Cloudflare 验证脚本加载失败");
      };
      document.head.appendChild(script);
    }

    async function submitVerification(cfResponse: string) {
      setStatus("submitting");
      setErrorMessage("");
      try {
        const res = await fetch(
          `/api/verify/turnstile/${encodeURIComponent(token)}`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ cf_response: cfResponse }),
          },
        );
        if (!res.ok) {
          let message = `验证失败 (${res.status})`;
          try {
            const p = (await res.json()) as { error?: string };
            if (p.error) message = p.error;
          } catch {}
          throw new Error(message);
        }
        setStatus("success");
      } catch (e) {
        setStatus("error");
        setErrorMessage(e instanceof Error ? e.message : "验证失败");
      }
    }

    void ensureScript();

    return () => {
      cancelled = true;
      if (widgetRef.current && window.turnstile) {
        window.turnstile.remove(widgetRef.current);
        widgetRef.current = null;
      }
    };
  }, [siteKey, token, widgetNonce]);

  return (
    <main className="flex min-h-screen items-center justify-center px-4">
      <div className="w-full max-w-sm">
        <div className="flex flex-col items-center gap-6">
          <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-[var(--accent)] text-white shadow-sm">
            <Shield className="h-6 w-6" strokeWidth={2.25} />
          </div>

          {status === "success" ? (
            <>
              <CheckCircle2
                className="h-16 w-16 text-[var(--success)]"
                strokeWidth={1.5}
              />
              <div className="text-center">
                <h1 className="text-xl font-semibold">验证通过</h1>
                <p className="mt-1.5 text-sm text-[var(--text-muted)]">
                  请返回 Telegram 群继续聊天
                </p>
              </div>
            </>
          ) : status === "error" ? (
            <>
              <XCircle
                className="h-16 w-16 text-[var(--danger)]"
                strokeWidth={1.5}
              />
              <div className="text-center">
                <h1 className="text-xl font-semibold">验证失败</h1>
                <p className="mt-1.5 text-sm text-[var(--text-muted)] break-words">
                  {errorMessage || "未知错误"}
                </p>
              </div>
              <Button
                variant="primary"
                onClick={() => {
                  setStatus("idle");
                  setErrorMessage("");
                  setWidgetNonce((n) => n + 1);
                }}
              >
                重试
              </Button>
            </>
          ) : (
            <>
              <div className="text-center">
                <h1 className="text-xl font-semibold">人机验证</h1>
                <p className="mt-1.5 text-sm text-[var(--text-muted)]">
                  完成下方验证后自动解除限制
                </p>
              </div>
              <div ref={containerRef} className="min-h-[65px]" />
              {status === "submitting" && (
                <p className="text-xs text-[var(--text-muted)]">
                  正在验证…
                </p>
              )}
            </>
          )}
        </div>
      </div>
    </main>
  );
}
