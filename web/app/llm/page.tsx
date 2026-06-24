"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { AdminShell } from "@/components/admin-shell";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { Tabs } from "@/components/ui/tabs";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from "@/components/ui/table";
import { apiFetch } from "@/lib/api";
import { useToast } from "@/components/providers";
import {
  Pencil,
  Play,
  Plus,
  RefreshCw,
  TestTube,
  Trash2,
} from "lucide-react";

type Provider = {
  id: number;
  key: string;
  label: string;
  type: string;
  base_url: string;
  api_key_set: boolean;
  api_key_hint: string;
  timeout_ms: number;
  extra_headers: Record<string, string>;
  enabled: boolean;
  created_at: string;
  updated_at: string;
};

type Model = {
  id: number;
  provider_id: number;
  provider_key: string;
  model_key: string;
  ref: string;
  label: string;
  api_format: string;
  enabled: boolean;
  supports_vision: boolean;
  supports_json: boolean;
  supports_tools: boolean;
  capability_tags: string[];
  priority: number;
  meta: Record<string, unknown> | null;
  probe_enabled: boolean;
  probe_interval_seconds: number;
};

type Stats = {
  model_id: number;
  healthy: boolean;
  last_check_at: string | null;
  last_ok_at: string | null;
  last_error: string;
  latency_p50_ms: number | null;
  latency_p95_ms: number | null;
  success_1h: number;
  fail_1h: number;
  success_24h: number;
  fail_24h: number;
  updated_at: string;
};

type ToastFn = (message: string, tone?: "success" | "error") => void;

type ProbeSettings = {
  auto_degrade: boolean;
  probe_enabled: boolean;
  probe_interval_seconds: number;
};

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <label className="flex flex-col gap-1 text-xs text-[var(--text-muted)]">
      <span>{label}</span>
      {children}
      {hint && (
        <span className="text-[11px] leading-snug text-[var(--text-subtle)]">
          {hint}
        </span>
      )}
    </label>
  );
}

// Helper: a styled <select> that matches Input look
function SelectBox({
  value,
  onChange,
  options,
  disabled,
}: {
  value: string;
  onChange: (v: string) => void;
  options: Array<{ value: string; label: string }>;
  disabled?: boolean;
}) {
  return (
    <select
      className="h-9 w-full rounded-md border border-[var(--border)] bg-[var(--surface)] px-3 text-sm disabled:opacity-60"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      disabled={disabled}
    >
      {options.map((o) => (
        <option key={o.value} value={o.value}>
          {o.label}
        </option>
      ))}
    </select>
  );
}

function fmtTime(s: string | null | undefined) {
  if (!s) return "—";
  const d = new Date(s);
  if (isNaN(d.getTime())) return "—";
  return d.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function asRecord(value: unknown): Record<string, unknown> {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as Record<string, unknown>;
  }
  return {};
}

export default function LLMAdminPage() {
  const { pushToast } = useToast();
  const [tab, setTab] = useState<"providers" | "models" | "stats" | "settings">("providers");
  const [providers, setProviders] = useState<Provider[]>([]);
  const [models, setModels] = useState<Model[]>([]);
  const [stats, setStats] = useState<Stats[]>([]);
  const [settings, setSettings] = useState<ProbeSettings>({
    auto_degrade: true,
    probe_enabled: false,
    probe_interval_seconds: 120,
  });
  const [loading, setLoading] = useState(true);

  const statsByModelId = useMemo(() => {
    const map = new Map<number, Stats>();
    stats.forEach((s) => map.set(s.model_id, s));
    return map;
  }, [stats]);

  const reloadAll = useCallback(async () => {
    setLoading(true);
    try {
      const [p, m, s, g] = await Promise.all([
        apiFetch<{ providers: Provider[] }>("/api/admin/llm/providers"),
        apiFetch<{ models: Model[] }>("/api/admin/llm/models"),
        apiFetch<{ stats: Stats[] }>("/api/admin/llm/stats"),
        apiFetch<{ config: Record<string, unknown> }>("/api/admin/global-config"),
      ]);
      setProviders(p.providers ?? []);
      setModels(m.models ?? []);
      setStats(s.stats ?? []);
      const ai = asRecord(asRecord(g.config).ai);
      setSettings({
        auto_degrade: Boolean(ai.auto_degrade ?? true),
        probe_enabled: Boolean(ai.probe_enabled ?? false),
        probe_interval_seconds: Number(ai.probe_interval_seconds ?? 120) || 120,
      });
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "加载失败", "error");
    } finally {
      setLoading(false);
    }
  }, [pushToast]);

  useEffect(() => {
    reloadAll();
  }, [reloadAll]);

  const tabs: Array<{ value: typeof tab; label: string }> = [
    { value: "providers", label: `Providers · ${providers.length}` },
    { value: "models", label: `Models · ${models.length}` },
    { value: "stats", label: `健康 · ${stats.length}` },
    { value: "settings", label: "探活设置" },
  ];

  return (
    <AdminShell
      title="模型管理"
      subtitle="Provider / Model / 健康统计"
      actions={
        <Button variant="secondary" onClick={reloadAll} disabled={loading}>
          <RefreshCw
            size={14}
            className={loading ? "animate-spin" : undefined}
          />
          刷新
        </Button>
      }
    >
      <div className="mb-4">
        <Tabs
          tabs={tabs}
          value={tab}
          onValueChange={(v) => setTab(v as typeof tab)}
        />
      </div>

      {tab === "providers" && (
        <ProviderPanel
          providers={providers}
          onChanged={reloadAll}
          pushToast={pushToast}
        />
      )}
      {tab === "models" && (
        <ModelPanel
          providers={providers}
          models={models}
          statsByModelId={statsByModelId}
          onChanged={reloadAll}
          pushToast={pushToast}
        />
      )}
      {tab === "stats" && <StatsPanel models={models} stats={stats} />}
      {tab === "settings" && (
        <SettingsPanel
          settings={settings}
          onChanged={reloadAll}
          pushToast={pushToast}
        />
      )}
    </AdminShell>
  );
}

