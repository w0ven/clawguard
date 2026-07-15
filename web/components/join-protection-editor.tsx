"use client";

import { useEffect, useMemo, useState } from "react";
import {
  ArrowRight,
  Activity,
  AlertTriangle,
  Ban,
  Bell,
  CheckCircle2,
  Clock3,
  Play,
  RotateCcw,
  Save,
  ShieldAlert,
  ShieldCheck,
  Users,
} from "lucide-react";
import { apiFetch } from "@/lib/api";
import type { JoinProtectionPolicy, JoinProtectionRuntimeStatus } from "@/lib/types";
import { useToast } from "@/components/providers";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";

type Props = {
  chatId: number;
};

type NumberField = Exclude<keyof JoinProtectionPolicy, "enabled">;

const fieldDefinitions: Array<{
  key: NumberField;
  label: string;
  description: string;
  unit: string;
  min: number;
  max: number;
  advanced?: boolean;
}> = [
  {
    key: "join_threshold",
    label: "触发人数",
    description: "在统计时间内达到这个入群人数，就立即进入防护。",
    unit: "人",
    min: 2,
    max: 1000,
  },
  {
    key: "join_window_seconds",
    label: "统计时间",
    description: "只统计最近这段时间内的新成员。",
    unit: "秒",
    min: 5,
    max: 3600,
  },
  {
    key: "protection_duration_seconds",
    label: "防护持续时间",
    description: "触发后保持防护多久，结束后自动恢复正常验证。",
    unit: "秒",
    min: 30,
    max: 86400,
  },
  {
    key: "temporary_ban_seconds",
    label: "临时封禁时间",
    description: "防护期间进群的新账号会被临时封禁这么久。",
    unit: "秒",
    min: 60,
    max: 604800,
  },
  {
    key: "admin_notify_interval_seconds",
    label: "管理员提醒间隔",
    description: "防护持续时，管理员最多按这个间隔收到一次汇总。",
    unit: "秒",
    min: 30,
    max: 86400,
  },
  {
    key: "max_pending_verifications",
    label: "最大待验证人数",
    description: "待验证人数将达到上限时，也会直接进入防护，避免任务堆积。",
    unit: "人",
    min: 1,
    max: 10000,
  },
  {
    key: "telegram_failure_cooldown_seconds",
    label: "Telegram 故障冷却",
    description: "清理失败后暂停重复请求，避免持续重试拖垮该群。",
    unit: "秒",
    min: 30,
    max: 3600,
    advanced: true,
  },
];

