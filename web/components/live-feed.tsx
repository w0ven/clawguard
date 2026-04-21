"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { apiFetch } from "@/lib/api";
import type { LiveEvent } from "@/lib/types";

const typeMeta: Record<string, { icon: string; href: string }> = {
  violation: { icon: "⛔", href: "/violations" },
  ai_decision: { icon: "🤖", href: "/ai-review" },
  verified: { icon: "✅", href: "/dashboard" },
  config_change: { icon: "⚙️", href: "/audit" },
};

function formatTime(ts: string) {
  const date = new Date(ts);
  if (Number.isNaN(date.getTime())) return "--:--";
  return date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" });
}

const PAGE_SIZE = 50;

const typeOptions = [
  { label: "全部", value: "all" },
  { label: "违规", value: "violation" },
  { label: "AI 判定", value: "ai_decision" },
  { label: "配置变更", value: "config_change" },
];

export function LiveFeed() {
  const [events, setEvents] = useState<LiveEvent[]>([]);
  const [page, setPage] = useState(1);
  const [total, setTotal] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [eventType, setEventType] = useState("all");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      if (!cancelled) {
        setLoading(true);
      }
      try {
        const offset = (page - 1) * PAGE_SIZE;
        const data = await apiFetch<{
          events: LiveEvent[];
          total: number;
          has_more: boolean;
        }>(
          `/api/admin/events?limit=${PAGE_SIZE}&offset=${offset}&type=${eventType}`,
        );
        if (cancelled) return;
        setEvents(data.events ?? []);
        setTotal(data.total ?? 0);
        setHasMore(Boolean(data.has_more));
        setError(null);
      } catch (e) {
        if (cancelled) return;
        setError(e instanceof Error ? e.message : "加载失败");
      } finally {
        if (!cancelled) setLoading(false);
      }
    };
    void load();
    if (page !== 1) {
      return () => {
        cancelled = true;
      };
    }
    const timer = setInterval(() => void load(), 5000);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [page, eventType]);

  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
          <CardTitle>实时事件流</CardTitle>
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <Select
              value={eventType}
              onChange={(e) => {
                setEventType(e.target.value);
                setPage(1);
              }}
              options={typeOptions}
              className="min-w-[132px]"
            />
            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={page <= 1}
                onClick={() => setPage((current) => Math.max(1, current - 1))}
              >
                上一页
              </Button>
              <span className="min-w-[72px] text-center text-xs text-[var(--text-muted)]">
                {page} / {totalPages}
              </span>
              <Button
                variant="outline"
                size="sm"
                disabled={!hasMore}
                onClick={() => setPage((current) => current + 1)}
              >
                下一页
              </Button>
            </div>
          </div>
        </div>
      </CardHeader>
      <CardBody className="space-y-2 font-mono text-xs">
        {page !== 1 && (
          <div className="rounded-md border border-[var(--border)] bg-[var(--surface-2)] px-3 py-2 text-[var(--text-muted)]">
            当前正在查看历史页，已暂停 5 秒自动刷新
          </div>
        )}
        {loading && events.length === 0 && (
          <div className="rounded-md border border-[var(--border)] bg-[var(--surface-2)] px-3 py-6 text-center text-[var(--text-muted)]">
            加载中…
          </div>
        )}
        {error && (
          <div className="rounded-md border border-[var(--danger)] bg-[var(--surface-2)] px-3 py-3 text-[var(--danger)]">
            加载失败：{error}
          </div>
        )}
        {!loading && !error && events.length === 0 && (
          <div className="rounded-md border border-[var(--border)] bg-[var(--surface-2)] px-3 py-6 text-center text-[var(--text-muted)]">
            暂无事件
          </div>
        )}
        {events.map((event) => {
          const meta = typeMeta[event.type] ?? { icon: "•", href: "/dashboard" };
          return (
            <div
              key={`${event.type}-${event.id}-${event.created_at}`}
              className="flex flex-wrap items-center gap-2 rounded-md border border-[var(--border)] bg-[var(--surface-2)] px-3 py-2"
            >
              <span className="text-[var(--text-muted)]">
                [{formatTime(event.created_at)}]
              </span>
              <span>{meta.icon}</span>
              <span>群 {event.chat_id || "-"}</span>
              <span>用户 {event.user_id || "-"}</span>
              <span className="text-[var(--text)]">{event.title}</span>
              <span className="text-[var(--text-muted)]">
                {event.detail || event.extra}
              </span>
              <Link
                href={meta.href}
                className="ml-auto text-[var(--accent)]"
              >
                详情
              </Link>
            </div>
          );
        })}
      </CardBody>
    </Card>
  );
}