function ProviderPanel({
  providers,
  onChanged,
  pushToast,
}: {
  providers: Provider[];
  onChanged: () => Promise<void>;
  pushToast: ToastFn;
}) {
  const [showForm, setShowForm] = useState(false);
  const [editingProvider, setEditingProvider] = useState<Provider | null>(null);

  async function remove(key: string) {
    if (!confirm(`删除 Provider "${key}"？会级联删除其下所有模型。`)) return;
    try {
      await apiFetch<{ ok: boolean }>(
        `/api/admin/llm/providers/${encodeURIComponent(key)}`,
        { method: "DELETE" },
      );
      pushToast("已删除", "success");
      await onChanged();
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "删除失败", "error");
    }
  }

  async function toggleEnabled(p: Provider) {
    try {
      await apiFetch(`/api/admin/llm/providers/${encodeURIComponent(p.key)}`, {
        method: "PUT",
        body: JSON.stringify({ enabled: !p.enabled }),
      });
      await onChanged();
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "更新失败", "error");
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Providers</CardTitle>
        <Button
          onClick={() => {
            setEditingProvider(null);
            setShowForm((v) => !v);
          }}
        >
          <Plus size={14} />
          {showForm || editingProvider ? "取消" : "新建"}
        </Button>
      </CardHeader>
      <CardBody>
        {(showForm || editingProvider) && (
          <ProviderForm
            provider={editingProvider}
            onDone={async () => {
              setShowForm(false);
              setEditingProvider(null);
              await onChanged();
            }}
            onCancel={() => {
              setShowForm(false);
              setEditingProvider(null);
            }}
            pushToast={pushToast}
          />
        )}
        <Table>
          <TableHead>
            <TableRow>
              <TableHeaderCell>Key</TableHeaderCell>
              <TableHeaderCell>标签</TableHeaderCell>
              <TableHeaderCell>类型</TableHeaderCell>
              <TableHeaderCell>Base URL</TableHeaderCell>
              <TableHeaderCell>API Key</TableHeaderCell>
              <TableHeaderCell>超时</TableHeaderCell>
              <TableHeaderCell>启用</TableHeaderCell>
              <TableHeaderCell>操作</TableHeaderCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {providers.map((p) => (
              <TableRow key={p.id}>
                <TableCell className="font-mono text-xs">{p.key}</TableCell>
                <TableCell>{p.label}</TableCell>
                <TableCell>
                  <Badge>{p.type}</Badge>
                </TableCell>
                <TableCell className="font-mono text-xs">
                  {p.base_url}
                </TableCell>
                <TableCell className="font-mono text-xs">
                  {p.api_key_set ? p.api_key_hint : "—"}
                </TableCell>
                <TableCell>{p.timeout_ms} ms</TableCell>
                <TableCell>
                  <Switch
                    checked={p.enabled}
                    onCheckedChange={() => toggleEnabled(p)}
                  />
                </TableCell>
                <TableCell>
                  <div className="flex gap-1">
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => {
                        setEditingProvider(p);
                        setShowForm(false);
                      }}
                      title="编辑 Provider"
                    >
                      <Pencil size={12} />
                    </Button>
                    <Button
                      variant="danger"
                      size="sm"
                      onClick={() => remove(p.key)}
                    >
                      <Trash2 size={12} />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardBody>
    </Card>
  );
}

function ProviderForm({
  provider,
  onDone,
  onCancel,
  pushToast,
}: {
  provider?: Provider | null;
  onDone: () => Promise<void>;
  onCancel: () => void;
  pushToast: ToastFn;
}) {
  const editing = Boolean(provider);
  const [key, setKey] = useState(provider?.key ?? "");
  const [label, setLabel] = useState(provider?.label ?? "");
  const [type, setType] = useState(provider?.type ?? "openai");
  const [baseURL, setBaseURL] = useState(provider?.base_url ?? "");
  const [apiKey, setApiKey] = useState("");
  const [timeoutMs, setTimeoutMs] = useState(provider?.timeout_ms ?? 8000);
  const [extraHeaders, setExtraHeaders] = useState(
    provider?.extra_headers ? JSON.stringify(provider.extra_headers, null, 2) : "",
  );
  const [enabled, setEnabled] = useState(provider?.enabled ?? true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setKey(provider?.key ?? "");
    setLabel(provider?.label ?? "");
    setType(provider?.type ?? "openai");
    setBaseURL(provider?.base_url ?? "");
    setApiKey("");
    setTimeoutMs(provider?.timeout_ms ?? 8000);
    setExtraHeaders(
      provider?.extra_headers
        ? JSON.stringify(provider.extra_headers, null, 2)
        : "",
    );
    setEnabled(provider?.enabled ?? true);
  }, [provider]);

  async function submit() {
    setSaving(true);
    try {
      let parsedHeaders: Record<string, string> = {};
      if (extraHeaders.trim()) {
        try {
          parsedHeaders = JSON.parse(extraHeaders);
        } catch {
          pushToast("extra_headers 不是合法 JSON", "error");
          setSaving(false);
          return;
        }
      }
      await apiFetch(
        editing
          ? `/api/admin/llm/providers/${encodeURIComponent(provider!.key)}`
          : "/api/admin/llm/providers",
        {
          method: editing ? "PUT" : "POST",
          body: JSON.stringify({
            key,
            label,
            type,
            base_url: baseURL,
            api_key: apiKey,
            timeout_ms: timeoutMs,
            extra_headers: parsedHeaders,
            enabled,
          }),
        },
      );
      pushToast(editing ? "Provider 已更新" : "Provider 已创建", "success");
      await onDone();
    } catch (e) {
      pushToast(
        e instanceof Error ? e.message : editing ? "更新失败" : "创建失败",
        "error",
      );
    } finally {
      setSaving(false);
    }
  }

  const [showAdvanced, setShowAdvanced] = useState(false);

  return (
    <div className="mb-4 rounded-md border border-[var(--border)] bg-[var(--surface-2)] p-4">
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
        <Field label="Key (唯一标识，英文/数字)">
          <Input
            value={key}
            onChange={(e) => setKey(e.target.value)}
            placeholder="newapi"
            disabled={editing}
          />
        </Field>
        <Field label="名字">
          <Input
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            placeholder="NewAPI 聚合"
          />
        </Field>
        <Field label="Base URL">
          <Input
            value={baseURL}
            onChange={(e) => setBaseURL(e.target.value)}
            placeholder="https://newapi.misaka.si/v1"
          />
        </Field>
        <Field label={editing ? "API Key (留空 = 不修改)" : "API Key"}>
          <Input
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            placeholder="sk-..."
            type="password"
          />
        </Field>
      </div>

      <div className="mt-3">
        <button
          type="button"
          className="text-xs text-[var(--text-muted)] hover:text-[var(--text)]"
          onClick={() => setShowAdvanced((v) => !v)}
        >
          {showAdvanced ? "▾ 隐藏高级选项" : "▸ 高级选项"}
        </button>
      </div>

      {showAdvanced && (
        <div className="mt-3 grid grid-cols-1 gap-3 border-t border-[var(--border)] pt-3 md:grid-cols-2">
          <Field
            label="类型"
            hint="绝大多数情况选 OpenAI 兼容。Claude 原生接口才选 Anthropic。"
          >
            <SelectBox
              value={type}
              onChange={setType}
              options={[
                { value: "openai", label: "OpenAI 兼容（GPT / GLM / Kimi / NewAPI 等）" },
                { value: "anthropic", label: "Anthropic（Claude 原生 messages API）" },
              ]}
            />
          </Field>
          <Field
            label="超时（毫秒）"
            hint="单次请求最长等待时间，默认 8000（8 秒）。慢模型可以调到 15000-30000。"
          >
            <Input
              value={String(timeoutMs)}
              onChange={(e) => setTimeoutMs(Number(e.target.value || 0))}
              type="number"
            />
          </Field>
          <div className="md:col-span-2">
            <Field
              label="额外请求头（JSON, 一般不填）"
              hint='比如某些 API 要求额外认证头。格式 {"Key":"Value"}。'
            >
              <Textarea
                value={extraHeaders}
                onChange={(e) => setExtraHeaders(e.target.value)}
                rows={3}
                placeholder={'{\n  "X-Source": "clawguard"\n}'}
              />
            </Field>
          </div>
        </div>
      )}

      <div className="mt-4 flex items-center justify-between">
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={enabled} onCheckedChange={setEnabled} />
          启用
        </label>
        <div className="flex gap-2">
          <Button type="button" variant="secondary" onClick={onCancel}>
            取消
          </Button>
          <Button onClick={submit} disabled={saving}>
            {saving ? "保存中…" : editing ? "保存修改" : "保存"}
          </Button>
        </div>
      </div>
    </div>
  );
}

