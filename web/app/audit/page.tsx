"use client";

import { useEffect, useMemo, useState } from "react";
import { AdminShell } from "@/components/admin-shell";
import { Card, CardHeader, CardTitle, CardBody } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Select } from "@/components/ui/select";
import { apiFetch } from "@/lib/api";
import type { AuditEntry } from "@/lib/types";
import { useToast } from "@/components/providers";
import {
  AlertTriangle,
  Bot,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Search,
  ShieldAlert,
  UserCog,
  XCircle,
} from "lucide-react";

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

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

function textValue(value: unknown, fallback = "—") {
  if (value == null) return fallback;
  const text = String(value).trim();
  return text || fallback;
}

function nested(source: unknown, ...path: string[]) {
  let current: unknown = source;
  for (const key of path) {
    const record = asRecord(current);
    if (!(key in record)) return undefined;
    current = record[key];
  }
  return current;
}

function moderationSummary(item: AuditEntry) {
  const before = asRecord(item.before);
  const after = asRecord(item.after);
  const source = textValue(after.source ?? before.source ?? item.scope, item.scope);
  const action = textValue(after.action ?? item.action, item.action);
  const reason = textValue(after.reason, "—");
  const outcome = textValue(after.outcome, "success");
  const target = asRecord(before.target ?? after.target);
  const operator = asRecord(before.operator ?? after.operator);
  const targetLabel =
    textValue(target.display, "") ||
    textValue(target.username, "") ||
    textValue(target.user_id, "未知用户");
  const operatorLabel =
    textValue(operator.display, "") ||
    textValue(operator.username, "") ||
    textValue(operator.user_id, "—");
  const messageId = textValue(after.message_id, "—");
  const referencedMessageId = textValue(after.referenced_message_id, "—");
  const verdict = textValue(after.verdict, "");
  const category = textValue(after.category, "");
  const confidence = textValue(after.confidence, "");

  return {
    source,
    action,
    reason,
    outcome,
    targetLabel,
    operatorLabel,
    messageId,
    referencedMessageId,
    verdict,
    category,
    confidence,
    error: textValue(after.error, ""),
  };
}

function isModerationAudit(item: AuditEntry) {
  const before = asRecord(item.before);
  const after = asRecord(item.after);
  return (
    item.scope === "moderation" ||
    item.scope === "other_bot" ||
    String(before.source ?? after.source ?? "").includes("command:") ||
    String(before.source ?? after.source ?? "").startsWith("ai") ||
    String(before.source ?? after.source ?? "").includes("warning_escalation") ||
    String(before.source ?? after.source ?? "").includes("other_bot_join")
  );
}

function outcomeTone(outcome: string): "success" | "warning" | "danger" | "default" {
  switch (outcome) {
    case "success":
      return "success";
    case "skipped":
      return "warning";
    case "failed":
      return "danger";
    default:
      return "default";
  }
}

function actionTone(action: string): "success" | "warning" | "danger" | "info" | "default" {
  if (action.includes("ban") || action.includes("kick") || action.includes("delete")) return "danger";
  if (action.includes("mute") || action.includes("warn")) return "warning";
  if (action.includes("unban") || action.includes("trust")) return "success";
  if (action.includes("audit") || action.includes("flag")) return "info";
  return "default";
}

function SourceIcon({ source }: { source: string }) {
  if (source.startsWith("ai")) return <Bot className="h-4 w-4" />;
  if (source.startsWith("command:")) return <UserCog className="h-4 w-4" />;
  if (source.includes("other_bot")) return <ShieldAlert className="h-4 w-4" />;
  if (source.includes("warning")) return <AlertTriangle className="h-4 w-4" />;
  return <ChevronRight className="h-4 w-4" />;
}

function OutcomeIcon({ outcome }: { outcome: string }) {
  if (outcome === "success") return <CheckCircle2 className="h-4 w-4 text-[var(--success)]" />;
  if (outcome === "failed") return <XCircle className="h-4 w-4 text-[var(--danger)]" />;
  return <AlertTriangle className="h-4 w-4 text-[var(--warning)]" />;
}

const sourceOptions = [
  { value: "", label: "全部来源" },
  { value: "ai", label: "AI 自动处置" },
  { value: "command", label: "管理员命令" },
  { value: "warning", label: "警告升级" },
  { value: "other_bot", label: "其他 Bot 入群" },
  { value: "config", label: "配置变更" },
];

