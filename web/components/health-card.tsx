"use client";

import { useEffect, useState } from "react";
import { AlertTriangle, CheckCircle2 } from "lucide-react";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { apiFetch } from "@/lib/api";
import type { HealthStatus } from "@/lib/types";

function ageLabel(seconds: number | null | undefined) {
  if (seconds == null) return "未记录";
  if (seconds < 60) return `${seconds} 秒前`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时前`;
  return `${Math.floor(seconds / 86400)} 天前`;
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
      } catch (loadError) {
        if (cancelled) return;
        setError(loadError instanceof Error ? loadError.message : "加载失败");
      }
    };
    void load();
    const timer = setInterval(() => void load(), 5000);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, []);

  const deadLetters = health?.join_cleanup_dead ?? 0;
  const unhealthy =
    Boolean(error) ||
    (health?.db_latency_ms ?? -1) < 0 ||
    (health != null && health.redis_latency_ms == null) ||
    (health != null && health.join_cleanup_dead == null) ||
    deadLetters > 0 ||
    (health != null && health.backup_seconds_ago == null) ||
    (health?.backup_seconds_ago ?? 0) > 36 * 3600 ||
    (health != null && health.verification_worker_seconds_ago == null) ||
    (health?.verification_worker_seconds_ago ?? 0) > 120 ||
    (health != null && health.join_recovery_worker_seconds_ago == null) ||
    (health?.join_recovery_worker_seconds_ago ?? 0) > 90;

  const metrics = [
    {
      label: "Webhook",
      value: ageLabel(health?.webhook_seconds_ago),
      alert: (health?.webhook_seconds_ago ?? 0) > 300,
    },
    {
      label: "数据库",
      value: health ? `${health.db_latency_ms} ms` : "-",
      alert: (health?.db_latency_ms ?? 0) < 0,
    },
    {
      label: "Redis",
      value:
        health?.redis_latency_ms == null
          ? "不可用"
          : `${health.redis_latency_ms} ms`,
      alert: health != null && health.redis_latency_ms == null,
    },
    {
      label: "待验证",
      value: health ? `${health.active_pending}` : "-",
      alert: false,
    },
    {
      label: "到期清理",
      value: health ? `${health.due_cleanup}` : "-",
      alert: (health?.due_cleanup ?? 0) > 20,
    },
    {
      label: "重试任务",
      value: health ? `${health.retrying_cleanup}` : "-",
      alert: (health?.retrying_cleanup ?? 0) > 10,
    },
    {
      label: "清理死信",
      value: health?.join_cleanup_dead == null ? "不可用" : `${deadLetters}`,
      alert: health != null && (health.join_cleanup_dead == null || deadLetters > 0),
    },
    {
      label: "最近备份",
      value: ageLabel(health?.backup_seconds_ago),
      alert:
        health != null &&
        (health.backup_seconds_ago == null || health.backup_seconds_ago > 36 * 3600),
    },
    {
      label: "验证清理 Worker",
      value: ageLabel(health?.verification_worker_seconds_ago),
      alert:
        health != null &&
        (health.verification_worker_seconds_ago == null ||
          health.verification_worker_seconds_ago > 120),
    },
    {
      label: "入群恢复 Worker",
      value: ageLabel(health?.join_recovery_worker_seconds_ago),
      alert:
        health != null &&
        (health.join_recovery_worker_seconds_ago == null ||
          health.join_recovery_worker_seconds_ago > 90),
    },
  ];

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-3">
        <CardTitle>运行状态</CardTitle>
        <div
          className={`flex items-center gap-1.5 text-xs ${
            unhealthy ? "text-[var(--danger)]" : "text-[var(--success)]"
          }`}
        >
          {unhealthy ? (
            <AlertTriangle className="h-4 w-4" />
          ) : (
            <CheckCircle2 className="h-4 w-4" />
          )}
          {unhealthy ? "需要检查" : "运行正常"}
        </div>
      </CardHeader>
      <CardBody className="space-y-3">
        {error && (
          <div className="rounded-md border border-[var(--danger)] bg-[var(--surface-2)] px-3 py-2 text-xs text-[var(--danger)]">
            状态加载失败：{error}
          </div>
        )}
        <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-5">
          {metrics.map((metric) => (
            <div
              key={metric.label}
              className="min-w-0 rounded-md border border-[var(--border)] bg-[var(--surface-2)] px-3 py-2.5"
            >
              <div className="truncate text-xs text-[var(--text-muted)]">
                {metric.label}
              </div>
              <div
                className={`mt-1 truncate font-mono text-sm ${
                  metric.alert ? "text-[var(--danger)]" : "text-[var(--text)]"
                }`}
                title={metric.value}
              >
                {metric.value}
              </div>
            </div>
          ))}
        </div>
      </CardBody>
    </Card>
  );
}
