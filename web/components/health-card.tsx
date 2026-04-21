"use client";

import { useEffect, useState } from "react";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { apiFetch } from "@/lib/api";
import type { HealthStatus } from "@/lib/types";

function agoLabel(seconds: number | null | undefined) {
  if (seconds == null) return "未收到";
  if (seconds < 60) return `${seconds}s`;
  return `${Math.floor(seconds / 60)}m`;
}

export function HealthCard() {
  const [health, setHealth] = useState<HealthStatus | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const data = await apiFetch<HealthStatus>("/api/admin/health");
        if (cancelled) return;
        setHealth(data);
        setError(null);
      } catch (e) {
        if (cancelled) return;
        setError(e instanceof Error ? e.message : "加载失败");
      }
    };
    void load();
    const timer = setInterval(() => void load(), 5000);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, []);

  const cards = [
    {
      label: "Webhook",
      value: agoLabel(health?.webhook_seconds_ago),
      tone:
        (health?.webhook_seconds_ago ?? 0) > 300
          ? "text-[var(--danger)]"
          : "text-[var(--text)]",
    },
    {
      label: "AI",
      value: health?.ai_last_error ? "最近失败" : "正常",
      tone: health?.ai_last_error
        ? "text-[var(--warning)]"
        : "text-[var(--success)]",
    },
    {
      label: "DB",
      value: health ? `${health.db_latency_ms}ms` : "—",
      tone: "text-[var(--text)]",
    },
    {
      label: "Redis",
      value:
        health?.redis_latency_ms == null
          ? "跳过"
          : `${health.redis_latency_ms}ms`,
      tone: "text-[var(--text)]",
    },
    {
      label: "今日成本",
      value: health ? `${health.today_cost_cents.toFixed(2)}¢` : "—",
      tone: "text-[var(--accent)]",
    },
  ];

  return (
    <Card>
      <CardHeader>
        <CardTitle>实时健康</CardTitle>
      </CardHeader>
      <CardBody className="space-y-3">
        {error && (
          <div className="rounded-md border border-[var(--danger)] bg-[var(--surface-2)] px-3 py-2 text-xs text-[var(--danger)]">
            加载失败：{error}
          </div>
        )}
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
          {cards.map((card) => (
            <div
              key={card.label}
              className="rounded-md border border-[var(--border)] bg-[var(--surface-2)] px-4 py-3"
            >
              <div className="text-xs text-[var(--text-muted)]">
                {card.label}
              </div>
              <div className={`mt-2 font-mono text-lg ${card.tone}`}>
                {card.value}
              </div>
            </div>
          ))}
        </div>
      </CardBody>
    </Card>
  );
}
