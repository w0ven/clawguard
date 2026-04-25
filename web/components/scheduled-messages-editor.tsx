"use client";

import type React from "react";
import { useEffect, useMemo, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { apiFetch } from "@/lib/api";
import type { Group, Nullable } from "@/lib/types";
import { validateHttpURL } from "@/lib/url";
import { useToast } from "@/components/providers";

type ScheduledButton = {
  text: string;
  url: string;
};

type ScheduledMessage = {
  id: number;
  chat_id: number;
  name: string;
  schedule_type: "interval" | "daily";
  interval_minutes: Nullable<number>;
  daily_times: string[];
  timezone: string;
  content: string;
  buttons: ScheduledButton[];
  auto_delete_seconds: number;
  enabled: boolean;
  status: "active" | "failed" | "paused";
  last_run_at: Nullable<string>;
  last_message_id: Nullable<number>;
  last_error: Nullable<string>;
  last_skip_reason: Nullable<string>;
  next_run_at: Nullable<string>;
  created_at: string;
  updated_at: string;
};

type ScheduledRun = {
  id: number;
  scheduled_message_id: number;
  ran_at: string;
  success: boolean;
  tg_message_id: Nullable<number>;
  rendered_preview: Nullable<string>;
  error: Nullable<string>;
  duration_ms: Nullable<number>;
};

type FormState = {
  id?: number;
  name: string;
  schedule_type: "interval" | "daily";
  interval_minutes: number;
  daily_times: string[];
  content: string;
  buttons: ScheduledButton[];
  auto_delete_seconds: number;
  enabled: boolean;
};

const emptyForm: FormState = {
  name: "",
  schedule_type: "interval",
  interval_minutes: 60,
  daily_times: ["09:00"],
  content: "",
  buttons: [],
  auto_delete_seconds: 0,
  enabled: true,
};

export function ScheduledMessagesEditor({ group }: { group: Group }) {
  const { pushToast } = useToast();
  const [items, setItems] = useState<ScheduledMessage[]>([]);
  const [limit, setLimit] = useState(20);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form, setForm] = useState<FormState | null>(null);
  const [timeInput, setTimeInput] = useState("");
  const [historyFor, setHistoryFor] = useState<ScheduledMessage | null>(null);
  const [runs, setRuns] = useState<ScheduledRun[]>([]);
  const [runsLoading, setRunsLoading] = useState(false);

  const reachedLimit = items.length >= limit;

  useEffect(() => {
    loadItems();
  }, [group.chat_id]);

  async function loadItems() {
    setLoading(true);
    try {
      const payload = await apiFetch<{
        scheduled_messages: ScheduledMessage[];
        limit: number;
      }>(`/api/admin/groups/${group.chat_id}/scheduled-messages`);
      setItems(payload.scheduled_messages ?? []);
      setLimit(payload.limit ?? 20);
    } catch (error) {
      pushToast(error instanceof Error ? error.message : "加载定时消息失败", "error");
    } finally {
      setLoading(false);
    }
  }

  function openCreate() {
    if (reachedLimit) {
      pushToast("每个群最多创建 20 条定时消息", "error");
      return;
    }
    setForm({ ...emptyForm, buttons: [], daily_times: ["09:00"] });
  }

  function openEdit(item: ScheduledMessage) {
    setForm({
      id: item.id,
      name: item.name,
      schedule_type: item.schedule_type,
      interval_minutes: item.interval_minutes ?? 60,
      daily_times: item.daily_times.length > 0 ? item.daily_times : ["09:00"],
      content: item.content,
      buttons: item.buttons ?? [],
      auto_delete_seconds: item.auto_delete_seconds,
      enabled: item.enabled,
    });
  }

  async function saveForm() {
    if (!form) return;
    const validation = validateForm(form);
    if (validation) {
      pushToast(validation, "error");
      return;
    }
    setSaving(true);
    try {
      const body = JSON.stringify({
        name: form.name.trim(),
        schedule_type: form.schedule_type,
        interval_minutes:
          form.schedule_type === "interval" ? Number(form.interval_minutes) : null,
        daily_times: form.schedule_type === "daily" ? form.daily_times : [],
        content: form.content,
        buttons: form.buttons.filter((button) => button.text.trim() || button.url.trim()),
        auto_delete_seconds: Number(form.auto_delete_seconds),
        enabled: form.enabled,
      });
      const path = form.id
        ? `/api/admin/groups/${group.chat_id}/scheduled-messages/${form.id}`
        : `/api/admin/groups/${group.chat_id}/scheduled-messages`;
      await apiFetch<{ scheduled_message: ScheduledMessage }>(path, {
        method: form.id ? "PUT" : "POST",
        body,
      });
      pushToast(form.id ? "已更新定时消息" : "已创建定时消息", "success");
      setForm(null);
      await loadItems();
    } catch (error) {
      pushToast(error instanceof Error ? error.message : "保存失败", "error");
    } finally {
      setSaving(false);
    }
  }

  async function deleteItem(item: ScheduledMessage) {
    if (!window.confirm(`确认删除定时消息「${item.name}」？`)) return;
    try {
      await apiFetch<void>(`/api/admin/groups/${group.chat_id}/scheduled-messages/${item.id}`, {
        method: "DELETE",
      });
      pushToast("已删除定时消息", "success");
      await loadItems();
    } catch (error) {
      pushToast(error instanceof Error ? error.message : "删除失败", "error");
    }
  }

  async function runNow(item: ScheduledMessage) {
    try {
      await apiFetch<{ status: string }>(
        `/api/admin/groups/${group.chat_id}/scheduled-messages/${item.id}/run-now`,
        { method: "POST" },
      );
      pushToast("试发成功", "success");
      await loadItems();
    } catch (error) {
      pushToast(error instanceof Error ? error.message : "试发失败", "error");
    }
  }

  async function openHistory(item: ScheduledMessage) {
    setHistoryFor(item);
    setRunsLoading(true);
    try {
      const payload = await apiFetch<{ runs: ScheduledRun[] }>(
        `/api/admin/groups/${group.chat_id}/scheduled-messages/${item.id}/runs`,
      );
      setRuns(payload.runs ?? []);
    } catch (error) {
      setRuns([]);
      pushToast(error instanceof Error ? error.message : "加载历史失败", "error");
    } finally {
      setRunsLoading(false);
    }
  }

  function addDailyTime() {
    if (!form) return;
    const value = timeInput.trim();
    if (!/^\d{2}:\d{2}$/.test(value)) {
      pushToast("时间格式必须是 HH:MM", "error");
      return;
    }
    setForm({
      ...form,
      daily_times: Array.from(new Set([...form.daily_times, value])).sort(),
    });
    setTimeInput("");
  }

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
            <div>
              <CardTitle>群定时消息</CardTitle>
              <p className="mt-1 text-xs text-[var(--text-muted)]">
                绑定当前群发送 MarkdownV2 文本，支持间隔发送、每日北京时间定点和链接按钮。
              </p>
            </div>
            <Button onClick={openCreate} disabled={reachedLimit} title={reachedLimit ? "每个群最多 20 条" : undefined}>
              新建定时消息
            </Button>
          </div>
        </CardHeader>
        <CardBody>
          {loading ? (
            <div className="py-12 text-center text-sm text-[var(--text-muted)]">加载中…</div>
          ) : items.length === 0 ? (
            <div className="py-12 text-center text-sm text-[var(--text-muted)]">暂无定时消息</div>
          ) : (
            <Table>
              <TableHead>
                <TableRow>
                  <TableHeaderCell>名称</TableHeaderCell>
                  <TableHeaderCell>类型</TableHeaderCell>
                  <TableHeaderCell>下次触发</TableHeaderCell>
                  <TableHeaderCell>状态</TableHeaderCell>
                  <TableHeaderCell>最后一次发送</TableHeaderCell>
                  <TableHeaderCell>操作</TableHeaderCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {items.map((item) => (
                  <TableRow key={item.id}>
                    <TableCell>
                      <div className="font-medium">{item.name}</div>
                      {item.last_error ? <div className="mt-1 text-xs text-[var(--danger)]">{item.last_error}</div> : null}
                      {item.last_skip_reason ? <div className="mt-1 text-xs text-[var(--warning)]">{item.last_skip_reason}</div> : null}
                    </TableCell>
                    <TableCell>{formatSchedule(item)}</TableCell>
                    <TableCell>{formatBeijing(item.next_run_at)}</TableCell>
                    <TableCell><StatusBadge item={item} /></TableCell>
                    <TableCell>{formatBeijing(item.last_run_at)}</TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-2">
                        <Button size="sm" variant="secondary" onClick={() => openEdit(item)}>编辑</Button>
                        <Button size="sm" variant="outline" onClick={() => runNow(item)}>立即试发</Button>
                        <Button size="sm" variant="ghost" onClick={() => openHistory(item)}>查看历史</Button>
                        <Button size="sm" variant="danger" onClick={() => deleteItem(item)}>删除</Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
          <p className="mt-3 text-xs text-[var(--text-muted)]">当前 {items.length}/{limit} 条。每日时间按北京时间显示和填写。</p>
        </CardBody>
      </Card>

      {form ? (
        <EditorDialog
          form={form}
          saving={saving}
          timeInput={timeInput}
          onTimeInputChange={setTimeInput}
          onAddTime={addDailyTime}
          onChange={setForm}
          onClose={() => setForm(null)}
          onSave={saveForm}
        />
      ) : null}

      {historyFor ? (
        <HistoryDrawer
          item={historyFor}
          runs={runs}
          loading={runsLoading}
          onClose={() => setHistoryFor(null)}
        />
      ) : null}
    </div>
  );
}

function EditorDialog({
  form,
  saving,
  timeInput,
  onTimeInputChange,
  onAddTime,
  onChange,
  onClose,
  onSave,
}: {
  form: FormState;
  saving: boolean;
  timeInput: string;
  onTimeInputChange: (value: string) => void;
  onAddTime: () => void;
  onChange: (value: FormState) => void;
  onClose: () => void;
  onSave: () => void;
}) {
  const title = form.id ? "编辑定时消息" : "新建定时消息";
  const dailyText = useMemo(() => form.daily_times.join("、"), [form.daily_times]);

  return (
    <div className="fixed inset-0 z-40 flex items-center justify-center bg-black/40 p-4">
      <Card className="max-h-[90vh] w-full max-w-3xl overflow-y-auto shadow-xl">
        <CardHeader>
          <div className="flex items-center justify-between gap-3">
            <CardTitle>{title}</CardTitle>
            <Button variant="ghost" size="sm" onClick={onClose}>关闭</Button>
          </div>
        </CardHeader>
        <CardBody className="space-y-4">
          <Field label="名称">
            <Input value={form.name} onChange={(e) => onChange({ ...form, name: e.target.value })} placeholder="例如：每日群公告" />
          </Field>
          <Field label="触发类型">
            <Select
              value={form.schedule_type}
              onChange={(e) => onChange({ ...form, schedule_type: e.target.value as "interval" | "daily" })}
              options={[{ label: "间隔", value: "interval" }, { label: "每日", value: "daily" }]}
            />
          </Field>
          {form.schedule_type === "interval" ? (
            <Field label="间隔分钟数">
              <Input type="number" min={1} max={10080} value={form.interval_minutes} onChange={(e) => onChange({ ...form, interval_minutes: Number(e.target.value) })} />
            </Field>
          ) : (
            <Field label="每日时间点（北京时间）">
              <div className="flex gap-2">
                <Input value={timeInput} onChange={(e) => onTimeInputChange(e.target.value)} placeholder="HH:MM" />
                <Button type="button" variant="secondary" onClick={onAddTime}>添加</Button>
              </div>
              <div className="mt-2 flex flex-wrap gap-2">
                {form.daily_times.map((value) => (
                  <button
                    key={value}
                    type="button"
                    className="rounded-full border border-[var(--border)] px-3 py-1 text-xs text-[var(--text-muted)] hover:text-[var(--danger)]"
                    onClick={() => onChange({ ...form, daily_times: form.daily_times.filter((item) => item !== value) })}
                    title="点击移除"
                  >
                    {value} ×
                  </button>
                ))}
              </div>
              <p className="mt-1 text-xs text-[var(--text-muted)]">当前：{dailyText || "未设置"}</p>
            </Field>
          )}
          <Field label="消息正文（MarkdownV2）">
            <Textarea rows={8} value={form.content} onChange={(e) => onChange({ ...form, content: e.target.value })} placeholder="欢迎来到 {group_title}，现在是 {date} {time}" />
            <p className="mt-1 text-xs text-[var(--text-muted)]">支持变量：{"{group_title}"} {"{member_count}"} {"{date}"} {"{time}"} {"{weekday}"}。用户正文保持原样，仅自动转义变量值。</p>
          </Field>
          <Field label="自动删除秒数">
            <Input type="number" min={0} max={3600} value={form.auto_delete_seconds} onChange={(e) => onChange({ ...form, auto_delete_seconds: Number(e.target.value) })} />
          </Field>
          <Field label="链接按钮">
            <div className="space-y-2">
              {form.buttons.map((button, index) => (
                <div key={index} className="grid gap-2 md:grid-cols-[1fr_2fr_auto]">
                  <Input value={button.text} onChange={(e) => updateButton(form, index, { text: e.target.value }, onChange)} placeholder="按钮文字" />
                  <Input value={button.url} onChange={(e) => updateButton(form, index, { url: e.target.value }, onChange)} placeholder="https://example.com" />
                  <Button variant="ghost" onClick={() => onChange({ ...form, buttons: form.buttons.filter((_, i) => i !== index) })}>删除</Button>
                </div>
              ))}
              <Button variant="secondary" type="button" onClick={() => onChange({ ...form, buttons: [...form.buttons, { text: "", url: "" }] })}>添加按钮</Button>
            </div>
          </Field>
          <label className="flex items-center gap-3 text-sm">
            <Switch checked={form.enabled} onCheckedChange={(checked) => onChange({ ...form, enabled: checked })} />
            启用任务
          </label>
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="secondary" onClick={onClose}>取消</Button>
            <Button onClick={onSave} disabled={saving}>{saving ? "保存中…" : "保存"}</Button>
          </div>
        </CardBody>
      </Card>
    </div>
  );
}

function HistoryDrawer({ item, runs, loading, onClose }: { item: ScheduledMessage; runs: ScheduledRun[]; loading: boolean; onClose: () => void }) {
  return (
    <div className="fixed inset-0 z-40 bg-black/30">
      <div className="ml-auto h-full w-full max-w-2xl overflow-y-auto border-l border-[var(--border)] bg-[var(--surface)] shadow-xl">
        <div className="sticky top-0 flex items-center justify-between border-b border-[var(--border)] bg-[var(--surface)] px-5 py-4">
          <div>
            <h3 className="text-sm font-semibold">发送历史</h3>
            <p className="text-xs text-[var(--text-muted)]">{item.name} · 最近 50 条</p>
          </div>
          <Button variant="ghost" onClick={onClose}>关闭</Button>
        </div>
        {loading ? (
          <div className="py-12 text-center text-sm text-[var(--text-muted)]">加载中…</div>
        ) : runs.length === 0 ? (
          <div className="py-12 text-center text-sm text-[var(--text-muted)]">暂无发送历史</div>
        ) : (
          <div className="divide-y divide-[var(--border)]">
            {runs.map((run) => (
              <div key={run.id} className="space-y-2 px-5 py-4 text-sm">
                <div className="flex items-center justify-between gap-3">
                  <span className="text-[var(--text-muted)]">{formatBeijing(run.ran_at)}</span>
                  <Badge tone={run.success ? "success" : "danger"}>{run.success ? "成功" : "失败/跳过"}</Badge>
                </div>
                {run.rendered_preview ? <pre className="whitespace-pre-wrap rounded-md bg-[var(--surface-2)] p-3 text-xs">{run.rendered_preview}</pre> : null}
                {run.error ? <p className="text-xs text-[var(--danger)]">{run.error}</p> : null}
                <p className="text-xs text-[var(--text-muted)]">消息 ID：{run.tg_message_id ?? "-"} · 耗时：{run.duration_ms ?? 0}ms</p>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block space-y-1.5 text-sm">
      <span className="font-medium">{label}</span>
      {children}
    </label>
  );
}

function StatusBadge({ item }: { item: ScheduledMessage }) {
  if (item.status === "failed") return <Badge tone="danger">失败</Badge>;
  if (!item.enabled || item.status === "paused") return <Badge tone="warning">暂停</Badge>;
  return <Badge tone="success">启用</Badge>;
}

function updateButton(form: FormState, index: number, patch: Partial<ScheduledButton>, onChange: (value: FormState) => void) {
  const buttons = form.buttons.map((button, i) => (i === index ? { ...button, ...patch } : button));
  onChange({ ...form, buttons });
}

function validateForm(form: FormState) {
  if (!form.name.trim()) return "名称不能为空";
  if (!form.content.trim()) return "消息正文不能为空";
  if (form.schedule_type === "interval" && (form.interval_minutes < 1 || form.interval_minutes > 10080)) return "间隔分钟数必须在 1-10080 之间";
  if (form.schedule_type === "daily" && form.daily_times.length === 0) return "每日模式至少需要一个时间点";
  if (form.auto_delete_seconds < 0 || form.auto_delete_seconds > 3600) return "自动删除秒数必须在 0-3600 之间";
  for (const button of form.buttons) {
    if ((button.text.trim() && !button.url.trim()) || (!button.text.trim() && button.url.trim())) return "按钮文本和链接都必须填写";
    const urlError = validateHttpURL(button.url);
    if (urlError) return urlError;
  }
  return "";
}

function formatSchedule(item: ScheduledMessage) {
  if (item.schedule_type === "interval") return `每 ${item.interval_minutes ?? "?"} 分钟`;
  return `每日 ${item.daily_times.join("、")}`;
}

function formatBeijing(value: Nullable<string>) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return date.toLocaleString("zh-CN", {
    timeZone: "Asia/Shanghai",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}