function ModelPanel({
  providers,
  models,
  statsByModelId,
  onChanged,
  pushToast,
}: {
  providers: Provider[];
  models: Model[];
  statsByModelId: Map<number, Stats>;
  onChanged: () => Promise<void>;
  pushToast: ToastFn;
}) {
  const [showForm, setShowForm] = useState(false);
  const [editingModel, setEditingModel] = useState<Model | null>(null);
  const [testingRef, setTestingRef] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<Record<string, string>>({});

  async function remove(m: Model) {
    if (!confirm(`删除模型 "${m.ref}"？`)) return;
    try {
      await apiFetch(
        `/api/admin/llm/models/${encodeURIComponent(m.provider_key)}/${encodeURIComponent(m.model_key)}`,
        { method: "DELETE" },
      );
      pushToast("已删除", "success");
      await onChanged();
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "删除失败", "error");
    }
  }

  async function toggleEnabled(m: Model) {
    try {
      await apiFetch(
        `/api/admin/llm/models/${encodeURIComponent(m.provider_key)}/${encodeURIComponent(m.model_key)}`,
        {
          method: "PUT",
          body: JSON.stringify({ enabled: !m.enabled }),
        },
      );
      await onChanged();
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "更新失败", "error");
    }
  }

  async function probe(m: Model) {
    setTestingRef(m.ref);
    try {
      const r = await apiFetch<{
        ok: boolean;
        latency_ms: number;
        error?: string;
      }>(
        `/api/admin/llm/models/${encodeURIComponent(m.provider_key)}/${encodeURIComponent(m.model_key)}/probe`,
        { method: "POST" },
      );
      const msg = r.ok ? `✅ ${r.latency_ms} ms` : `❌ ${r.error}`;
      setTestResult((prev) => ({ ...prev, [m.ref]: msg }));
      pushToast(`Probe ${m.ref}: ${msg}`, r.ok ? "success" : "error");
      await onChanged();
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "probe 失败", "error");
    } finally {
      setTestingRef(null);
    }
  }

  async function test(m: Model) {
    const text = prompt(`向 ${m.ref} 发送一条测试消息：`, "ping");
    if (!text) return;
    setTestingRef(m.ref);
    try {
      const r = await apiFetch<{
        ok: boolean;
        latency_ms?: number;
        error?: string;
        reply?: string;
        prompt_tokens?: number;
        completion_tokens?: number;
      }>(
        `/api/admin/llm/models/${encodeURIComponent(m.provider_key)}/${encodeURIComponent(m.model_key)}/test`,
        {
          method: "POST",
          body: JSON.stringify({ text }),
        },
      );
      if (!r.ok) {
        pushToast(`❌ ${r.error}`, "error");
        setTestResult((prev) => ({ ...prev, [m.ref]: `❌ ${r.error}` }));
      } else {
        const reply = (r.reply ?? "").trim().slice(0, 120);
        const summary = `✅ ${r.latency_ms ?? "?"}ms · ${r.prompt_tokens ?? 0}→${r.completion_tokens ?? 0} tok${reply ? ` · ${reply}` : ""}`;
        pushToast(`✅ ${r.latency_ms ?? "?"}ms`, "success");
        setTestResult((prev) => ({ ...prev, [m.ref]: summary }));
        await onChanged();
      }
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "test 失败", "error");
    } finally {
      setTestingRef(null);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Models</CardTitle>
        <Button
          onClick={() => {
            setEditingModel(null);
            setShowForm((v) => !v);
          }}
        >
          <Plus size={14} />
          {showForm || editingModel ? "取消" : "新建"}
        </Button>
      </CardHeader>
      <CardBody>
        {(showForm || editingModel) && (
          <ModelForm
            providers={providers}
            model={editingModel}
            onDone={async () => {
              setShowForm(false);
              setEditingModel(null);
              await onChanged();
            }}
            onCancel={() => {
              setShowForm(false);
              setEditingModel(null);
            }}
            pushToast={pushToast}
          />
        )}
        <Table>
          <TableHead>
            <TableRow>
              <TableHeaderCell>Ref</TableHeaderCell>
              <TableHeaderCell>标签</TableHeaderCell>
              <TableHeaderCell>API</TableHeaderCell>
              <TableHeaderCell>能力</TableHeaderCell>
              <TableHeaderCell>优先级</TableHeaderCell>
              <TableHeaderCell>健康</TableHeaderCell>
              <TableHeaderCell>启用</TableHeaderCell>
              <TableHeaderCell>操作</TableHeaderCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {models.map((m) => {
              const st = statsByModelId.get(m.id);
              return (
                <TableRow key={m.id}>
                  <TableCell className="font-mono text-xs">{m.ref}</TableCell>
                  <TableCell>{m.label}</TableCell>
                  <TableCell>
                    <Badge>{m.api_format}</Badge>
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap gap-1">
                      {(() => {
                        const tags = new Set<string>(m.capability_tags ?? []);
                        if (m.supports_vision) tags.add("vision");
                        if (m.supports_tools) tags.add("tools");
                        return [...tags].map((t) => (
                          <Badge key={t}>{t}</Badge>
                        ));
                      })()}
                    </div>
                  </TableCell>
                  <TableCell>{m.priority}</TableCell>
                  <TableCell>
                    {!m.probe_enabled ? (
                      <span className="text-xs text-[var(--text-subtle)]">
                        未启用探活
                      </span>
                    ) : !st ? (
                      <span className="text-xs text-[var(--text-subtle)]">
                        等待首次探活
                      </span>
                    ) : st.healthy ? (
                      <Badge tone="success">
                        OK · {st.latency_p95_ms ?? "-"}ms
                      </Badge>
                    ) : (
                      <Badge tone="danger" title={st.last_error || undefined}>
                        FAIL
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell>
                    <Switch
                      checked={m.enabled}
                      onCheckedChange={() => toggleEnabled(m)}
                    />
                  </TableCell>
                  <TableCell>
                    <div className="flex gap-1">
                      <Button
                        variant="secondary"
                        size="sm"
                        disabled={testingRef === m.ref}
                        onClick={() => {
                          setEditingModel(m);
                          setShowForm(false);
                        }}
                        title="编辑模型"
                      >
                        <Pencil size={12} />
                      </Button>
                      <Button
                        variant="secondary"
                        size="sm"
                        disabled={testingRef === m.ref}
                        onClick={() => probe(m)}
                        title="最小 ping"
                      >
                        <Play size={12} />
                      </Button>
                      <Button
                        variant="secondary"
                        size="sm"
                        disabled={testingRef === m.ref}
                        onClick={() => test(m)}
                        title="发测试消息"
                      >
                        <TestTube size={12} />
                      </Button>
                      <Button
                        variant="danger"
                        size="sm"
                        onClick={() => remove(m)}
                      >
                        <Trash2 size={12} />
                      </Button>
                    </div>
                    {testResult[m.ref] && (
                      <div className="mt-1 text-xs text-[var(--text-muted)]">
                        {testResult[m.ref]}
                      </div>
                    )}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </CardBody>
    </Card>
  );
}

function ModelForm({
  providers,
  model,
  onDone,
  onCancel,
  pushToast,
}: {
  providers: Provider[];
  model?: Model | null;
  onDone: () => Promise<void>;
  onCancel: () => void;
  pushToast: ToastFn;
}) {
  const editing = Boolean(model);
  const [providerKey, setProviderKey] = useState(
    model?.provider_key ?? providers[0]?.key ?? "",
  );
  const [modelKey, setModelKey] = useState(model?.model_key ?? "");
  const [label, setLabel] = useState(model?.label ?? "");
  const [apiFormat, setApiFormat] = useState(model?.api_format ?? "openai_chat");
  const [priority, setPriority] = useState(model?.priority ?? 100);
  const [caps, setCaps] = useState(
    (model?.capability_tags ?? ["moderation"]).join(", "),
  );
  const [supportsVision, setSupportsVision] = useState(
    model?.supports_vision ?? false,
  );
  const [supportsJSON, setSupportsJSON] = useState(
    model?.supports_json ?? true,
  );
  const [supportsTools, setSupportsTools] = useState(
    model?.supports_tools ?? false,
  );
  const [enabled, setEnabled] = useState(model?.enabled ?? true);
  const [probeEnabled, setProbeEnabled] = useState(
    model?.probe_enabled ?? true,
  );
  const [probeInterval, setProbeInterval] = useState(
    model?.probe_interval_seconds ?? 0,
  );
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setProviderKey(model?.provider_key ?? providers[0]?.key ?? "");
    setModelKey(model?.model_key ?? "");
    setLabel(model?.label ?? "");
    setApiFormat(model?.api_format ?? "openai_chat");
    setPriority(model?.priority ?? 100);
    setCaps((model?.capability_tags ?? ["moderation"]).join(", "));
    setSupportsVision(model?.supports_vision ?? false);
    setSupportsJSON(model?.supports_json ?? true);
    setSupportsTools(model?.supports_tools ?? false);
    setEnabled(model?.enabled ?? true);
    setProbeEnabled(model?.probe_enabled ?? true);
    setProbeInterval(model?.probe_interval_seconds ?? 0);
  }, [model, providers]);

  async function submit() {
    const capabilityTags = caps
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    if (supportsVision && !capabilityTags.includes("vision")) {
      capabilityTags.push("vision");
    }
    setSaving(true);
    try {
      await apiFetch(
        editing
          ? `/api/admin/llm/models/${encodeURIComponent(model!.provider_key)}/${encodeURIComponent(model!.model_key)}`
          : "/api/admin/llm/models",
        {
          method: editing ? "PUT" : "POST",
          body: JSON.stringify({
            provider_key: providerKey,
            model_key: modelKey,
            label,
            api_format: apiFormat,
            priority,
            capability_tags: capabilityTags,
            supports_vision: supportsVision,
            supports_json: supportsJSON,
            supports_tools: supportsTools,
            enabled,
            probe_enabled: probeEnabled,
            probe_interval_seconds: probeInterval,
          }),
        },
      );
      pushToast(editing ? "Model 已更新" : "Model 已创建", "success");
      await onDone();
    } catch (e) {
      pushToast(
        e instanceof Error ? e.message : editing ? "更新失败" : "创建失败",
        "error",
      );
    } finally {
      setSaving(false);
    }
  }

  const [showAdvanced, setShowAdvanced] = useState(false);

  return (
    <div className="mb-4 rounded-md border border-[var(--border)] bg-[var(--surface-2)] p-4">
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
        <Field
          label="Provider"
          hint="该模型从哪家 API 调。要先在 Providers 页添加。"
        >
          <SelectBox
            value={providerKey}
            onChange={setProviderKey}
            options={providers.map((p) => ({
              value: p.key,
              label: `${p.key} · ${p.label}`,
            }))}
            disabled={editing}
          />
        </Field>
        <Field
          label="Model ID"
          hint="调用 API 时填的 model 参数，必须跟 Provider 那边完全一致，例如 gpt-5.4、glm-5-turbo。"
        >
          <Input
            value={modelKey}
            onChange={(e) => setModelKey(e.target.value)}
            placeholder="gpt-5.4"
            disabled={editing}
          />
        </Field>
        <Field
          label="显示名字"
          hint="随便起，只是列表里方便人看。"
        >
          <Input
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            placeholder="GPT-5.4"
          />
        </Field>
        <div className="flex flex-col gap-1 text-xs text-[var(--text-muted)]">
          <span>是否支持图片</span>
          <label className="flex items-center gap-2 self-start pt-2 text-sm">
            <Switch checked={supportsVision} onCheckedChange={setSupportsVision} />
            <span>这个模型能看图片</span>
          </label>
          <span className="text-[11px] leading-snug text-[var(--text-subtle)]">
            GPT-5.4、Claude、Gemini 等可以看图；普通 GLM / Kimi 不行。图片审核必须开。
          </span>
        </div>
      </div>

      <div className="mt-3">
        <button
          type="button"
          className="text-xs text-[var(--text-muted)] hover:text-[var(--text)]"
          onClick={() => setShowAdvanced((v) => !v)}
        >
          {showAdvanced ? "▾ 隐藏高级选项" : "▸ 高级选项"}
        </button>
      </div>

      {showAdvanced && (
        <div className="mt-3 grid grid-cols-1 gap-3 border-t border-[var(--border)] pt-3 md:grid-cols-2">
          <Field
            label="API 风格"
            hint="99% 情况下保持 OpenAI 兼容。Claude 原生 API 才选 Anthropic Messages。"
          >
            <SelectBox
              value={apiFormat}
              onChange={setApiFormat}
              options={[
                { value: "openai_chat", label: "OpenAI Chat Completions（推荐）" },
                { value: "openai_responses", label: "OpenAI Responses（新版）" },
                { value: "anthropic_messages", label: "Anthropic Messages（Claude 原生）" },
              ]}
            />
          </Field>
          <Field
            label="优先级"
            hint="数字越小越先用。默认 100。想让某个当首选就填 10 甚至 1。"
          >
            <Input
              value={String(priority)}
              onChange={(e) => setPriority(Number(e.target.value || 0))}
              type="number"
            />
          </Field>
          <div className="md:col-span-2">
            <div className="flex flex-col gap-1 text-xs text-[var(--text-muted)]">
              <span>用途标签</span>
              <div className="flex flex-wrap gap-2 pt-1">
                {[
                  { tag: "moderation", label: "审核（群消息违规检测）" },
                  { tag: "vision", label: "视觉（图片/贴纸审核）" },
                ].map(({ tag, label: tl }) => {
                  const list = caps
                    .split(",")
                    .map((x) => x.trim())
                    .filter(Boolean);
                  const on = list.includes(tag);
                  return (
                    <label
                      key={tag}
                      className={`inline-flex cursor-pointer items-center gap-1.5 rounded-full border px-3 py-1 text-xs ${
                        on
                          ? "border-[var(--accent)] bg-[var(--accent-soft)] text-[var(--accent)]"
                          : "border-[var(--border)] bg-[var(--surface)] text-[var(--text-muted)]"
                      }`}
                    >
                      <input
                        type="checkbox"
                        className="hidden"
                        checked={on}
                        onChange={() => {
                          const next = on
                            ? list.filter((x) => x !== tag)
                            : [...list, tag];
                          setCaps(next.join(", "));
                        }}
                      />
                      {on ? "✓ " : ""}
                      {tl}
                    </label>
                  );
                })}
              </div>
              <span className="text-[11px] leading-snug text-[var(--text-subtle)]">
                用于给"不同场景"匹配模型。审核就选 "审核"。
              </span>
            </div>
          </div>
          <div className="flex flex-col gap-1">
            <label className="flex items-center gap-2 text-sm">
              <Switch checked={supportsJSON} onCheckedChange={setSupportsJSON} />
              支持强制 JSON 输出
            </label>
            <span className="text-[11px] leading-snug text-[var(--text-subtle)]">
              审核需要模型严格回 JSON。现代模型基本都支持，默认开启。
            </span>
          </div>
          <div className="flex flex-col gap-1">
            <label className="flex items-center gap-2 text-sm">
              <Switch checked={supportsTools} onCheckedChange={setSupportsTools} />
              支持 Tool calling（函数调用）
            </label>
            <span className="text-[11px] leading-snug text-[var(--text-subtle)]">
              让模型调用外部工具的能力。审核用不上，默认关闭。
            </span>
          </div>
          <div className="md:col-span-2 flex flex-col gap-3">
            <label className="flex items-center gap-2 text-sm">
              <Switch
                checked={probeEnabled}
                onCheckedChange={setProbeEnabled}
              />
              <span>启用自动探活</span>
              <span className="text-[11px] leading-snug text-[var(--text-subtle)]">
                关闭后本模型永不自动探活（仍可手动 ▷ test）。适合担心 rate
                limit 或付费按次的模型。
              </span>
            </label>

            <label className="flex flex-col gap-1 text-sm">
              <span>探活间隔（秒）</span>
              <Input
                type="number"
                min={0}
                value={probeInterval}
                onChange={(e) => setProbeInterval(Number(e.target.value) || 0)}
              />
              <span className="text-[11px] leading-snug text-[var(--text-subtle)]">
                0 = 跟随全局设置（默认 60 秒）。最小 30 秒，小于此值以 30
                秒为准。建议：便宜模型 60-120 秒；贵模型或有频率限制的
                300-600 秒。
              </span>
            </label>
          </div>
        </div>
      )}

      <div className="mt-4 flex items-center justify-between">
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={enabled} onCheckedChange={setEnabled} />
          启用
        </label>
        <div className="flex gap-2">
          <Button type="button" variant="secondary" onClick={onCancel}>
            取消
          </Button>
          <Button onClick={submit} disabled={saving || !providerKey || !modelKey}>
            {saving ? "保存中…" : editing ? "保存修改" : "保存"}
          </Button>
        </div>
      </div>
    </div>
  );
}

function SettingsPanel({
  settings,
  onChanged,
  pushToast,
}: {
  settings: ProbeSettings;
  onChanged: () => Promise<void>;
  pushToast: ToastFn;
}) {
  const [probeEnabled, setProbeEnabled] = useState(settings.probe_enabled);
  const [autoDegrade, setAutoDegrade] = useState(settings.auto_degrade);
  const [interval, setInterval] = useState(settings.probe_interval_seconds || 120);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setProbeEnabled(settings.probe_enabled);
    setAutoDegrade(settings.auto_degrade);
    setInterval(settings.probe_interval_seconds || 120);
  }, [settings]);

  async function save() {
    setSaving(true);
    try {
      const current = await apiFetch<{ config: Record<string, unknown> }>(
        "/api/admin/global-config",
      );
      const currentConfig = asRecord(current.config);
      const currentAI = asRecord(currentConfig.ai);
      const nextInterval = Math.max(30, Number(interval) || 120);
      const payload = await apiFetch<{ config: Record<string, unknown> }>(
        "/api/admin/global-config",
        {
          method: "PUT",
          body: JSON.stringify({
            ...currentConfig,
            ai: {
              ...currentAI,
              probe_enabled: probeEnabled,
              auto_degrade: autoDegrade,
              probe_interval_seconds: nextInterval,
            },
          }),
        },
      );
      const next = asRecord(asRecord(payload.config).ai);
      setProbeEnabled(Boolean(next.probe_enabled ?? false));
      setAutoDegrade(Boolean(next.auto_degrade ?? true));
      setInterval(Number(next.probe_interval_seconds ?? 120) || 120);
      pushToast("探活设置已保存", "success");
      await onChanged();
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "保存失败", "error");
    } finally {
      setSaving(false);
    }
  }

  async function runNow() {
    try {
      await apiFetch("/api/admin/llm/reload", { method: "POST" });
      pushToast("模型注册表已刷新；探活任务会按主节拍尽快执行", "success");
      await onChanged();
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "执行失败", "error");
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>探活设置</CardTitle>
      </CardHeader>
      <CardBody>
        <div className="grid gap-4 md:grid-cols-2">
          <div className="flex flex-col gap-3">
            <label className="flex items-center gap-2 text-sm">
              <Switch checked={probeEnabled} onCheckedChange={setProbeEnabled} />
              <span>启用全局探活</span>
            </label>
            <span className="text-[11px] leading-snug text-[var(--text-subtle)]">
              定期探测所有已启用且允许自动探活的模型，更新健康状态。
            </span>
          </div>

          <div className="flex flex-col gap-3">
            <label className="flex items-center gap-2 text-sm">
              <Switch checked={autoDegrade} onCheckedChange={setAutoDegrade} />
              <span>自动降级不健康模型</span>
            </label>
            <span className="text-[11px] leading-snug text-[var(--text-subtle)]">
              探活判定为不健康时，Resolver 会自动跳过该模型；各群仍可单独覆盖这个策略。
            </span>
          </div>

          <label className="flex flex-col gap-1 text-sm md:col-span-2">
            <span>默认探活间隔（秒）</span>
            <Input
              type="number"
              min={30}
              value={interval}
              onChange={(e) => setInterval(Number(e.target.value) || 120)}
            />
            <span className="text-[11px] leading-snug text-[var(--text-subtle)]">
              作为所有模型的默认间隔；单个模型填 0 时跟随这里。建议 120-300 秒。
            </span>
          </label>
        </div>

        <div className="mt-4 flex flex-wrap gap-2">
          <Button onClick={save} disabled={saving}>
            {saving ? "保存中…" : "保存设置"}
          </Button>
          <Button type="button" variant="secondary" onClick={runNow}>
            立即跑一轮
          </Button>
        </div>
      </CardBody>
    </Card>
  );
}

