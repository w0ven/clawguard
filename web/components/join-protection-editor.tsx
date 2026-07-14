"use client";

import { useEffect, useMemo, useState } from "react";
import {
  ArrowRight,
  Ban,
  Bell,
  CheckCircle2,
  Clock3,
  Play,
  RotateCcw,
  Save,
  ShieldAlert,
  Users,
} from "lucide-react";
import { apiFetch } from "@/lib/api";
import type { JoinProtectionPolicy } from "@/lib/types";
import { useToast } from "@/components/providers";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";

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
    }>(`/api/admin/groups/${chatId}/join-protection`)
      .then((payload) => {
        if (!alive) return;
        setInitial(payload.join_protection);
        setDraft(payload.join_protection);
        setDefaults(payload.defaults);
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
      const payload = await apiFetch<{ join_protection: JoinProtectionPolicy }>(
        `/api/admin/groups/${chatId}/join-protection`,
        { method: "PUT", body: JSON.stringify(draft) },
      );
      setInitial(payload.join_protection);
      setDraft(payload.join_protection);
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
              <p className="mt-1 text-xs text-[var(--text-muted)]">少量正常入群仍按原流程验证；短时间大量涌入时自动切换为安静防护。</p>
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
            <p className="mt-1 text-[var(--text-muted)]">不发验证图，不调用 CAS、资料检查或 AI，不逐人发送提示；后续新人直接临时封禁，只向管理员发送限频汇总。正常少量用户仍按原来的数学图片等验证流程处理。</p>
          </div>
        </CardBody>
      </Card>

      <Card>
        <CardHeader><CardTitle>自动处理流程</CardTitle></CardHeader>
        <CardBody>
          <div className="grid gap-3 lg:grid-cols-[1fr_auto_1fr_auto_1fr_auto_1fr] lg:items-stretch">
            {[
              [CheckCircle2, "正常时", "新人保持受限并完成原有验证"],
              [Users, "达到阈值", `${draft.join_window_seconds} 秒内达到 ${draft.join_threshold} 人，或待验证人数将到上限`],
              [ShieldAlert, "防护期间", "后续新人临时封禁，管理员只收汇总"],
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
              <p className="mt-2 text-[var(--text-muted)]">最近 {draft.join_window_seconds} 秒入群达到 {draft.join_threshold} 人。后续新人临时封禁 {Math.ceil(draft.temporary_ban_seconds / 60)} 分钟；预计 {Math.ceil(draft.protection_duration_seconds / 60)} 分钟后恢复。</p>
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
