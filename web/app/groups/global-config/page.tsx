"use client";

import type { Dispatch, SetStateAction } from "react";
import { useEffect, useMemo, useRef, useState } from "react";
import { AdminShell } from "@/components/admin-shell";
import {
  ArrayTextarea,
  FeedbackSection,
  fieldDescriptors,
  KeywordReplySection,
  type LLMModelOption,
  ModelRefListControl,
  ModelRefSelect,
} from "@/components/group-config-editor";
import { Card, CardBody } from "@/components/ui/card";
import { Tabs } from "@/components/ui/tabs";
import { Switch } from "@/components/ui/switch";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Select } from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { apiFetch } from "@/lib/api";
import {
  GLOBAL_CONFIG_CONFLICT_MESSAGE,
  fetchGlobalConfig,
  isGlobalConfigConflict,
  saveGlobalConfigDocument,
} from "@/lib/global-config";
import {
  cleanStaleAIModelRefs,
  getPathValue,
  hasPath,
  parseArrayValue,
  setPath,
} from "@/lib/policy";
import type { GuardPolicy, KeywordReplyRule } from "@/lib/types";
import { useToast } from "@/components/providers";
import { useDirtyGuard } from "@/components/dirty-guard";
import { Badge } from "@/components/ui/badge";
import { Save } from "lucide-react";

const tabs = [
  { value: "verify", label: "验证与资料" },
  { value: "filter", label: "消息过滤" },
  { value: "replies", label: "关键词回复" },
  { value: "warnings", label: "警告" },
  { value: "anti-spam", label: "反垃圾" },
  { value: "ai", label: "AI 审核" },
  { value: "feedback", label: "动作反馈" },
  { value: "logging", label: "运行日志" },
];

const globalScenes = [
  { value: "membership", label: "入群与资料", tabs: ["verify"] },
  { value: "content", label: "内容与处置", tabs: ["filter", "replies", "warnings"] },
  { value: "automation", label: "群运营", tabs: ["anti-spam"] },
  { value: "intelligence", label: "AI 与反馈", tabs: ["ai", "feedback"] },
  { value: "records", label: "记录与诊断", tabs: ["logging"] },
] as const;

function tabsForScene(scene: string) {
  const selected = globalScenes.find((item) => item.value === scene) ?? globalScenes[0];
  return selected.tabs
    .map((value) => tabs.find((tab) => tab.value === value))
    .filter((tab): tab is (typeof tabs)[number] => Boolean(tab));
}

function formatValue(value: unknown) {
  if (value === undefined) return "未设置";
  if (value === null) return "空值";
  if (typeof value === "boolean") return value ? "开启" : "关闭";
  if (typeof value === "number") return String(value);
  if (typeof value === "string") return value.trim() || "空字符串";
  if (Array.isArray(value)) return value.length ? `${value.length} 项` : "空列表";
  return "已配置";
}

