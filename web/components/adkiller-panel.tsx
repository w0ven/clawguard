"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Select } from "@/components/ui/select";
import { apiFetch } from "@/lib/api";
import {
  GLOBAL_CONFIG_CONFLICT_MESSAGE,
  fetchGlobalConfig,
  isGlobalConfigConflict,
  saveGlobalConfigSection,
} from "@/lib/global-config";
import { useToast } from "@/components/providers";
import { useDirtyGuard } from "@/components/dirty-guard";
import type { Group } from "@/lib/types";

type ToastFn = (message: string, tone?: "success" | "error") => void;

type ScoreBand = {
  min_score: number;
  max_score: number;
  action: string;
};

type AdKillerConfig = {
  enabled: boolean;
  min_score: number;
  timeout_ms: number;
  on_failure: string;
  enabled_chat_ids: number[];
  score_bands: ScoreBand[];
};

const ACTION_OPTIONS = [
  { value: "none", label: "不处罚，交给后续模型" },
  { value: "warn", label: "警告" },
  { value: "mute", label: "禁言" },
  { value: "kick", label: "踢出" },
  { value: "ban", label: "封禁" },
  { value: "delete", label: "只删消息" },
];

const DEFAULT_BANDS: ScoreBand[] = [
  { min_score: 0, max_score: 80, action: "none" },
  { min_score: 81, max_score: 90, action: "warn" },
  { min_score: 91, max_score: 100, action: "kick" },
];

function asRecord(value: unknown): Record<string, unknown> {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as Record<string, unknown>;
  }
  return {};
}

function asNumber(value: unknown, fallback: number): number {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : fallback;
}

function parseConfig(raw: unknown): AdKillerConfig {
  const data = asRecord(raw);
  const bands = Array.isArray(data.score_bands)
    ? data.score_bands
        .map((item) => asRecord(item))
        .map((item) => ({
          min_score: asNumber(item.min_score, 0),
          max_score: asNumber(item.max_score, 100),
          action: String(item.action || "none"),
        }))
    : DEFAULT_BANDS;
  const chatIDs = Array.isArray(data.enabled_chat_ids)
    ? data.enabled_chat_ids
        .map((item) => Number(item))
        .filter((item) => Number.isFinite(item) && item !== 0)
    : [];
  return {
    enabled: Boolean(data.enabled),
    min_score: asNumber(data.min_score, 81),
    timeout_ms: asNumber(data.timeout_ms, 1500),
    on_failure: String(data.on_failure || "fallback"),
    enabled_chat_ids: chatIDs,
    score_bands: bands.length > 0 ? bands : DEFAULT_BANDS,
  };
}

