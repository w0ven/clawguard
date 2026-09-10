"use client";

import type React from "react";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  AlertTriangle,
  Bot,
  Check,
  Database,
  History,
  Info,
  Loader2,
  MessageCircle,
  Pencil,
  Plus,
  RefreshCw,
  RotateCcw,
  Save,
  Search,
  ShieldCheck,
  Trash2,
  Wrench,
  X,
} from "lucide-react";
import { useRouter } from "next/navigation";
import { AdminShell } from "@/components/admin-shell";
import { NativeGlobalSettings, NativeGroupSettings, NativePermanentMemories, NativeSkills } from "@/components/native-assistant-settings";
import { GuardedLink } from "@/components/guarded-link";
import { useDirtyGuard, useDirtyNavigation } from "@/components/dirty-guard";
import { useToast } from "@/components/providers";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Tabs } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { ApiError, apiFetch } from "@/lib/api";
import type { Group } from "@/lib/types";
import {
  createAssistantMemory,
  fetchAssistantConflicts,
  fetchAssistantDispatches,
  fetchAssistantHistory,
  fetchAssistantMemory,
  fetchAssistantMemoryVersions,
  fetchAssistantMemories,
  fetchAssistantOverview,
  fetchAssistantPool,
  fetchAssistantRecentSenders,
  fetchAssistantStatus,
  fetchAssistantTools,
  fetchAssistantGlobal,
  fetchAssistantPrompts,
  fetchRegistryModels,
  saveAssistantGlobal,
  saveAssistantPrompts,
  forgetAssistantMemory,
  resolveAssistantConflict,
  saveAssistantPolicy,
  updateAssistantMemory,
  type AssistantConflict,
  type AssistantDefaults,
  type AssistantDispatch,
  type AssistantHistoryMessage,
  type AssistantMemory,
  type AssistantMemorySource,
  type AssistantMemoryValidScope,
  type AssistantMemoryVersion,
  type AssistantOverview,
  type AssistantPolicy,
  type AssistantPolicyWrite,
  type AssistantPool,
  type AssistantRecentSender,
  type AssistantReadiness,
  type AssistantStatus,
  type AssistantToolName,
  type AssistantToolsResponse,
  type AssistantGlobalSettings,
  type AssistantModelRole,
  type RegistryModel,
} from "@/lib/group-assistant";
import { cn } from "@/lib/utils";
import { GroupAssistantPoolEditor, ModelChainEditor } from "@/components/assistant-model-load-editor";

const TOOL_NAMES: AssistantToolName[] = [
  "knowledge_query",
  "conversation_recall",
  "webfetch_readonly",
  "send_sticker",
];

const TOOL_LABELS: Record<AssistantToolName, string> = {
  knowledge_query: "查群知识",
  conversation_recall: "回忆近期聊天",
  webfetch_readonly: "读指定网页",
  send_sticker: "发贴纸",
  doubao_tts: "发语音",
};

const TOOL_DESCRIPTIONS: Record<AssistantToolName, string> = {
  knowledge_query: "只检索当前群已授权的事实和约定，不跨群读取。",
  conversation_recall: "只回忆当前群保留期内、已审核送达的聊天上下文。",
  webfetch_readonly: "只读取管理员登记的公开网页，受域名和超时限制。",
  send_sticker: "只在当前群发送贴纸；不能指定别的群。无 file_id 时按语义挑选，失败用回退 File ID。",
  doubao_tts: "按群语音模式合成并发送语音；没配置密钥时不会假装发送成功。",
};

type AssistantSection = "start" | "speech" | "style" | "memory" | "skills" | "global";
type MemoryFilter = "active" | "base" | "learned" | "pending" | "expired" | "inactive";
type ToolCapability = "declared" | "unsupported" | "unspecified";

type PolicyDraft = AssistantPolicyWrite;

type ReadinessView = {
  canChat: boolean;
  blockers: string[];
  selectedModel: RegistryModel | null;
  capability: ToolCapability | null;
};

const MEMORY_SCOPE_OPTIONS = [
  { value: "today", label: "今天" },
  { value: "this_week", label: "本周" },
  { value: "this_month", label: "本月" },
  { value: "current_group", label: "当前群" },
  { value: "long_term", label: "长期" },
  { value: "weekly", label: "每周" },
  { value: "retention_window", label: "保留窗口" },
] as const;

const MEMORY_SCOPE_VALUES = new Set<string>(MEMORY_SCOPE_OPTIONS.map((item) => item.value));
const FOCUSABLE_SELECTOR = [
  "button:not([disabled])",
  "[href]",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "[tabindex]:not([tabindex=\"-1\"]):not([disabled])",
].join(",");

function isAssistantMemoryValidScope(value: string): value is AssistantMemoryValidScope {
  return MEMORY_SCOPE_VALUES.has(value);
}

function normalizeMemoryScope(value: string): AssistantMemoryValidScope | "" {
  const trimmed = value.trim();
  if (isAssistantMemoryValidScope(trimmed)) return trimmed;
  const aliases: Record<string, AssistantMemoryValidScope> = {
    今天: "today",
    本群: "current_group",
    本群公开规则: "current_group",
    群内: "current_group",
    本周有效: "this_week",
    本月有效: "this_month",
    长期: "long_term",
    长期有效: "long_term",
    永久: "long_term",
    每周: "weekly",
    保留窗口: "retention_window",
  };
  return aliases[trimmed] ?? "";
}

function isAbortError(error: unknown) {
  return Boolean(
    error &&
      typeof error === "object" &&
      "name" in error &&
      (error as { name?: string }).name === "AbortError",
  );
}

function useModalFocusTrap(onEscape: () => void) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const onEscapeRef = useRef(onEscape);

  useEffect(() => {
    onEscapeRef.current = onEscape;
  }, [onEscape]);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    const firstFocusable = dialog.querySelector<HTMLElement>(FOCUSABLE_SELECTOR);
    firstFocusable?.focus();
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onEscapeRef.current();
        return;
      }
      if (event.key !== "Tab") return;
      const focusable = Array.from(dialog.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(
        (element) =>
          element.offsetWidth > 0 || element.offsetHeight > 0 || element === document.activeElement,
      );
      if (focusable.length === 0) {
        event.preventDefault();
        dialog.focus();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (!dialog.contains(document.activeElement)) {
        event.preventDefault();
        (event.shiftKey ? last : first).focus();
      } else if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, []);

  return dialogRef;
}