export function JoinProtectionEditor({ chatId }: Props) {
  const { pushToast } = useToast();
  const [initial, setInitial] = useState<JoinProtectionPolicy | null>(null);
  const [draft, setDraft] = useState<JoinProtectionPolicy | null>(null);
  const [defaults, setDefaults] = useState<JoinProtectionPolicy | null>(null);
  const [runtimeStatus, setRuntimeStatus] = useState<JoinProtectionRuntimeStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [simulated, setSimulated] = useState(false);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setError(null);
    apiFetch<{
      join_protection: JoinProtectionPolicy;
      defaults: JoinProtectionPolicy;
      status: JoinProtectionRuntimeStatus;
    }>(`/api/admin/groups/${chatId}/join-protection`)
      .then((payload) => {
        if (!alive) return;
        setInitial(payload.join_protection);
        setDraft(payload.join_protection);
        setDefaults(payload.defaults);
        setRuntimeStatus(payload.status);
      })
      .catch((loadError) => {
        if (!alive) return;
        setError(loadError instanceof Error ? loadError.message : "加载入群防护失败");
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [chatId]);

  useEffect(() => {
    let alive = true;
    const timer = window.setInterval(() => {
      apiFetch<{ status: JoinProtectionRuntimeStatus }>(`/api/admin/groups/${chatId}/join-protection`)
        .then((payload) => {
          if (alive) setRuntimeStatus(payload.status);
        })
        .catch(() => undefined);
    }, 10_000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, [chatId]);

  const validationError = useMemo(() => {
    if (!draft) return null;
    for (const field of fieldDefinitions) {
      const value = draft[field.key];
      if (!Number.isInteger(value) || value < field.min || value > field.max) {
        return `${field.label}需要填写 ${field.min} 到 ${field.max} 之间的整数`;
      }
    }
    return null;
  }, [draft]);

  const dirty = useMemo(
    () => Boolean(initial && draft && JSON.stringify(initial) !== JSON.stringify(draft)),
    [initial, draft],
  );

  useEffect(() => {
    if (!dirty) return;
    const warnBeforeLeave = (event: BeforeUnloadEvent) => {
      event.preventDefault();
    };
    window.addEventListener("beforeunload", warnBeforeLeave);
    return () => window.removeEventListener("beforeunload", warnBeforeLeave);
  }, [dirty]);

  function updateNumber(key: NumberField, raw: string) {
    const value = Number(raw);
    setError(null);
    setDraft((current) => (current ? { ...current, [key]: value } : current));
  }

  async function save() {
    if (!draft || validationError) {
      setError(validationError ?? "配置尚未加载完成");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      const payload = await apiFetch<{ join_protection: JoinProtectionPolicy; status: JoinProtectionRuntimeStatus }>(
        `/api/admin/groups/${chatId}/join-protection`,
        { method: "PUT", body: JSON.stringify(draft) },
      );
      setInitial(payload.join_protection);
      setDraft(payload.join_protection);
      setRuntimeStatus(payload.status);
      pushToast("入群防护配置已保存", "success");
    } catch (saveError) {
      const message = saveError instanceof Error ? saveError.message : "保存失败";
      setError(message);
      pushToast(message, "error");
    } finally {
      setSaving(false);
    }
  }

  if (loading) {
    return <Card><CardBody className="py-10 text-center text-sm text-[var(--text-muted)]">正在加载入群防护配置…</CardBody></Card>;
  }

  if (!draft) {
    return <Card><CardBody className="py-10 text-center text-sm text-[var(--danger)]">{error ?? "无法加载配置"}</CardBody></Card>;
  }

  const standardFields = fieldDefinitions.filter((field) => !field.advanced);
  const advancedFields = fieldDefinitions.filter((field) => field.advanced);

  return (
    <div className="space-y-5 pb-20">
      <Card>
        <CardHeader>
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <CardTitle>入群洪泛防护</CardTitle>
              <p className="mt-1 text-xs text-[var(--text-muted)]">监测短时间内集中涌入的新账号，正常入群仍按原有验证流程处理。</p>
            </div>
            <div className="flex items-center gap-3">
              <span className="text-sm text-[var(--text-muted)]">{draft.enabled ? "已开启" : "已关闭"}</span>
              <Switch checked={draft.enabled} onCheckedChange={(enabled) => { setError(null); setDraft({ ...draft, enabled }); }} />
            </div>
          </div>
        </CardHeader>
        <CardBody>
          <div className="rounded-lg border border-[var(--border)] bg-[var(--surface-2)] p-4 text-sm leading-6">
            <p className="font-medium text-[var(--text)]">防护期间会发生什么？</p>
            <p className="mt-1 text-[var(--text-muted)]">不发验证图，不调用 CAS、资料检查或 AI，也不逐人发送提示；防护期间继续涌入的新账号会被临时封禁，结束后自动恢复原有验证流程。</p>
          </div>
        </CardBody>
      </Card>

      {runtimeStatus && <JoinProtectionStatusCard status={runtimeStatus} />}

      <Card>
        <CardHeader><CardTitle>自动处理流程</CardTitle></CardHeader>
        <CardBody>
          <div className="grid gap-3 lg:grid-cols-[1fr_auto_1fr_auto_1fr_auto_1fr] lg:items-stretch">
            {[
              [CheckCircle2, "正常时", "新人保持受限并完成原有验证"],
              [Users, "达到阈值", `${draft.join_window_seconds} 秒内新入群账号达到 ${draft.join_threshold} 人，或待验证人数接近上限`],
              [ShieldAlert, "防护期间", `后续新入群账号临时封禁 ${Math.ceil(draft.temporary_ban_seconds / 60)} 分钟`],
              [Clock3, "自动恢复", `${Math.ceil(draft.protection_duration_seconds / 60)} 分钟后恢复正常验证`],
            ].map(([Icon, title, description], index) => (
              <div key={String(title)} className="contents">
                <div className="rounded-lg border border-[var(--border)] p-4">
                  <Icon className="mb-3 h-5 w-5 text-[var(--accent)]" />
                  <p className="font-medium">{String(title)}</p>
                  <p className="mt-1 text-xs leading-5 text-[var(--text-muted)]">{String(description)}</p>
                </div>
                {index < 3 && <ArrowRight className="hidden h-5 w-5 self-center text-[var(--text-subtle)] lg:block" />}
              </div>
            ))}
          </div>
        </CardBody>
      </Card>

      <Card>
        <CardHeader><CardTitle>触发与处理设置</CardTitle></CardHeader>
        <CardBody className="grid gap-4 md:grid-cols-2">
          {standardFields.map((field) => (
            <NumberSetting key={field.key} field={field} value={draft[field.key]} onChange={(value) => updateNumber(field.key, value)} />
          ))}
        </CardBody>
      </Card>

      <Card>
        <CardHeader><CardTitle>高级设置</CardTitle></CardHeader>
        <CardBody className="grid gap-4 md:grid-cols-2">
          {advancedFields.map((field) => (
            <NumberSetting key={field.key} field={field} value={draft[field.key]} onChange={(value) => updateNumber(field.key, value)} />
          ))}
        </CardBody>
      </Card>

      <Card>
        <CardHeader><CardTitle>模拟触发效果</CardTitle></CardHeader>
        <CardBody>
          <p className="text-sm text-[var(--text-muted)]">这里只展示管理员会看到的效果，不会封禁用户，也不会调用任何生产动作。</p>
          <Button className="mt-4" variant="outline" onClick={() => setSimulated((value) => !value)}><Play className="h-4 w-4" />{simulated ? "收起模拟" : "模拟触发"}</Button>
          {simulated && (
            <div className="mt-4 rounded-lg border border-[var(--border)] bg-[var(--surface-2)] p-4 text-sm">
              <p className="font-medium">🛡️ 入群防护已触发</p>
              <p className="mt-2 text-[var(--text-muted)]">最近 {draft.join_window_seconds} 秒新入群账号达到 {draft.join_threshold} 人。防护期间，后续新入群账号临时封禁 {Math.ceil(draft.temporary_ban_seconds / 60)} 分钟，预计 {Math.ceil(draft.protection_duration_seconds / 60)} 分钟后自动恢复正常验证。</p>
            </div>
          )}
        </CardBody>
      </Card>

      {(error || validationError) && <div className="rounded-md border border-[var(--danger)] bg-[var(--danger-soft)] px-4 py-3 text-sm text-[var(--danger)]">{error ?? validationError}</div>}

      <div className="fixed bottom-4 left-4 right-4 z-20 flex flex-col gap-2 sm:bottom-6 sm:left-auto sm:right-6 sm:flex-row">
        <Button className="w-full sm:w-auto" variant="secondary" size="lg" disabled={!defaults || saving} onClick={() => { if (defaults) { setError(null); setDraft({ ...defaults }); } }}><RotateCcw className="h-4 w-4" />恢复默认值</Button>
        <Button className="w-full sm:w-auto" size="lg" disabled={!dirty || saving || Boolean(validationError)} onClick={save}><Save className="h-4 w-4" />{saving ? "保存中…" : dirty ? "保存配置" : "已保存"}</Button>
      </div>
    </div>
  );
}

const runtimeStateLabels: Record<JoinProtectionRuntimeStatus["state"], string> = {
  disabled: "已关闭",
  normal: "运行正常",
  protecting: "防护中",
  cleanup_cooldown: "清理冷却中",
};

function formatRuntimeTime(value?: string) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString("zh-CN", { hour12: false });
}

function JoinProtectionStatusCard({ status }: { status: JoinProtectionRuntimeStatus }) {
  const tone = status.state === "protecting" ? "warning" : status.state === "cleanup_cooldown" ? "danger" : status.state === "normal" ? "success" : "default";
  const stateIcon = status.state === "protecting" || status.state === "cleanup_cooldown" ? ShieldAlert : ShieldCheck;
  const StateIcon = stateIcon;
  const triggerLabel = status.trigger === "join_threshold" ? "入群人数阈值" : status.trigger === "pending_limit" ? "待验证人数上限" : "-";
  const metrics = [
    [Users, "窗口内入群", String(status.recent_joins)],
    [Activity, "待验证", String(status.pending_verifications)],
    [Ban, "本轮已拦截", String(status.intercepted)],
    [Clock3, "防护结束", formatRuntimeTime(status.protection_until)],
  ] as const;

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <CardTitle>实时运行状态</CardTitle>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={tone}><StateIcon className="mr-1 h-3.5 w-3.5" />{runtimeStateLabels[status.state]}</Badge>
            {status.degraded && <Badge tone="warning">内存降级</Badge>}
          </div>
        </div>
      </CardHeader>
      <CardBody className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          {metrics.map(([Icon, label, value]) => (
            <div key={label} className="min-w-0 rounded-lg border border-[var(--border)] p-4">
              <span className="flex items-center gap-2 text-xs text-[var(--text-muted)]"><Icon className="h-4 w-4" />{label}</span>
              <p className="mt-2 break-words text-sm font-medium text-[var(--text)]">{value}</p>
            </div>
          ))}
        </div>
        <div className="grid gap-x-8 gap-y-2 border-t border-[var(--border)] pt-4 text-xs text-[var(--text-muted)] sm:grid-cols-2">
          <p>本轮触发原因：<span className="text-[var(--text)]">{triggerLabel}</span></p>
          <p>最近恢复：<span className="text-[var(--text)]">{formatRuntimeTime(status.last_recovered_at)}</span></p>
          <p>上轮拦截：<span className="text-[var(--text)]">{status.last_intercepted}</span></p>
          <p>Redis 兜底任务：<span className="text-[var(--text)]">{status.deferred_cleanup_task_count}</span></p>
        </div>
        {status.last_cleanup_error && (
          <div className="flex gap-3 rounded-lg border border-[var(--warning)]/30 bg-[var(--warning-soft)] p-4 text-xs text-[var(--warning)]">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
            <p className="min-w-0 break-words">最近 Telegram 清理错误：{status.last_cleanup_error}（{formatRuntimeTime(status.last_cleanup_error_at)}）</p>
          </div>
        )}
      </CardBody>
    </Card>
  );
}

function NumberSetting({ field, value, onChange }: {
  field: (typeof fieldDefinitions)[number];
  value: number;
  onChange: (value: string) => void;
}) {
  const Icon = field.key.includes("notify") ? Bell : field.key.includes("ban") ? Ban : field.key.includes("pending") || field.key.includes("threshold") ? Users : Clock3;
  return (
    <label className="rounded-lg border border-[var(--border)] p-4">
      <span className="flex items-center gap-2 text-sm font-medium"><Icon className="h-4 w-4 text-[var(--accent)]" />{field.label}</span>
      <span className="mt-1 block min-h-10 text-xs leading-5 text-[var(--text-muted)]">{field.description}</span>
      <span className="mt-3 flex items-center gap-2"><Input type="number" min={field.min} max={field.max} step={1} value={Number.isNaN(value) ? "" : value} onChange={(event) => onChange(event.target.value)} /><span className="w-10 text-xs text-[var(--text-muted)]">{field.unit}</span></span>
    </label>
  );
}
