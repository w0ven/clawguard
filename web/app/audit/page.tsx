"use client";

import { useEffect, useState } from "react";
import { AdminShell } from "@/components/admin-shell";
import { Card, CardHeader, CardTitle, CardBody } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from "@/components/ui/table";
import { apiFetch } from "@/lib/api";
import type { AuditEntry } from "@/lib/types";
import { useToast } from "@/components/providers";
import { Search, ChevronDown, ChevronRight } from "lucide-react";

function formatTime(ts: string) {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return "-";
  return d.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function renderValue(v: unknown): string {
  if (v == null) return "—";
  if (typeof v === "string") return v;
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

export default function AuditPage() {
  const { pushToast } = useToast();
  const [chatId, setChatId] = useState("");
  const [items, setItems] = useState<AuditEntry[]>([]);
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const [loading, setLoading] = useState(true);

  async function load() {
    setLoading(true);
    const search = new URLSearchParams({ limit: "50" });
    if (chatId) search.set("chat_id", chatId);

    try {
      const payload = await apiFetch<{ audit: AuditEntry[] }>(
        `/api/admin/audit?${search}`,
      );
      setItems(payload.audit ?? []);
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "加载失败", "error");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    load();
  }, []);

  function toggle(id: number) {
    const next = new Set(expanded);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    setExpanded(next);
  }

  return (
    <AdminShell title="审计日志" subtitle="配置变更历史">
      <Card>
        <CardBody>
          <div className="grid gap-3 grid-cols-1 sm:grid-cols-[1fr_auto]">
            <Input
              placeholder="按 chat_id 过滤"
              value={chatId}
              onChange={(e) => setChatId(e.target.value)}
            />
            <Button onClick={load}>
              <Search className="h-3.5 w-3.5" />
              筛选
            </Button>
          </div>
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>变更记录 ({items.length})</CardTitle>
        </CardHeader>
        {items.length === 0 ? (
          <div className="px-5 py-12 text-center text-sm text-[var(--text-muted)]">
            {loading ? "加载中…" : "无审计记录"}
          </div>
        ) : (
          <div className="divide-y divide-[var(--border)]">
            {items.map((item) => {
              const isOpen = expanded.has(item.id);
              return (
                <div key={item.id} className="px-5 py-3">
                  <button
                    type="button"
                    onClick={() => toggle(item.id)}
                    className="flex w-full items-center gap-3 text-left"
                  >
                    {isOpen ? (
                      <ChevronDown className="h-4 w-4 text-[var(--text-muted)]" />
                    ) : (
                      <ChevronRight className="h-4 w-4 text-[var(--text-muted)]" />
                    )}
                    <span className="text-[var(--text-muted)] tabular-nums whitespace-nowrap text-sm">
                      {formatTime(item.created_at)}
                    </span>
                    <Badge tone="info">{item.scope}</Badge>
                    {item.chat_id ? (
                      <span className="tabular-nums text-sm">{item.chat_id}</span>
                    ) : (
                      <span className="text-sm text-[var(--text-muted)]">global</span>
                    )}
                    <code className="text-xs">{item.action}</code>
                    <span className="ml-auto text-xs text-[var(--text-muted)]">
                      admin {item.admin_id}
                    </span>
                  </button>
                  {isOpen && (
                    <div className="mt-3 ml-7 grid gap-3 sm:grid-cols-2">
                      <div>
                        <p className="text-xs font-medium text-[var(--text-muted)] mb-1">
                          Before
                        </p>
                        <pre className="overflow-x-auto rounded-md border border-[var(--border)] bg-[var(--surface-2)] p-3 text-xs leading-relaxed">
                          {renderValue(item.before)}
                        </pre>
                      </div>
                      <div>
                        <p className="text-xs font-medium text-[var(--text-muted)] mb-1">
                          After
                        </p>
                        <pre className="overflow-x-auto rounded-md border border-[var(--border)] bg-[var(--surface-2)] p-3 text-xs leading-relaxed">
                          {renderValue(item.after)}
                        </pre>
                      </div>
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </Card>
    </AdminShell>
  );
}
