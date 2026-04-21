"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { Shield } from "lucide-react";
import { apiFetch } from "@/lib/api";

const botUsername = process.env.NEXT_PUBLIC_BOT_USERNAME ?? "rfcguard_bot";

export default function HomePage() {
  const router = useRouter();
  const widgetRef = useRef<HTMLDivElement>(null);
  const [checking, setChecking] = useState(true);

  useEffect(() => {
    apiFetch<{ admin: unknown }>("/api/auth/me")
      .then(() => router.replace("/dashboard"))
      .catch(() => setChecking(false));
  }, [router]);

  useEffect(() => {
    if (checking) return;
    if (!widgetRef.current) return;
    widgetRef.current.innerHTML = "";
    const script = document.createElement("script");
    script.src = "https://telegram.org/js/telegram-widget.js?22";
    script.async = true;
    script.setAttribute("data-telegram-login", botUsername);
    script.setAttribute("data-size", "large");
    script.setAttribute("data-radius", "8");
    script.setAttribute("data-auth-url", "/api/auth/telegram-login");
    script.setAttribute("data-request-access", "write");
    widgetRef.current.appendChild(script);
  }, [checking]);

  if (checking) {
    return (
      <main className="flex min-h-screen items-center justify-center">
        <div className="text-sm text-[var(--text-muted)]">Loading…</div>
      </main>
    );
  }

  return (
    <main className="flex min-h-screen items-center justify-center px-4">
      <div className="w-full max-w-sm">
        <div className="flex flex-col items-center gap-6">
          <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-[var(--accent)] text-white shadow-sm">
            <Shield className="h-6 w-6" strokeWidth={2.25} />
          </div>
          <div className="text-center">
            <h1 className="text-2xl font-semibold tracking-tight">ClawGuard</h1>
            <p className="mt-1.5 text-sm text-[var(--text-muted)]">
              Telegram 群管理控制台
            </p>
          </div>
          <div ref={widgetRef} className="min-h-[40px]" />
          <p className="text-xs text-[var(--text-subtle)]">
            仅限管理员访问
          </p>
        </div>
      </div>
    </main>
  );
}