export default function GlobalConfigPage() {
  const { pushToast } = useToast();
  const [activeScene, setActiveScene] = useState("membership");
  const [activeTab, setActiveTab] = useState("verify");
  const [draft, setDraft] = useState<Record<string, unknown>>({});
  const [initial, setInitial] = useState<Record<string, unknown>>({});
  const [version, setVersion] = useState<number | null>(null);
  const [saving, setSaving] = useState(false);
  const [llmModelOptions, setLLMModelOptions] = useState<LLMModelOption[]>([]);
  const [llmOptionsError, setLLMOptionsError] = useState<string | null>(null);
  const modelOptionsRef = useRef<LLMModelOption[]>([]);
  const configLoadedRef = useRef(false);

  useEffect(() => {
    fetchGlobalConfig()
      .then((p) => {
        const loadedConfig = p.config ?? {};
        const normalizedConfig = cleanStaleAIModelRefs(loadedConfig, modelOptionsRef.current);
        setDraft(normalizedConfig);
        setInitial(normalizedConfig);
        setVersion(typeof p.version === "number" ? p.version : null);
        configLoadedRef.current = true;
      })
      .catch((e) =>
        pushToast(e instanceof Error ? e.message : "加载失败", "error"),
      );
  }, [pushToast]);

  useEffect(() => {
    let alive = true;
    apiFetch<{
      models?: Array<{
        provider_key: string;
        model_key: string;
        label?: string;
      }>;
    }>("/api/admin/llm/models")
      .then((payload) => {
        if (!alive) {
          return;
        }
        const options = (payload.models ?? []).map((model) => {
          const value = `${model.provider_key}:${model.model_key}`;
          return {
            value,
            label: `${value} · ${model.label || model.model_key}`,
          };
        });
        modelOptionsRef.current = options;
        setLLMModelOptions(options);
        if (configLoadedRef.current) {
          setInitial((current) => cleanStaleAIModelRefs(current, options));
          setDraft((current) => cleanStaleAIModelRefs(current, options));
        }
        setLLMOptionsError(null);
      })
      .catch((error) => {
        if (!alive) {
          return;
        }
        setLLMModelOptions([]);
        setLLMOptionsError(
          error instanceof Error ? error.message : "加载模型列表失败",
        );
      });
    return () => {
      alive = false;
    };
  }, []);

  const isDirty = useMemo(
    () => JSON.stringify(initial) !== JSON.stringify(draft),
    [draft, initial],
  );
  useDirtyGuard(isDirty, "全局策略尚有未保存修改，确定离开吗？");

  async function save() {
    if (version == null) {
      pushToast("缺少配置版本，请刷新后再保存", "error");
      return;
    }
    setSaving(true);
    try {
      const nextDraft = cleanStaleAIModelRefs(draft, llmModelOptions);
      const p = await saveGlobalConfigDocument(version, nextDraft);
      setDraft(p.config ?? {});
      setInitial(p.config ?? {});
      setVersion(typeof p.version === "number" ? p.version : version);
      pushToast("已保存", "success");
    } catch (e) {
      pushToast(
        isGlobalConfigConflict(e)
          ? GLOBAL_CONFIG_CONFLICT_MESSAGE
          : e instanceof Error
            ? e.message
            : "保存失败",
        "error",
      );
    } finally {
      setSaving(false);
    }
  }

  function discard() {
    if (!isDirty || window.confirm("放弃当前全局策略草稿？未保存修改将被丢弃。")) {
      setDraft(structuredClone(initial) as Record<string, unknown>);
    }
  }

  const tabFields = fieldDescriptors.filter((f) => f.tab === activeTab);
  const keywordReplies = hasPath(draft, ["messages", "keyword_replies"])
    ? ((getPathValue(draft, [
        "messages",
        "keyword_replies",
      ]) as KeywordReplyRule[]) ?? [])
    : [];

  return (
    <AdminShell
      title="全局策略"
      subtitle="所有群的默认策略，群级页面可按字段覆盖或继承"
    >
      <section className="space-y-3 rounded-2xl border border-[var(--border)] bg-[var(--surface-2)]/60 p-3 md:p-4" aria-label="全局策略场景">
        <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
          <div>
            <p className="text-[10px] font-semibold uppercase tracking-[0.16em] text-[var(--accent)]">默认策略工作区</p>
            <p className="mt-1 text-xs text-[var(--text-muted)]">群级页面可逐字段覆盖；全局文档保存带版本冲突保护。</p>
          </div>
          <Badge tone={isDirty ? "warning" : "success"}>{isDirty ? "有未保存修改" : "已同步"}</Badge>
        </div>
        <Tabs
          tabs={globalScenes.map(({ value, label }) => ({ value, label }))}
          value={activeScene}
          onValueChange={(scene) => {
            setActiveScene(scene);
            const nextTab = tabsForScene(scene)[0]?.value;
            if (nextTab) setActiveTab(nextTab);
          }}
          className="w-full"
        />
        <Tabs tabs={tabsForScene(activeScene)} value={activeTab} onValueChange={setActiveTab} className="w-full border-0 bg-transparent p-0" />
      </section>

      <fieldset disabled={saving} className="min-w-0 border-0 p-0">
      <div className="space-y-3 pb-20">
        {tabFields.map((field) => {
          const value = getPathValue(draft as GuardPolicy, field.path);
          return (
            <Card key={field.path.join(".")}>
              <CardBody>
                <div className="mb-3">
                  <p className="text-sm font-medium">{field.label}</p>
                  {field.description && (
                    <p className="mt-0.5 text-xs text-[var(--text-muted)]">
                      {field.description}
                    </p>
                  )}
                  <p className="mt-2 text-[11px] text-[var(--text-muted)]">当前有效值：<strong className="font-medium text-[var(--text)]">{formatValue(value)}</strong></p>
                </div>
                {field.kind === "switch" && (
                  <Switch
                    checked={Boolean(value)}
                    onChange={(v) =>
                      updateField(setDraft, draft, field.path, v, field.kind)
                    }
                  />
                )}
                {field.kind === "text" && (
                  <Input
                    placeholder={field.placeholder}
                    value={
                      typeof value === "string" || value === null
                        ? (value ?? "")
                        : ""
                    }
                    onChange={(e) =>
                      updateField(
                        setDraft,
                        draft,
                        field.path,
                        e.target.value,
                        field.kind,
                      )
                    }
                  />
                )}
                {field.kind === "number" &&
                  (() => {
                    const numericValue =
                      typeof value === "number"
                        ? value
                        : value == null || Number.isNaN(Number(value))
                          ? ""
                          : Number(value);
                    return (
                      <Input
                        type="number"
                        value={numericValue}
                        onChange={(e) =>
                          updateField(
                            setDraft,
                            draft,
                            field.path,
                            e.target.value,
                            field.kind,
                          )
                        }
                      />
                    );
                  })()}
                {field.kind === "textarea" && (
                  <ArrayTextarea
                    value={Array.isArray(value) ? (value as string[]) : []}
                    onChange={(raw) =>
                      updateField(
                        setDraft,
                        draft,
                        field.path,
                        raw,
                        field.kind,
                      )
                    }
                    className="font-mono text-xs"
                  />
                )}
                {field.kind === "template" && (
                  <Textarea
                    placeholder={field.placeholder}
                    value={
                      typeof value === "string" || value === null
                        ? (value ?? "")
                        : ""
                    }
                    onChange={(e) =>
                      updateField(
                        setDraft,
                        draft,
                        field.path,
                        e.target.value,
                        field.kind,
                      )
                    }
                    className="min-h-[120px] font-mono text-xs"
                  />
                )}
                {field.kind === "select" && field.options && (
                  <Select
                    value={
                      typeof value === "string"
                        ? value
                        : field.options[0]?.value
                    }
                    options={field.options}
                    onChange={(e) =>
                      updateField(
                        setDraft,
                        draft,
                        field.path,
                        e.target.value,
                        field.kind,
                      )
                    }
                  />
                )}
                {field.kind === "model-ref" && (
                  <div className="space-y-2">
                    <ModelRefSelect
                      value={typeof value === "string" ? value : ""}
                      options={llmModelOptions}
                      placeholder="provider:model"
                      onChange={(nextValue) =>
                        updateField(
                          setDraft,
                          draft,
                          field.path,
                          nextValue,
                          field.kind,
                        )
                      }
                    />
                    {llmOptionsError && (
                      <p className="text-xs text-[var(--warning)]">
                        模型列表加载失败，已降级为文本输入：{llmOptionsError}
                      </p>
                    )}
                  </div>
                )}
                {field.kind === "model-ref-list" && (
                  <div className="space-y-2">
                    <ModelRefListControl
                      value={Array.isArray(value) ? (value as string[]) : []}
                      options={llmModelOptions}
                      onChange={(nextValue) =>
                        updateField(
                          setDraft,
                          draft,
                          field.path,
                          nextValue,
                          field.kind,
                        )
                      }
                    />
                    {llmOptionsError && (
                      <p className="text-xs text-[var(--warning)]">
                        模型列表加载失败，已降级为文本输入：{llmOptionsError}
                      </p>
                    )}
                  </div>
                )}
              </CardBody>
            </Card>
          );
        })}

        {activeTab === "feedback" && (
          <FeedbackSection
            value={
              ((getPathValue(draft, ["feedback"]) as
                | Record<string, unknown>
                | undefined) ?? {}) as Record<
                string,
                {
                  enabled: boolean;
                  template: string;
                  auto_delete_seconds: number;
                  reply_to_message: boolean;
                }
              >
            }
            onChange={(next) => {
              const cur = structuredClone(draft);
              setPath(cur, ["feedback"], next);
              setDraft(cur);
            }}
          />
        )}

        {activeTab === "replies" && (
          <KeywordReplySection
            rules={keywordReplies}
            onChange={(rules) => {
              const next = structuredClone(draft);
              setPath(next, ["messages", "keyword_replies"], rules);
              setDraft(next);
            }}
          />
        )}
      </div>
      </fieldset>

      <div className="glass-savebar fixed inset-x-3 bottom-4 z-20 flex items-center justify-between gap-3 rounded-2xl px-3 py-2.5 md:inset-x-auto md:bottom-6 md:left-[calc(17rem+2rem)] md:right-8 md:px-4">
        <div className="min-w-0">
          <p className="truncate text-sm font-semibold">{isDirty ? "全局策略草稿" : "全局策略已保存"}</p>
          <p className="hidden text-xs text-[var(--text-muted)] sm:block">{isDirty ? "切换场景不会丢失草稿；409 冲突会保留本地内容" : "保存整份文档，版本号用于并发保护"}</p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Button type="button" variant="ghost" size="sm" onClick={discard} disabled={!isDirty || saving}>放弃</Button>
          <Button onClick={save} disabled={!isDirty || saving} variant={isDirty ? "primary" : "secondary"} size="sm">
            <Save className="h-4 w-4" />
            {saving ? "保存中…" : isDirty ? "保存全局策略" : "已保存"}
          </Button>
        </div>
      </div>
    </AdminShell>
  );
}

function updateField(
  setDraft: Dispatch<SetStateAction<Record<string, unknown>>>,
  draft: Record<string, unknown>,
  path: string[],
  value: unknown,
  kind: string,
) {
  const next = structuredClone(draft);
  let normalized = value;

  if (kind === "number") {
    if (String(value).trim() === "") {
      normalized = null;
    } else {
      const parsed = Number(value);
      normalized = Number.isFinite(parsed) ? parsed : 0;
    }
  }
  if (kind === "textarea") {
    normalized = parseArrayValue(String(value));
  }
  if (kind === "model-ref") {
    normalized = String(value);
  }
  if (kind === "model-ref-list") {
    normalized = Array.isArray(value)
      ? value.map((item) => String(item).trim()).filter(Boolean)
      : parseArrayValue(String(value));
  }

  setPath(next, path, normalized);
  setDraft(next);
}