function formatDate(value?: string | null) {
  if (!value) return "未知";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "未知";
  return date.toLocaleString("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function formatDateTimeLocal(value?: string | null) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const pad = (number: number) => String(number).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function toISOStringOrNull(value: string) {
  if (!value.trim()) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date.toISOString();
}

function errorText(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}

function displayServerText(value: unknown) {
  const text = String(value ?? "");
  if(text.includes("weighted_selected"))return "健康且有容量的模型按权重选中";
  if(text.includes("primary_selected"))return "主模型健康且有容量，优先选中";
  if(text.includes("concurrency_full"))return "主模型本地并发已满，使用备用";
  if(text.includes("disabled_or_incompatible"))return "主模型停用或能力不匹配，使用兼容备用";
  if(text.includes("provider_disabled"))return "主供应商停用，使用兼容备用";
  if(text.includes("cooldown"))return "主模型冷却中，使用备用";
  if(text.includes("overflow_http_429"))return "上游限流，按顺序回退备用";
  if(/overflow_http_5\d\d/.test(text))return "上游暂时故障，回退备用";
  if(text.includes("overflow_endpoint_timeout"))return "端点超时，回退备用";
  if(text.includes("overflow_network"))return "上游连接失败，回退备用";
  const translated = text.replace(/\b(?:Assistant scope|Source of truth|thread_id|sender_id|SupportsTools|knowledge_query|conversation_recall|webfetch_readonly|primary-overflow|CAS)\b/gi, "群助手设置");
  if (!translated) return "服务端暂时没有更多说明。";
  if (/Fixture /i.test(translated) || /[\u3400-\u9fff]/.test(translated)) return translated;
  return "服务端暂时没有更多说明。";
}

function valueOrUnknown(value: unknown) {
  return value === null || value === undefined || value === "" ? "未知" : String(value);
}

function retentionLabel(value: unknown) {
  const text = valueOrUnknown(value);
  const matched = text.match(/^(\d+)\s*days?$/i);
  return matched ? `${matched[1]} 天` : text;
}

function safeNumber(value: unknown, fallback: number) {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string" && value.trim() && Number.isFinite(Number(value))) return Number(value);
  return fallback;
}

function policyDraft(policy: AssistantPolicy): PolicyDraft {
  return {
    chat_enabled: Boolean(policy.chat_enabled),
    learning_enabled: Boolean(policy.learning_enabled),
    trigger_mode: policy.trigger_mode || "mention_or_reply",
    followup_window_sec: safeNumber(policy.followup_window_sec, 300),
    max_followup_turns: safeNumber(policy.max_followup_turns, 5),
    chat_model_ref: policy.chat_model_ref || "",
    learning_model_ref: policy.learning_model_ref || "",
    temperature: safeNumber(policy.temperature, 0.3),
    system_prompt: policy.system_prompt || "",
    history_limit: safeNumber(policy.history_limit, 500),
    retention_days: safeNumber(policy.retention_days, 7),
    collection_policy: policy.collection_policy || "history_7d_and_long_term_summary",
    tool_allowlist: Array.isArray(policy.tool_allowlist) ? [...policy.tool_allowlist] : [],
    allow_domains: Array.isArray(policy.allow_domains) ? [...policy.allow_domains] : [],
    max_queue_depth: safeNumber(policy.max_queue_depth, 10),
    max_queue_wait_sec: safeNumber(policy.max_queue_wait_sec, 15),
    proactive_interject_enabled: Boolean(policy.proactive_interject_enabled),
    proactive_cold_topic_enabled: Boolean(policy.proactive_cold_topic_enabled),
    cold_topic_idle_minutes: Math.max(180, safeNumber(policy.cold_topic_idle_minutes, 180)),
    cold_topic_quiet_start: safeNumber(policy.cold_topic_quiet_start, 0),
    cold_topic_quiet_end: safeNumber(policy.cold_topic_quiet_end, 8),
    mimic_target_user_id: safeNumber(policy.mimic_target_user_id, 0),
    mimic_target_user_name: policy.mimic_target_user_name || "",
    mimic_profile_text: policy.mimic_profile_text || "",
    mimic_sample_count: safeNumber(policy.mimic_sample_count, 0),
    mimic_distilled_at_count: safeNumber(policy.mimic_distilled_at_count, 0),
    tts_mode: policy.tts_mode === "on" || policy.tts_mode === "always" ? policy.tts_mode : "off",
    sticker_fallback_file_ids: Array.isArray(policy.sticker_fallback_file_ids) ? [...policy.sticker_fallback_file_ids] : [],
    proactive_task_brief: policy.proactive_task_brief || "",
  };
}

function normalizePool(pool: AssistantPool | null | undefined): AssistantPool | null {
  if (!pool) return null;
  const config = pool.config ?? {
    task_assignments: {},
    endpoints: [],
    max_queue_depth: 0,
    max_queue_wait_sec: 0,
  };
  return {
    ...pool,
    config: {
      ...config,
      task_assignments: config.task_assignments ?? {},
      endpoints: Array.isArray(config.endpoints) ? config.endpoints : [],
      max_queue_depth: safeNumber(config.max_queue_depth, 0),
      max_queue_wait_sec: safeNumber(config.max_queue_wait_sec, 0),
    },
  };
}

function modelCapability(model: RegistryModel): ToolCapability {
  const extended = model as RegistryModel & {
    tool_capability?: string;
    tool_support?: string;
  };
  const explicit = extended.tool_capability ?? extended.tool_support;
  if (explicit === "yes" || explicit === "declared" || explicit === "supported") return "declared";
  if (explicit === "no" || explicit === "unsupported" || explicit === "not_supported") return "unsupported";
  if (model.tools_declared === true || model.supports_tools_declared === true) {
    return model.supports_tools ? "declared" : "unsupported";
  }
  if (model.tools_declared === false || model.supports_tools_declared === false) {
    return model.supports_tools ? "declared" : "unsupported";
  }
  if (model.supports_tools) return "declared";
  const tags = Array.isArray(model.capability_tags) ? model.capability_tags : [];
  if (tags.some((tag) => ["no_tools", "tools_unsupported", "tool_calling_disabled"].includes(tag))) {
    return "unsupported";
  }
  return "unspecified";
}

function capabilityLabel(capability: ToolCapability) {
  if (capability === "declared") return "已声明支持技能";
  if (capability === "unsupported") return "确定不支持技能";
  return "尚未声明技能";
}

function modelLabel(model: RegistryModel) {
  return `${model.label || model.ref} · ${capabilityLabel(modelCapability(model))}`;
}

function readinessBlockerText(value: string) {
  const lower = value.toLowerCase();
  if (lower.includes("chat_model") || lower.includes("no model") || value.includes("先选聊天模型")) {
    return "还不能回复：请先选择聊天模型。";
  }
  if (lower.includes("supports_tools") || lower.includes("tool") || value.includes("技能")) {
    return "聊天模型还没有可用的技能调用声明，请去模型管理打开「支持工具调用」声明，或换一个已声明的模型。";
  }
  if (lower.includes("enabled") || value.includes("启用")) {
    return "群助手还没有完成启用条件，请检查群授权和聊天模型。";
  }
  return value.replace(/\b(?:Assistant scope|Source of truth|thread_id|sender_id|SupportsTools|knowledge_query|conversation_recall|webfetch_readonly|primary-overflow|CAS)\b/gi, "群助手设置");
}

function getReadiness(
  draft: PolicyDraft,
  readiness: AssistantReadiness | undefined,
  models: RegistryModel[],
): ReadinessView {
  const effectiveRef = draft.chat_model_ref || readiness?.chat_primary_model_ref || "";
  const selectedModel = models.find((model) => model.ref === effectiveRef) ?? null;
  const capability = selectedModel ? modelCapability(selectedModel) : null;
  const blockers: string[] = [];
  if (!effectiveRef) {
    blockers.push("还不能回复：请先选择聊天模型。");
  } else if (!selectedModel) {
    blockers.push("当前聊天模型不在可用模型清单中，请去模型管理确认模型已启用。");
  } else if (capability === "unspecified") {
    blockers.push("这个模型还没声明能调用技能，请去模型管理打开「支持工具调用」声明，或换一个已声明的模型。");
  } else if (capability === "unsupported") {
    blockers.push("这个模型确定不能调用技能，不能作为聊天模型；请换一个已声明支持技能的模型。");
  }
  const serverBlockers = Array.isArray(readiness?.blockers) ? readiness.blockers : [];
  for (const blocker of serverBlockers) {
    const raw = String(blocker);
    const lower = raw.toLowerCase();
    const isModelBlocker = lower.includes("chat_model") || lower.includes("no model") || raw.includes("先选聊天模型") || raw.includes("选择聊天模型");
    const isToolBlocker = lower.includes("supports_tools") || lower.includes("tool") || raw.includes("技能");
    if ((draft.chat_model_ref && isModelBlocker) || (capability === "declared" && isToolBlocker)) continue;
    const translated = readinessBlockerText(raw);
    if (translated && !blockers.includes(translated)) blockers.push(translated);
  }
  return {
    canChat: blockers.length === 0,
    blockers,
    selectedModel,
    capability,
  };
}

function memoryTypeLabel(value: string) {
  if (value === "base") return "基础";
  if (value === "learned") return "学到的";
  if (value === "pending") return "待处理";
  return value === "" ? "未知" : value === "inactive" ? "已忘记" : value === "expired" ? "已过期" : "其他";
}

function authorityLabel(value: string) {
  const labels: Record<string, string> = {
    admin_base: "管理员基础事实",
    admin_explicit: "管理员更正",
    admin_explicit_correction: "管理员更正",
    admin_conflict_accept: "管理员确认",
    pinned_announcement: "群置顶",
    telegram_approved_message: "群里已审核消息",
    learned_fact: "学习沉淀",
    unknown: "未知",
  };
  return labels[value] ?? (value ? "其他来源" : "未知");
}

function sourceLabel(value: string) {
  const labels: Record<string, string> = {
    admin_base: "管理员写的",
    admin_explicit: "管理员更正",
    admin_explicit_correction: "管理员更正",
    admin_conflict_accept: "管理员确认",
    pinned_announcement: "群置顶",
    telegram_message: "群里聊到的",
    telegram_approved_message: "群里已审核消息",
    telegram_admin_explicit_correction: "管理员更正",
    telegram_edited_message: "群里编辑过的消息",
    learned_fact: "学习沉淀",
    unknown: "来源未知",
  };
  return labels[value] ?? (value ? "其他来源" : "来源未知");
}

function scopeLabel(value: string) {
  const found = MEMORY_SCOPE_OPTIONS.find((item) => item.value === value);
  return found?.label ?? (value ? "其他范围" : "未知");
}

function verificationLabel(value?: string) {
  if (!value) return "未知";
  if (value === "server_verified" || value === "verified") return "服务端已确认";
  if (value === "unknown") return "未知";
  return "已记录";
}

function taskLabel(value: string) {
  if (value === "chat") return "聊天回复";
  if (value === "learning") return "学习整理";
  if (value === "decision") return "回复决策";
  if (value === "vision") return "视觉理解";
  if (value === "compress") return "热窗口压缩";
  if (value === "vector") return "向量召回";
  return "其他任务";
}

function localLimitLabel(value?: string) {
  if (!value || value === "local") return "本地限制";
  return displayServerText(value);
}

function statusLabel(value: string) {
  const labels: Record<string, string> = {
    healthy: "正常",
    disabled: "模型或供应商已停用",
    cooldown: "冷却中",
    half_open: "恢复探测",
    unhealthy: "异常",
    unknown: "未知",
    ok: "成功",
    success: "成功",
    failed: "失败",
    pending: "待处理",
    accepted: "已接受",
    rejected: "已拒绝",
  };
  return labels[value] ?? (value ? "其他状态" : "未知");
}

function sourceType(source: AssistantMemorySource) {
  return sourceLabel(source.source_type || source.type || "");
}

function sourceMessageId(source: AssistantMemorySource) {
  return source.source_message_id ?? source.message_id ?? null;
}

function sourceChatId(source: AssistantMemorySource) {
  return source.source_chat_id ?? source.chat_id ?? null;
}

function ApiState({
  label,
  error,
  onRetry,
}: {
  label: string;
  error?: string | null;
  onRetry?: () => void;
}) {
  return (
    <div className="flex items-start gap-3 rounded-xl border border-[var(--border)] bg-[var(--surface-2)] p-4 text-sm">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-[var(--warning)]" />
      <div className="min-w-0 flex-1">
        <p className="font-medium">{label}</p>
        <p className="mt-1 text-xs text-[var(--text-muted)]">{error ? displayServerText(error) : "没有返回数据。"}</p>
      </div>
      {onRetry && (
        <Button type="button" variant="secondary" size="sm" onClick={onRetry}>
          重试
        </Button>
      )}
    </div>
  );
}

function Field({
  label,
  hint,
  children,
  className,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <label className={cn("flex flex-col gap-1.5 text-xs text-[var(--text-muted)]", className)}>
      <span>{label}</span>
      {children}
      {hint && <span className="text-[11px] leading-relaxed text-[var(--text-subtle)]">{hint}</span>}
    </label>
  );
}

function SectionTitle({
  icon: Icon,
  title,
  description,
}: {
  icon: React.ElementType;
  title: string;
  description?: string;
}) {
  return (
    <div className="mb-4 flex items-start gap-3">
      <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-[var(--accent-soft)] text-[var(--accent)]">
        <Icon className="h-4 w-4" />
      </div>
      <div>
        <h2 className="text-sm font-semibold">{title}</h2>
        {description && <p className="mt-0.5 text-xs text-[var(--text-muted)]">{description}</p>}
      </div>
    </div>
  );
}

function StatusTile({
  label,
  value,
  detail,
  tone = "default",
}: {
  label: string;
  value: string;
  detail: string;
  tone?: "success" | "warning" | "danger" | "default";
}) {
  return (
    <div className="rounded-xl border border-[var(--border)] bg-[var(--surface-2)] p-3">
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="text-xs text-[var(--text-muted)]">{label}</span>
        <Badge tone={tone}>{value}</Badge>
      </div>
      <p className="text-[11px] leading-relaxed text-[var(--text-subtle)]">{detail}</p>
    </div>
  );
}

function SaveBar({
  dirty,
  saving,
  onSave,
  onCancel,
  scope,
}: {
  dirty: boolean;
  saving: boolean;
  onSave: () => void;
  onCancel: () => void;
  scope: string;
}) {
  return (
    <div className="miniapp-savebar sticky bottom-3 z-10 flex flex-col gap-3 rounded-2xl border border-[var(--border)] bg-[var(--savebar-bg)] p-3 shadow-xl backdrop-blur-xl sm:flex-row sm:items-center sm:justify-between">
      <div className="flex items-center gap-2 text-xs text-[var(--text-muted)]">
        <Save className="h-4 w-4" />
        {dirty ? `${scope}有未保存修改` : `${scope}与服务端一致`}
      </div>
      <div className="flex gap-2">
        <Button type="button" variant="secondary" size="sm" disabled={!dirty || saving} onClick={onCancel}>
          <RotateCcw className="h-3.5 w-3.5" />取消草稿
        </Button>
        <Button type="button" size="sm" disabled={!dirty || saving} onClick={onSave}>
          {saving ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
          {saving ? "保存中…" : "保存设置"}
        </Button>
      </div>
    </div>
  );
}

export function GroupAssistantWorkspace({ chatId }: { chatId: number }) {
  const { pushToast } = useToast();
  const { confirmNavigation } = useDirtyNavigation();
  const router = useRouter();
  const [group, setGroup] = useState<Group | null>(null);
  const [groups, setGroups] = useState<Group[]>([]);
  const [overview, setOverview] = useState<AssistantOverview | null>(null);
  const [pool, setPool] = useState<AssistantPool | null>(null);
  const [settingsDraft, setSettingsDraft] = useState<PolicyDraft | null>(null);
  const [settingsSnapshot, setSettingsSnapshot] = useState<PolicyDraft | null>(null);
  const [status, setStatus] = useState<AssistantStatus | null>(null);
  const [tools, setTools] = useState<AssistantToolsResponse | null>(null);
  const [models, setModels] = useState<RegistryModel[]>([]);
  const [recentSenders, setRecentSenders] = useState<AssistantRecentSender[]>([]);
  const [registryError, setRegistryError] = useState<string | null>(null);
  const [recentSendersError, setRecentSendersError] = useState<string | null>(null);
  const [statusError, setStatusError] = useState<string | null>(null);
  const [toolsError, setToolsError] = useState<string | null>(null);
  const [dispatches, setDispatches] = useState<AssistantDispatch[]>([]);
  const [dispatchError, setDispatchError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<ApiError | Error | null>(null);
  const [savingSettings, setSavingSettings] = useState(false);
  const [section, setSection] = useState<AssistantSection>("start");
  const [lastSavedMessage, setLastSavedMessage] = useState<string | null>(null);

  const settingsDirty = Boolean(
    settingsDraft &&
      settingsSnapshot &&
      JSON.stringify(settingsDraft) !== JSON.stringify(settingsSnapshot),
  );
  useDirtyGuard(settingsDirty, "群助手设置还有未保存修改，确定离开吗？", "assistant-settings");

  const loadCore = useCallback(async () => {
    if (!Number.isFinite(chatId)) {
      setLoadError(new Error("群编号无效。"));
      setLoading(false);
      return;
    }
    setLoading(true);
    setLoadError(null);
    setLastSavedMessage(null);
    try {
      const [groupResponse, assistantResponse, poolResponse] = await Promise.all([
        apiFetch<{ group: Group }>(`/api/admin/groups/${encodeURIComponent(String(chatId))}`),
        fetchAssistantOverview(chatId),
        fetchAssistantPool(chatId),
      ]);
      setGroup(groupResponse.group ?? null);
      setOverview(assistantResponse);
      const nextSettings = policyDraft(assistantResponse.policy);
      setSettingsDraft(nextSettings);
      setSettingsSnapshot(nextSettings);
      setPool(normalizePool(poolResponse));
    } catch (error) {
      setLoadError(error instanceof ApiError ? error : new Error(errorText(error, "群助手加载失败。")));
    } finally {
      setLoading(false);
    }
  }, [chatId]);

  const loadOptional = useCallback(async () => {
    if (!Number.isFinite(chatId)) return;
    const results = await Promise.allSettled([
      fetchAssistantStatus(chatId),
      fetchAssistantTools(chatId),
      fetchAssistantDispatches(chatId),
      fetchRegistryModels(),
      apiFetch<{ groups: Group[] }>("/api/admin/groups"),
      fetchAssistantRecentSenders(chatId),
    ]);
    const [statusResult, toolsResult, dispatchResult, modelsResult, groupsResult, sendersResult] = results;
    if (statusResult.status === "fulfilled") {
      setStatus(statusResult.value);
      setStatusError(null);
    } else {
      setStatus(null);
      setStatusError(errorText(statusResult.reason, "运行状态暂时没有返回。"));
    }
    if (toolsResult.status === "fulfilled") {
      setTools(toolsResult.value);
      setToolsError(null);
    } else {
      setTools(null);
      setToolsError(errorText(toolsResult.reason, "技能状态暂时没有返回。"));
    }
    if (dispatchResult.status === "fulfilled") {
      setDispatches(dispatchResult.value.dispatches ?? []);
      setDispatchError(null);
    } else {
      setDispatches([]);
      setDispatchError(errorText(dispatchResult.reason, "调度记录暂时没有返回。"));
    }
    if (modelsResult.status === "fulfilled") {
      setModels(modelsResult.value.models ?? []);
      setRegistryError(null);
    } else {
      setModels([]);
      setRegistryError(
        modelsResult.reason instanceof ApiError && modelsResult.reason.status === 403
          ? "当前管理员没有查看模型清单的权限；现有模型引用仍会交给服务端校验。"
          : "模型清单暂时加载失败，请稍后重试。",
      );
    }
    if (groupsResult.status === "fulfilled") {
      setGroups((groupsResult.value.groups ?? []).filter((item) => item.enabled));
    }
    if (sendersResult.status === "fulfilled") {
      const value = sendersResult.value;
      setRecentSenders(Array.isArray(value) ? value : value.senders ?? []);
      setRecentSendersError(null);
    } else {
      setRecentSenders([]);
      setRecentSendersError(
        sendersResult.reason instanceof ApiError && sendersResult.reason.status === 404
          ? "当前服务端还没有近期发言人接口，暂时不能选择学习对象。"
          : "近期发言人暂时加载失败，请稍后重试。",
      );
    }
  }, [chatId]);

  useEffect(() => {
    setGroup(null);
    setGroups([]);
    setOverview(null);
    setPool(null);
    setSettingsDraft(null);
    setSettingsSnapshot(null);
    setStatus(null);
    setTools(null);
    setModels([]);
    setRecentSenders([]);
    setLoadError(null);
    void loadCore().then(() => {
      void loadOptional();
    });
  }, [loadCore, loadOptional]);

  const reloadRuntime = useCallback(async () => {
    if (!Number.isFinite(chatId)) return;
    try {
      const [nextStatus, nextDispatches] = await Promise.all([
        fetchAssistantStatus(chatId),
        fetchAssistantDispatches(chatId),
      ]);
      setStatus(nextStatus);
      setStatusError(null);
      setDispatches(nextDispatches.dispatches ?? []);
      setDispatchError(null);
    } catch (error) {
      setStatusError(errorText(error, "运行状态暂时没有返回。"));
    }
  }, [chatId]);

  useEffect(() => {
    if (section !== "global" || !Number.isFinite(chatId)) return;
    let alive = true;
    const controller = new AbortController();
    const refresh = async () => {
      if (!alive || (typeof document !== "undefined" && document.visibilityState !== "visible")) return;
      try {
        const [nextStatus, nextDispatches] = await Promise.all([
          fetchAssistantStatus(chatId, controller.signal),
          fetchAssistantDispatches(chatId, 50, controller.signal),
        ]);
        if (!alive) return;
        setStatus(nextStatus);
        setStatusError(null);
        setDispatches(nextDispatches.dispatches ?? []);
        setDispatchError(null);
      } catch (error) {
        if (!alive || controller.signal.aborted || isAbortError(error)) return;
        setStatusError(errorText(error, "运行状态暂时没有返回。"));
      }
    };
    const timer = window.setInterval(refresh, 20_000);
    return () => {
      alive = false;
      controller.abort();
      window.clearInterval(timer);
    };
  }, [chatId, section]);

  const confirmWorkspaceNavigation = useCallback(
    (message: string) => confirmNavigation(message, "assistant-settings"),
    [confirmNavigation],
  );

  const changeSection = (next: string) => {
    const nextSection = next as AssistantSection;
    if (nextSection === section) return;
    if (section === "global") {
      if (!confirmNavigation("模型负载还有未保存草稿，确定切换分区吗？", "assistant-pool")) return;
      if (!confirmNavigation("全局助手还有未保存草稿，确定切换分区吗？", "assistant-global")) return;
    }
    const sharedDraft = section === "speech" || section === "style" || section === "global";
    const nextShared = nextSection === "speech" || nextSection === "style" || nextSection === "global";
    if (sharedDraft && nextShared) {
      setSection(nextSection);
      return;
    }
    if (!confirmWorkspaceNavigation("群助手设置还有未保存修改，确定切换分区吗？")) return;
    setSection(nextSection);
  };

  const switchGroup = (nextId: string) => {
    if (!nextId || Number(nextId) === chatId) return;
    if (!confirmNavigation("当前页面有未保存草稿，确定切换群组吗？")) return;
    router.push(`/groups/${encodeURIComponent(nextId)}/assistant`);
  };

  const updateSetting = <K extends keyof PolicyDraft>(key: K, value: PolicyDraft[K]) => {
    setSettingsDraft((current) => (current ? { ...current, [key]: value } : current));
  };

  const readiness = useMemo(
    () => getReadiness(settingsDraft ?? policyDraft(overview?.policy ?? ({} as AssistantPolicy)), overview?.readiness, models),
    [models, overview?.policy, overview?.readiness, settingsDraft],
  );

  const saveSettings = async () => {
    if (!settingsDraft || !overview || savingSettings) return;
    const saveBlockers = getReadiness(settingsDraft, overview.readiness, models).blockers;
    if (settingsDraft.chat_enabled && saveBlockers.length > 0) {
      const message = saveBlockers[0];
      setLastSavedMessage(message);
      pushToast(message, "error");
      return;
    }
    setSavingSettings(true);
    setLastSavedMessage(null);
    try {
      const response = await saveAssistantPolicy(chatId, settingsDraft, overview.policy.version);
      const nextSettings = policyDraft(response.policy);
      setOverview((current) => (current ? { ...current, policy: response.policy } : current));
      setSettingsDraft(nextSettings);
      setSettingsSnapshot(nextSettings);
      setLastSavedMessage("设置已保存；聊天模型会由服务端自动作为主模型端点。 ");
      pushToast("群助手设置已保存", "success");
      try {
        const nextPool = await fetchAssistantPool(chatId);
        setPool(normalizePool(nextPool));
      } catch {
        // 主设置保存已经成功，模型池刷新失败不覆盖草稿结果。
      }
    } catch (error) {
      const message =
        error instanceof ApiError && error.status === 409
          ? "设置版本冲突：草稿已保留，请刷新后比较再保存。"
          : displayServerText(errorText(error, "保存群助手设置失败。"));
      setLastSavedMessage(message);
      pushToast(message, "error");
    } finally {
      setSavingSettings(false);
    }
  };

  const cancelSettings = () => {
    if (!settingsSnapshot) return;
    if (!confirmNavigation("放弃当前群助手设置草稿吗？", "assistant-settings")) return;
    setSettingsDraft({
      ...settingsSnapshot,
      tool_allowlist: [...settingsSnapshot.tool_allowlist],
      allow_domains: [...settingsSnapshot.allow_domains],
    });
    setLastSavedMessage(null);
  };

  if (loading) {
    return (
      <AdminShell title="群助手" subtitle="正在读取这个群的助手设置">
        <Card>
          <CardBody className="flex items-center gap-3 py-14 text-sm text-[var(--text-muted)]">
            <Loader2 className="h-4 w-4 animate-spin text-[var(--accent)]" />正在读取群助手数据…
          </CardBody>
        </Card>
      </AdminShell>
    );
  }

  if (loadError || !overview || !group || !settingsDraft || !settingsSnapshot) {
    const statusCode = loadError instanceof ApiError ? loadError.status : undefined;
    const title =
      statusCode === 403
        ? "没有该群的管理范围"
        : statusCode === 404
          ? "群组不存在或已停用"
          : "群助手加载失败";
    return (
      <AdminShell title="群助手" subtitle="群助手设置">
        <Card>
          <CardBody className="flex flex-col gap-3 py-12">
            <Badge tone={statusCode === 403 ? "warning" : "danger"}>{title}</Badge>
            <p className="text-sm text-[var(--text-muted)]">{loadError ? displayServerText(loadError.message) : "服务端没有返回完整的群助手数据。"}</p>
            <div className="flex gap-2">
              <Button
                type="button"
                onClick={() => {
                  void loadCore();
                }}
              >
                <RefreshCw className="h-4 w-4" />重试
              </Button>
              <GuardedLink href="/assistant">
                <Button type="button" variant="secondary">
                  返回选群
                </Button>
              </GuardedLink>
            </div>
          </CardBody>
        </Card>
      </AdminShell>
    );
  }

  const defaults = overview.defaults;
  const activeGroupTitle = group.title || String(chatId);
  const readinessForDraft = getReadiness(settingsDraft, overview.readiness, models);

  return (
    <AdminShell
      title="群助手工作台"
      subtitle={`${activeGroupTitle} · 群编号 ${chatId} · 不改现有审核策略`}
      actions={
        <Button
          type="button"
          variant="secondary"
          size="sm"
          onClick={() => {
            if (!confirmNavigation("当前页面的群设置、模型负载或全局设置还有未保存草稿，确定刷新并放弃这些修改吗？")) return;
            void loadCore();
            void loadOptional();
          }}
          disabled={loading || savingSettings}
        >
          <RefreshCw className="h-3.5 w-3.5" />刷新
        </Button>
      }
    >
      <div className="flex flex-col gap-4">
        <div className="flex flex-col gap-3 rounded-2xl border border-[var(--border)] bg-[var(--surface)] p-3 shadow-[var(--shadow)] sm:flex-row sm:items-center sm:justify-between">
          <div className="flex min-w-0 items-center gap-3">
            <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-[var(--accent-soft)] text-[var(--accent)]">
              <Bot className="h-4 w-4" />
            </div>
            <div className="min-w-0">
              <p className="text-[10px] font-semibold tracking-[0.16em] text-[var(--accent)]">当前群助手</p>
              <p className="truncate text-sm font-medium">{activeGroupTitle}</p>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={settingsDraft.chat_enabled && readinessForDraft.canChat ? "success" : "warning"}>
              {settingsDraft.chat_enabled && readinessForDraft.canChat ? "可以回复" : "还不能回复"}
            </Badge>
            {groups.length > 0 ? (
              <label className="flex min-w-0 items-center gap-2 text-xs text-[var(--text-muted)]">
                <span className="shrink-0">切换群组</span>
                <select
                  aria-label="切换群组"
                  value={String(chatId)}
                  onChange={(event) => switchGroup(event.target.value)}
                  className="h-10 min-w-0 rounded-xl border border-[var(--input-border)] bg-[var(--input-bg)] px-3 text-sm text-[var(--input-color)]"
                >
                  {groups.map((item) => (
                    <option key={item.chat_id} value={String(item.chat_id)}>
                      {item.title} · {item.chat_id}
                    </option>
                  ))}
                </select>
              </label>
            ) : (
              <span className="text-xs text-[var(--text-muted)]">可管理群列表没有数据</span>
            )}
            <GuardedLink href={`/groups/${encodeURIComponent(String(chatId))}`} className="text-xs text-[var(--accent)] underline-offset-2 hover:underline">
              查看现有群审核策略（只读链接）
            </GuardedLink>
          </div>
        </div>

        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <Tabs
            tabs={[
              { value: "start", label: "开始使用" },
              { value: "speech", label: "回复与媒体" },
              { value: "style", label: "主动与风格" },
              { value: "memory", label: "群记忆" },
              { value: "skills", label: "能查什么" },
              { value: "global", label: "全局" },
            ]}
            value={section}
            onValueChange={changeSection}
          />
          <span className="text-xs text-[var(--text-muted)]">
            {settingsDirty ? "有未保存设置" : lastSavedMessage ?? "服务端数据"}
          </span>
        </div>

        {overview.native_engine && section === "start" && <Card><CardHeader><CardTitle>原生 SGB 助手</CardTitle><CardDescription>运行固定源编排；普通聊天自动抽事实已停止，管理员永久记忆与长期 Wiki 按授权检索。原生故障不会切回旧 Go 重复处理。</CardDescription></CardHeader><CardBody>模型主备/权重、群覆盖及工具轮绑定仍在「全局」中配置；聊天、主动与学语气入口使用原生有效设置。</CardBody></Card>}
        {section === "start" && !overview.native_engine && (
          <StartPanel
            policy={settingsDraft}
            defaults={defaults}
            readiness={readinessForDraft}
            status={status}
            statusError={statusError}
            onRetry={() => {
              void loadOptional();
            }}
            onOpen={changeSection}
          />
        )}
        {overview.native_engine && (section === "speech" || section === "style") && <NativeGroupSettings key={`native-group-${chatId}`} chatId={chatId} chatEnabled={settingsDraft.chat_enabled} onChatChange={value=>updateSetting("chat_enabled",value)} onSaveChat={saveSettings} chatDirty={settingsDirty} />}
        {section === "speech" && !overview.native_engine && (
          <SpeechPanel
            draft={settingsDraft}
            models={models}
            registryError={registryError}
            readiness={readinessForDraft}
            saving={savingSettings}
            dirty={settingsDirty}
            onChange={updateSetting}
            onRequestEnable={(enabled) => {
              if (!enabled) {
                updateSetting("chat_enabled", false);
                return;
              }
              if (readinessForDraft.blockers.length > 0) {
                const message = readinessForDraft.blockers[0];
                setLastSavedMessage(message);
                pushToast(message, "error");
                return;
              }
              updateSetting("chat_enabled", true);
            }}
            onSave={saveSettings}
            onCancel={cancelSettings}
          />
        )}
        {section === "memory" && <>{overview.native_engine && <NativePermanentMemories key={`native-memory-${chatId}`} chatId={chatId} />}<MemoryPanel readOnly={!!overview.native_engine} chatId={chatId} onToast={pushToast} /></>}
        {section === "style" && !overview.native_engine && (
          <StylePanel
            draft={settingsDraft}
            recentSenders={recentSenders}
            recentSendersError={recentSendersError}
            saving={savingSettings}
            dirty={settingsDirty}
            onChange={updateSetting}
            onSave={saveSettings}
            onCancel={cancelSettings}
          />
        )}
        {section === "global" && (
          <div className="space-y-4">
          {pool && <GroupAssistantPoolEditor chatId={chatId} pool={pool} models={models} registryError={registryError} onSaved={(next)=>{
            setPool(normalizePool(next));
            void fetchAssistantOverview(chatId).then(value=>setOverview(current=>current?{...current,readiness:value.readiness,model_pool:value.model_pool}:current));
            void reloadRuntime();
          }} />}
          <ModelsPanel
            nativeEnabled={!!overview.native_engine}
            draft={settingsDraft}
            pool={pool}
            status={status}
            statusError={statusError}
            dispatches={dispatches}
            dispatchError={dispatchError}
            modelByRef={new Map(models.map((model) => [model.ref, model]))}
            registryError={registryError}
            readiness={readinessForDraft}
            onChange={updateSetting}
            saving={savingSettings}
            dirty={settingsDirty}
            onSave={saveSettings}
            onCancel={cancelSettings}
            onRefreshStatus={() => {
              void reloadRuntime();
            }}
            onGlobalSaved={() => {
              void fetchAssistantPool(chatId).then(next=>setPool(normalizePool(next)));
              void fetchAssistantOverview(chatId).then(value=>setOverview(current=>current?{...current,readiness:value.readiness,model_pool:value.model_pool}:current));
              void reloadRuntime();
            }}
          />
          </div>
        )}
        {section === "skills" && overview.native_engine && <NativeSkills key={`native-skills-${chatId}`} chatId={chatId} />}
        {section === "skills" && !overview.native_engine && (
          <SkillsPanel
            tools={tools}
            error={toolsError}
            policy={settingsDraft}
            onRetry={() => {
              void loadOptional();
            }}
            onOpenSettings={() => changeSection("speech")}
          />
        )}
      </div>
    </AdminShell>
  );
}

function StartPanel({
  policy,
  defaults,
  readiness,
  status,
  statusError,
  onRetry,
  onOpen,
}: {
  policy: PolicyDraft;
  defaults: AssistantDefaults;
  readiness: ReadinessView;
  status: AssistantStatus | null;
  statusError: string | null;
  onRetry: () => void;
  onOpen: (section: AssistantSection) => void;
}) {
  const blockerList = readiness.blockers.length > 0 ? readiness.blockers : ["没有待办，这个群已具备聊天回复条件。"];
  return (
    <div className="grid gap-4 xl:grid-cols-[minmax(0,1.35fr)_minmax(300px,0.8fr)]">
      <div className="space-y-4">
        <Card>
          <CardHeader>
            <CardTitle>{policy.chat_enabled && readiness.canChat ? "这个群现在可以回复" : "这个群现在还不能回复"}</CardTitle>
            <CardDescription>
              {policy.chat_enabled && readiness.canChat
                ? "聊天已打开；默认不会主动插话。"
                : readiness.blockers[0] ?? "先完成下面的待办，再打开聊天。"}
            </CardDescription>
          </CardHeader>
          <CardBody className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <StatusTile
              label="聊天"
              value={policy.chat_enabled ? "已打开" : "已关闭"}
              tone={policy.chat_enabled ? "success" : "default"}
              detail="只响应 @、回复和连续追问。"
            />
            <StatusTile
              label="聊天模型"
              value={readiness.selectedModel?.label || "未选择"}
              tone={readiness.capability === "declared" ? "success" : "warning"}
              detail={readiness.selectedModel ? capabilityLabel(readiness.capability ?? "unspecified") : "选择后自动成为主模型。"}
            />
            <StatusTile
              label="主动说话"
              value="默认关闭"
              detail={`有把握插一句：${policy.proactive_interject_enabled ? "开" : "关"} · 冷群找话题：${policy.proactive_cold_topic_enabled ? "开" : "关"}`}
            />
            <StatusTile
              label="原文保留"
              value={policy.retention_days ? `${policy.retention_days} 天` : "未知"}
              detail={`历史默认政策：${retentionLabel(defaults.history_retention)}`}
            />
          </CardBody>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>开始前待办</CardTitle>
            <CardDescription>这里显示服务端返回的待办；没有可用聊天模型时不会假装能回消息。</CardDescription>
          </CardHeader>
          <CardBody className="space-y-3">
            <ul className="space-y-2 text-sm text-[var(--text-muted)]">
              {blockerList.map((blocker, index) => (
                <li key={`${blocker}-${index}`} className="flex items-start gap-2">
                  {readiness.blockers.length > 0 ? (
                    <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-[var(--warning)]" />
                  ) : (
                    <Check className="mt-0.5 h-4 w-4 shrink-0 text-[var(--success)]" />
                  )}
                  <span>{blocker}</span>
                </li>
              ))}
            </ul>
            <div className="flex flex-wrap gap-2 pt-1">
              <Button type="button" size="sm" onClick={() => onOpen("speech")}>
                <MessageCircle className="h-3.5 w-3.5" />去选择聊天模型
              </Button>
              <Button type="button" size="sm" variant="secondary" onClick={() => onOpen("memory")}>
                <Database className="h-3.5 w-3.5" />查看群记忆
              </Button>
            </div>
          </CardBody>
        </Card>
      </div>

      <div className="space-y-4">
        <Card>
          <CardHeader>
            <CardTitle>启用前说明</CardTitle>
            <CardDescription>聊天与学习独立保存，不改变旧群审核、权限或 Telegram 原文。</CardDescription>
          </CardHeader>
          <CardBody className="space-y-3 text-sm text-[var(--text-muted)]">
            <div className="flex gap-3">
              <ShieldCheck className="mt-0.5 h-4 w-4 shrink-0 text-[var(--success)]" />
              <p>聊天只在被点名、被回复或连续追问时回答；两个主动说话开关默认关闭。</p>
            </div>
            <div className="flex gap-3">
              <Database className="mt-0.5 h-4 w-4 shrink-0 text-[var(--accent)]" />
              <p>普通群聊学习只收录审核通过的文本，长期只沉淀可追溯事实。</p>
            </div>
            <div className="flex gap-3">
              <Info className="mt-0.5 h-4 w-4 shrink-0 text-[var(--warning)]" />
              <p>忘记记忆只移出群助手本地召回范围，不删除 Telegram 远端原文。</p>
            </div>
          </CardBody>
        </Card>
        <Card>
          <CardHeader className="flex-row items-center justify-between">
            <div>
              <CardTitle>实际状态</CardTitle>
              <CardDescription>来自当前群助手运行接口。</CardDescription>
            </div>
            <Button type="button" variant="ghost" size="sm" onClick={onRetry}>
              <RefreshCw className="h-3.5 w-3.5" />刷新
            </Button>
          </CardHeader>
          <CardBody>
            {status ? (
              <div className="space-y-2 text-sm">
                <div className="flex justify-between gap-3">
                  <span className="text-[var(--text-muted)]">本地队列</span>
                  <span>{status.queue_depth}</span>
                </div>
                <div className="flex justify-between gap-3">
                  <span className="text-[var(--text-muted)]">远程配额</span>
                  <span>未知</span>
                </div>
                <div className="flex justify-between gap-3">
                  <span className="text-[var(--text-muted)]">最近调度</span>
                  <span className="text-right">{status.last_dispatch_event ? "已有记录" : "未知"}</span>
                </div>
              </div>
            ) : (
              <ApiState label="运行状态暂时没有数据" error={statusError} onRetry={onRetry} />
            )}
          </CardBody>
        </Card>
      </div>
    </div>
  );
}

function ToggleRow({
  label,
  hint,
  checked,
  onChange,
  ariaLabel,
  disabled,
  extra,
}: {
  label: string;
  hint: string;
  checked: boolean;
  onChange: (value: boolean) => void;
  ariaLabel: string;
  disabled?: boolean;
  extra?: React.ReactNode;
}) {
  return (
    <div className="flex items-start justify-between gap-4 rounded-xl border border-[var(--border)] bg-[var(--surface-2)] p-4">
      <div>
        <p className="text-sm font-medium">{label}</p>
        <p className="mt-1 text-xs leading-relaxed text-[var(--text-muted)]">{hint}</p>
        {extra}
      </div>
      <Switch checked={checked} onCheckedChange={onChange} aria-label={ariaLabel} disabled={disabled} />
    </div>
  );
}

function SpeechPanel({
  draft,
  models,
  registryError,
  readiness,
  saving,
  dirty,
  onChange,
  onRequestEnable,
  onSave,
  onCancel,
}: {
  draft: PolicyDraft;
  models: RegistryModel[];
  registryError: string | null;
  readiness: ReadinessView;
  saving: boolean;
  dirty: boolean;
  onChange: <K extends keyof PolicyDraft>(key: K, value: PolicyDraft[K]) => void;
  onRequestEnable: (enabled: boolean) => void;
  onSave: () => void;
  onCancel: () => void;
}) {
  const enabledModels = models.filter((model) => model.enabled);
  const currentChatMissing = Boolean(draft.chat_model_ref && !enabledModels.some((model) => model.ref === draft.chat_model_ref));
  const currentLearningMissing = Boolean(draft.learning_model_ref && !enabledModels.some((model) => model.ref === draft.learning_model_ref));

  return (
    <div className="space-y-4">
      <fieldset disabled={saving} aria-busy={saving} className={cn("space-y-4 border-0 p-0", saving && "opacity-70")}>
        <Card>
          <CardHeader>
            <SectionTitle icon={MessageCircle} title="聊天能不能开" description="聊天和全群旁听学习是两个独立开关；两个主动说话开关默认关闭。" />
          </CardHeader>
          <CardBody className="space-y-3">
            <ToggleRow
              label="启用群聊天应答"
              hint="没有已声明支持技能的聊天模型时不能打开。@ 或回复才会必答。"
              checked={draft.chat_enabled}
              onChange={onRequestEnable}
              ariaLabel="启用聊天"
              extra={
                !draft.chat_enabled && readiness.blockers.length > 0 ? (
                  <p className="mt-2 rounded-lg border border-[var(--warning)]/30 bg-[var(--warning-soft)]/40 px-3 py-2 text-xs text-[var(--warning)]">
                    {readiness.blockers[0]}
                  </p>
                ) : null
              }
            />
            <ToggleRow
              label="全群旁听学习"
              hint="默认只听不插话。普通成员说的话不能覆盖管理员事实。"
              checked={draft.learning_enabled}
              onChange={(value) => onChange("learning_enabled", value)}
              ariaLabel="启用普通群聊自动学习"
            />
          </CardBody>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>聊天模型</CardTitle>
            <CardDescription>选中后会直接作为主模型端点；不需要再填端点表或任务分配。</CardDescription>
          </CardHeader>
          <CardBody className="space-y-3">
            <Field label="聊天模型" hint={registryError ?? "必须已声明支持技能调用。"}>
              <Select
                aria-label="聊天模型"
                value={draft.chat_model_ref}
                onChange={(event) => onChange("chat_model_ref", event.target.value)}
              >
                <option value="">还没选</option>
                {currentChatMissing && <option value={draft.chat_model_ref}>{draft.chat_model_ref} · 当前引用不可用</option>}
                {enabledModels.map((model) => (
                  <option key={model.ref} value={model.ref}>
                    {modelLabel(model)}
                  </option>
                ))}
              </Select>
            </Field>
            {readiness.selectedModel && readiness.capability !== "declared" && (
              <div className="rounded-xl border border-[var(--warning)]/30 bg-[var(--warning-soft)]/40 p-3 text-sm text-[var(--warning)]">
                <p className="font-medium">
                  {readiness.capability === "unsupported" ? "这个模型确定不能作为聊天模型" : "这个模型还没声明能调用技能"}
                </p>
                <p className="mt-1 text-xs leading-relaxed">
                  请去 <GuardedLink className="underline" href="/llm">模型管理</GuardedLink> 打开「支持工具调用」声明，或换一个已声明的模型。
                </p>
              </div>
            )}
            {readiness.selectedModel && readiness.capability === "declared" && (
              <p className="rounded-xl border border-[var(--success)]/20 bg-[var(--success-soft)]/40 p-3 text-xs text-[var(--success)]">
                已声明支持技能。保存后服务端会自动建立聊天主端点。
              </p>
            )}
          </CardBody>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>什么时候开口</CardTitle>
            <CardDescription>@ 或回复必经聊天；未点名只有开启主动插话才交给决策模型，追问不再强制直回。</CardDescription>
          </CardHeader>
          <CardBody className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <Field label="唤起方式">
              <Select value={draft.trigger_mode} onChange={(event) => onChange("trigger_mode", event.target.value)}>
                <option value="mention_or_reply">@ 我或回复我</option>
                <option value="mention_only">仅 @ / 回复我（不主动插话）</option>
              </Select>
            </Field>
            <Field label="语音模式">
              <Select aria-label="语音模式" value={draft.tts_mode} onChange={(event) => onChange("tts_mode", event.target.value)}>
                <option value="off">关闭</option>
                <option value="on">允许按需语音</option>
                <option value="always">始终发送语音</option>
              </Select>
            </Field>
            <Field label="贴纸回退 File ID" hint="多个用逗号分隔；当前群发送失败时使用。" className="sm:col-span-2">
              <Input
                aria-label="贴纸回退 File ID"
                value={draft.sticker_fallback_file_ids.join(",")}
                onChange={(event) => onChange("sticker_fallback_file_ids", event.target.value.split(/[,\s]+/).map((item) => item.trim()).filter(Boolean))}
              />
            </Field>
            <Field label="旧追问窗口（秒）" hint="保留旧存储值；当前由决策处理，不再强制回复">
              <Input type="number" min={30} max={3600} value={draft.followup_window_sec} onChange={(event) => onChange("followup_window_sec", Number(event.target.value))} />
            </Field>
            <Field label="旧追问轮次（保留值）" hint="不绕过主动聊天开关">
              <Input type="number" min={1} max={20} value={draft.max_followup_turns} onChange={(event) => onChange("max_followup_turns", Number(event.target.value))} />
            </Field>
            <Field label="历史上下文条数" hint="最多500条，另按token预算裁剪提示；库原文不删除">
              <Input type="number" min={1} max={500} value={draft.history_limit} onChange={(event) => onChange("history_limit", Number(event.target.value))} />
            </Field>
          </CardBody>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>学习模型</CardTitle>
            <CardDescription>学习模型可以是纯文本模型，不参与聊天技能门禁。</CardDescription>
          </CardHeader>
          <CardBody className="grid gap-4 md:grid-cols-2">
            <Field label="学习模型">
              <Select
                aria-label="学习模型"
                value={draft.learning_model_ref}
                onChange={(event) => onChange("learning_model_ref", event.target.value)}
              >
                <option value="">不指定</option>
                {currentLearningMissing && <option value={draft.learning_model_ref}>{draft.learning_model_ref} · 当前引用不可用</option>}
                {enabledModels.map((model) => (
                  <option key={model.ref} value={model.ref}>
                    {model.label || model.ref}
                  </option>
                ))}
              </Select>
            </Field>
            <p className="self-end text-xs leading-relaxed text-[var(--text-muted)]">{registryError ?? "保存时服务端会再次检查模型是否可用。"}</p>
          </CardBody>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>只读技能选择</CardTitle>
            <CardDescription>聊天模型必须能调用已选技能；服务端范围和最终权限仍以实际接口为准。</CardDescription>
          </CardHeader>
          <CardBody className="space-y-4">
            <div className="grid gap-2 sm:grid-cols-3">
              {TOOL_NAMES.map((tool) => {
                const checked = draft.tool_allowlist.includes(tool);
                return (
                  <label key={tool} className="flex cursor-pointer items-center gap-2 rounded-xl border border-[var(--border)] p-3 text-sm">
                    <input type="checkbox" checked={checked} onChange={(event) => onChange("tool_allowlist", event.target.checked ? [...draft.tool_allowlist, tool] : draft.tool_allowlist.filter((item) => item !== tool))} />
                    {TOOL_LABELS[tool]}
                  </label>
                );
              })}
            </div>
            <Field label="网页域名白名单" hint="只填域名，每行一个；不要填网址、路径或认证信息。">
              <Textarea rows={3} value={draft.allow_domains.join("\n")} onChange={(event) => onChange("allow_domains", event.target.value.split("\n").map((item) => item.trim()).filter(Boolean))} placeholder="docs.example.com" />
            </Field>
          </CardBody>
        </Card>
      </fieldset>
      <SaveBar dirty={dirty} saving={saving} onSave={onSave} onCancel={onCancel} scope="群助手设置" />
    </div>
  );
}

function StylePanel({
  draft,
  recentSenders,
  recentSendersError,
  saving,
  dirty,
  onChange,
  onSave,
  onCancel,
}: {
  draft: PolicyDraft;
  recentSenders: AssistantRecentSender[];
  recentSendersError: string | null;
  saving: boolean;
  dirty: boolean;
  onChange: <K extends keyof PolicyDraft>(key: K, value: PolicyDraft[K]) => void;
  onSave: () => void;
  onCancel: () => void;
}) {
  return (
    <div className="space-y-4">
      <fieldset disabled={saving} aria-busy={saving} className={cn("space-y-4 border-0 p-0", saving && "opacity-70")}>
        <Card>
          <CardHeader>
            <CardTitle>主动与风格</CardTitle>
            <CardDescription>插话、冷群和学语气。近期发言人点一下即可填入 ID 和名字。</CardDescription>
          </CardHeader>
          <CardBody className="space-y-4">
            <ToggleRow label="有把握才插一句" hint="开启后未点名消息交给模型决策；关闭时绝不插话。" checked={draft.proactive_interject_enabled} onChange={(value) => onChange("proactive_interject_enabled", value)} ariaLabel="有把握才插一句" />
            <ToggleRow label="冷群找话题" hint="闲置后才随口一提。" checked={draft.proactive_cold_topic_enabled} onChange={(value) => onChange("proactive_cold_topic_enabled", value)} ariaLabel="冷群找话题" />
            <Field label="闲置多久才找话题（分钟）" hint="最少 180 分钟">
              <Input type="number" min={180} max={1440} value={draft.cold_topic_idle_minutes} onChange={(event) => onChange("cold_topic_idle_minutes", Math.max(180, Number(event.target.value) || 180))} />
            </Field>
            <Field label="静默开始（小时）" hint="0–23 点">
              <Input type="number" min={0} max={23} value={draft.cold_topic_quiet_start} onChange={(event) => onChange("cold_topic_quiet_start", Number(event.target.value))} />
            </Field>
            <Field label="静默结束（小时）" hint="相等表示不设静默">
              <Input type="number" min={0} max={23} value={draft.cold_topic_quiet_end} onChange={(event) => onChange("cold_topic_quiet_end", Number(event.target.value))} />
            </Field>
            <Field label="主动任务简述">
              <Textarea aria-label="主动任务简述" rows={3} maxLength={2000} value={draft.proactive_task_brief} onChange={(event) => onChange("proactive_task_brief", event.target.value)} />
            </Field>
            <div className="grid gap-3 md:grid-cols-2">
              <Field label="风格目标用户 ID">
                <Input
                  aria-label="风格目标用户 ID"
                  type="number"
                  min={0}
                  value={draft.mimic_target_user_id || 0}
                  onChange={(event) => {
                    const nextId = Math.max(0, Number(event.target.value) || 0);
                    onChange("mimic_target_user_id", nextId);
                    if (!nextId) {
                      onChange("mimic_target_user_name", "");
                      onChange("mimic_profile_text", "");
                      onChange("mimic_sample_count", 0);
                      onChange("mimic_distilled_at_count", 0);
                    } else if (nextId !== draft.mimic_target_user_id) {
                      onChange("mimic_profile_text", "");
                      onChange("mimic_sample_count", 0);
                      onChange("mimic_distilled_at_count", 0);
                    }
                  }}
                />
              </Field>
              <Field label="风格目标名称">
                <Input aria-label="风格目标名称" maxLength={80} value={draft.mimic_target_user_name} onChange={(event) => onChange("mimic_target_user_name", event.target.value)} />
              </Field>
            </div>
            <Field label="说话风格画像" hint={`已采样 ${draft.mimic_sample_count} 条，最近蒸馏点 ${draft.mimic_distilled_at_count} 条`}>
              <Textarea aria-label="说话风格画像" rows={5} maxLength={1200} value={draft.mimic_profile_text} onChange={(event) => onChange("mimic_profile_text", event.target.value)} />
            </Field>
            <Field label="近期发言人一键填入" hint={recentSendersError ?? "只列出当前群近期发言人。"}>
              <Select
                aria-label="近期发言人"
                value={draft.mimic_target_user_id ? String(draft.mimic_target_user_id) : ""}
                onChange={(event) => {
                  const nextId = Number(event.target.value) || 0;
                  const sender = recentSenders.find((item) => item.user_id === nextId);
                  onChange("mimic_target_user_id", nextId);
                  onChange("mimic_target_user_name", sender?.user_name ?? "");
                  onChange("mimic_profile_text", "");
                  onChange("mimic_sample_count", 0);
                  onChange("mimic_distilled_at_count", 0);
                }}
              >
                <option value="">不学习，保持默认口吻</option>
                {recentSenders.map((sender) => (
                  <option key={sender.user_id} value={String(sender.user_id)}>
                    {sender.user_name} · {sender.user_id}
                  </option>
                ))}
              </Select>
            </Field>
          </CardBody>
        </Card>
      </fieldset>
      <SaveBar dirty={dirty} saving={saving} onSave={onSave} onCancel={onCancel} scope="群助手设置" />
    </div>
  );
}

function ModelsPanel({
  nativeEnabled = false,
  draft,
  pool,
  status,
  statusError,
  dispatches,
  dispatchError,
  modelByRef,
  registryError,
  readiness,
  onChange,
  saving,
  dirty,
  onSave,
  onCancel,
  onRefreshStatus,
  onGlobalSaved,
}: {
  nativeEnabled?: boolean;
  draft: PolicyDraft;
  pool: AssistantPool | null;
  status: AssistantStatus | null;
  statusError: string | null;
  dispatches: AssistantDispatch[];
  dispatchError: string | null;
  modelByRef: Map<string, RegistryModel>;
  registryError: string | null;
  readiness: ReadinessView;
  onChange: <K extends keyof PolicyDraft>(key: K, value: PolicyDraft[K]) => void;
  saving: boolean;
  dirty: boolean;
  onSave: () => void;
  onCancel: () => void;
  onRefreshStatus: () => void;
  onGlobalSaved: () => void;
}) {
  const endpoints = pool?.config.endpoints ?? [];
  const backupEndpoints = endpoints.filter((endpoint) => endpoint.role === "backup");
  const activeChatRef=endpoints.find(e=>e.id===pool?.config.task_assignments.chat?.primary)?.model_ref;
  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <CardTitle>模型与负载</CardTitle>
              <CardDescription>群聊天参数与模型负载分别保存，不修改全局默认。</CardDescription>
            </div>
            <Badge tone="info">{pool?.strategy === "weighted" ? "按权重分流" : "主备优先"}</Badge>
          </div>
        </CardHeader>
        <CardBody className="space-y-4">
          <fieldset disabled={saving} aria-busy={saving} className={cn("space-y-4 border-0 p-0", saving && "opacity-70")}>
          <div className="rounded-xl border border-[var(--accent)]/20 bg-[var(--accent-soft)]/40 p-4">
            <p className="text-sm font-medium">当前主模型</p>
            <p className="mt-1 text-sm text-[var(--text-muted)]">{(activeChatRef && (modelByRef.get(activeChatRef)?.label || activeChatRef)) || "尚未选择"}</p>
            <p className="mt-2 text-xs leading-relaxed text-[var(--text-muted)]">
              {readiness.capability === "declared"
                ? "已具备技能调用声明。保存聊天设置时，服务端会自动建立主模型端点。"
                : readiness.blockers[0] ?? "选择聊天模型后会自动建立主模型端点。"}
            </p>
          </div>
          <details className="rounded-xl border border-[var(--border)] p-4">
            <summary className="cursor-pointer text-sm font-medium">备用模型、排队与回答随机程度</summary>
            <div className="mt-4 space-y-4">
              <p className="text-xs text-[var(--text-muted)]">备用模型、顺序、权重与继承在上方「助手共享模型负载」直接编辑，单独保存；下方是群聊天设置。</p>
              <div className="grid gap-4 md:grid-cols-3">
                <Field label="回答随机程度" hint="范围 0–2">
                  <Input type="number" min={0} max={2} step={0.1} value={draft.temperature} onChange={(event) => onChange("temperature", Number(event.target.value))} />
                </Field>
                <Field label="原文保留天数" hint="范围 1–30 天">
                  <Input type="number" min={1} max={30} value={draft.retention_days} onChange={(event) => onChange("retention_days", Number(event.target.value))} />
                </Field>
                <Field label="最大本地队列深度">
                  <Input type="number" min={0} max={100} value={draft.max_queue_depth} onChange={(event) => onChange("max_queue_depth", Number(event.target.value))} />
                </Field>
                <Field label="最大排队等待（秒）">
                  <Input type="number" min={1} max={60} value={draft.max_queue_wait_sec} onChange={(event) => onChange("max_queue_wait_sec", Number(event.target.value))} />
                </Field>
                <Field label="默认系统指令" hint="可选；仅用于回答风格，不写入管理员身份或来源。" className="md:col-span-3">
                  <Textarea aria-label="默认系统指令" rows={4} value={draft.system_prompt} onChange={(event) => onChange("system_prompt", event.target.value)} />
                </Field>
              </div>
            </div>
          </details>
          </fieldset>
        </CardBody>
      </Card>

      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <div>
            <CardTitle>当前负载</CardTitle>
            <CardDescription>远程配额不伪造，统一显示为未知。</CardDescription>
          </div>
          <Button type="button" variant="ghost" size="sm" onClick={onRefreshStatus}>
            <RefreshCw className="h-3.5 w-3.5" />刷新
          </Button>
        </CardHeader>
        <CardBody>
          {status ? (
            <div className="space-y-3">
              <div className="grid gap-3 sm:grid-cols-3">
                <StatusTile label="本地队列" value={String(status.queue_depth)} detail="等待中的本地任务" />
                <StatusTile label="远程配额" value="未知" detail="不伪造供应商限额" />
                <StatusTile label="端点数量" value={String(status.endpoints_status.length)} detail="来自当前状态接口" />
              </div>
              {status.endpoints_status.length > 0 && <EndpointStatusTable endpoints={status.endpoints_status} />}
            </div>
          ) : (
            <ApiState label="负载状态暂时没有数据" error={statusError} onRetry={onRefreshStatus} />
          )}
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>最近调度</CardTitle>
          <CardDescription>仅展示服务端已返回的记录。</CardDescription>
        </CardHeader>
        <CardBody>
          {dispatchError ? <ApiState label="调度记录暂时没有数据" error={dispatchError} onRetry={onRefreshStatus} /> : dispatches.length === 0 ? <EmptyState title="还没有调度记录" detail="服务端没有返回最近任务。" /> : <DispatchTable dispatches={dispatches} />}
        </CardBody>
      </Card>
      <SaveBar dirty={dirty} saving={saving} onSave={onSave} onCancel={onCancel} scope="群助手设置" />
      <GlobalAssistantBlock nativeEnabled={nativeEnabled} models={[...modelByRef.values()]} registryError={registryError} onSaved={onGlobalSaved} />
    </div>
  );
}

function EndpointStatusTable({ endpoints }: { endpoints: AssistantStatus["endpoints_status"] }) {
  return (
    <div className="overflow-x-auto rounded-xl border border-[var(--border)]">
      <table className="w-full min-w-[660px] text-left text-xs">
        <thead className="bg-[var(--table-th-bg)] text-[var(--text-muted)]">
          <tr><th className="px-3 py-3 font-medium">模型</th><th className="px-3 py-3 font-medium">状态</th><th className="px-3 py-3 font-medium">并发</th><th className="px-3 py-3 font-medium">本地限制</th><th className="px-3 py-3 font-medium">远程配额</th></tr>
        </thead>
        <tbody>
          {endpoints.map((endpoint) => (
            <tr key={endpoint.id} className="border-t border-[var(--border)] align-top">
              <td className="px-3 py-3"><p className="font-medium">{endpoint.model_label || endpoint.model_ref || "未知"}</p><p className="mt-1 text-[var(--text-subtle)]">{endpoint.role === "backup" ? "备用" : "主模型"}</p></td>
              <td className="px-3 py-3"><Badge tone={endpoint.status === "healthy" ? "success" : endpoint.status === "unhealthy" ? "danger" : "warning"}>{statusLabel(endpoint.status)}</Badge></td>
              <td className="px-3 py-3">{endpoint.current_active} / {endpoint.max_concurrency}</td>
              <td className="px-3 py-3 text-[var(--text-muted)]">{localLimitLabel(endpoint.local_limit_label)}</td>
              <td className="px-3 py-3 text-[var(--text-muted)]">未知</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function DispatchTable({ dispatches }: { dispatches: AssistantDispatch[] }) {
  return (
    <div className="overflow-x-auto rounded-xl border border-[var(--border)]">
      <table className="w-full min-w-[720px] text-left text-xs">
        <thead className="bg-[var(--table-th-bg)] text-[var(--text-muted)]"><tr><th className="px-3 py-3 font-medium">时间</th><th className="px-3 py-3 font-medium">任务与模型</th><th className="px-3 py-3 font-medium">原因</th><th className="px-3 py-3 font-medium">结果</th><th className="px-3 py-3 font-medium">耗时</th></tr></thead>
        <tbody>
          {dispatches.map((dispatch) => (
            <tr key={dispatch.id} className="border-t border-[var(--border)] align-top">
              <td className="whitespace-nowrap px-3 py-3 text-[var(--text-muted)]">{formatDate(dispatch.created_at)}</td>
              <td className="px-3 py-3"><p>{taskLabel(dispatch.task_type)}</p><p className="mt-1 text-[var(--text-muted)]">{dispatch.model_ref || "未知模型"}</p></td>
              <td className="max-w-[280px] px-3 py-3 text-[var(--text-muted)]">{displayServerText(dispatch.reason || "未知")}</td>
              <td className="px-3 py-3"><Badge tone={dispatch.status === "ok" || dispatch.status === "success" ? "success" : dispatch.status === "failed" ? "danger" : "default"}>{statusLabel(dispatch.status)}</Badge></td>
              <td className="px-3 py-3">{dispatch.latency_ms == null ? "未知" : `${dispatch.latency_ms} 毫秒`}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function SkillsPanel({
  tools,
  error,
  policy,
  onRetry,
  onOpenSettings,
}: {
  tools: AssistantToolsResponse | null;
  error: string | null;
  policy: PolicyDraft;
  onRetry: () => void;
  onOpenSettings: () => void;
}) {
  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(280px,0.7fr)]">
      <Card>
        <CardHeader>
          <SectionTitle icon={Wrench} title="能查什么" description="现有只读三件套，加上当前群贴纸发送。语音按模式，不是随便写库。" />
        </CardHeader>
        <CardBody>
          {tools ? (
            <div className="space-y-3">
              {TOOL_NAMES.map((tool) => {
                const actual = tools.tools.find((item) => item.name === tool);
                return (
                  <div key={tool} className="rounded-xl border border-[var(--border)] p-4">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div className="flex items-center gap-2"><span className="font-medium">{TOOL_LABELS[tool]}</span><Badge tone={actual?.enabled ? "success" : "default"}>{actual?.enabled ? "已启用" : "未启用"}</Badge></div>
                      <Badge tone={tool === "send_sticker" ? "info" : actual?.read_only === false ? "danger" : "success"}>{tool === "send_sticker" ? "当前群" : actual?.read_only === false ? "异常：可写" : "只读"}</Badge>
                    </div>
                    <p className="mt-2 text-xs leading-relaxed text-[var(--text-muted)]">{TOOL_DESCRIPTIONS[tool]}</p>
                    {tool === "webfetch_readonly" && <p className="mt-2 text-xs text-[var(--text-subtle)]">只允许服务端约束的公开网页读取；失败、超时或截断会明确返回失败。</p>}
                  </div>
                );
              })}
              <div className="rounded-xl border border-[var(--border)] bg-[var(--surface-2)] p-3 text-xs text-[var(--text-muted)]">可写技能：无。当前群范围：{tools.server_bound_scope ? "已绑定" : "未知"}。</div>
            </div>
          ) : (
            <ApiState label="技能状态暂时没有数据" error={error} onRetry={onRetry} />
          )}
        </CardBody>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>当前启用的查询能力</CardTitle>
          <CardDescription>这里显示策略选择，不代表越过服务端权限。</CardDescription>
        </CardHeader>
        <CardBody className="space-y-3">
          {TOOL_NAMES.map((tool) => (
            <div key={tool} className="flex items-center justify-between gap-3 text-sm">
              <span>{TOOL_LABELS[tool]}</span>
              <Badge tone={policy.tool_allowlist.includes(tool) ? "success" : "default"}>{policy.tool_allowlist.includes(tool) ? "已选择" : "未选择"}</Badge>
            </div>
          ))}
          <div className="border-t border-[var(--border)] pt-3 text-xs text-[var(--text-muted)]"><p>网页域名白名单</p><p className="mt-1 break-words">{policy.allow_domains.length ? policy.allow_domains.join("、") : "未设置"}</p></div>
          <Button type="button" variant="secondary" size="sm" onClick={onOpenSettings}>去回复与媒体设置</Button>
        </CardBody>
      </Card>
    </div>
  );
}

type MemoryPanelProps = { chatId: number; readOnly?: boolean; onToast: (message: string, tone?: "success" | "error") => void };

function MemoryPanel({ chatId, onToast, readOnly = false }: MemoryPanelProps) {
  const [memories, setMemories] = useState<AssistantMemory[]>([]);
  const [conflicts, setConflicts] = useState<AssistantConflict[]>([]);
  const [memoryFilter, setMemoryFilter] = useState<MemoryFilter>("active");
  const [query, setQuery] = useState("");
  const [historyQuery, setHistoryQuery] = useState("");
  const [historyThread, setHistoryThread] = useState("");
  const [historySender, setHistorySender] = useState("");
  const [history, setHistory] = useState<AssistantHistoryMessage[]>([]);
  const [historyRetention, setHistoryRetention] = useState<number | null>(null);
  const [historyNotice, setHistoryNotice] = useState<string | null>(null);
  const [memoryNotice, setMemoryNotice] = useState<string | null>(null);
  const [memoryError, setMemoryError] = useState<string | null>(null);
  const [historyError, setHistoryError] = useState<string | null>(null);
  const [conflictError, setConflictError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadingHistory, setLoadingHistory] = useState(false);
  const [selectedMemory, setSelectedMemory] = useState<AssistantMemory | null>(null);
  const [selectedVersions, setSelectedVersions] = useState<AssistantMemoryVersion[]>([]);
  const [detailLoading, setDetailLoading] = useState(false);
  const [editorOpen, setEditorOpen] = useState(false);
  const [editorMemory, setEditorMemory] = useState<AssistantMemory | null>(null);
  const [editorVersions, setEditorVersions] = useState<AssistantMemoryVersion[]>([]);
  const memoriesRequestGenerationRef = useRef(0);
  const memoriesAbortRef = useRef<AbortController | null>(null);
  const historyRequestGenerationRef = useRef(0);
  const historyAbortRef = useRef<AbortController | null>(null);
  const detailRequestGenerationRef = useRef(0);
  const detailAbortRef = useRef<AbortController | null>(null);
  const editorVersionsRequestGenerationRef = useRef(0);
  const editorVersionsAbortRef = useRef<AbortController | null>(null);
  const selectedTriggerRef = useRef<HTMLElement | null>(null);
  const editorTriggerRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    return () => {
      memoriesAbortRef.current?.abort();
      historyAbortRef.current?.abort();
      detailAbortRef.current?.abort();
      editorVersionsAbortRef.current?.abort();
    };
  }, []);

  const loadMemories = useCallback(async () => {
    memoriesAbortRef.current?.abort();
    const controller = new AbortController();
    const generation = memoriesRequestGenerationRef.current + 1;
    memoriesRequestGenerationRef.current = generation;
    memoriesAbortRef.current = controller;
    const isCurrent = () => memoriesRequestGenerationRef.current === generation && memoriesAbortRef.current === controller;
    setLoading(true);
    setMemoryError(null);
    try {
      const includeInactive = memoryFilter !== "active";
      const [memoryResponse, conflictResponse] = await Promise.all([
        fetchAssistantMemories(chatId, { query, includeInactive, limit: 200 }, controller.signal),
        fetchAssistantConflicts(chatId, "pending", controller.signal),
      ]);
      if (!isCurrent()) return;
      setMemories(memoryResponse.memories ?? []);
      setMemoryNotice(memoryResponse.retention_notice ?? null);
      setConflicts(conflictResponse.conflicts ?? []);
      setConflictError(null);
    } catch (error) {
      if (!isCurrent() || controller.signal.aborted || isAbortError(error)) return;
      setMemoryError(errorText(error, "记忆列表加载失败。"));
      if (error instanceof ApiError && error.status === 403) setConflictError("当前管理员没有查看待处理冲突的权限。 ");
    } finally {
      if (isCurrent()) {
        setLoading(false);
        memoriesAbortRef.current = null;
      }
    }
  }, [chatId, memoryFilter, query]);

  const loadHistory = useCallback(async () => {
    historyAbortRef.current?.abort();
    const controller = new AbortController();
    const generation = historyRequestGenerationRef.current + 1;
    historyRequestGenerationRef.current = generation;
    historyAbortRef.current = controller;
    const isCurrent = () => historyRequestGenerationRef.current === generation && historyAbortRef.current === controller;
    setLoadingHistory(true);
    setHistoryError(null);
    try {
      const result = await fetchAssistantHistory(chatId, { query: historyQuery, threadId: historyThread, senderId: historySender, limit: 100 }, controller.signal);
      if (!isCurrent()) return;
      setHistory(result.history ?? []);
      setHistoryRetention(result.retention_days);
      setHistoryNotice(result.expired_auto_removed ? "服务端已自动排除超过保留期的原文。" : "服务端没有返回过期清理说明。 ");
    } catch (error) {
      if (!isCurrent() || controller.signal.aborted || isAbortError(error)) return;
      setHistoryError(errorText(error, "历史检索失败。"));
    } finally {
      if (isCurrent()) {
        setLoadingHistory(false);
        historyAbortRef.current = null;
      }
    }
  }, [chatId, historyQuery, historySender, historyThread]);

  useEffect(() => {
    void loadMemories();
    void loadHistory();
  }, [loadHistory, loadMemories]);

  const visibleMemories = useMemo(() => {
    const now = Date.now();
    return memories.filter((memory) => {
      const expiresAt = memory.expires_at ? new Date(memory.expires_at).getTime() : Number.NaN;
      const expired = Number.isFinite(expiresAt) && expiresAt <= now;
      if (memoryFilter === "base") return memory.memory_type === "base";
      if (memoryFilter === "learned") return memory.memory_type === "learned";
      if (memoryFilter === "pending") return memory.memory_type === "pending";
      if (memoryFilter === "expired") return expired;
      if (memoryFilter === "inactive") return !memory.active;
      return memory.active && !expired;
    });
  }, [memories, memoryFilter]);

  const restoreFocus = (target: HTMLElement | null) => {
    if (!target) return;
    window.setTimeout(() => {
      if (target.isConnected) target.focus();
    }, 0);
  };

  const dismissSelectedMemory = (restoreTrigger = true) => {
    detailRequestGenerationRef.current += 1;
    detailAbortRef.current?.abort();
    detailAbortRef.current = null;
    setSelectedMemory(null);
    setSelectedVersions([]);
    setDetailLoading(false);
    const trigger = selectedTriggerRef.current;
    selectedTriggerRef.current = null;
    if (restoreTrigger) restoreFocus(trigger);
  };

  const openMemoryDetail = async (memory: AssistantMemory, trigger?: HTMLElement | null) => {
    selectedTriggerRef.current = trigger ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null);
    detailAbortRef.current?.abort();
    const controller = new AbortController();
    const generation = detailRequestGenerationRef.current + 1;
    detailRequestGenerationRef.current = generation;
    detailAbortRef.current = controller;
    const isCurrent = () => detailRequestGenerationRef.current === generation && detailAbortRef.current === controller;
    setSelectedMemory(memory);
    setSelectedVersions([]);
    setDetailLoading(true);
    try {
      const [detail, versions] = await Promise.all([
        fetchAssistantMemory(chatId, memory.id, controller.signal),
        fetchAssistantMemoryVersions(chatId, memory.id, controller.signal),
      ]);
      if (!isCurrent()) return;
      setSelectedMemory(detail.memory);
      setSelectedVersions(versions.versions ?? []);
    } catch (error) {
      if (!isCurrent() || controller.signal.aborted || isAbortError(error)) return;
      onToast(displayServerText(errorText(error, "来源详情加载失败。")), "error");
    } finally {
      if (isCurrent()) {
        setDetailLoading(false);
        detailAbortRef.current = null;
      }
    }
  };

  const closeEditor = (restoreTrigger = true) => {
    editorVersionsRequestGenerationRef.current += 1;
    editorVersionsAbortRef.current?.abort();
    editorVersionsAbortRef.current = null;
    setEditorOpen(false);
    setEditorMemory(null);
    setEditorVersions([]);
    const trigger = editorTriggerRef.current;
    editorTriggerRef.current = null;
    if (restoreTrigger) restoreFocus(trigger);
  };

  const openEditor = async (memory: AssistantMemory | null, trigger?: HTMLElement | null) => {
    editorTriggerRef.current = trigger ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null);
    editorVersionsAbortRef.current?.abort();
    const generation = editorVersionsRequestGenerationRef.current + 1;
    editorVersionsRequestGenerationRef.current = generation;
    setEditorMemory(memory);
    setEditorVersions([]);
    setEditorOpen(true);
    if (!memory) {
      editorVersionsAbortRef.current = null;
      return;
    }
    const controller = new AbortController();
    editorVersionsAbortRef.current = controller;
    const isCurrent = () => editorVersionsRequestGenerationRef.current === generation && editorVersionsAbortRef.current === controller;
    try {
      const versions = await fetchAssistantMemoryVersions(chatId, memory.id, controller.signal);
      if (isCurrent()) setEditorVersions(versions.versions ?? []);
    } catch (error) {
      if (!isCurrent() || controller.signal.aborted || isAbortError(error)) return;
      onToast(displayServerText(errorText(error, "版本链加载失败。")), "error");
    } finally {
      if (isCurrent()) editorVersionsAbortRef.current = null;
    }
  };

  const afterMemorySaved = async () => {
    closeEditor();
    await loadMemories();
  };

  const forgetMemory = async (memory: AssistantMemory) => {
    if (!window.confirm(`确定忘记“${memory.subject}”吗？这只会移出本地召回/检索范围，不会删除 Telegram 远端原消息。`)) return;
    try {
      const result = await forgetAssistantMemory(chatId, memory.id);
      onToast(result.remote_telegram_deleted ? "记忆已忘记" : "已移出本地召回范围；Telegram 原文未删除", "success");
      await loadMemories();
    } catch (error) {
      onToast(displayServerText(errorText(error, "忘记记忆失败。")), "error");
    }
  };

  const resolveConflict = async (conflict: AssistantConflict, accept: boolean) => {
    const action = accept ? `接受候选事实，并以当前管理员明确更正“${conflict.subject}”` : `拒绝候选事实“${conflict.subject}”`;
    if (!window.confirm(`确定${action}吗？`)) return;
    try {
      if (accept) {
        if (conflict.memory_id == null) {
          onToast(`冲突“${conflict.subject}”没有目标记忆，无法更正。`, "error");
          return;
        }
        const current = await fetchAssistantMemory(chatId, conflict.memory_id);
        const expectedVersion = current.memory.version;
        if (!Number.isInteger(expectedVersion) || expectedVersion <= 0) {
          onToast(`冲突“${conflict.subject}”的目标记忆版本未知，请刷新后重试。`, "error");
          return;
        }
        await resolveAssistantConflict(chatId, conflict.id, { accept: true, expected_memory_version: expectedVersion, resolution_mode: "admin_explicit_correction" });
      } else {
        await resolveAssistantConflict(chatId, conflict.id, { accept: false });
      }
      onToast(accept ? `冲突已接受，并已以当前管理员明确纠正“${conflict.subject}”` : "冲突已拒绝", "success");
      await loadMemories();
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) {
        onToast(`冲突“${conflict.subject}”版本已变化，请刷新记忆列表后重新处理。`, "error");
        await loadMemories();
        return;
      }
      onToast(displayServerText(errorText(error, "处理冲突失败。")), "error");
    }
  };

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
          <div><SectionTitle icon={Database} title={readOnly?"旧助手档案（只读）":"群记忆"} description={readOnly?"保留旧学习结果、出处、版本和有效性供查阅；不作为原生管理员永久事实，不接受旧域写入。新知识请使用上方永久记忆入口。":"基础事实、学习沉淀、待处理冲突和历史检索均来自当前群接口。"} /></div>
          {!readOnly && <Button type="button" size="sm" onClick={(event) => { void openEditor(null, event.currentTarget); }}><Plus className="h-3.5 w-3.5" />新增基础事实</Button>}
        </CardHeader>
        <CardBody className="space-y-4">
          <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_180px_auto]">
            <Field label="搜索当前群记忆">
              <div className="flex gap-2"><Input value={query} onChange={(event) => setQuery(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") void loadMemories(); }} placeholder="搜索主题或内容" /><Button type="button" variant="secondary" size="sm" onClick={() => { void loadMemories(); }}><Search className="h-3.5 w-3.5" />检索</Button></div>
            </Field>
            <Field label="列表筛选">
              <Select aria-label="列表筛选" value={memoryFilter} onChange={(event) => setMemoryFilter(event.target.value as MemoryFilter)}>
                <option value="active">当前有效</option><option value="base">基础</option><option value="learned">学到的</option><option value="pending">待处理</option><option value="expired">已过期</option><option value="inactive">已忘记</option>
              </Select>
            </Field>
            <div className="flex items-end"><Button type="button" variant="ghost" size="sm" onClick={() => { void loadMemories(); }}><RefreshCw className="h-3.5 w-3.5" />刷新</Button></div>
          </div>
          <div className="flex flex-wrap gap-2 text-xs text-[var(--text-muted)]"><span className="rounded-lg border border-[var(--border)] px-2 py-1">接口返回 {memories.length} 条</span><span className="rounded-lg border border-[var(--border)] px-2 py-1">当前视图 {visibleMemories.length} 条</span>{memoryNotice && <span className="rounded-lg border border-[var(--border)] px-2 py-1">{displayServerText(memoryNotice)}</span>}</div>
          {memoryError ? <ApiState label="记忆列表没有数据" error={memoryError} onRetry={() => { void loadMemories(); }} /> : loading ? <div className="py-8 text-center text-sm text-[var(--text-muted)]"><Loader2 className="mx-auto mb-2 h-4 w-4 animate-spin" />正在加载记忆…</div> : visibleMemories.length === 0 ? <EmptyState title="当前筛选没有记忆" detail="不会用原型示例填充服务端列表。" /> : <MemoryTable readOnly={readOnly} memories={visibleMemories} onDetail={openMemoryDetail} onEdit={openEditor} onForget={forgetMemory} />}
        </CardBody>
      </Card>

      <Card>
        <CardHeader><SectionTitle icon={AlertTriangle} title="待处理冲突" description="只显示服务端标记为待处理的候选事实。" /></CardHeader>
        <CardBody>{conflictError ? <ApiState label="冲突列表没有数据" error={conflictError} onRetry={() => { void loadMemories(); }} /> : conflicts.length === 0 ? <EmptyState title="没有待处理冲突" detail="服务端没有返回待处理记录。" /> : <div className="space-y-3">{conflicts.map((conflict) => <ConflictRow readOnly={readOnly} key={conflict.id} conflict={conflict} onResolve={resolveConflict} />)}</div>}</CardBody>
      </Card>

      <Card>
        <CardHeader><SectionTitle icon={History} title="历史检索" description="只检索当前群、仍在真实保留期内且已审核送达的原文。" /></CardHeader>
        <CardBody className="space-y-4">
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <Field label="关键词"><Input aria-label="关键词" value={historyQuery} onChange={(event) => setHistoryQuery(event.target.value)} /></Field>
            <Field label="话题编号"><Input aria-label="话题编号" inputMode="numeric" value={historyThread} onChange={(event) => setHistoryThread(event.target.value)} placeholder="可选" /></Field>
            <Field label="成员编号"><Input aria-label="成员编号" inputMode="numeric" value={historySender} onChange={(event) => setHistorySender(event.target.value)} placeholder="可选" /></Field>
            <div className="flex items-end"><Button type="button" onClick={() => { void loadHistory(); }} disabled={loadingHistory}><Search className="h-3.5 w-3.5" />{loadingHistory ? "检索中…" : "检索历史"}</Button></div>
          </div>
          {historyNotice && <p className="text-xs text-[var(--text-muted)]">{historyNotice} 保留天数：{historyRetention ?? "未知"}</p>}
          {historyError ? <ApiState label="历史没有数据" error={historyError} onRetry={() => { void loadHistory(); }} /> : history.length === 0 ? <EmptyState title="没有历史消息" detail="过期原文不会在后台伪造展示。" /> : <HistoryTable history={history} />}
        </CardBody>
      </Card>

      {selectedMemory && <MemoryDetailDialog readOnly={readOnly} memory={selectedMemory} versions={selectedVersions} loading={detailLoading} onClose={() => dismissSelectedMemory()} onEdit={() => { const memoryToEdit = selectedMemory; const detailTrigger = selectedTriggerRef.current; dismissSelectedMemory(false); void openEditor(memoryToEdit, detailTrigger); }} />}
      {!readOnly && editorOpen && <MemoryEditorDialog chatId={chatId} memory={editorMemory} versions={editorVersions} onClose={() => closeEditor()} onSaved={afterMemorySaved} onToast={onToast} />}
    </div>
  );
}

function MemoryTable({
  memories,
  readOnly = false,
  onDetail,
  onEdit,
  onForget,
}: {
  memories: AssistantMemory[];
  readOnly?: boolean;
  onDetail: (memory: AssistantMemory, trigger: HTMLElement) => void;
  onEdit: (memory: AssistantMemory | null, trigger: HTMLElement) => void;
  onForget: (memory: AssistantMemory) => void;
}) {
  return (
    <div className="overflow-x-auto rounded-xl border border-[var(--border)]">
      <table className="w-full min-w-[880px] text-left text-xs">
        <thead className="bg-[var(--table-th-bg)] text-[var(--text-muted)]"><tr><th className="px-3 py-3 font-medium">事实</th><th className="px-3 py-3 font-medium">类型 / 权威</th><th className="px-3 py-3 font-medium">有效范围</th><th className="px-3 py-3 font-medium">来源</th><th className="px-3 py-3 font-medium">有效期</th><th className="px-3 py-3 font-medium">操作</th></tr></thead>
        <tbody>
          {memories.map((memory) => {
            const expiresAt = memory.expires_at ? new Date(memory.expires_at).getTime() : Number.NaN;
            const expired = Number.isFinite(expiresAt) && expiresAt <= Date.now();
            return (
              <tr key={memory.id} className="border-t border-[var(--border)] align-top hover:bg-[var(--table-hover)]">
                <td className="max-w-[280px] px-3 py-3"><p className="font-medium">{memory.subject}</p><p className="mt-1 line-clamp-3 text-[var(--text-muted)]">{memory.content}</p><p className="mt-1 text-[var(--text-subtle)]">第 {memory.version} 版 · {memory.active ? "当前有效" : "已忘记"}</p></td>
                <td className="px-3 py-3"><Badge>{memoryTypeLabel(memory.memory_type)}</Badge><p className="mt-2 text-[var(--text-muted)]">{authorityLabel(memory.authority_level)}</p></td>
                <td className="max-w-[150px] px-3 py-3 text-[var(--text-muted)]">{scopeLabel(memory.valid_scope)}</td>
                <td className="max-w-[210px] px-3 py-3"><p>{sourceType(memory.source)}</p><p className="mt-1 text-[var(--text-muted)]">{memory.source.operator_name || (sourceMessageId(memory.source) != null ? `消息 ${sourceMessageId(memory.source)}` : "来源未知")}</p><p className="mt-1 text-[var(--text-subtle)]">校验：{verificationLabel(memory.source.verified)}{memory.source.currently_verified === undefined ? "" : memory.source.currently_verified ? " · 当前有效" : " · 未确认"}</p></td>
                <td className="px-3 py-3 text-[var(--text-muted)]"><span className={expired ? "text-[var(--warning)]" : undefined}>{formatDate(memory.expires_at)}</span>{expired && <span className="mt-1 block">已过期</span>}</td>
                <td className="px-3 py-3"><div className="flex flex-wrap gap-1"><Button type="button" variant="secondary" size="sm" onClick={(event) => onDetail(memory, event.currentTarget)}>来源</Button>{!readOnly && <Button type="button" variant="secondary" size="sm" onClick={(event) => onEdit(memory, event.currentTarget)}><Pencil className="h-3 w-3" />编辑</Button>}{!readOnly && memory.active && <Button type="button" variant="danger" size="sm" onClick={() => onForget(memory)}><Trash2 className="h-3 w-3" />忘记</Button>}</div></td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function ConflictRow({
  conflict,
  readOnly = false,
  onResolve,
}: {
  conflict: AssistantConflict;
  readOnly?: boolean;
  onResolve: (conflict: AssistantConflict, accept: boolean) => void;
}) {
  const messageId = sourceMessageId(conflict.source);
  const chatId = sourceChatId(conflict.source);
  return (
    <div className="rounded-xl border border-[var(--warning)]/30 bg-[var(--warning-soft)]/40 p-4">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2"><Badge tone="warning">{statusLabel(conflict.status)}</Badge><span className="font-medium">{conflict.subject}</span></div>
          <p className="mt-2 text-sm">{conflict.candidate_content}</p>
          <p className="mt-2 text-xs text-[var(--text-muted)]">候选权威：{authorityLabel(conflict.candidate_authority)} · 有效范围：{scopeLabel(conflict.candidate_scope)} · 来源：{sourceType(conflict.source)} · 原消息：{valueOrUnknown(messageId)} · 来源群：{valueOrUnknown(chatId)}</p>
          {conflict.source.snippet && <p className="mt-2 rounded-lg border border-[var(--border)] bg-[var(--surface)] p-2 text-xs text-[var(--text-muted)]">{conflict.source.snippet}</p>}
          <p className="mt-2 text-xs text-[var(--text-muted)]">接受后会以当前管理员明确更正；普通来源不会自动提升为权威。</p>
        </div>
        {!readOnly && <div className="flex shrink-0 gap-2"><Button type="button" size="sm" onClick={() => onResolve(conflict, true)}><Check className="h-3.5 w-3.5" />接受</Button><Button type="button" variant="secondary" size="sm" onClick={() => onResolve(conflict, false)}><X className="h-3.5 w-3.5" />拒绝</Button></div>}
      </div>
    </div>
  );
}

function HistoryTable({ history }: { history: AssistantHistoryMessage[] }) {
  return (
    <div className="overflow-x-auto rounded-xl border border-[var(--border)]">
      <table className="w-full min-w-[780px] text-left text-xs">
        <thead className="bg-[var(--table-th-bg)] text-[var(--text-muted)]"><tr><th className="px-3 py-3 font-medium">时间</th><th className="px-3 py-3 font-medium">发送者 / 话题</th><th className="px-3 py-3 font-medium">角色</th><th className="px-3 py-3 font-medium">原文</th><th className="px-3 py-3 font-medium">来源与有效期</th></tr></thead>
        <tbody>
          {history.map((item) => (
            <tr key={item.id} className="border-t border-[var(--border)] align-top hover:bg-[var(--table-hover)]">
              <td className="whitespace-nowrap px-3 py-3 text-[var(--text-muted)]">{formatDate(item.created_at)}</td>
              <td className="px-3 py-3"><p>{item.sender_name || "未知"}</p><p className="mt-1 text-[var(--text-subtle)]">成员 {item.sender_id} · 话题 {item.thread_id}</p></td>
              <td className="px-3 py-3"><Badge>{item.role === "user" ? "成员" : item.role === "assistant" ? "助手" : "其他"}</Badge></td>
              <td className="max-w-[360px] whitespace-pre-wrap px-3 py-3">{item.text}</td>
              <td className="px-3 py-3 text-[var(--text-muted)]"><p>{sourceLabel(item.source.type)} · {item.source.id || "未知"}</p><p className="mt-1">{item.delivered ? "已送达" : "未送达"} · {item.approved ? "已审核" : "审核状态未知"}</p><p className="mt-1">到期：{formatDate(item.expires_at)}</p></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function EmptyState({ title, detail }: { title: string; detail: string }) {
  return <div className="rounded-xl border border-dashed border-[var(--border-strong)] p-8 text-center"><p className="text-sm font-medium">{title}</p><p className="mt-1 text-xs text-[var(--text-muted)]">{detail}</p></div>;
}

function MemoryDetailDialog({
  memory,
  readOnly = false,
  versions,
  loading,
  onClose,
  onEdit,
}: {
  memory: AssistantMemory;
  readOnly?: boolean;
  versions: AssistantMemoryVersion[];
  loading: boolean;
  onClose: () => void;
  onEdit: () => void;
}) {
  const dialogRef = useModalFocusTrap(onClose);
  return (
    <div className="fixed inset-0 z-50 flex items-end justify-center bg-black/45 p-0 backdrop-blur-sm sm:items-center sm:p-4" role="dialog" aria-modal="true" aria-label="记忆来源详情">
      <div ref={dialogRef} tabIndex={-1} className="max-h-[90vh] w-full max-w-3xl overflow-y-auto rounded-t-3xl border border-[var(--border)] bg-[var(--dialog-bg)] p-5 shadow-2xl sm:rounded-3xl">
        <div className="flex items-start justify-between gap-4"><div><p className="text-[10px] font-semibold tracking-[0.16em] text-[var(--accent)]">记忆来源</p><h2 className="mt-1 text-lg font-semibold">{memory.subject}</h2><p className="mt-1 text-xs text-[var(--text-muted)]">记忆编号 {memory.id} · 当前第 {memory.version} 版</p></div><Button type="button" variant="ghost" size="sm" onClick={onClose} aria-label="关闭" autoFocus><X className="h-4 w-4" /></Button></div>
        <div className="mt-5 grid gap-4 md:grid-cols-2"><div className="space-y-3"><div className="rounded-xl border border-[var(--border)] bg-[var(--surface-2)] p-3 text-sm whitespace-pre-wrap">{memory.content}</div><div className="grid gap-2 text-xs text-[var(--text-muted)]"><p>类型：{memoryTypeLabel(memory.memory_type)} · 权威：{authorityLabel(memory.authority_level)}</p><p>有效范围：{scopeLabel(memory.valid_scope)}</p><p>有效至：{formatDate(memory.expires_at)} · {memory.active ? "当前有效" : "已忘记"}</p></div></div><div className="space-y-3 text-xs"><div className="rounded-xl border border-[var(--border)] p-3"><p className="mb-2 font-medium">来源（服务端只读）</p><p>来源类型：{sourceType(memory.source)}</p><p>来源消息编号：{valueOrUnknown(sourceMessageId(memory.source))}</p><p>来源群编号：{valueOrUnknown(sourceChatId(memory.source))}</p><p>操作人：{memory.source.operator_name || valueOrUnknown(memory.source.operator_id)}</p><p>校验：{verificationLabel(memory.source.verified)}{memory.source.currently_verified === undefined ? "" : memory.source.currently_verified ? " · 当前有效" : " · 未确认"}</p>{memory.source.snippet && <p className="mt-2 rounded-lg bg-[var(--surface-2)] p-2 text-[var(--text-muted)]">{memory.source.snippet}</p>}</div><div className="rounded-xl border border-[var(--warning)]/30 bg-[var(--warning-soft)]/40 p-3">来源过期或未验证时，不会被后台升级为权威；管理员更正由服务端记录。</div></div></div>
        <div className="mt-5"><div className="mb-2 flex items-center justify-between"><h3 className="text-sm font-semibold">版本记录</h3>{loading && <Loader2 className="h-4 w-4 animate-spin text-[var(--accent)]" />}</div>{versions.length === 0 ? <p className="text-xs text-[var(--text-muted)]">暂时没有版本记录。</p> : <div className="space-y-2">{versions.map((version) => <div key={version.id} className="rounded-xl border border-[var(--border)] p-3 text-xs"><div className="flex flex-wrap gap-2"><Badge>第 {version.version} 版</Badge><span>{version.change_kind === "create" ? "创建" : "更新"}</span><span className="text-[var(--text-muted)]">{formatDate(version.created_at)}</span></div><p className="mt-2 whitespace-pre-wrap">{version.content}</p><p className="mt-1 text-[var(--text-muted)]">{authorityLabel(version.authority_level)} · {scopeLabel(version.valid_scope)} · {sourceLabel(version.source_type)}</p></div>)}</div>}</div>
        <div className="mt-5 flex justify-end gap-2"><Button type="button" variant="secondary" onClick={onClose}>关闭</Button>{!readOnly && <Button type="button" onClick={onEdit}><Pencil className="h-3.5 w-3.5" />管理员更正</Button>}</div>
      </div>
    </div>
  );
}

function MemoryEditorDialog({
  chatId,
  memory,
  versions,
  onClose,
  onSaved,
  onToast,
}: {
  chatId: number;
  memory: AssistantMemory | null;
  versions: AssistantMemoryVersion[];
  onClose: () => void;
  onSaved: () => Promise<void>;
  onToast: (message: string, tone?: "success" | "error") => void;
}) {
  const { confirmNavigation } = useDirtyNavigation();
  const [subject, setSubject] = useState(memory?.subject ?? "");
  const [content, setContent] = useState(memory?.content ?? "");
  const [scope, setScope] = useState(memory?.valid_scope ?? "");
  const [expiresAt, setExpiresAt] = useState(formatDateTimeLocal(memory?.expires_at));
  const [snippet, setSnippet] = useState(memory?.source.snippet ?? "");
  const [saving, setSaving] = useState(false);
  const savingRef = useRef(false);
  const initial = `${memory?.version ?? 0}|${memory?.subject ?? ""}|${memory?.content ?? ""}|${memory?.valid_scope ?? ""}|${formatDateTimeLocal(memory?.expires_at)}|${memory?.source.snippet ?? ""}`;
  const current = `${memory?.version ?? 0}|${subject}|${content}|${scope}|${expiresAt}|${snippet}`;
  const dirty = current !== initial;
  useDirtyGuard(dirty, "记忆更正草稿还有未保存修改，确定离开吗？", "assistant-memory");

  const close = useCallback(() => {
    if (saving || savingRef.current) return;
    if (!confirmNavigation("记忆更正草稿还有未保存修改，确定关闭吗？", "assistant-memory")) return;
    onClose();
  }, [confirmNavigation, onClose, saving]);
  const dialogRef = useModalFocusTrap(close);

  const save = async () => {
    if (!subject.trim() || !content.trim() || !scope.trim() || saving || savingRef.current) return;
    const normalizedScope = normalizeMemoryScope(scope);
    if (!normalizedScope) {
      onToast("有效范围不受支持，请填写今天、本周、本月、当前群、长期、每周或保留窗口。", "error");
      return;
    }
    savingRef.current = true;
    setSaving(true);
    try {
      if (memory) {
        await updateAssistantMemory(chatId, memory.id, { expected_version: memory.version, subject: subject.trim(), content: content.trim(), valid_scope: normalizedScope, expires_at: toISOStringOrNull(expiresAt), source_snippet: snippet.trim() });
      } else {
        await createAssistantMemory(chatId, { subject: subject.trim(), content: content.trim(), valid_scope: normalizedScope, expires_at: toISOStringOrNull(expiresAt), source_type: "admin_base", source_snippet: snippet.trim() });
      }
      onToast(memory ? "管理员更正已保存" : "基础事实已新增", "success");
      await onSaved();
    } catch (error) {
      onToast(error instanceof ApiError && error.status === 409 ? "记忆版本冲突：草稿保留，请重新打开当前版本比较。" : displayServerText(errorText(error, "保存记忆失败。")), "error");
    } finally {
      savingRef.current = false;
      setSaving(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-end justify-center bg-black/45 p-0 backdrop-blur-sm sm:items-center sm:p-4" role="dialog" aria-modal="true" aria-label={memory ? "管理员更正" : "新增基础事实"}>
      <div ref={dialogRef} tabIndex={-1} aria-busy={saving} className="max-h-[92vh] w-full max-w-3xl overflow-y-auto rounded-t-3xl border border-[var(--border)] bg-[var(--dialog-bg)] p-5 shadow-2xl sm:rounded-3xl">
        <div className="flex items-start justify-between gap-4"><div><p className="text-[10px] font-semibold tracking-[0.16em] text-[var(--accent)]">{memory ? "管理员更正" : "管理员写入"}</p><h2 className="mt-1 text-lg font-semibold">{memory ? "更正群记忆" : "新增基础事实"}</h2><p className="mt-1 text-xs text-[var(--text-muted)]">服务端会记录真实管理员身份；表单不接受伪造 Telegram 来源或权限字段。</p></div><Button type="button" variant="ghost" size="sm" onClick={close} aria-label="关闭" autoFocus><X className="h-4 w-4" /></Button></div>
        <fieldset disabled={saving} aria-busy={saving} className={cn("contents", saving && "opacity-70")}>
          <div className="mt-5 grid gap-4 md:grid-cols-2">
            <Field label="主题"><Input aria-label="主题" value={subject} onChange={(event) => setSubject(event.target.value)} maxLength={200} /></Field>
            <Field label="有效范围" hint="可填今天、本周、本月、当前群、长期、每周或保留窗口"><Input aria-label="有效范围" value={scope} onChange={(event) => setScope(event.target.value)} maxLength={300} placeholder="例如：本周有效" /></Field>
            <Field label="事实内容" className="md:col-span-2"><Textarea aria-label="事实内容" rows={7} value={content} onChange={(event) => setContent(event.target.value)} maxLength={4000} /></Field>
            <Field label="到期时间" hint="留空由服务端设定默认期限"><Input aria-label="到期时间" type="datetime-local" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} /></Field>
            <Field label="来源摘要（可选）" hint="只写摘要，不填写 Telegram 消息身份"><Textarea aria-label="来源摘要（可选）" rows={3} value={snippet} onChange={(event) => setSnippet(event.target.value)} maxLength={1000} /></Field>
          </div>
          {memory && versions.length > 0 && <div className="mt-4 rounded-xl border border-[var(--border)] bg-[var(--surface-2)] p-3 text-xs text-[var(--text-muted)]">已有 {versions.length} 条版本记录；保存会基于当前第 {memory.version} 版。</div>}
          <div className="mt-5 flex flex-col-reverse gap-2 sm:flex-row sm:justify-end"><Button type="button" variant="secondary" onClick={close}>取消</Button><Button type="button" onClick={() => { void save(); }} disabled={saving || !subject.trim() || !content.trim() || !scope.trim()}>{saving ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}{saving ? "保存中…" : "保存更正"}</Button></div>
        </fieldset>
      </div>
    </div>
  );
}

const ROLE_LABELS: Record<string, string> = {
  main: "主模型",
  decision: "决策",
  vision: "视觉",
  compress: "压缩",
  vector: "向量",
};

function emptyRole(): AssistantModelRole {
  return { model_ref: "", timeout_sec: 12, temperature: 0.7, max_tokens: 2048, fallbacks: [] };
}

function RoleFallbackList({
  name,
  label,
  fallbacks,
  models,
  onChange,
}: {
  name: string;
  label: string;
  fallbacks: string[];
  models: RegistryModel[];
  onChange: (next: string[]) => void;
}) {
  const items = fallbacks.length > 0 ? fallbacks : [""];
  return (
    <div className="space-y-2 md:col-span-2">
      {items.map((ref, index) => (
        <div key={`${name}-fallback-${index}`} className="flex gap-2">
          <Select
            aria-label={index === 0 ? `${label}回退` : `${label}回退 ${index + 1}`}
            value={ref}
            onChange={(event) => {
              const next = [...items];
              next[index] = event.target.value;
              onChange(next);
            }}
          >
            <option value="">不设回退</option>
            {models.map((model) => (
              <option key={model.ref} value={model.ref}>{model.label || model.ref}</option>
            ))}
          </Select>
          {items.length > 1 && (
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={() => onChange(items.filter((_, i) => i !== index).filter(Boolean))}
            >
              删除
            </Button>
          )}
        </div>
      ))}
      <Button
        type="button"
        variant="secondary"
        size="sm"
        onClick={() => onChange([...items, ""])}
      >
        添加回退模型
      </Button>
    </div>
  );
}

function GlobalAssistantBlock({ models, registryError, onSaved, nativeEnabled = false }: { models: RegistryModel[]; registryError: string | null; onSaved: () => void; nativeEnabled?: boolean }) {
  const { pushToast } = useToast();
  const [globalSettings, setGlobalSettings] = useState<AssistantGlobalSettings | null>(null);
  const [prompts, setPrompts] = useState<Record<string, string>>({ persona: "", casual: "", decision: "", proactive_topic: "", style_distill: "" });
  const [appKey, setAppKey] = useState("");
  const [accessKey, setAccessKey] = useState("");
  const [saving, setSaving] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [globalSnapshot, setGlobalSnapshot] = useState<AssistantGlobalSettings | null>(null);
  const [promptSnapshot, setPromptSnapshot] = useState<Record<string,string>>({});
  const globalDirty = JSON.stringify(globalSettings) !== JSON.stringify(globalSnapshot) || JSON.stringify(prompts) !== JSON.stringify(promptSnapshot) || Boolean(appKey || accessKey);
  useDirtyGuard(!loading && globalDirty, "全局助手还有未保存修改，确定离开吗？", "assistant-global");

  const loadGlobal = () => {
    setLoading(true);
    setLoadError(null);
    void Promise.all([fetchAssistantGlobal(), fetchAssistantPrompts()])
      .then(([nextGlobal, nextPrompts]) => {
        setGlobalSettings(nextGlobal);
        setGlobalSnapshot(nextGlobal);
        const loadedPrompts = { persona: "", casual: "", decision: "", proactive_topic: "", style_distill: "", ...(nextPrompts.prompts ?? {}) };
        setPrompts(loadedPrompts);
        setPromptSnapshot(loadedPrompts);
        setLoadError(null);
      })
      .catch((error) => {
        const message = errorText(error, "全局助手设置暂时无法加载。");
        setLoadError(message);
        pushToast(message, "error");
      })
      .finally(() => {
        setLoading(false);
      });
  };

  useEffect(() => {
    loadGlobal();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pushToast]);

  if (loadError) {
    return (
      <div className="mb-3 rounded-xl border border-[var(--warning)]/30 bg-[var(--warning-soft)]/40 p-3 text-sm text-[var(--warning)]">
        <p>{loadError}</p>
        <Button type="button" variant="secondary" size="sm" className="mt-2" onClick={() => loadGlobal()}>重试</Button>
      </div>
    );
  }

  if (loading || !globalSettings) {
    return <p className="mb-3 text-xs text-[var(--text-muted)]">正在加载全局模型角色、提示词和媒体设置…</p>;
  }

  const updateRole = (name: string, patch: Partial<AssistantModelRole>) => {
    const current = globalSettings.model_roles[name] ?? emptyRole();
    setGlobalSettings({
      ...globalSettings,
      model_roles: { ...globalSettings.model_roles, [name]: { ...current, ...patch } },
    });
  };

  const save = async () => {
    setSaving(true);
    try {
      for(const role of Object.values(globalSettings.model_roles)){
        if((role.fallbacks??[]).some(ref=>!ref.trim()) || (!role.model_ref && (role.fallbacks??[]).length))throw new Error("请完成已添加模型的选择，或移除空备用行；草稿已保留。");
      }
      const roles = Object.fromEntries(
        Object.entries(globalSettings.model_roles).map(([key, role]) => [
          key,
          { ...role, fallbacks: (role.fallbacks ?? []).map((item) => item.trim()).filter(Boolean) },
        ]),
      );
      const {app_key_configured,access_key_configured,...ttsWrite}=globalSettings.tts;
      const saved = await saveAssistantGlobal({
        expected_version: globalSettings.version,
        model_roles: roles,
        bot: globalSettings.bot,
        tts: { ...ttsWrite, app_key: appKey, access_key: accessKey },
        stickers: globalSettings.stickers,
      });
      setGlobalSettings(saved);
      setGlobalSnapshot(saved);
      onSaved();
      setAppKey("");
      setAccessKey("");
      await saveAssistantPrompts(prompts);
      setPromptSnapshot(prompts);
      pushToast("全局助手设置已保存", "success");
    } catch (error) {
      pushToast(errorText(error, "保存全局助手设置失败。"), "error");
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="mb-4 space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>模型角色</CardTitle>
          <CardDescription>只引用现有模型，不另建供应商。空白非主模型继承主模型。</CardDescription>
        </CardHeader>
        <CardBody className="space-y-3">
          <label className="block text-sm">全局负载策略<Select aria-label="全局负载策略" value={globalSettings.model_roles.main?.strategy ?? "primary-overflow"} onChange={e=>updateRole("main",{strategy:e.target.value as "primary-overflow"|"weighted"})}><option value="primary-overflow">主备优先</option><option value="weighted">按权重分流</option></Select></label>
          <p className="text-xs text-[var(--text-muted)]">新群默认继承；旧显式群池保留原模型，群页面可恢复继承。每个任务按能力筛选，同一模型跨群/角色共用本地容量，远端额度未知。学习沿用群显式路由，未配置时继承主角色。</p>
          {(["main", "decision", "vision", "compress", "vector"] as const).map((name) => {
            const role = globalSettings.model_roles[name] ?? emptyRole();
            return (
              <div key={name} className="rounded-xl border border-[var(--border)] p-3">
                <p className="mb-2 text-sm font-medium">{ROLE_LABELS[name]}</p>
                <div className="grid gap-2 md:grid-cols-2">
                  {name!=="main"&&<label className="text-xs md:col-span-2">角色负载策略<Select aria-label={`${ROLE_LABELS[name]}负载策略`} value={role.strategy??""} onChange={e=>updateRole(name,{strategy:(e.target.value||undefined) as AssistantModelRole["strategy"]})}><option value="">继承全局策略</option><option value="primary-overflow">主备优先</option><option value="weighted">按权重分流</option></Select></label>}
                  <div className="md:col-span-2"><ModelChainEditor task={name} registryError={registryError} refs={role.model_ref ? [role.model_ref, ...(role.fallbacks ?? [])] : ["", ...(role.fallbacks ?? [])]} options={role.model_options ?? {}} models={models} weighted={(role.strategy || globalSettings.model_roles.main?.strategy) === "weighted"} timeout={(role.timeout_sec || 12)*1000} onChange={(refs)=>updateRole(name,{model_ref:refs[0]??"",fallbacks:refs.slice(1)})} onOptions={(model_options)=>updateRole(name,{model_options})}/></div>
                  <Input aria-label={`${ROLE_LABELS[name]}超时`} type="number" min={1} max={120} placeholder="超时秒" value={role.timeout_sec ?? ""} onChange={(event) => updateRole(name, { timeout_sec: Number(event.target.value) || 0 })} />
                  <Input aria-label={`${ROLE_LABELS[name]}温度`} type="number" min={0} max={2} step={0.1} placeholder="温度" value={role.temperature ?? ""} onChange={(event) => updateRole(name, { temperature: Number(event.target.value) })} />
                  <Input aria-label={`${ROLE_LABELS[name]}最大输出`} type="number" min={0} max={8192} placeholder="最大输出" value={role.max_tokens ?? ""} onChange={(event) => updateRole(name, { max_tokens: Number(event.target.value) || 0 })} />
                </div>
              </div>
            );
          })}
        </CardBody>
      </Card>
      {!nativeEnabled && <><Card>
        <CardHeader>
          <CardTitle>提示词</CardTitle>
          <CardDescription>空则用内嵌默认。安全段、只读边界和 ACTIVE_PERSONA 规则仍由程序追加。</CardDescription>
        </CardHeader>
        <CardBody className="space-y-3">
          {(["persona", "casual", "decision", "proactive_topic", "style_distill"] as const).map((key) => {
            const label = key === "persona" ? "人格" : key === "casual" ? "日常对话" : key === "decision" ? "回复决策" : key === "proactive_topic" ? "主动话题" : "风格提炼";
            return (
            <Field key={key} label={label}>
              <Textarea aria-label={label} rows={4} value={prompts[key] ?? ""} onChange={(event) => setPrompts({ ...prompts, [key]: event.target.value })} />
            </Field>
            );
          })}
        </CardBody>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>机器人行为</CardTitle>
          <CardDescription>入站合并、回复时限、上下文条数和召回开关。</CardDescription>
        </CardHeader>
        <CardBody className="grid gap-3 md:grid-cols-3">
          <Field label="入站合并窗口（秒）"><Input type="number" min={0} max={60} step={0.1} value={globalSettings.bot.inbound_merge_window_sec} onChange={(event) => setGlobalSettings({ ...globalSettings, bot: { ...globalSettings.bot, inbound_merge_window_sec: Number(event.target.value) } })} /></Field>
          <Field label="回复总时限（秒）"><Input type="number" min={5} max={120} value={globalSettings.bot.reply_total_timeout_sec} onChange={(event) => setGlobalSettings({ ...globalSettings, bot: { ...globalSettings.bot, reply_total_timeout_sec: Number(event.target.value) } })} /></Field>
          <Field label="决策上下文条数"><Input type="number" min={0} max={20} value={globalSettings.bot.decision_context_items} onChange={(event) => setGlobalSettings({ ...globalSettings, bot: { ...globalSettings.bot, decision_context_items: Number(event.target.value) } })} /></Field>
          <ToggleRow label="原文保留" hint="默认开。开启时热窗口压缩不会用摘要替换原文。" checked={globalSettings.bot.keep_original_text} onChange={(value) => setGlobalSettings({ ...globalSettings, bot: { ...globalSettings.bot, keep_original_text: value } })} ariaLabel="原文保留" />
          <ToggleRow label="召回开关" hint="关闭后会话召回不再补充向量候选。" checked={globalSettings.bot.memory_recall_enabled} onChange={(value) => setGlobalSettings({ ...globalSettings, bot: { ...globalSettings.bot, memory_recall_enabled: value } })} ariaLabel="召回开关" />
          <ToggleRow label="热窗口压缩" hint="默认关。配了压缩模型且开启才压热窗口，不删原文档案。" checked={globalSettings.bot.hot_window_compress_enabled} onChange={(value) => setGlobalSettings({ ...globalSettings, bot: { ...globalSettings.bot, hot_window_compress_enabled: value } })} ariaLabel="热窗口压缩" />
          <Field label="空闲触发（分钟）"><Input type="number" min={180} max={43200} value={globalSettings.bot.proactive_idle_minutes} onChange={(event) => setGlobalSettings({ ...globalSettings, bot: { ...globalSettings.bot, proactive_idle_minutes: Number(event.target.value) } })} /></Field>
          <Field label="安静时段开始"><Input type="number" min={0} max={23} value={globalSettings.bot.proactive_quiet_start} onChange={(event) => setGlobalSettings({ ...globalSettings, bot: { ...globalSettings.bot, proactive_quiet_start: Number(event.target.value) } })} /></Field>
          <Field label="安静时段结束"><Input type="number" min={0} max={23} value={globalSettings.bot.proactive_quiet_end} onChange={(event) => setGlobalSettings({ ...globalSettings, bot: { ...globalSettings.bot, proactive_quiet_end: Number(event.target.value) } })} /></Field>
          <Field label="检查间隔（秒）"><Input type="number" min={15} max={3600} value={globalSettings.bot.proactive_check_interval_sec} onChange={(event) => setGlobalSettings({ ...globalSettings, bot: { ...globalSettings.bot, proactive_check_interval_sec: Number(event.target.value) } })} /></Field>
        </CardBody>
      </Card>
      </>}
      {nativeEnabled && <NativeGlobalSettings />}
      <Card>
        <CardHeader>
          <CardTitle>媒体</CardTitle>
          <CardDescription>豆包语音全局开关与连接参数；密钥不回显。贴纸回退 File ID 是全局默认。</CardDescription>
        </CardHeader>
        <CardBody className="grid gap-3 md:grid-cols-2">
          <ToggleRow label="启用语音合成" hint="关闭后任何群都不发语音。" checked={globalSettings.tts.enabled} onChange={(value) => setGlobalSettings({ ...globalSettings, tts: { ...globalSettings.tts, enabled: value } })} ariaLabel="启用语音合成" />
          <Field label="接口地址"><Input value={globalSettings.tts.api_base} onChange={(event) => setGlobalSettings({ ...globalSettings, tts: { ...globalSettings.tts, api_base: event.target.value } })} /></Field>
          <Field label="应用编号"><Input value={globalSettings.tts.app_id} onChange={(event) => setGlobalSettings({ ...globalSettings, tts: { ...globalSettings.tts, app_id: event.target.value } })} /></Field>
          <Field label="音色"><Input value={globalSettings.tts.speaker} onChange={(event) => setGlobalSettings({ ...globalSettings, tts: { ...globalSettings.tts, speaker: event.target.value } })} /></Field>
          <Field label="应用密钥">{globalSettings.tts.app_key_configured ? "已配置" : "未配置"}<Input type="password" value={appKey} onChange={(event) => setAppKey(event.target.value)} placeholder="空白不覆盖" /></Field>
          <Field label="访问密钥">{globalSettings.tts.access_key_configured ? "已配置" : "未配置"}<Input type="password" value={accessKey} onChange={(event) => setAccessKey(event.target.value)} placeholder="空白不覆盖" /></Field>
          <Field label="请求超时（秒）"><Input type="number" min={1} max={300} step={0.1} value={globalSettings.tts.http_timeout_sec} onChange={(event) => setGlobalSettings({ ...globalSettings, tts: { ...globalSettings.tts, http_timeout_sec: Number(event.target.value) } })} /></Field>
          <Field label="最大文本长度"><Input type="number" min={1} max={10000} value={globalSettings.tts.max_text_length} onChange={(event) => setGlobalSettings({ ...globalSettings, tts: { ...globalSettings.tts, max_text_length: Number(event.target.value) } })} /></Field>
          <Field label="资源编号"><Input value={globalSettings.tts.resource_id} onChange={(event) => setGlobalSettings({ ...globalSettings, tts: { ...globalSettings.tts, resource_id: event.target.value } })} /></Field>
          <Field label="模型"><Input value={globalSettings.tts.model} onChange={(event) => setGlobalSettings({ ...globalSettings, tts: { ...globalSettings.tts, model: event.target.value } })} /></Field>
          <Field label="音频格式"><Input value={globalSettings.tts.audio_format} onChange={(event) => setGlobalSettings({ ...globalSettings, tts: { ...globalSettings.tts, audio_format: event.target.value } })} /></Field>
          <Field label="采样率"><Input type="number" min={8000} max={192000} value={globalSettings.tts.sample_rate} onChange={(event) => setGlobalSettings({ ...globalSettings, tts: { ...globalSettings.tts, sample_rate: Number(event.target.value) } })} /></Field>
          <Field label="比特率"><Input type="number" min={0} max={512000} value={globalSettings.tts.bit_rate} onChange={(event) => setGlobalSettings({ ...globalSettings, tts: { ...globalSettings.tts, bit_rate: Number(event.target.value) } })} /></Field>
          <Field label="情感"><Input value={globalSettings.tts.emotion} onChange={(event) => setGlobalSettings({ ...globalSettings, tts: { ...globalSettings.tts, emotion: event.target.value } })} /></Field>
          <Field label="情感强度"><Input type="number" min={1} max={5} value={globalSettings.tts.emotion_scale} onChange={(event) => setGlobalSettings({ ...globalSettings, tts: { ...globalSettings.tts, emotion_scale: Number(event.target.value) } })} /></Field>
          <Field label="语速调整"><Input type="number" min={-100} max={100} value={globalSettings.tts.speech_rate} onChange={(event) => setGlobalSettings({ ...globalSettings, tts: { ...globalSettings.tts, speech_rate: Number(event.target.value) } })} /></Field>
          <Field label="音量调整"><Input type="number" min={-100} max={100} value={globalSettings.tts.loudness_rate} onChange={(event) => setGlobalSettings({ ...globalSettings, tts: { ...globalSettings.tts, loudness_rate: Number(event.target.value) } })} /></Field>
          <Field label="尾部静音（毫秒）"><Input type="number" min={0} max={10000} value={globalSettings.tts.silence_duration_ms} onChange={(event) => setGlobalSettings({ ...globalSettings, tts: { ...globalSettings.tts, silence_duration_ms: Number(event.target.value) } })} /></Field>
          <Field label="贴纸回退 File ID" className="md:col-span-2"><Input value={globalSettings.stickers.fallback_file_ids.join(",")} onChange={(event) => setGlobalSettings({ ...globalSettings, stickers: { fallback_file_ids: event.target.value.split(/[,\s]+/).map((item) => item.trim()).filter(Boolean) } })} /></Field>
        </CardBody>
      </Card>
      <div className="flex flex-wrap gap-2"><Button type="button" size="sm" disabled={saving || !globalDirty} onClick={() => { void save(); }}>{saving ? "保存中…" : "保存全局设置"}</Button><Button type="button" variant="secondary" size="sm" disabled={saving || !globalDirty} onClick={()=>{setGlobalSettings(globalSnapshot);setPrompts(promptSnapshot);setAppKey("");setAccessKey("");}}>取消全局修改</Button><span className="text-xs">{globalDirty?"未保存全局草稿":"全局已保存"}</span></div>
    </div>
  );
}