function StatsPanel({ models, stats }: { models: Model[]; stats: Stats[] }) {
  const modelById = useMemo(() => {
    const m = new Map<number, Model>();
    models.forEach((mm) => m.set(mm.id, mm));
    return m;
  }, [models]);

  return (
    <Card>
      <CardHeader>
        <CardTitle>健康快照</CardTitle>
      </CardHeader>
      <CardBody>
        <Table>
          <TableHead>
            <TableRow>
              <TableHeaderCell>Ref</TableHeaderCell>
              <TableHeaderCell>状态</TableHeaderCell>
              <TableHeaderCell>p50 / p95</TableHeaderCell>
              <TableHeaderCell>1h 成/败</TableHeaderCell>
              <TableHeaderCell>24h 成/败</TableHeaderCell>
              <TableHeaderCell>最后探活</TableHeaderCell>
              <TableHeaderCell>最后 OK</TableHeaderCell>
              <TableHeaderCell>最近错误</TableHeaderCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {stats.length === 0 && (
              <TableRow>
                <TableCell colSpan={8}>
                  <div className="py-6 text-center text-sm text-[var(--text-muted)]">
                    暂无数据。请切到上方「探活设置」页签，打开全局探活后等待首次探测。
                  </div>
                </TableCell>
              </TableRow>
            )}
            {stats.map((s) => {
              const m = modelById.get(s.model_id);
              return (
                <TableRow key={s.model_id}>
                  <TableCell className="font-mono text-xs">
                    {m?.ref ?? `#${s.model_id}`}
                  </TableCell>
                  <TableCell>
                    <Badge tone={s.healthy ? "success" : "danger"}>
                      {s.healthy ? "OK" : "FAIL"}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    {s.latency_p50_ms ?? "—"} / {s.latency_p95_ms ?? "—"} ms
                  </TableCell>
                  <TableCell>
                    {s.success_1h} / {s.fail_1h}
                  </TableCell>
                  <TableCell>
                    {s.success_24h} / {s.fail_24h}
                  </TableCell>
                  <TableCell>{fmtTime(s.last_check_at)}</TableCell>
                  <TableCell>{fmtTime(s.last_ok_at)}</TableCell>
                  <TableCell
                    className="max-w-[240px] truncate text-xs text-[var(--danger)]"
                    title={s.last_error}
                  >
                    {s.last_error || "—"}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </CardBody>
    </Card>
  );
}
