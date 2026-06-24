"use client";

import type { Dispatch, SetStateAction } from "react";
import { useEffect, useMemo, useState } from "react";
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
  cleanStaleAIModelRefs,
  formatArrayValue,
  getPathValue,
  hasPath,
  parseArrayValue,
  setPath,
} from "@/lib/policy";
import type { GuardPolicy, KeywordReplyRule } from "@/lib/types";
import { useToast } from "@/components/providers";
import { Save } from "lucide-react";

const tabs = [
  { value: "verify", label: "验证" },
  { value: "filter", label: "过滤" },
  { value: "replies", label: "关键词回复" },
  { value: "warnings", label: "警告" },
  { value: "anti-spam", label: "反垃圾" },
  { value: "ai", label: "AI" },
  { value: "feedback", label: "动作反馈" },
  { value: "logging", label: "日志" },
];

export default function GlobalConfigPage() {
  const { pushToast } = useToast();
  const [activeTab, setActiveTab] = useState("verify");
  const [draft, setDraft] = useState<Record<string, unknown>>({});
  const [initial, setInitial] = useState<Record<string, unknown>>({});
  const [saving, setSaving] = useState(false);
  const [llmModelOptions, setLLMModelOptions] = useState<LLMModelOption[]>([]);
  const [llmOptionsError, setLLMOptionsError] = useState<string | null>(null);

  useEffect(() => {
    apiFetch<{ config: Record<string, unknown> }>("/api/admin/global-config")
      .then((p) => {
        setDraft(p.config ?? {});
        setInitial(p.config ?? {});
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
        setLLMModelOptions(options);
        setDraft((current) => cleanStaleAIModelRefs(current, options));
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

  async function save() {
    setSaving(true);
    try {
      const nextDraft = cleanStaleAIModelRefs(draft, llmModelOptions);
      const p = await apiFetch<{ config: Record<string, unknown> }>(
        "/api/admin/global-config",
        {
          method: "PUT",
          body: JSON.stringify(nextDraft),
        },
      );
      setDraft(p.config ?? {});
      setInitial(p.config ?? {});
      pushToast("已保存", "success");
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "保存失败", "error");
    } finally {
      setSaving(false);
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
      <Tabs tabs={tabs} value={activeTab} onValueChange={setActiveTab} />

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

      <div className="fixed bottom-6 right-6 z-20">
        <Button
          onClick={save}
          disabled={!isDirty || saving}
          variant={isDirty ? "primary" : "secondary"}
          size="lg"
          className="shadow-lg"
        >
          <Save className="h-4 w-4" />
          {saving ? "保存中…" : isDirty ? "保存全局配置" : "已保存"}
        </Button>
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
