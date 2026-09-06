"use client";

import { useEffect, useState } from "react";
import { AdminShell } from "@/components/admin-shell";
import {
  Card,
  CardBody,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Textarea } from "@/components/ui/textarea";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { apiFetch } from "@/lib/api";
import {
  GLOBAL_CONFIG_CONFLICT_MESSAGE,
  fetchGlobalConfig,
  isGlobalConfigConflict,
  saveGlobalConfigSection,
} from "@/lib/global-config";
import { useToast } from "@/components/providers";
import { Copy, Eye, Play, Save } from "lucide-react";

type AIConfigShape = {
  custom_rules?: string;
  message_rules?: string;
  bio_rules?: string;
};

type ProviderPayload = {
  providers?: Array<{
    name?: string;
    label?: string;
    models?: Array<{
      key?: string;
      label?: string;
    }>;
  }>;
};

type ModelOption = {
  ref: string;
  label: string;
};

function normalizeRuleValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

export default function PromptEditorPage() {
  const { pushToast } = useToast();
  const [messageRules, setMessageRules] = useState("");
  const [bioRules, setBioRules] = useState("");
  const [originalMessage, setOriginalMessage] = useState("");
  const [originalBio, setOriginalBio] = useState("");

  const [testScene, setTestScene] = useState<"message" | "bio">("message");
  const [testText, setTestText] = useState("");
  const [testModel, setTestModel] = useState("");
  const [useDraftRules, setUseDraftRules] = useState(true);
  const [models, setModels] = useState<ModelOption[]>([]);

  const [result, setResult] = useState("");
  const [previewOpen, setPreviewOpen] = useState(false);
  const [previewText, setPreviewText] = useState("");

  const [savingTarget, setSavingTarget] = useState<"message" | "bio" | null>(
    null,
  );
  const [testing, setTesting] = useState(false);
  const [previewing, setPreviewing] = useState(false);

  useEffect(() => {
    let active = true;
    Promise.all([
      fetchGlobalConfig(),
      apiFetch<ProviderPayload>("/api/admin/ai-providers"),
    ])
      .then(([configPayload, providerPayload]) => {
        if (!active) {
          return;
        }

        const nextConfig = configPayload.config ?? {};
        const aiConfig = (nextConfig.ai ?? {}) as AIConfigShape;
        const fallbackRules = normalizeRuleValue(aiConfig.custom_rules);
        const nextMessage = normalizeRuleValue(aiConfig.message_rules) || fallbackRules;
        const nextBio = normalizeRuleValue(aiConfig.bio_rules) || fallbackRules;

        setMessageRules(nextMessage);
        setBioRules(nextBio);
        setOriginalMessage(nextMessage);
        setOriginalBio(nextBio);

        const nextModels =
          providerPayload.providers?.flatMap((provider) =>
            (provider.models ?? []).map((model) => {
              const providerKey = `${provider.name ?? ""}`.trim();
              const modelKey = `${model.key ?? ""}`.trim();
              return {
                ref: `${providerKey}:${modelKey}`,
                label: `${provider.label || providerKey} · ${model.label || modelKey}`,
              };
            }),
          ) ?? [];
        setModels(
          nextModels.filter(
            (item) =>
              item.ref.includes(":") &&
              !item.ref.startsWith(":") &&
              !item.ref.endsWith(":"),
          ),
        );
      })
      .catch((error) => {
        if (!active) {
          return;
        }
        pushToast(
          error instanceof Error ? error.message : "加载 Prompt 编辑器失败",
          "error",
        );
      });

    return () => {
      active = false;
    };
  }, [pushToast]);

  const messageDirty = messageRules !== originalMessage;
  const bioDirty = bioRules !== originalBio;

  function switchTestScene(scene: "message" | "bio") {
    setTestScene(scene);
    setPreviewOpen(false);
    setPreviewText("");
  }

  async function saveRules(target: "message" | "bio") {
    setSavingTarget(target);
    try {
      const nextAI: Record<string, unknown> =
        target === "message"
          ? { message_rules: messageRules }
          : { bio_rules: bioRules };
      await saveGlobalConfigSection("prompt", { ai: nextAI });
      if (target === "message") {
        setOriginalMessage(messageRules);
      } else {
        setOriginalBio(bioRules);
      }
      pushToast("规则已保存", "success");
    } catch (error) {
      pushToast(
        isGlobalConfigConflict(error)
          ? GLOBAL_CONFIG_CONFLICT_MESSAGE
          : error instanceof Error
            ? error.message
            : "保存规则失败",
        "error",
      );
    } finally {
      setSavingTarget(null);
    }
  }

  async function test() {
    if (!testText.trim()) {
      pushToast("测试文本不能为空", "error");
      return;
    }
    setTesting(true);
    try {
      const body: Record<string, unknown> = {
        text: testText,
        scene: testScene,
      };
      if (testModel) {
        body.model_ref = testModel;
      }
      if (useDraftRules) {
        body.rules_override = testScene === "bio" ? bioRules : messageRules;
      }
      const response = await apiFetch<{ result: unknown }>("/api/admin/ai-test", {
        method: "POST",
        body: JSON.stringify(body),
      });
      setResult(JSON.stringify(response.result, null, 2));
    } catch (error) {
      pushToast(
        error instanceof Error ? error.message : "AI 测试失败",
        "error",
      );
    } finally {
      setTesting(false);
    }
  }

  async function preview() {
    setPreviewing(true);
    try {
      const body: Record<string, unknown> = {
        scene: testScene,
        sample_text: testText || undefined,
      };
      if (useDraftRules) {
        body.rules_override = testScene === "bio" ? bioRules : messageRules;
      }
      const response = await apiFetch<{ prompt: string }>(
        "/api/admin/ai-prompt-preview",
        {
          method: "POST",
          body: JSON.stringify(body),
        },
      );
      setPreviewText(response.prompt);
      setPreviewOpen(true);
    } catch (error) {
      pushToast(
        error instanceof Error ? error.message : "加载 Prompt 预览失败",
        "error",
      );
    } finally {
      setPreviewing(false);
    }
  }

  async function copyPreview() {
    if (!previewText) {
      return;
    }
    try {
      await navigator.clipboard.writeText(previewText);
      pushToast("完整 Prompt 已复制", "success");
    } catch (error) {
      pushToast(
        error instanceof Error ? error.message : "复制 Prompt 失败",
        "error",
      );
    }
  }

  return (
    <AdminShell
      title="Prompt 编辑器"
      subtitle="将发言审核和简介审核拆成两套独立规则，并支持实时测试与完整 Prompt 预览"
    >
      <div className="grid gap-4 xl:grid-cols-[minmax(0,1.15fr)_minmax(360px,0.85fr)]">
        <div className="space-y-4">
          <Card>
            <CardHeader className="flex-row items-start justify-between gap-3">
              <div>
                <CardTitle>发言规则</CardTitle>
                <CardDescription>只作用于群聊消息审核。</CardDescription>
              </div>
              <div className="flex items-center gap-2">
                {messageDirty && (
                  <span className="rounded-full bg-[var(--surface-2)] px-2 py-1 text-[11px] text-[var(--warning)]">
                    未保存
                  </span>
                )}
                <Button
                  onClick={() => saveRules("message")}
                  disabled={savingTarget !== null || !messageDirty}
                  size="sm"
                >
                  <Save className="h-3.5 w-3.5" />
                  {savingTarget === "message" ? "保存中" : "保存"}
                </Button>
              </div>
            </CardHeader>
            <CardBody>
              <Textarea
                value={messageRules}
                onChange={(event) => setMessageRules(event.target.value)}
                className="min-h-[240px] font-mono text-xs leading-relaxed"
                placeholder="例如：本群禁止讨论币圈、刷单、招聘；本群禁止所有外站链接；娱乐内容从宽处理。"
              />
            </CardBody>
          </Card>

          <Card>
            <CardHeader className="flex-row items-start justify-between gap-3">
              <div>
                <CardTitle>简介规则</CardTitle>
                <CardDescription>只作用于 Telegram 用户资料简介审核。</CardDescription>
              </div>
              <div className="flex items-center gap-2">
                {bioDirty && (
                  <span className="rounded-full bg-[var(--surface-2)] px-2 py-1 text-[11px] text-[var(--warning)]">
                    未保存
                  </span>
                )}
                <Button
                  onClick={() => saveRules("bio")}
                  disabled={savingTarget !== null || !bioDirty}
                  size="sm"
                >
                  <Save className="h-3.5 w-3.5" />
                  {savingTarget === "bio" ? "保存中" : "保存"}
                </Button>
              </div>
            </CardHeader>
            <CardBody>
              <Textarea
                value={bioRules}
                onChange={(event) => setBioRules(event.target.value)}
                className="min-h-[240px] font-mono text-xs leading-relaxed"
                placeholder="例如：简介里出现 TRC20/USDT/收款码一律视为引流；简介带博彩相关词从严判定 ad。"
              />
            </CardBody>
          </Card>
        </div>

        <Card className="h-fit">
          <CardHeader>
            <CardTitle>实时测试</CardTitle>
            <CardDescription>支持切换场景、临时换模型，以及预览完整 Prompt。</CardDescription>
          </CardHeader>
          <CardBody className="space-y-4">
            <div className="space-y-2">
              <div className="text-xs font-medium text-[var(--text-muted)]">场景</div>
              <div className="flex items-center gap-3 text-sm">
                <label className="flex items-center gap-2">
                  <input
                    type="radio"
                    name="test-scene"
                    checked={testScene === "message"}
                    onChange={() => switchTestScene("message")}
                  />
                  发言
                </label>
                <label className="flex items-center gap-2">
                  <input
                    type="radio"
                    name="test-scene"
                    checked={testScene === "bio"}
                    onChange={() => switchTestScene("bio")}
                  />
                  简介
                </label>
              </div>
            </div>

            <div className="space-y-2">
              <div className="text-xs font-medium text-[var(--text-muted)]">模型</div>
              <Select
                value={testModel}
                onChange={(event) => setTestModel(event.target.value)}
              >
                <option value="">使用配置默认模型</option>
                {models.map((model) => (
                  <option key={model.ref} value={model.ref}>
                    {model.label}
                  </option>
                ))}
              </Select>
            </div>

            <div className="flex items-center justify-between rounded-lg border border-[var(--border)] bg-[var(--surface-2)] px-3 py-2">
              <div>
                <div className="text-sm font-medium">使用草稿规则</div>
                <div className="text-xs text-[var(--text-muted)]">
                  打开后直接用当前文本框内容测试，不必先保存。
                </div>
              </div>
              <Switch checked={useDraftRules} onChange={setUseDraftRules} />
            </div>

            <div className="space-y-2">
              <div className="text-xs font-medium text-[var(--text-muted)]">测试文本</div>
              <Textarea
                value={testText}
                onChange={(event) => setTestText(event.target.value)}
                className="min-h-[180px] font-mono text-xs leading-relaxed"
                placeholder={
                  testScene === "bio"
                    ? "输入一条要模拟审核的用户简介"
                    : "输入一条要模拟审核的群聊消息"
                }
              />
            </div>

            <div className="flex flex-wrap gap-2">
              <Button onClick={test} disabled={testing}>
                <Play className="h-3.5 w-3.5" />
                {testing ? "调用中" : "调用 AI 测试"}
              </Button>
              <Button onClick={preview} disabled={previewing} variant="outline">
                <Eye className="h-3.5 w-3.5" />
                {previewing ? "生成中" : "预览完整 Prompt"}
              </Button>
            </div>

            <div className="space-y-2">
              <div className="text-xs font-medium text-[var(--text-muted)]">测试结果</div>
              <pre className="min-h-[220px] overflow-x-auto rounded-lg border border-[var(--border)] bg-[var(--surface-2)] p-3 text-xs leading-relaxed">
                {result || "结果将显示在这里"}
              </pre>
            </div>

            {previewOpen && (
              <div className="space-y-2">
                <div className="flex items-center justify-between">
                  <div className="text-xs font-medium text-[var(--text-muted)]">
                    完整 Prompt
                  </div>
                  <div className="flex gap-2">
                    <Button onClick={copyPreview} size="sm" variant="outline">
                      <Copy className="h-3.5 w-3.5" />
                      复制全文
                    </Button>
                    <Button
                      onClick={() => setPreviewOpen(false)}
                      size="sm"
                      variant="ghost"
                    >
                      收起
                    </Button>
                  </div>
                </div>
                <pre className="max-h-[360px] overflow-auto rounded-lg border border-[var(--border)] bg-[var(--surface-2)] p-3 text-xs leading-relaxed whitespace-pre-wrap break-words">
                  {previewText}
                </pre>
              </div>
            )}
          </CardBody>
        </Card>
      </div>
    </AdminShell>
  );
}