export function AdKillerPanel({ pushToast }: { pushToast?: ToastFn }) {
  const toast = useToast();
  const notify = pushToast ?? toast.pushToast;
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [groups, setGroups] = useState<Group[]>([]);
  const [config, setConfig] = useState<AdKillerConfig>(parseConfig({}));
  const [initialConfig, setInitialConfig] = useState<AdKillerConfig>(parseConfig({}));
  const [keySet, setKeySet] = useState(false);
  const [keyHint, setKeyHint] = useState("");
  const [keyDraft, setKeyDraft] = useState("");
  const [keySaving, setKeySaving] = useState(false);
  const [testText, setTestText] = useState("");
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState("");
  const configRef = useRef(config);
  configRef.current = config;

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [secret, global, groupPayload] = await Promise.all([
        apiFetch<{ api_key_set?: boolean; api_key_hint?: string }>(
          "/api/admin/adkiller",
        ),
        fetchGlobalConfig(),
        apiFetch<{ groups?: Group[] }>("/api/admin/groups"),
      ]);
      setKeySet(Boolean(secret.api_key_set));
      setKeyHint(secret.api_key_hint ?? "");
      const nextConfig = parseConfig(asRecord(asRecord(global.config).ai).adkiller);
      setConfig(nextConfig);
      setInitialConfig(nextConfig);
      setGroups(groupPayload.groups ?? []);
    } catch (error) {
      notify(error instanceof Error ? error.message : "加载 AdKiller 失败", "error");
    } finally {
      setLoading(false);
    }
  }, [notify]);

  useEffect(() => {
    void load();
  }, [load]);

  const selectedCount = config.enabled_chat_ids.length;
  const configDirty = JSON.stringify(config) !== JSON.stringify(initialConfig);
  const dirty = configDirty || keyDraft.trim() !== "";
  useDirtyGuard(
    dirty,
    "AdKiller 草稿尚未保存，确定离开吗？",
    "adkiller",
  );
  const groupTitle = useMemo(() => {
    const map = new Map(groups.map((group) => [group.chat_id, group.title]));
    return (chatID: number) => map.get(chatID) || String(chatID);
  }, [groups]);

  function updateBand(index: number, patch: Partial<ScoreBand>) {
    setConfig((current) => ({
      ...current,
      score_bands: current.score_bands.map((band, i) =>
        i === index ? { ...band, ...patch } : band,
      ),
    }));
  }

  function addBand() {
    setConfig((current) => ({
      ...current,
      score_bands: [
        ...current.score_bands,
        { min_score: 0, max_score: 100, action: "none" },
      ],
    }));
  }

  function removeBand(index: number) {
    setConfig((current) => ({
      ...current,
      score_bands:
        current.score_bands.length <= 1
          ? current.score_bands
          : current.score_bands.filter((_, i) => i !== index),
    }));
  }

  function toggleChat(chatID: number, enabled: boolean) {
    setConfig((current) => {
      const next = new Set(current.enabled_chat_ids);
      if (enabled) {
        next.add(chatID);
      } else {
        next.delete(chatID);
      }
      return { ...current, enabled_chat_ids: Array.from(next) };
    });
  }

  async function saveSettings() {
    const submittedConfig = config;
    setSaving(true);
    try {
      const payload = await saveGlobalConfigSection("adkiller", {
        ai: {
          adkiller: {
            enabled: config.enabled,
            timeout_ms: config.timeout_ms,
            on_failure: config.on_failure,
            enabled_chat_ids: config.enabled_chat_ids,
            score_bands: config.score_bands,
          },
        },
      });
      const nextConfig = parseConfig(asRecord(asRecord(payload.config).ai).adkiller);
      const latestDraft = configRef.current;
      const hasNewerDraft =
        JSON.stringify(latestDraft) !== JSON.stringify(submittedConfig);
      // The response represents the snapshot sent above. If the user edited
      // while it was in flight, keep that newer draft and mark it dirty against
      // the acknowledged server snapshot instead of overwriting it.
      setInitialConfig(nextConfig);
      setConfig(hasNewerDraft ? latestDraft : nextConfig);
      notify("AdKiller 设置已保存", "success");
    } catch (error) {
      notify(
        isGlobalConfigConflict(error)
          ? GLOBAL_CONFIG_CONFLICT_MESSAGE
          : error instanceof Error
            ? error.message
            : "保存 AdKiller 失败",
        "error",
      );
    } finally {
      setSaving(false);
    }
  }

  async function saveKey() {
    const apiKey = keyDraft.trim();
    if (!apiKey) {
      notify("请输入新的 AdKiller API Key", "error");
      return;
    }
    setKeySaving(true);
    try {
      const payload = await apiFetch<{
        api_key_set?: boolean;
        api_key_hint?: string;
      }>("/api/admin/adkiller", {
        method: "PUT",
        body: JSON.stringify({ api_key: apiKey }),
      });
      setKeySet(Boolean(payload.api_key_set));
      setKeyHint(payload.api_key_hint ?? "");
      setKeyDraft("");
      notify("AdKiller 密钥已保存", "success");
    } catch (error) {
      notify(
        error instanceof Error ? error.message : "保存 AdKiller 密钥失败",
        "error",
      );
    } finally {
      setKeySaving(false);
    }
  }

  async function clearKey() {
    setKeySaving(true);
    try {
      const payload = await apiFetch<{
        api_key_set?: boolean;
        api_key_hint?: string;
      }>("/api/admin/adkiller", {
        method: "PUT",
        body: JSON.stringify({ clear: true }),
      });
      setKeySet(Boolean(payload.api_key_set));
      setKeyHint(payload.api_key_hint ?? "");
      setKeyDraft("");
      notify("AdKiller 密钥已清除", "success");
    } catch (error) {
      notify(
        error instanceof Error ? error.message : "清除 AdKiller 密钥失败",
        "error",
      );
    } finally {
      setKeySaving(false);
    }
  }

  async function testAdKiller() {
    const text = testText.trim();
    if (!text) {
      notify("请输入要测试的文本", "error");
      return;
    }
    setTesting(true);
    setTestResult("");
    try {
      const payload = await apiFetch<Record<string, unknown>>(
        "/api/admin/adkiller/test",
        {
          method: "POST",
          body: JSON.stringify({ text }),
        },
      );
      setTestResult(JSON.stringify(payload, null, 2));
      if (payload.ok) {
        notify(
          typeof payload.outcome === "string"
            ? payload.outcome
            : "AdKiller 测试完成",
          "success",
        );
      } else {
        notify(
          typeof payload.error === "string" ? payload.error : "AdKiller 测试失败",
          "error",
        );
      }
    } catch (error) {
      const message =
        error instanceof Error ? error.message : "AdKiller 测试失败";
      setTestResult(message);
      notify(message, "error");
    } finally {
      setTesting(false);
    }
  }

  if (loading) {
    return (
      <Card>
        <CardBody>
          <p className="text-sm text-[var(--text-muted)]">正在加载 AdKiller…</p>
        </CardBody>
      </Card>
    );
  }

  return (
    <div className="space-y-4">
      <fieldset
        aria-disabled={saving}
        aria-busy={saving}
        className="min-w-0 space-y-4 border-0 p-0"
      >
      <Card>
        <CardHeader>
          <CardTitle>AdKiller 前置广告检测</CardTitle>
        </CardHeader>
        <CardBody className="space-y-4">
          <p className="text-xs text-[var(--text-muted)]">
            独立广告评分，不读原来的提示词。命中分段动作后直接处理；判断不了或接口故障再交给后续模型。只对勾选的群生效。
          </p>
          <label className="flex items-center gap-2 text-sm">
            <Switch
              aria-disabled={saving}
              checked={config.enabled}
              onCheckedChange={(enabled) =>
                setConfig((current) => ({ ...current, enabled }))
              }
            />
            启用 AdKiller
          </label>
          <div className="grid gap-3 md:grid-cols-2">
            <label className="flex flex-col gap-1 text-sm">
              <span>超时（ms）</span>
              <Input
                type="number"
                min={100}
                max={10000}
                aria-disabled={saving}
                value={config.timeout_ms}
                onChange={(event) =>
                  setConfig((current) => ({
                    ...current,
                    timeout_ms: Number(event.target.value) || 1500,
                  }))
                }
              />
            </label>
            <label className="flex flex-col gap-1 text-sm">
              <span>失败策略</span>
              <Select
                aria-disabled={saving}
                value={config.on_failure}
                onChange={(event) =>
                  setConfig((current) => ({
                    ...current,
                    on_failure: event.target.value,
                  }))
                }
                options={[
                  { label: "交给后续模型（推荐）", value: "fallback" },
                  { label: "跳过后续模型", value: "skip" },
                ]}
              />
            </label>
          </div>
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>分数分段动作</CardTitle>
        </CardHeader>
        <CardBody className="space-y-3">
          <p className="text-xs text-[var(--text-muted)]">
            不跟原来的广告动作绑死。默认 0-80 交给后续模型，81-90 警告，91-100 踢出。
          </p>
          {config.score_bands.map((band, index) => (
            <div
              key={`${band.min_score}-${band.max_score}-${index}`}
              className="grid gap-2 rounded-md border border-[var(--border)] p-3 md:grid-cols-[1fr_1fr_1.4fr_auto]"
            >
              <Input
                type="number"
                min={0}
                max={100}
                aria-disabled={saving}
                value={band.min_score}
                onChange={(event) =>
                  updateBand(index, {
                    min_score: Number(event.target.value) || 0,
                  })
                }
              />
              <Input
                type="number"
                min={0}
                max={100}
                aria-disabled={saving}
                value={band.max_score}
                onChange={(event) =>
                  updateBand(index, {
                    max_score: Number(event.target.value) || 0,
                  })
                }
              />
              <Select
                aria-disabled={saving}
                value={band.action}
                onChange={(event) =>
                  updateBand(index, { action: event.target.value })
                }
                options={ACTION_OPTIONS}
              />
              <Button
                type="button"
                variant="ghost"
                onClick={() => removeBand(index)}
                aria-disabled={saving}
                disabled={saving || config.score_bands.length <= 1}
              >
                删除
              </Button>
            </div>
          ))}
          <Button
            type="button"
            variant="secondary"
            aria-disabled={saving}
            onClick={addBand}
            disabled={saving}
          >
            增加分段
          </Button>
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>启用的群 · {selectedCount}</CardTitle>
        </CardHeader>
        <CardBody className="space-y-3">
          <p className="text-xs text-[var(--text-muted)]">
            没勾的群完全不走 AdKiller，还是原来的模型。默认全关。
          </p>
          {groups.length === 0 ? (
            <p className="text-sm text-[var(--text-muted)]">暂无已授权群。</p>
          ) : (
            <div className="grid gap-2">
              {groups.map((group) => {
                const checked = config.enabled_chat_ids.includes(group.chat_id);
                return (
                  <label
                    key={group.chat_id}
                    className="flex items-center gap-2 text-sm"
                  >
                    <Switch
                      aria-disabled={saving}
                      checked={checked}
                      onCheckedChange={(next) =>
                        toggleChat(group.chat_id, next)
                      }
                    />
                    <span>
                      {group.title || groupTitle(group.chat_id)}
                      <span className="ml-2 font-mono text-xs text-[var(--text-subtle)]">
                        {group.chat_id}
                      </span>
                    </span>
                  </label>
                );
              })}
            </div>
          )}
          <Button onClick={() => void saveSettings()} disabled={saving}>
            {saving ? "保存中…" : "保存 AdKiller 设置"}
          </Button>
        </CardBody>
      </Card>
      </fieldset>

      <Card>
        <CardHeader>
          <CardTitle>API Key</CardTitle>
        </CardHeader>
        <CardBody className="space-y-3">
          <p className="text-xs text-[var(--text-muted)]">
            密钥加密存储，保存后不会回显明文。当前状态：
            {keySet ? ` 已配置 ${keyHint}` : " 未配置"}
          </p>
          <Input
            type="password"
            autoComplete="off"
            placeholder={keySet ? "输入新 Key 以轮换" : "粘贴 AdKiller API Key"}
            value={keyDraft}
            onChange={(event) => setKeyDraft(event.target.value)}
          />
          <div className="flex flex-wrap gap-2">
            <Button
              onClick={() => void saveKey()}
              disabled={keySaving || keyDraft.trim() === ""}
            >
              {keySaving ? "保存中…" : "保存密钥"}
            </Button>
            <Button
              variant="ghost"
              onClick={() => void clearKey()}
              disabled={keySaving || !keySet}
            >
              清除密钥
            </Button>
          </div>
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>测试</CardTitle>
        </CardHeader>
        <CardBody className="space-y-3">
          <p className="text-xs text-[var(--text-muted)]">
            用当前已保存的密钥和分段动作试跑一条文本，不会删消息、也不会处罚用户。
          </p>
          <Textarea
            className="min-h-[120px] font-mono text-xs"
            placeholder="输入一条要检测的群聊消息"
            value={testText}
            onChange={(event) => setTestText(event.target.value)}
          />
          <Button
            onClick={() => void testAdKiller()}
            disabled={testing || testText.trim() === ""}
          >
            {testing ? "测试中…" : "测试 AdKiller"}
          </Button>
          <pre className="min-h-[160px] overflow-x-auto rounded-lg border border-[var(--border)] bg-[var(--surface-2)] p-3 text-xs leading-relaxed">
            {testResult || "结果将显示在这里"}
          </pre>
        </CardBody>
      </Card>
    </div>
  );
}