const outcomeOptions = [
  { value: "", label: "全部结果" },
  { value: "success", label: "成功" },
  { value: "failed", label: "失败" },
  { value: "skipped", label: "跳过" },
];

function matchesSource(item: AuditEntry, source: string) {
  if (!source) return true;
  const summary = moderationSummary(item);
  if (source === "config") return !isModerationAudit(item);
  if (source === "command") return summary.source.startsWith("command:");
  if (source === "ai") return summary.source.startsWith("ai");
  return summary.source.includes(source);
}

function matchesQuery(item: AuditEntry, query: string) {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  const s = moderationSummary(item);
  return [
    item.scope,
    item.action,
    item.chat_id,
    item.admin_id,
    s.source,
    s.action,
    s.reason,
    s.targetLabel,
    s.operatorLabel,
    s.messageId,
    s.referencedMessageId,
    s.verdict,
    s.category,
  ]
    .map((value) => String(value ?? "").toLowerCase())
    .some((value) => value.includes(q));
}

function ModerationAuditCard({ item, open, onToggle }: { item: AuditEntry; open: boolean; onToggle: () => void }) {
  const s = moderationSummary(item);
  return (
    <div className="px-5 py-4">
      <button type="button" onClick={onToggle} className="grid w-full gap-3 text-left lg:grid-cols-[auto_92px_1.2fr_1fr_auto] lg:items-center">
        <div className="flex items-center gap-2 text-[var(--text-muted)]">
          {open ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
          <SourceIcon source={s.source} />
        </div>
        <span className="text-sm tabular-nums text-[var(--text-muted)]">{formatTime(item.created_at)}</span>
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={actionTone(s.action)}>{s.action}</Badge>
            <Badge tone={outcomeTone(s.outcome)}>{s.outcome}</Badge>
            <span className="text-sm font-medium truncate">{s.targetLabel}</span>
          </div>
          <p className="mt-1 truncate text-xs text-[var(--text-muted)]">
            {s.reason}{s.verdict ? ` · ${s.verdict}` : ""}{s.category ? `/${s.category}` : ""}
          </p>
        </div>
        <div className="text-xs text-[var(--text-muted)]">
          <div>source: <code>{s.source}</code></div>
          <div>operator: {s.operatorLabel}</div>
        </div>
        <OutcomeIcon outcome={s.outcome} />
      </button>

      {open && (
        <div className="mt-4 ml-0 grid gap-3 lg:ml-12">
          <div className="grid gap-3 md:grid-cols-4">
            <MiniField label="Chat ID" value={item.chat_id ?? "global"} />
            <MiniField label="Message ID" value={s.messageId} />
            <MiniField label="Referenced" value={s.referencedMessageId} />
            <MiniField label="Confidence" value={s.confidence || "—"} />
          </div>
          {s.error && (
            <div className="rounded-md border border-[var(--danger)]/40 bg-[var(--danger)]/10 px-3 py-2 text-sm text-[var(--danger)]">
              {s.error}
            </div>
          )}
          <div className="grid gap-3 lg:grid-cols-2">
            <JSONPanel title="Before / Actor / Target" value={item.before} />
            <JSONPanel title="After / Outcome" value={item.after} />
          </div>
        </div>
      )}
    </div>
  );
}

function MiniField({ label, value }: { label: string; value: unknown }) {
  return (
    <div className="rounded-md border border-[var(--border)] bg-[var(--surface-2)] px-3 py-2">
      <p className="text-[10px] uppercase tracking-[0.16em] text-[var(--text-muted)]">{label}</p>
      <p className="mt-1 truncate text-sm font-medium tabular-nums">{textValue(value)}</p>
    </div>
  );
}

function JSONPanel({ title, value }: { title: string; value: unknown }) {
  return (
    <div>
      <p className="mb-1 text-xs font-medium text-[var(--text-muted)]">{title}</p>
      <pre className="max-h-72 overflow-auto rounded-md border border-[var(--border)] bg-[var(--surface-2)] p-3 text-xs leading-relaxed">
        {renderValue(value)}
      </pre>
    </div>
  );
}

function GenericAuditRow({ item, open, onToggle }: { item: AuditEntry; open: boolean; onToggle: () => void }) {
  return (
    <div className="px-5 py-3">
      <button type="button" onClick={onToggle} className="flex w-full items-center gap-3 text-left">
        {open ? <ChevronDown className="h-4 w-4 text-[var(--text-muted)]" /> : <ChevronRight className="h-4 w-4 text-[var(--text-muted)]" />}
        <span className="whitespace-nowrap text-sm tabular-nums text-[var(--text-muted)]">{formatTime(item.created_at)}</span>
        <Badge tone="info">{item.scope}</Badge>
        {item.chat_id ? <span className="text-sm tabular-nums">{item.chat_id}</span> : <span className="text-sm text-[var(--text-muted)]">global</span>}
        <code className="text-xs">{item.action}</code>
        <span className="ml-auto text-xs text-[var(--text-muted)]">admin {item.admin_id}</span>
      </button>
      {open && (
        <div className="mt-3 ml-7 grid gap-3 sm:grid-cols-2">
          <JSONPanel title="Before" value={item.before} />
          <JSONPanel title="After" value={item.after} />
        </div>
      )}
    </div>
  );
}

export default function AuditPage() {
  const { pushToast } = useToast();
  const [chatId, setChatId] = useState("");
  const [query, setQuery] = useState("");
  const [source, setSource] = useState("");
  const [outcome, setOutcome] = useState("");
  const [items, setItems] = useState<AuditEntry[]>([]);
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const [loading, setLoading] = useState(true);

  async function load() {
    setLoading(true);
    const search = new URLSearchParams({ limit: "100" });
    if (chatId) search.set("chat_id", chatId);

    try {
      const payload = await apiFetch<{ audit: AuditEntry[] }>(`/api/admin/audit?${search}`);
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

  const filtered = useMemo(
    () =>
      items.filter((item) => {
        if (!matchesSource(item, source)) return false;
        if (!matchesQuery(item, query)) return false;
        if (outcome && moderationSummary(item).outcome !== outcome) return false;
        return true;
      }),
    [items, outcome, query, source],
  );

  const stats = useMemo(() => {
    let failed = 0;
    let moderation = 0;
    for (const item of items) {
      if (isModerationAudit(item)) moderation += 1;
      if (moderationSummary(item).outcome === "failed") failed += 1;
    }
    return { total: items.length, moderation, failed };
  }, [items]);

  function toggle(id: number) {
    const next = new Set(expanded);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    setExpanded(next);
  }

  return (
    <AdminShell title="审计日志" subtitle="配置变更与群管处置追踪">
      <div className="grid gap-3 md:grid-cols-3">
        <Card><CardBody><MiniField label="Total" value={stats.total} /></CardBody></Card>
        <Card><CardBody><MiniField label="Moderation" value={stats.moderation} /></CardBody></Card>
        <Card><CardBody><MiniField label="Failed" value={stats.failed} /></CardBody></Card>
      </div>

      <Card>
        <CardBody>
          <div className="grid gap-3 lg:grid-cols-[1fr_1fr_170px_150px_auto]">
            <Input placeholder="按 chat_id 过滤" value={chatId} onChange={(e) => setChatId(e.target.value)} />
            <Input placeholder="搜索用户 / 动作 / 原因 / message_id" value={query} onChange={(e) => setQuery(e.target.value)} />
            <Select options={sourceOptions} value={source} onChange={(e) => setSource(e.target.value)} />
            <Select options={outcomeOptions} value={outcome} onChange={(e) => setOutcome(e.target.value)} />
            <Button onClick={load}>
              <Search className="h-3.5 w-3.5" />
              刷新
            </Button>
          </div>
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>审计记录 ({filtered.length}/{items.length})</CardTitle>
        </CardHeader>
        {filtered.length === 0 ? (
          <div className="px-5 py-12 text-center text-sm text-[var(--text-muted)]">
            {loading ? "加载中…" : "无审计记录"}
          </div>
        ) : (
          <div className="divide-y divide-[var(--border)]">
            {filtered.map((item) => {
              const open = expanded.has(item.id);
              return isModerationAudit(item) ? (
                <ModerationAuditCard key={item.id} item={item} open={open} onToggle={() => toggle(item.id)} />
              ) : (
                <GenericAuditRow key={item.id} item={item} open={open} onToggle={() => toggle(item.id)} />
              );
            })}
          </div>
        )}
      </Card>
    </AdminShell>
  );
}
