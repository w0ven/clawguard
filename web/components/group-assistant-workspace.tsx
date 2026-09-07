"use client";

import type React from "react";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useRouter } from "next/navigation";
import {
  AlertTriangle,
  Bot,
  Check,
  Clock3,
  Database,
  ExternalLink,
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
import { AdminShell } from "@/components/admin-shell";
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
  fetchAssistantStatus,
  fetchAssistantTools,
  fetchRegistryModels,
  forgetAssistantMemory,
  resolveAssistantConflict,
  saveAssistantPolicy,
  saveAssistantPool,
  updateAssistantMemory,
  type AssistantConflict,
  type AssistantDefaults,
  type AssistantDispatch,
  type AssistantHistoryMessage,
  type AssistantLastDispatch,
  type AssistantMemory,
  type AssistantMemoryValidScope,
  type AssistantMemoryVersion,
  type AssistantPolicy,
  type AssistantPool,
  type AssistantPoolConfig,
  type AssistantPoolEndpoint,
  type AssistantStatus,
  type AssistantToolName,
  type AssistantToolsResponse,
  type RegistryModel,
} from "@/lib/group-assistant";
import { cn } from "@/lib/utils";

const TOOL_NAMES: AssistantToolName[] = [
  "knowledge_query",
  "conversation_recall",
  "webfetch_readonly",
];

const TOOL_LABELS: Record<AssistantToolName, string> = {
  knowledge_query: "群知识查询",
  conversation_recall: "保留期历史检索",
  webfetch_readonly: "白名单网页读取",
};

type AssistantTab = "overview" | "chat" | "memory" | "pool" | "tools";
type MemoryFilter = "active" | "base" | "learned" | "pending" | "expired" | "inactive";

type PolicyDraft = Omit<
  AssistantPolicy,
  "chat_id" | "version" | "updated_by" | "created_at" | "updated_at"
>;

type PoolDraft = {
  version: number;
  strategy: string;
  config: AssistantPoolConfig;
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
    "今天": "today",
    "本群": "current_group",
    "本群公开规则": "current_group",
    "群内": "current_group",
    "本周有效": "this_week",
    "本月有效": "this_month",
    "长期": "long_term",
    "长期有效": "long_term",
    "永久": "long_term",
    "每周": "weekly",
    "保留窗口": "retention_window",
  };
  return aliases[trimmed] ?? "";
}

function isAbortError(error: unknown) {
  return Boolean(error && typeof error === "object" && "name" in error && (error as { name?: string }).name === "AbortError");
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
        (element) => element.offsetWidth > 0 || element.offsetHeight > 0 || element === document.activeElement,
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

function emptyTaskAssignment() {
  return { primary: "", backups: [] };
}

function normalizePool(pool: AssistantPool, policy: AssistantPolicy): PoolDraft {
  const raw = pool.config ?? ({} as AssistantPoolConfig);
  return {
    version: pool.version ?? 0,
    strategy: pool.strategy || "primary-overflow",
    config: {
      task_assignments: {
        chat: raw.task_assignments?.chat ?? emptyTaskAssignment(),
        learning: raw.task_assignments?.learning ?? emptyTaskAssignment(),
      },
      endpoints: Array.isArray(raw.endpoints) ? raw.endpoints : [],
      max_queue_depth: Number.isFinite(raw.max_queue_depth)
        ? raw.max_queue_depth
        : policy.max_queue_depth,
      max_queue_wait_sec: Number.isFinite(raw.max_queue_wait_sec)
        ? raw.max_queue_wait_sec
        : policy.max_queue_wait_sec,
    },
  };
}

function policyDraft(policy: AssistantPolicy): PolicyDraft {
  return {
    chat_enabled: policy.chat_enabled,
    learning_enabled: policy.learning_enabled,
    trigger_mode: policy.trigger_mode,
    followup_window_sec: policy.followup_window_sec,
    max_followup_turns: policy.max_followup_turns,
    chat_model_ref: policy.chat_model_ref,
    learning_model_ref: policy.learning_model_ref,
    temperature: policy.temperature,
    system_prompt: policy.system_prompt,
    history_limit: policy.history_limit,
    retention_days: policy.retention_days,
    collection_policy: policy.collection_policy,
    tool_allowlist: policy.tool_allowlist ?? [],
    allow_domains: policy.allow_domains ?? [],
    max_queue_depth: policy.max_queue_depth,
    max_queue_wait_sec: policy.max_queue_wait_sec,
  };
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
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function toISOStringOrNull(value: string) {
  if (!value.trim()) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date.toISOString();
}

function valueOrUnknown(value: unknown) {
  if (value === null || value === undefined || value === "") return "未知";
  return String(value);
}

function statusTone(status: string): "success" | "warning" | "danger" | "default" {
  if (status === "healthy") return "success";
  if (status === "cooldown" || status === "half_open") return "warning";
  if (status === "unhealthy") return "danger";
  return "default";
}

function errorText(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
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
        <p className="mt-1 text-xs text-[var(--text-muted)]">{error ?? "无数据"}</p>
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

function SectionTitle({ icon: Icon, title, description }: { icon: React.ElementType; title: string; description?: string }) {
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

export function GroupAssistantWorkspace({ chatId }: { chatId: number }) {
  const { pushToast } = useToast();
  const { confirmNavigation } = useDirtyNavigation();
  const router = useRouter();
  const [group, setGroup] = useState<Group | null>(null);
  const [groups, setGroups] = useState<Group[]>([]);
  const [overview, setOverview] = useState<{ policy: AssistantPolicy; model_pool: AssistantPool; defaults: AssistantDefaults } | null>(null);
  const [settingsDraft, setSettingsDraft] = useState<PolicyDraft | null>(null);
  const [settingsSnapshot, setSettingsSnapshot] = useState<PolicyDraft | null>(null);
  const [poolDraft, setPoolDraft] = useState<PoolDraft | null>(null);
  const [poolSnapshot, setPoolSnapshot] = useState<PoolDraft | null>(null);
  const [status, setStatus] = useState<AssistantStatus | null>(null);
  const [tools, setTools] = useState<AssistantToolsResponse | null>(null);
  const [models, setModels] = useState<RegistryModel[]>([]);
  const [registryError, setRegistryError] = useState<string | null>(null);
  const [statusError, setStatusError] = useState<string | null>(null);
  const [toolsError, setToolsError] = useState<string | null>(null);
  const [dispatches, setDispatches] = useState<AssistantDispatch[]>([]);
  const [dispatchError, setDispatchError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<ApiError | Error | null>(null);
  const [savingSettings, setSavingSettings] = useState(false);
  const [savingPool, setSavingPool] = useState(false);
  const [tab, setTab] = useState<AssistantTab>("overview");
  const [lastSavedMessage, setLastSavedMessage] = useState<string | null>(null);

  const settingsDirty = Boolean(settingsDraft && settingsSnapshot && JSON.stringify(settingsDraft) !== JSON.stringify(settingsSnapshot));
  const poolDirty = Boolean(poolDraft && poolSnapshot && JSON.stringify(poolDraft) !== JSON.stringify(poolSnapshot));
  useDirtyGuard(settingsDirty, "群助手聊天与学习配置尚未保存，确定离开吗？", "assistant-settings");
  useDirtyGuard(poolDirty, "群助手模型池草稿尚未保存，确定离开吗？", "assistant-pool");

  const loadCore = useCallback(async () => {
    if (!Number.isFinite(chatId)) {
      setLoadError(new Error("无效的群 ID"));
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
      const nextPool = normalizePool(poolResponse, assistantResponse.policy);
      setSettingsDraft(nextSettings);
      setSettingsSnapshot(nextSettings);
      setPoolDraft(nextPool);
      setPoolSnapshot(nextPool);
    } catch (error) {
      if (error instanceof ApiError) setLoadError(error);
      else setLoadError(new Error(errorText(error, "加载群助手失败")));
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
    ]);
    const [statusResult, toolsResult, dispatchResult, modelsResult, groupsResult] = results;
    if (statusResult.status === "fulfilled") {
      setStatus(statusResult.value);
      setStatusError(null);
    } else {
      setStatus(null);
      setStatusError(errorText(statusResult.reason, "运行状态加载失败"));
    }
    if (toolsResult.status === "fulfilled") {
      setTools(toolsResult.value);
      setToolsError(null);
    } else {
      setTools(null);
      setToolsError(errorText(toolsResult.reason, "工具能力加载失败"));
    }
    if (dispatchResult.status === "fulfilled") {
      setDispatches(dispatchResult.value.dispatches ?? []);
      setDispatchError(null);
    } else {
      setDispatches([]);
      setDispatchError(errorText(dispatchResult.reason, "调度记录加载失败"));
    }
    if (modelsResult.status === "fulfilled") {
      setModels(modelsResult.value.models ?? []);
      setRegistryError(null);
    } else {
      setModels([]);
      setRegistryError(
        modelsResult.reason instanceof ApiError && modelsResult.reason.status === 403
          ? "当前管理员没有模型 registry 查看能力；保留当前引用，服务端仍会最终校验。"
          : errorText(modelsResult.reason, "模型 registry 加载失败"),
      );
    }
    if (groupsResult.status === "fulfilled") setGroups((groupsResult.value.groups ?? []).filter((item) => item.enabled));
  }, [chatId]);

  useEffect(() => {
    let alive = true;
    setGroup(null);
    setGroups([]);
    setOverview(null);
    setSettingsDraft(null);
    setSettingsSnapshot(null);
    setPoolDraft(null);
    setPoolSnapshot(null);
    setStatus(null);
    setTools(null);
    setModels([]);
    setLoadError(null);
    void loadCore().then(() => {
      if (alive) void loadOptional();
    });
    return () => {
      alive = false;
    };
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
      setStatusError(errorText(error, "运行状态加载失败"));
    }
  }, [chatId]);

  useEffect(() => {
    if (tab !== "pool" || !Number.isFinite(chatId)) return;
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
        if (!alive || controller.signal.aborted) return;
        setStatusError(errorText(error, "运行状态轮询失败"));
      }
    };
    const timer = window.setInterval(refresh, 20_000);
    return () => {
      alive = false;
      controller.abort();
      window.clearInterval(timer);
    };
  }, [chatId, tab]);

  const confirmWorkspaceNavigation = (message: string) => {
    return ["assistant-settings", "assistant-pool", "assistant-memory"].every((scope) => confirmNavigation(message, scope));
  };

  const changeTab = (next: string) => {
    const nextTab = next as AssistantTab;
    if (nextTab === tab) return;
    if (!confirmWorkspaceNavigation("当前群助手有未保存修改，确定切换场景吗？")) return;
    setTab(nextTab);
  };

  const switchGroup = (nextId: string) => {
    if (!nextId || Number(nextId) === chatId) return;
    if (!confirmWorkspaceNavigation("当前群助手有未保存修改，确定切换群组吗？")) return;
    router.push(`/groups/${encodeURIComponent(nextId)}/assistant`);
  };

  const updateSetting = <K extends keyof PolicyDraft>(key: K, value: PolicyDraft[K]) => {
    setSettingsDraft((current) => (current ? { ...current, [key]: value } : current));
  };

  const saveSettings = async () => {
    if (!settingsDraft || !overview || savingSettings) return;
    setSavingSettings(true);
    setLastSavedMessage(null);
    try {
      const response = await saveAssistantPolicy(chatId, settingsDraft, overview.policy.version);
      const nextSettings = policyDraft(response.policy);
      setOverview((current) => (current ? { ...current, policy: response.policy } : current));
      setSettingsDraft(nextSettings);
      setSettingsSnapshot(nextSettings);
      setLastSavedMessage("聊天与学习配置已按服务端版本保存");
      pushToast("群助手配置已保存", "success");
    } catch (error) {
      const message = error instanceof ApiError && error.status === 409
        ? "版本冲突：草稿已保留，请刷新后比较并重新保存。"
        : errorText(error, "保存群助手配置失败");
      setLastSavedMessage(message);
      pushToast(message, "error");
    } finally {
      setSavingSettings(false);
    }
  };

  const cancelSettings = () => {
    if (!settingsSnapshot) return;
    if (!confirmNavigation("放弃当前聊天与学习配置草稿吗？", "assistant-settings")) return;
    setSettingsDraft({ ...settingsSnapshot, tool_allowlist: [...settingsSnapshot.tool_allowlist], allow_domains: [...settingsSnapshot.allow_domains] });
    setLastSavedMessage(null);
  };

  const savePool = async () => {
    if (!poolDraft || savingPool) return;
    const chatAssignment = poolDraft.config.task_assignments.chat;
    const learningAssignment = poolDraft.config.task_assignments.learning;
    if (!poolDraft.config.endpoints.length || !chatAssignment?.primary || !learningAssignment?.primary) {
      const message = "模型池至少需要端点，以及 chat / learning 各一个主端点；未向服务端提交。";
      setLastSavedMessage(message);
      pushToast(message, "error");
      return;
    }
    setSavingPool(true);
    setLastSavedMessage(null);
    try {
      const response = await saveAssistantPool(
        chatId,
        {
          strategy: poolDraft.strategy,
          task_assignments: poolDraft.config.task_assignments,
          endpoints: poolDraft.config.endpoints,
          max_queue_depth: poolDraft.config.max_queue_depth,
          max_queue_wait_sec: poolDraft.config.max_queue_wait_sec,
        },
        poolDraft.version,
      );
      const nextPool = normalizePool(response, overview?.policy ?? ({} as AssistantPolicy));
      setPoolDraft(nextPool);
      setPoolSnapshot(nextPool);
      setOverview((current) => (current ? { ...current, model_pool: response } : current));
      setLastSavedMessage("模型池已按服务端版本保存");
      pushToast("模型池已保存", "success");
    } catch (error) {
      const message = error instanceof ApiError && error.status === 409
        ? "模型池版本冲突：当前草稿已保留，没有覆盖你的编辑。"
        : errorText(error, "保存模型池失败");
      setLastSavedMessage(message);
      pushToast(message, "error");
    } finally {
      setSavingPool(false);
    }
  };

  const cancelPool = () => {
    if (!poolSnapshot) return;
    if (!confirmNavigation("放弃当前模型池草稿吗？", "assistant-pool")) return;
    setPoolDraft({
      ...poolSnapshot,
      config: {
        ...poolSnapshot.config,
        task_assignments: {
          chat: { ...poolSnapshot.config.task_assignments.chat, backups: [...poolSnapshot.config.task_assignments.chat.backups] },
          learning: { ...poolSnapshot.config.task_assignments.learning, backups: [...poolSnapshot.config.task_assignments.learning.backups] },
        },
        endpoints: poolSnapshot.config.endpoints.map((endpoint) => ({ ...endpoint })),
      },
    });
    setLastSavedMessage(null);
  };

  if (loading) {
    return (
      <AdminShell title="群助手" subtitle="加载实际群助手策略与运行数据">
        <Card><CardBody className="flex items-center gap-3 py-14 text-sm text-[var(--text-muted)]"><Loader2 className="h-4 w-4 animate-spin text-[var(--accent)]" />正在读取群助手数据…</CardBody></Card>
      </AdminShell>
    );
  }

  if (loadError || !overview || !group || !settingsDraft || !poolDraft) {
    const statusCode = loadError instanceof ApiError ? loadError.status : undefined;
    const title = statusCode === 403 ? "没有该群的管理范围" : statusCode === 404 ? "群组不存在或已停用" : "群助手加载失败";
    return (
      <AdminShell title="群助手" subtitle="真实 API 页面">
        <Card><CardBody className="flex flex-col gap-3 py-12">
          <Badge tone={statusCode === 403 ? "warning" : "danger"}>{title}</Badge>
          <p className="text-sm text-[var(--text-muted)]">{loadError?.message ?? "服务端没有返回完整群助手数据。"}</p>
          <div className="flex gap-2">
            <Button type="button" onClick={() => { void loadCore(); }}><RefreshCw className="h-4 w-4" />重试</Button>
            <GuardedLink href="/assistant"><Button type="button" variant="secondary">返回选群</Button></GuardedLink>
          </div>
        </CardBody></Card>
      </AdminShell>
    );
  }

  const policy = overview.policy;
  const defaults = overview.defaults;
  const modelByRef = new Map(models.map((model) => [model.ref, model]));
  const modelOptions = models.filter((model) => model.enabled);
  const chatModelOptions = modelOptions.filter((model) => model.supports_tools);
  const activeGroupTitle = group.title || String(chatId);

  return (
    <AdminShell
      title="群助手"
      subtitle={`${activeGroupTitle} · Chat ID ${chatId} · 独立于现有审核策略`}
      actions={
        <Button type="button" variant="secondary" size="sm" onClick={() => { if (!confirmWorkspaceNavigation("当前群助手有未保存修改，确定刷新并放弃远端未合并草稿吗？")) return; void loadCore(); void loadOptional(); }} disabled={loading || savingSettings || savingPool}>
          <RefreshCw className="h-3.5 w-3.5" />刷新
        </Button>
      }
    >
      <div className="flex flex-col gap-4">
        <div className="flex flex-col gap-3 rounded-2xl border border-[var(--border)] bg-[var(--surface)] p-3 shadow-[var(--shadow)] sm:flex-row sm:items-center sm:justify-between">
          <div className="flex min-w-0 items-center gap-3">
            <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-[var(--accent-soft)] text-[var(--accent)]"><Bot className="h-4 w-4" /></div>
            <div className="min-w-0">
              <p className="text-[10px] font-semibold uppercase tracking-[0.16em] text-[var(--accent)]">Assistant scope</p>
              <p className="truncate text-sm font-medium">{activeGroupTitle}</p>
            </div>
          </div>
          {groups.length > 0 ? (
            <label className="flex min-w-0 items-center gap-2 text-xs text-[var(--text-muted)]">
              <span className="shrink-0">切换群组</span>
              <select value={String(chatId)} onChange={(event) => switchGroup(event.target.value)} className="h-10 min-w-0 rounded-xl border border-[var(--input-border)] bg-[var(--input-bg)] px-3 text-sm text-[var(--input-color)]">
                {groups.map((item) => <option key={item.chat_id} value={String(item.chat_id)}>{item.title} · {item.chat_id}</option>)}
              </select>
            </label>
          ) : (
            <span className="text-xs text-[var(--text-muted)]">可管理群列表无数据</span>
          )}
        </div>

        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <Tabs
            tabs={[
              { value: "overview", label: "总览" },
              { value: "chat", label: "聊天与学习" },
              { value: "memory", label: "记忆中心" },
              { value: "pool", label: "模型池" },
              { value: "tools", label: "只读技能" },
            ]}
            value={tab}
            onValueChange={changeTab}
          />
          <span className="text-xs text-[var(--text-muted)]">{settingsDirty || poolDirty ? "有未保存草稿" : lastSavedMessage ?? "服务端数据"}</span>
        </div>

        {tab === "overview" && (
          <OverviewPanel
            policy={policy}
            defaults={defaults}
            status={status}
            statusError={statusError}
            tools={tools}
            onRetry={() => { void loadOptional(); }}
            onOpen={(nextTab) => changeTab(nextTab)}
          />
        )}
        {tab === "chat" && (
          <ChatSettingsPanel
            draft={settingsDraft}
            models={modelOptions}
            chatModels={chatModelOptions}
            registryError={registryError}
            saving={savingSettings}
            dirty={settingsDirty}
            onChange={updateSetting}
            onSave={saveSettings}
            onCancel={cancelSettings}
          />
        )}
        {tab === "memory" && (
          <MemoryPanel chatId={chatId} onToast={pushToast} />
        )}
        {tab === "pool" && (
          <PoolPanel
            chatId={chatId}
            draft={poolDraft}
            models={models}
            modelByRef={modelByRef}
            registryError={registryError}
            status={status}
            statusError={statusError}
            dispatches={dispatches}
            dispatchError={dispatchError}
            saving={savingPool}
            dirty={poolDirty}
            onChange={setPoolDraft}
            onSave={savePool}
            onCancel={cancelPool}
            onRefreshStatus={reloadRuntime}
          />
        )}
        {tab === "tools" && (
          <ToolsPanel tools={tools} error={toolsError} policy={policy} onOpenSettings={() => changeTab("chat")} />
        )}
      </div>
    </AdminShell>
  );
}

function OverviewPanel({
  policy,
  defaults,
  status,
  statusError,
  tools,
  onRetry,
  onOpen,
}: {
  policy: AssistantPolicy;
  defaults: AssistantDefaults;
  status: AssistantStatus | null;
  statusError: string | null;
  tools: AssistantToolsResponse | null;
  onRetry: () => void;
  onOpen: (tab: AssistantTab) => void;
}) {
  return (
    <div className="grid gap-4 xl:grid-cols-[minmax(0,1.4fr)_minmax(300px,0.8fr)]">
      <div className="space-y-4">
        <Card>
          <CardHeader><CardTitle>实际状态</CardTitle><CardDescription>仅展示群助手 API 已返回的策略与运行状态，不读取或改变原审核策略。</CardDescription></CardHeader>
          <CardBody className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <StatusTile label="聊天" value={policy.chat_enabled ? "已启用" : "已关闭"} tone={policy.chat_enabled ? "success" : "default"} detail="仅 @、回复 Bot 或连续追问" />
            <StatusTile label="普通群聊学习" value={policy.learning_enabled ? "已启用" : "已关闭"} tone={policy.learning_enabled ? "success" : "default"} detail="审核通过后独立提炼" />
            <StatusTile label="原文保留" value={policy.retention_days ? `${policy.retention_days} 天` : "未知"} detail="可配置 1–30 天" />
            <StatusTile label="本地队列" value={status ? String(status.queue_depth) : "未知"} detail={status?.remote_quota_note ?? defaults.remote_quota ?? "无数据"} />
          </CardBody>
        </Card>
        <Card>
          <CardHeader><CardTitle>启用前说明</CardTitle><CardDescription>首次启用不会改变旧群审核、权限或 Telegram 数据。</CardDescription></CardHeader>
          <CardBody className="space-y-3 text-sm text-[var(--text-muted)]">
            <div className="flex gap-3"><ShieldCheck className="mt-0.5 h-4 w-4 shrink-0 text-[var(--success)]" /><p>聊天和学习是两个独立开关；新群默认关闭。聊天只响应 @/reply 和同群、同话题、同用户的窗口内连续追问，不主动插话。</p></div>
            <div className="flex gap-3"><Database className="mt-0.5 h-4 w-4 shrink-0 text-[var(--accent)]" /><p>普通群聊学习只收录已通过审核且在策略保留期内的文本，长期只保留可追溯事实及必要出处摘要。</p></div>
            <div className="flex gap-3"><Info className="mt-0.5 h-4 w-4 shrink-0 text-[var(--warning)]" /><p>历史默认政策：{valueOrUnknown(defaults.history_retention)}；忘记操作只移出群助手本地召回范围，不删除 Telegram 远端原文。</p></div>
            <div className="flex flex-wrap gap-2 pt-1"><Button type="button" size="sm" onClick={() => onOpen("chat")}>配置聊天与学习</Button><Button type="button" size="sm" variant="secondary" onClick={() => onOpen("memory")}>打开记忆中心</Button></div>
          </CardBody>
        </Card>
      </div>
      <div className="space-y-4">
        <Card>
          <CardHeader className="flex-row items-center justify-between"><div><CardTitle>只读技能概览</CardTitle><CardDescription>能力来源于实际 tools API。</CardDescription></div><Button type="button" variant="ghost" size="sm" onClick={() => onOpen("tools")}>详情</Button></CardHeader>
          <CardBody>
            {tools ? <div className="space-y-2">{tools.tools.map((tool) => <div key={tool.name} className="flex items-center justify-between gap-3 rounded-xl border border-[var(--border)] px-3 py-2"><span className="text-sm">{TOOL_LABELS[tool.name as AssistantToolName] ?? tool.name}</span><Badge tone={tool.enabled ? "success" : "default"}>{tool.enabled ? "启用" : "关闭"}</Badge></div>)}<p className="mt-3 text-xs text-[var(--text-muted)]">写工具：{tools.write_tools.length === 0 ? "无" : tools.write_tools.join("、")} · 服务端绑定范围：{tools.server_bound_scope ? "是" : "未知"}</p></div> : <ApiState label="工具状态无数据" error="未将原型沙箱按钮当作真实执行。" onRetry={onRetry} />}
          </CardBody>
        </Card>
        <Card>
          <CardHeader><CardTitle>群内使用方式</CardTitle><CardDescription>真实 Bot 在 Telegram 执行，后台不模拟发送界面。</CardDescription></CardHeader>
          <CardBody className="space-y-2 text-sm text-[var(--text-muted)]"><p>1. 在聊天配置中分别启用聊天或学习，并选择服务端 registry 中的模型。</p><p>2. 群内使用 @Bot 或回复 Bot 开始提问；窗口内连续追问按服务端会话隔离。</p><p>3. 技能执行、审核门禁、来源权威和群 scope 均由服务端最终决定。</p></CardBody>
        </Card>
        <Card>
          <CardHeader><CardTitle>运行状态</CardTitle></CardHeader>
          <CardBody>{status ? <div className="space-y-2 text-sm"><div className="flex justify-between gap-3"><span className="text-[var(--text-muted)]">调度策略</span><span className="font-medium">{valueOrUnknown(status.active_strategy)}</span></div><div className="flex justify-between gap-3"><span className="text-[var(--text-muted)]">远程配额</span><span className="text-right">{valueOrUnknown(status.remote_quota_note)}</span></div><div className="flex justify-between gap-3"><span className="text-[var(--text-muted)]">最近路由</span><span className="text-right">{status.last_dispatch_event ? `${status.last_dispatch_event.task_type} · ${status.last_dispatch_event.selected_endpoint_id}` : "未知"}</span></div></div> : <ApiState label="状态 API 无数据" error={statusError ?? "未知"} onRetry={onRetry} />}</CardBody>
        </Card>
      </div>
    </div>
  );
}

function StatusTile({ label, value, detail, tone = "default" }: { label: string; value: string; detail: string; tone?: "success" | "warning" | "danger" | "default" }) {
  return <div className="rounded-xl border border-[var(--border)] bg-[var(--surface-2)] p-3"><div className="mb-2 flex items-center justify-between gap-2"><span className="text-xs text-[var(--text-muted)]">{label}</span><Badge tone={tone}>{value}</Badge></div><p className="text-[11px] leading-relaxed text-[var(--text-subtle)]">{detail}</p></div>;
}

function ChatSettingsPanel({
  draft,
  models,
  chatModels,
  registryError,
  saving,
  dirty,
  onChange,
  onSave,
  onCancel,
}: {
  draft: PolicyDraft;
  models: RegistryModel[];
  chatModels: RegistryModel[];
  registryError: string | null;
  saving: boolean;
  dirty: boolean;
  onChange: <K extends keyof PolicyDraft>(key: K, value: PolicyDraft[K]) => void;
  onSave: () => void;
  onCancel: () => void;
}) {
  const modelOption = (model: RegistryModel) => `${model.label || model.ref} · ${model.ref}`;
  const chatModelRefs = new Set(chatModels.map((model) => model.ref));
  const learningModelRefs = new Set(models.map((model) => model.ref));
  const currentChatMissing = Boolean(draft.chat_model_ref && !chatModelRefs.has(draft.chat_model_ref));
  const currentLearningMissing = Boolean(draft.learning_model_ref && !learningModelRefs.has(draft.learning_model_ref));
  return (
    <div className="space-y-4">
      <fieldset disabled={saving} aria-busy={saving} className={cn("space-y-4 border-0 p-0", saving && "opacity-70")}>
      <Card>
        <CardHeader><SectionTitle icon={MessageCircle} title="聊天与学习开关" description="两个开关独立保存；服务端最终验证群 scope、模型能力和权限。" /></CardHeader>
        <CardBody className="grid gap-4 md:grid-cols-2">
          <div className="flex items-start justify-between gap-4 rounded-xl border border-[var(--border)] bg-[var(--surface-2)] p-4"><div><p className="text-sm font-medium">聊天回复</p><p className="mt-1 text-xs leading-relaxed text-[var(--text-muted)]">响应 mention_or_reply 或 mention_only；连续追问受窗口和次数限制。</p></div><Switch checked={draft.chat_enabled} onCheckedChange={(value) => onChange("chat_enabled", value)} aria-label="启用聊天" /></div>
          <div className="flex items-start justify-between gap-4 rounded-xl border border-[var(--border)] bg-[var(--surface-2)] p-4"><div><p className="text-sm font-medium">普通群聊自动学习</p><p className="mt-1 text-xs leading-relaxed text-[var(--text-muted)]">仅对审核通过消息进入独立学习队列；不会改变审核策略。</p></div><Switch checked={draft.learning_enabled} onCheckedChange={(value) => onChange("learning_enabled", value)} aria-label="启用普通群聊自动学习" /></div>
        </CardBody>
      </Card>
      <Card>
        <CardHeader><CardTitle>触发与会话</CardTitle><CardDescription>追问隔离键由服务端使用 chat_id + thread_id + sender_id 绑定，后台不注入身份。</CardDescription></CardHeader>
        <CardBody className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <Field label="触发模式"><Select value={draft.trigger_mode} onChange={(event) => onChange("trigger_mode", event.target.value)}><option value="mention_or_reply">@ 或回复 Bot</option><option value="mention_only">仅 @ Bot</option></Select></Field>
          <Field label="追问窗口（秒）" hint="服务端范围 30–3600"><Input type="number" min={30} max={3600} value={draft.followup_window_sec} onChange={(event) => onChange("followup_window_sec", Number(event.target.value))} /></Field>
          <Field label="最多追问轮次" hint="服务端范围 1–20"><Input type="number" min={1} max={20} value={draft.max_followup_turns} onChange={(event) => onChange("max_followup_turns", Number(event.target.value))} /></Field>
          <Field label="历史上下文条数" hint="服务端范围 1–200"><Input type="number" min={1} max={200} value={draft.history_limit} onChange={(event) => onChange("history_limit", Number(event.target.value))} /></Field>
        </CardBody>
      </Card>
      <Card>
        <CardHeader><CardTitle>模型引用与生成参数</CardTitle><CardDescription>模型只能从 registry 引用；此页不接受 base URL、key 或任意 provider 配置。</CardDescription></CardHeader>
        <CardBody className="grid gap-4 md:grid-cols-2">
          <Field label="聊天模型（必须支持 tools）" hint={registryError ?? "chat 模型由服务端 SupportsTools 硬门槛校验"}>
            <select className="h-10 w-full rounded-xl border border-[var(--input-border)] bg-[var(--input-bg)] px-3 text-sm text-[var(--input-color)]" value={draft.chat_model_ref} onChange={(event) => onChange("chat_model_ref", event.target.value)}>
              <option value="">不指定（由服务端策略处理）</option>
              {currentChatMissing && <option value={draft.chat_model_ref}>{draft.chat_model_ref} · 当前引用（registry 无此能力）</option>}
              {chatModels.map((model) => <option key={model.ref} value={model.ref}>{modelOption(model)}</option>)}
            </select>
          </Field>
          <Field label="学习模型" hint={registryError ?? "learning 可使用 enabled registry 模型"}>
            <select className="h-10 w-full rounded-xl border border-[var(--input-border)] bg-[var(--input-bg)] px-3 text-sm text-[var(--input-color)]" value={draft.learning_model_ref} onChange={(event) => onChange("learning_model_ref", event.target.value)}>
              <option value="">不指定（由服务端策略处理）</option>
              {currentLearningMissing && <option value={draft.learning_model_ref}>{draft.learning_model_ref} · 当前引用（registry 无此能力）</option>}
              {models.map((model) => <option key={model.ref} value={model.ref}>{modelOption(model)}</option>)}
            </select>
          </Field>
          <Field label="temperature" hint="服务端范围 0–2；独立于 system prompt"><Input type="number" min={0} max={2} step={0.1} value={draft.temperature} onChange={(event) => onChange("temperature", Number(event.target.value))} /></Field>
          <Field label="原文保留天数" hint="服务端范围 1–30；长期事实不等于保留原文"><Input type="number" min={1} max={30} value={draft.retention_days} onChange={(event) => onChange("retention_days", Number(event.target.value))} /></Field>
          <Field label="最大本地队列深度"><Input type="number" min={0} max={100} value={draft.max_queue_depth} onChange={(event) => onChange("max_queue_depth", Number(event.target.value))} /></Field>
          <Field label="最大排队等待（秒）"><Input type="number" min={1} max={60} value={draft.max_queue_wait_sec} onChange={(event) => onChange("max_queue_wait_sec", Number(event.target.value))} /></Field>
          <Field label="collection_policy" className="md:col-span-2"><Input value={draft.collection_policy} onChange={(event) => onChange("collection_policy", event.target.value)} /></Field>
          <Field label="system prompt" hint="服务端最多 8000 字符；保存时按 CAS 提交" className="md:col-span-2"><Textarea rows={7} value={draft.system_prompt} onChange={(event) => onChange("system_prompt", event.target.value)} /></Field>
        </CardBody>
      </Card>
      <Card>
        <CardHeader><CardTitle>只读技能 allowlist</CardTitle><CardDescription>allowlist 是策略草稿；tools API 返回的 server_bound_scope 和只读能力仍是最终权威。</CardDescription></CardHeader>
        <CardBody className="space-y-4">
          <div className="grid gap-2 sm:grid-cols-3">{TOOL_NAMES.map((tool) => { const checked = draft.tool_allowlist.includes(tool); return <label key={tool} className="flex cursor-pointer items-center gap-2 rounded-xl border border-[var(--border)] p-3 text-sm"><input type="checkbox" checked={checked} onChange={(event) => onChange("tool_allowlist", event.target.checked ? [...draft.tool_allowlist, tool] : draft.tool_allowlist.filter((item) => item !== tool))} />{TOOL_LABELS[tool]}</label>; })}</div>
          <Field label="网页域名白名单" hint="仅公开 GET 读取；不要填写 URL、路径、认证信息。空列表由服务端按策略处理。"><Textarea rows={4} value={draft.allow_domains.join("\n")} onChange={(event) => onChange("allow_domains", event.target.value.split("\n").map((item) => item.trim()).filter(Boolean))} placeholder="docs.example.com" /></Field>
        </CardBody>
      </Card>
      </fieldset>
      <SaveBar dirty={dirty} saving={saving} onSave={onSave} onCancel={onCancel} scope="聊天与学习配置" />
    </div>
  );
}

function SaveBar({ dirty, saving, onSave, onCancel, scope }: { dirty: boolean; saving: boolean; onSave: () => void; onCancel: () => void; scope: string }) {
  return <div className="miniapp-savebar sticky bottom-3 z-10 flex flex-col gap-3 rounded-2xl border border-[var(--border)] bg-[var(--savebar-bg)] p-3 shadow-xl backdrop-blur-xl sm:flex-row sm:items-center sm:justify-between"><div className="flex items-center gap-2 text-xs text-[var(--text-muted)]"><Save className="h-4 w-4" />{dirty ? `${scope}有未保存修改` : `${scope}与服务端一致`}</div><div className="flex gap-2"><Button type="button" variant="secondary" size="sm" disabled={!dirty || saving} onClick={onCancel}><RotateCcw className="h-3.5 w-3.5" />取消草稿</Button><Button type="button" size="sm" disabled={!dirty || saving} onClick={onSave}>{saving ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}{saving ? "保存中…" : "按版本保存"}</Button></div></div>;
}

function MemoryPanel({ chatId, onToast }: { chatId: number; onToast: (message: string, tone?: "success" | "error") => void }) {
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
      setMemoryError(errorText(error, "记忆列表加载失败"));
      if (error instanceof ApiError && error.status === 403) setConflictError("当前管理员没有冲突查看能力");
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
      setHistoryNotice(result.expired_auto_removed ? "服务端已自动排除超过保留期的原文。" : "服务端未返回过期清理说明。");
    } catch (error) {
      if (!isCurrent() || controller.signal.aborted || isAbortError(error)) return;
      setHistoryError(errorText(error, "历史检索失败"));
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
      const expired = Boolean(memory.expires_at && !Number.isNaN(new Date(memory.expires_at).getTime()) && new Date(memory.expires_at).getTime() <= now);
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

  const openMemoryDetail = async (memory: AssistantMemory, trigger?: HTMLElement) => {
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
      onToast(errorText(error, "来源详情加载失败"), "error");
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
      onToast(errorText(error, "版本链加载失败"), "error");
    } finally {
      if (isCurrent()) editorVersionsAbortRef.current = null;
    }
  };

  const afterMemorySaved = async () => {
    closeEditor();
    await loadMemories();
  };

  const forgetMemory = async (memory: AssistantMemory) => {
    if (!window.confirm(`确定忘记“${memory.subject}”吗？这只会移出群助手本地召回/检索范围，不会删除 Telegram 远端原消息。`)) return;
    try {
      const result = await forgetAssistantMemory(chatId, memory.id);
      onToast(result.remote_telegram_deleted ? "记忆已忘记" : "已移出本地召回范围；Telegram 原文未删除", "success");
      await loadMemories();
    } catch (error) {
      onToast(errorText(error, "忘记记忆失败"), "error");
    }
  };

  const resolveConflict = async (conflict: AssistantConflict, accept: boolean) => {
    const action = accept
      ? `接受候选事实，并以当前管理员明确纠正“${conflict.subject}”`
      : `拒绝候选事实“${conflict.subject}”`;
    if (!window.confirm(`确定${action}吗？`)) return;
    try {
      if (accept) {
        if (conflict.memory_id == null) {
          onToast(`冲突“${conflict.subject}”没有目标记忆，无法以当前管理员明确纠正。`, "error");
          return;
        }
        const current = await fetchAssistantMemory(chatId, conflict.memory_id);
        const expectedVersion = current.memory.version;
        if (!Number.isInteger(expectedVersion) || expectedVersion <= 0) {
          onToast(`冲突“${conflict.subject}”的目标记忆版本未知，未猜测版本号，请刷新后重试。`, "error");
          return;
        }
        await resolveAssistantConflict(chatId, conflict.id, {
          accept: true,
          expected_memory_version: expectedVersion,
          resolution_mode: "admin_explicit_correction",
        });
      } else {
        await resolveAssistantConflict(chatId, conflict.id, { accept: false });
      }
      onToast(accept ? `冲突已接受，并已以当前管理员明确纠正“${conflict.subject}”` : "冲突已拒绝", "success");
      await loadMemories();
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) {
        onToast(`冲突“${conflict.subject}”版本已变化，请刷新记忆列表后重新处理；本次未成功接受。`, "error");
        await loadMemories();
        return;
      }
      onToast(errorText(error, "处理冲突失败"), "error");
    }
  };

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader className="flex-row flex-wrap items-start justify-between gap-3"><div><SectionTitle icon={Database} title="记忆中心" description="列表、来源、版本与冲突均来自当前群 API；普通成员字段只读展示。" /></div><Button type="button" size="sm" onClick={(event) => { void openEditor(null, event.currentTarget); }}><Plus className="h-3.5 w-3.5" />新增基础事实</Button></CardHeader>
        <CardBody className="space-y-4">
          <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_180px_auto]"><Field label="搜索 subject / content"><div className="flex gap-2"><Input value={query} onChange={(event) => setQuery(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") void loadMemories(); }} placeholder="搜索当前群记忆" /><Button type="button" variant="secondary" size="sm" onClick={() => { void loadMemories(); }}><Search className="h-3.5 w-3.5" />检索</Button></div></Field><Field label="列表筛选"><select className="h-10 w-full rounded-xl border border-[var(--input-border)] bg-[var(--input-bg)] px-3 text-sm text-[var(--input-color)]" value={memoryFilter} onChange={(event) => setMemoryFilter(event.target.value as MemoryFilter)}><option value="active">当前有效</option><option value="base">base 基础</option><option value="learned">learned 学习</option><option value="pending">pending 待处理</option><option value="expired">已过期</option><option value="inactive">inactive 已忘记</option></select></Field><div className="flex items-end"><Button type="button" variant="ghost" size="sm" onClick={() => { void loadMemories(); }}><RefreshCw className="h-3.5 w-3.5" />刷新</Button></div></div>
          <div className="flex flex-wrap gap-2 text-xs text-[var(--text-muted)]"><span className="rounded-lg border border-[var(--border)] px-2 py-1">API 返回 {memories.length} 条</span><span className="rounded-lg border border-[var(--border)] px-2 py-1">当前视图 {visibleMemories.length} 条</span>{memoryNotice && <span className="rounded-lg border border-[var(--border)] px-2 py-1">{memoryNotice}</span>}</div>
          {memoryError ? <ApiState label="记忆列表无数据" error={memoryError} onRetry={() => { void loadMemories(); }} /> : loading ? <div className="py-8 text-center text-sm text-[var(--text-muted)]"><Loader2 className="mx-auto mb-2 h-4 w-4 animate-spin" />正在加载记忆…</div> : visibleMemories.length === 0 ? <EmptyState title="当前筛选没有 API 返回的记忆" detail="不会用原型中的示例事实填充此列表。" /> : <MemoryTable memories={visibleMemories} onDetail={openMemoryDetail} onEdit={openEditor} onForget={forgetMemory} />}
        </CardBody>
      </Card>

      <Card>
        <CardHeader><SectionTitle icon={AlertTriangle} title="待处理冲突" description="仅显示服务端标记为 pending 的候选事实；接受/拒绝由服务端按事务处理。" /></CardHeader>
        <CardBody>{conflictError ? <ApiState label="冲突列表无数据" error={conflictError} onRetry={() => { void loadMemories(); }} /> : conflicts.length === 0 ? <EmptyState title="没有待处理冲突" detail="未从 conflicts API 返回 pending 记录。" /> : <div className="space-y-3">{conflicts.map((conflict) => <ConflictRow key={conflict.id} conflict={conflict} onResolve={resolveConflict} />)}</div>}</CardBody>
      </Card>

      <Card>
        <CardHeader><SectionTitle icon={History} title="历史检索" description="仅检索当前群、仍在真实保留期内且已审核送达的原文。" /></CardHeader>
        <CardBody className="space-y-4">
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4"><Field label="关键词"><Input value={historyQuery} onChange={(event) => setHistoryQuery(event.target.value)} /></Field><Field label="thread_id"><Input inputMode="numeric" value={historyThread} onChange={(event) => setHistoryThread(event.target.value)} placeholder="可选" /></Field><Field label="sender_id"><Input inputMode="numeric" value={historySender} onChange={(event) => setHistorySender(event.target.value)} placeholder="可选" /></Field><div className="flex items-end"><Button type="button" onClick={() => { void loadHistory(); }} disabled={loadingHistory}><Search className="h-3.5 w-3.5" />{loadingHistory ? "检索中…" : "检索历史"}</Button></div></div>
          {historyNotice && <p className="text-xs text-[var(--text-muted)]">{historyNotice} 保留天数：{historyRetention ?? "未知"}</p>}
          {historyError ? <ApiState label="历史无数据" error={historyError} onRetry={() => { void loadHistory(); }} /> : history.length === 0 ? <EmptyState title="没有 API 返回的历史消息" detail="过期原文不会在后台伪造展示。" /> : <HistoryTable history={history} />}
        </CardBody>
      </Card>

      {selectedMemory && <MemoryDetailDialog memory={selectedMemory} versions={selectedVersions} loading={detailLoading} onClose={() => dismissSelectedMemory()} onEdit={() => { const memoryToEdit = selectedMemory; const detailTrigger = selectedTriggerRef.current; dismissSelectedMemory(false); void openEditor(memoryToEdit, detailTrigger); }} />}
      {editorOpen && <MemoryEditorDialog chatId={chatId} memory={editorMemory} versions={editorVersions} onClose={() => closeEditor()} onSaved={afterMemorySaved} onToast={onToast} />}
    </div>
  );
}

function sourceType(source: { source_type?: string; type?: string }) {
  return source.source_type || source.type || "未知";
}

function sourceMessageId(source: { source_message_id?: number | null; message_id?: number | null }) {
  return source.source_message_id ?? source.message_id ?? null;
}

function sourceChatId(source: { source_chat_id?: number | null; chat_id?: number | null }) {
  return source.source_chat_id ?? source.chat_id ?? null;
}

function MemoryTable({ memories, onDetail, onEdit, onForget }: { memories: AssistantMemory[]; onDetail: (memory: AssistantMemory, trigger: HTMLElement) => void; onEdit: (memory: AssistantMemory, trigger: HTMLElement) => void; onForget: (memory: AssistantMemory) => void }) {
  return <div className="overflow-x-auto rounded-xl border border-[var(--border)]"><table className="w-full min-w-[820px] text-left text-xs"><thead className="bg-[var(--table-th-bg)] text-[var(--text-muted)]"><tr><th className="px-3 py-3 font-medium">事实</th><th className="px-3 py-3 font-medium">类型 / 权威</th><th className="px-3 py-3 font-medium">作用域</th><th className="px-3 py-3 font-medium">来源</th><th className="px-3 py-3 font-medium">有效期</th><th className="px-3 py-3 font-medium">操作</th></tr></thead><tbody>{memories.map((memory) => { const expired = memory.expires_at && new Date(memory.expires_at).getTime() <= Date.now(); return <tr key={memory.id} className="border-t border-[var(--border)] align-top hover:bg-[var(--table-hover)]"><td className="max-w-[280px] px-3 py-3"><p className="font-medium">{memory.subject}</p><p className="mt-1 line-clamp-3 text-[var(--text-muted)]">{memory.content}</p><p className="mt-1 text-[var(--text-subtle)]">v{memory.version} · {memory.active ? "active" : "inactive"}</p></td><td className="px-3 py-3"><Badge>{memory.memory_type}</Badge><p className="mt-2 text-[var(--text-muted)]">{memory.authority_level || "未知"}</p></td><td className="max-w-[150px] px-3 py-3 text-[var(--text-muted)]">{memory.valid_scope || "未知"}</td><td className="max-w-[190px] px-3 py-3"><p>{sourceType(memory.source)}</p><p className="mt-1 text-[var(--text-muted)]">{memory.source.operator_name || (sourceMessageId(memory.source) != null ? `消息 ${sourceMessageId(memory.source)}` : "来源未知")}</p><p className="mt-1 text-[var(--text-subtle)]">校验：{memory.source.verified || "未知"}{memory.source.currently_verified === undefined ? "" : ` · currently_verified=${String(memory.source.currently_verified)}`}</p></td><td className="px-3 py-3 text-[var(--text-muted)]"><span className={expired ? "text-[var(--warning)]" : undefined}>{formatDate(memory.expires_at)}</span>{expired && <span className="mt-1 block">已过期</span>}</td><td className="px-3 py-3"><div className="flex flex-wrap gap-1"><Button type="button" variant="secondary" size="sm" onClick={(event) => onDetail(memory, event.currentTarget)}>来源</Button><Button type="button" variant="secondary" size="sm" onClick={(event) => onEdit(memory, event.currentTarget)}><Pencil className="h-3 w-3" />编辑</Button>{memory.active && <Button type="button" variant="danger" size="sm" onClick={() => onForget(memory)}><Trash2 className="h-3 w-3" />忘记</Button>}</div></td></tr>; })}</tbody></table></div>;
}

function ConflictRow({ conflict, onResolve }: { conflict: AssistantConflict; onResolve: (conflict: AssistantConflict, accept: boolean) => void }) {
  const messageId = sourceMessageId(conflict.source);
  const chatId = sourceChatId(conflict.source);
  return <div className="rounded-xl border border-[var(--warning)]/30 bg-[var(--warning-soft)]/40 p-4"><div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between"><div className="min-w-0"><div className="flex flex-wrap items-center gap-2"><Badge tone="warning">{conflict.status}</Badge><span className="font-medium">{conflict.subject}</span></div><p className="mt-2 text-sm">{conflict.candidate_content}</p><p className="mt-2 text-xs text-[var(--text-muted)]">候选权威：{conflict.candidate_authority || "未知"} · scope：{conflict.candidate_scope || "未知"} · 来源：{sourceType(conflict.source)} · 原消息：{valueOrUnknown(messageId)} · 来源群：{valueOrUnknown(chatId)}</p>{conflict.source.snippet && <p className="mt-2 rounded-lg border border-[var(--border)] bg-[var(--surface)] p-2 text-xs text-[var(--text-muted)]">{conflict.source.snippet}</p>}<p className="mt-2 text-xs text-[var(--text-muted)]">接受后仅会以当前管理员明确纠正“{conflict.subject}”，普通来源不会自动升权；服务端会保留 {sourceType(conflict.source)} 等原始来源字段。</p></div><div className="flex shrink-0 gap-2"><Button type="button" size="sm" onClick={() => onResolve(conflict, true)}><Check className="h-3.5 w-3.5" />接受</Button><Button type="button" variant="secondary" size="sm" onClick={() => onResolve(conflict, false)}><X className="h-3.5 w-3.5" />拒绝</Button></div></div></div>;
}

function HistoryTable({ history }: { history: AssistantHistoryMessage[] }) {
  return <div className="overflow-x-auto rounded-xl border border-[var(--border)]"><table className="w-full min-w-[780px] text-left text-xs"><thead className="bg-[var(--table-th-bg)] text-[var(--text-muted)]"><tr><th className="px-3 py-3 font-medium">时间</th><th className="px-3 py-3 font-medium">发送者 / 线程</th><th className="px-3 py-3 font-medium">角色</th><th className="px-3 py-3 font-medium">原文</th><th className="px-3 py-3 font-medium">来源与有效期</th></tr></thead><tbody>{history.map((item) => <tr key={item.id} className="border-t border-[var(--border)] align-top hover:bg-[var(--table-hover)]"><td className="whitespace-nowrap px-3 py-3 text-[var(--text-muted)]">{formatDate(item.created_at)}</td><td className="px-3 py-3"><p>{item.sender_name || "未知"}</p><p className="mt-1 text-[var(--text-subtle)]">sender {item.sender_id} · thread {item.thread_id}</p></td><td className="px-3 py-3"><Badge>{item.role}</Badge></td><td className="max-w-[360px] whitespace-pre-wrap px-3 py-3">{item.text}</td><td className="px-3 py-3 text-[var(--text-muted)]"><p>{item.source.type || "未知"} · {item.source.id || "未知"}</p><p className="mt-1">{item.delivered ? "已送达" : "未送达"} · {item.approved ? "已审核" : "审核状态未知"}</p><p className="mt-1">到期：{formatDate(item.expires_at)}</p></td></tr>)}</tbody></table></div>;
}

function EmptyState({ title, detail }: { title: string; detail: string }) {
  return <div className="rounded-xl border border-dashed border-[var(--border-strong)] p-8 text-center"><p className="text-sm font-medium">{title}</p><p className="mt-1 text-xs text-[var(--text-muted)]">{detail}</p></div>;
}

function MemoryDetailDialog({ memory, versions, loading, onClose, onEdit }: { memory: AssistantMemory; versions: AssistantMemoryVersion[]; loading: boolean; onClose: () => void; onEdit: () => void }) {
  const dialogRef = useModalFocusTrap(onClose);
  return <div className="fixed inset-0 z-50 flex items-end justify-center bg-black/45 p-0 backdrop-blur-sm sm:items-center sm:p-4" role="dialog" aria-modal="true"><div ref={dialogRef} tabIndex={-1} className="max-h-[90vh] w-full max-w-3xl overflow-y-auto rounded-t-3xl border border-[var(--border)] bg-[var(--dialog-bg)] p-5 shadow-2xl sm:rounded-3xl"><div className="flex items-start justify-between gap-4"><div><p className="text-[10px] uppercase tracking-[0.16em] text-[var(--accent)]">Source of truth</p><h2 className="mt-1 text-lg font-semibold">{memory.subject}</h2><p className="mt-1 text-xs text-[var(--text-muted)]">记忆 ID {memory.id} · 当前版本 v{memory.version}</p></div><Button type="button" variant="ghost" size="sm" onClick={onClose} aria-label="关闭" autoFocus><X className="h-4 w-4" /></Button></div><div className="mt-5 grid gap-4 md:grid-cols-2"><div className="space-y-3"><div className="rounded-xl border border-[var(--border)] bg-[var(--surface-2)] p-3 text-sm whitespace-pre-wrap">{memory.content}</div><div className="grid gap-2 text-xs text-[var(--text-muted)]"><p>类型：{memory.memory_type} · 权威：{memory.authority_level || "未知"}</p><p>作用域：{memory.valid_scope || "未知"}</p><p>有效至：{formatDate(memory.expires_at)} · active：{String(memory.active)}</p></div></div><div className="space-y-3 text-xs"><div className="rounded-xl border border-[var(--border)] p-3"><p className="mb-2 font-medium">来源（服务端字段，只读）</p><p>source_type：{sourceType(memory.source)}</p><p>消息 ID：{valueOrUnknown(sourceMessageId(memory.source))}</p><p>source_chat_id：{valueOrUnknown(sourceChatId(memory.source))}</p><p>operator：{memory.source.operator_name || valueOrUnknown(memory.source.operator_id)}</p><p>校验：{memory.source.verified || "未知"}{memory.source.currently_verified === undefined ? "" : ` · currently_verified=${String(memory.source.currently_verified)}`}</p>{memory.source.snippet && <p className="mt-2 rounded-lg bg-[var(--surface-2)] p-2 text-[var(--text-muted)]">{memory.source.snippet}</p>}</div><div className="rounded-xl border border-[var(--warning)]/30 bg-[var(--warning-soft)]/40 p-3">来源过期或未验证时，后台不会把它升级成 admin / pinned 权威；置顶权威由服务端再次核验。管理员更正会以当前管理员明确纠正“{memory.subject}”，普通来源不会自动升权。</div></div></div><div className="mt-5"><div className="mb-2 flex items-center justify-between"><h3 className="text-sm font-semibold">版本链</h3>{loading && <Loader2 className="h-4 w-4 animate-spin text-[var(--accent)]" />}</div>{versions.length === 0 ? <p className="text-xs text-[var(--text-muted)]">暂无 API 返回版本。</p> : <div className="space-y-2">{versions.map((version) => <div key={version.id} className="rounded-xl border border-[var(--border)] p-3 text-xs"><div className="flex flex-wrap gap-2"><Badge>v{version.version}</Badge><span>{version.change_kind}</span><span className="text-[var(--text-muted)]">{formatDate(version.created_at)}</span></div><p className="mt-2 whitespace-pre-wrap">{version.content}</p><p className="mt-1 text-[var(--text-muted)]">{version.authority_level} · {version.valid_scope || "未知"} · {version.source_type}</p></div>)}</div>}</div><div className="mt-5 flex justify-end gap-2"><Button type="button" variant="secondary" onClick={onClose}>关闭</Button><Button type="button" onClick={onEdit}><Pencil className="h-3.5 w-3.5" />管理员更正</Button></div></div></div>;
}

function MemoryEditorDialog({ chatId, memory, versions, onClose, onSaved, onToast }: { chatId: number; memory: AssistantMemory | null; versions: AssistantMemoryVersion[]; onClose: () => void; onSaved: () => Promise<void>; onToast: (message: string, tone?: "success" | "error") => void }) {
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
  useDirtyGuard(dirty, "记忆更正草稿尚未保存，确定离开吗？", "assistant-memory");

  const close = useCallback(() => {
    if (saving || savingRef.current) return;
    if (!confirmNavigation("记忆更正草稿尚未保存，确定关闭吗？", "assistant-memory")) return;
    onClose();
  }, [confirmNavigation, onClose, saving]);
  const dialogRef = useModalFocusTrap(close);

  const save = async () => {
    if (!subject.trim() || !content.trim() || !scope.trim() || saving || savingRef.current) return;
    const normalizedScope = normalizeMemoryScope(scope);
    if (!normalizedScope) {
      onToast("有效范围必须使用服务端支持的范围（today、this_week、this_month、current_group、long_term、weekly 或 retention_window）。", "error");
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
      onToast(error instanceof ApiError && error.status === 409 ? "记忆版本冲突：草稿保留，请重新打开当前版本比较。" : errorText(error, "保存记忆失败"), "error");
    } finally {
      savingRef.current = false;
      setSaving(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-end justify-center bg-black/45 p-0 backdrop-blur-sm sm:items-center sm:p-4" role="dialog" aria-modal="true">
      <div ref={dialogRef} tabIndex={-1} aria-busy={saving} className="max-h-[92vh] w-full max-w-3xl overflow-y-auto rounded-t-3xl border border-[var(--border)] bg-[var(--dialog-bg)] p-5 shadow-2xl sm:rounded-3xl">
        <div className="flex items-start justify-between gap-4">
          <div>
            <p className="text-[10px] uppercase tracking-[0.16em] text-[var(--accent)]">{memory ? "Admin correction" : "Admin base fact"}</p>
            <h2 className="mt-1 text-lg font-semibold">{memory ? "更正长期记忆" : "新增预置基础事实"}</h2>
            <p className="mt-1 text-xs text-[var(--text-muted)]">本次{memory ? `会以当前管理员明确纠正“${memory.subject}”` : "会创建管理员基础事实"}；服务端注入真实管理员身份并决定 authority_level。表单不接受伪造 pinned、sender 或 Telegram message 来源，普通来源不会自动升权。</p>
          </div>
          <Button type="button" variant="ghost" size="sm" onClick={close} aria-label="关闭" autoFocus><X className="h-4 w-4" /></Button>
        </div>
        <fieldset disabled={saving} aria-busy={saving} className={cn("contents", saving && "opacity-70")}>
          <div className="mt-5 grid gap-4 md:grid-cols-2">
            <Field label="主题"><Input value={subject} onChange={(event) => setSubject(event.target.value)} maxLength={200} /></Field>
            <Field label="有效范围" hint="请输入 today、this_week、this_month、current_group、long_term、weekly、retention_window，或本周有效/本群等受支持别名"><Input value={scope} onChange={(event) => setScope(event.target.value)} maxLength={300} placeholder="例如：本周有效" /></Field>
            <Field label="事实内容" className="md:col-span-2"><Textarea rows={7} value={content} onChange={(event) => setContent(event.target.value)} maxLength={4000} /></Field>
            <Field label="到期时间" hint="留空由服务端设定默认期限；过期来源不应被当作当前权威"><Input type="datetime-local" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} /></Field>
            <Field label="来源摘要（可选）" hint="仅摘要说明，不可填写 source_message_id 伪造 Telegram 来源"><Textarea rows={3} value={snippet} onChange={(event) => setSnippet(event.target.value)} maxLength={1000} /></Field>
          </div>
          {memory && versions.length > 0 && <div className="mt-4 rounded-xl border border-[var(--border)] bg-[var(--surface-2)] p-3 text-xs text-[var(--text-muted)]">已有版本链 {versions.length} 条；提交使用当前 v{memory.version} CAS。</div>}
          <div className="mt-5 flex flex-col-reverse gap-2 sm:flex-row sm:justify-end"><Button type="button" variant="secondary" onClick={close}>取消</Button><Button type="button" onClick={() => { void save(); }} disabled={saving || !subject.trim() || !content.trim() || !scope.trim()}>{saving ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}{saving ? "保存中…" : "保存更正"}</Button></div>
        </fieldset>
      </div>
    </div>
  );
}

function PoolPanel({
  chatId,
  draft,
  models,
  modelByRef,
  registryError,
  status,
  statusError,
  dispatches,
  dispatchError,
  saving,
  dirty,
  onChange,
  onSave,
  onCancel,
  onRefreshStatus,
}: {
  chatId: number;
  draft: PoolDraft;
  models: RegistryModel[];
  modelByRef: Map<string, RegistryModel>;
  registryError: string | null;
  status: AssistantStatus | null;
  statusError: string | null;
  dispatches: AssistantDispatch[];
  dispatchError: string | null;
  saving: boolean;
  dirty: boolean;
  onChange: React.Dispatch<React.SetStateAction<PoolDraft | null>>;
  onSave: () => void;
  onCancel: () => void;
  onRefreshStatus: () => void;
}) {
  const enabledModels = models.filter((model) => model.enabled);
  const addEndpoint = () => {
    const model = enabledModels[0];
    if (!model) return;
    const id = `ep-${Date.now()}`;
    onChange((current) => {
      if (!current) return current;
      const endpoint: AssistantPoolEndpoint = { id, name: model.label, model_ref: model.ref, role: "backup", priority: current.config.endpoints.length, max_concurrency: 1, timeout_ms: 10000, cooldown_duration_sec: 30, supports_tools: model.supports_tools };
      return { ...current, config: { ...current.config, endpoints: [...current.config.endpoints, endpoint] } };
    });
  };
  const removeEndpoint = (id: string) => onChange((current) => {
    if (!current) return current;
    const assignments = { ...current.config.task_assignments };
    for (const task of ["chat", "learning"]) {
      const assignment = assignments[task] ?? emptyTaskAssignment();
      assignments[task] = { primary: assignment.primary === id ? "" : assignment.primary, backups: assignment.backups.filter((backup) => backup !== id) };
    }
    return { ...current, config: { ...current.config, task_assignments: assignments, endpoints: current.config.endpoints.filter((endpoint) => endpoint.id !== id) } };
  });
  const updateEndpoint = (id: string, patch: Partial<AssistantPoolEndpoint>) => onChange((current) => current ? { ...current, config: { ...current.config, endpoints: current.config.endpoints.map((endpoint) => endpoint.id === id ? { ...endpoint, ...patch } : endpoint) } } : current);
  const assignment = (task: string) => draft.config.task_assignments[task] ?? emptyTaskAssignment();
  const endpointSupportsTools = (endpoint: AssistantPoolEndpoint) => {
    const model = modelByRef.get(endpoint.model_ref);
    return model ? model.enabled && model.supports_tools : endpoint.supports_tools;
  };
  const endpointsForTask = (task: string) => task === "chat" ? draft.config.endpoints.filter(endpointSupportsTools) : draft.config.endpoints;
  const updateAssignment = (task: string, patch: Partial<{ primary: string; backups: string[] }>) => onChange((current) => current ? { ...current, config: { ...current.config, task_assignments: { ...current.config.task_assignments, [task]: { ...assignment(task), ...patch } } } } : current);
  const endpointLabel = (id: string) => { const endpoint = draft.config.endpoints.find((item) => item.id === id); return endpoint ? `${endpoint.id} · ${endpoint.model_ref}` : id; };

  return <div className="space-y-4">
    <fieldset disabled={saving} aria-busy={saving} className={cn("space-y-4 border-0 p-0", saving && "opacity-70")}>
    <Card><CardHeader className="flex-row items-start justify-between gap-3"><div><SectionTitle icon={RotateCcw} title="primary-overflow 模型池" description="固定主优先、容量满/429 冷却/故障时按备用顺序分流；审核流水线不进入此池。" /><GuardedLink href={`/groups/${chatId}`} className="inline-flex items-center gap-1 text-xs text-[var(--accent)] hover:underline"><ExternalLink className="h-3 w-3" />查看现有群审核策略（只读链接）</GuardedLink></div><Button type="button" size="sm" onClick={addEndpoint} disabled={!enabledModels.length}><Plus className="h-3.5 w-3.5" />从 registry 添加端点</Button></CardHeader><CardBody className="space-y-4"><div className="grid gap-3 sm:grid-cols-3"><Field label="策略"><Input value={draft.strategy} readOnly /></Field><Field label="最大队列深度"><Input type="number" min={0} max={100} value={draft.config.max_queue_depth} onChange={(event) => onChange((current) => current ? { ...current, config: { ...current.config, max_queue_depth: Number(event.target.value) } } : current)} /></Field><Field label="最大排队等待（秒）"><Input type="number" min={0} max={60} value={draft.config.max_queue_wait_sec} onChange={(event) => onChange((current) => current ? { ...current, config: { ...current.config, max_queue_wait_sec: Number(event.target.value) } } : current)} /></Field></div>{registryError && <div className="rounded-xl border border-[var(--warning)]/30 bg-[var(--warning-soft)]/40 p-3 text-xs text-[var(--text-muted)]"><AlertTriangle className="mr-1 inline h-3.5 w-3.5 text-[var(--warning)]" />{registryError} 不提供 base URL/key 输入；已保存的 endpoint 能力仍以服务端 registry 校验。</div>}{draft.config.endpoints.length === 0 ? <EmptyState title="模型池没有 API 返回的端点" detail="不能用原型默认端点填充；请在有 registry 查看权限时添加现有 model_ref。" /> : <div className="overflow-x-auto rounded-xl border border-[var(--border)]"><table className="w-full min-w-[1100px] text-left text-xs"><thead className="bg-[var(--table-th-bg)] text-[var(--text-muted)]"><tr><th className="px-3 py-3">端点 ID / 名称</th><th className="px-3 py-3">registry model_ref</th><th className="px-3 py-3">角色 / 顺序</th><th className="px-3 py-3">本地并发</th><th className="px-3 py-3">超时 / 冷却</th><th className="px-3 py-3">tools 能力</th><th className="px-3 py-3">操作</th></tr></thead><tbody>{draft.config.endpoints.map((endpoint) => { const registryModel = modelByRef.get(endpoint.model_ref); return <tr key={endpoint.id} className="border-t border-[var(--border)] align-top"><td className="space-y-2 px-3 py-3"><Input value={endpoint.id} onChange={(event) => { const nextId = event.target.value; onChange((current) => { if (!current) return current; const rename = (task: string) => { const currentAssignment = current.config.task_assignments[task] ?? emptyTaskAssignment(); return { ...currentAssignment, primary: currentAssignment.primary === endpoint.id ? nextId : currentAssignment.primary, backups: currentAssignment.backups.map((id) => id === endpoint.id ? nextId : id) }; }; return { ...current, config: { ...current.config, task_assignments: { ...current.config.task_assignments, chat: rename("chat"), learning: rename("learning") }, endpoints: current.config.endpoints.map((item) => item.id === endpoint.id ? { ...item, id: nextId } : item) } }; }); }} /><Input value={endpoint.name ?? ""} placeholder="可选名称" onChange={(event) => updateEndpoint(endpoint.id, { name: event.target.value })} /></td><td className="px-3 py-3"><select className="h-9 min-w-[220px] rounded-lg border border-[var(--input-border)] bg-[var(--input-bg)] px-2 text-xs text-[var(--input-color)]" value={endpoint.model_ref} onChange={(event) => { const model = modelByRef.get(event.target.value); updateEndpoint(endpoint.id, { model_ref: event.target.value, supports_tools: model?.supports_tools ?? endpoint.supports_tools }); }}><option value={endpoint.model_ref}>{endpoint.model_ref} · 当前</option>{enabledModels.map((model) => <option key={model.ref} value={model.ref}>{model.label} · {model.ref}</option>)}</select><p className="mt-1 text-[11px] text-[var(--text-subtle)]">provider/key 不在此页面配置</p></td><td className="space-y-2 px-3 py-3"><select className="h-9 rounded-lg border border-[var(--input-border)] bg-[var(--input-bg)] px-2 text-xs text-[var(--input-color)]" value={endpoint.role} onChange={(event) => updateEndpoint(endpoint.id, { role: event.target.value })}><option value="primary">primary</option><option value="backup">backup</option></select><Input type="number" min={0} value={endpoint.priority} onChange={(event) => updateEndpoint(endpoint.id, { priority: Number(event.target.value) })} /></td><td className="px-3 py-3"><Input className="w-24" type="number" min={1} max={100} value={endpoint.max_concurrency} onChange={(event) => updateEndpoint(endpoint.id, { max_concurrency: Number(event.target.value) })} /></td><td className="space-y-2 px-3 py-3"><Input className="w-28" type="number" min={1000} max={120000} value={endpoint.timeout_ms} onChange={(event) => updateEndpoint(endpoint.id, { timeout_ms: Number(event.target.value) })} /><Input className="w-28" type="number" min={1} max={3600} value={endpoint.cooldown_duration_sec} onChange={(event) => updateEndpoint(endpoint.id, { cooldown_duration_sec: Number(event.target.value) })} /></td><td className="px-3 py-3">{registryModel ? <Badge tone={registryModel.supports_tools ? "success" : "warning"}>{registryModel.supports_tools ? "SupportsTools" : "纯文本"}</Badge> : <Badge>未知</Badge>}</td><td className="px-3 py-3"><Button type="button" variant="danger" size="sm" onClick={() => removeEndpoint(endpoint.id)}><Trash2 className="h-3 w-3" />删除</Button></td></tr>; })}</tbody></table></div>}</CardBody></Card>
    <Card><CardHeader><CardTitle>任务分配</CardTitle><CardDescription>chat 的主端点和备用端点必须满足 tools 硬门槛；learning 可使用纯文本模型。备用顺序按逗号分隔的端点 ID 保存。</CardDescription></CardHeader><CardBody className="grid gap-4 md:grid-cols-2">{(["chat", "learning"] as const).map((task) => { const current = assignment(task); return <div key={task} className="rounded-xl border border-[var(--border)] bg-[var(--surface-2)] p-4"><p className="mb-3 text-sm font-medium">{task === "chat" ? "聊天任务" : "学习任务"}</p><Field label="主端点"><select className="h-10 w-full rounded-xl border border-[var(--input-border)] bg-[var(--input-bg)] px-3 text-sm text-[var(--input-color)]" value={current.primary} onChange={(event) => updateAssignment(task, { primary: event.target.value })}><option value="">请选择实际端点</option>{current.primary && task === "chat" && !endpointsForTask(task).some((endpoint) => endpoint.id === current.primary) && <option value={current.primary} disabled>{endpointLabel(current.primary)} · 当前引用不满足 tools</option>}{endpointsForTask(task).map((endpoint) => <option key={endpoint.id} value={endpoint.id}>{endpointLabel(endpoint.id)}</option>)}</select></Field><Field label="有序备用端点" hint="只填写当前端点 ID，不是 model_ref；服务端会再次校验重复、存在性和 chat tools 能力" className="mt-3"><Input value={current.backups.join(", ")} onChange={(event) => { const backups = event.target.value.split(",").map((item) => item.trim()).filter(Boolean); updateAssignment(task, { backups: task === "chat" ? backups.filter((id) => { const endpoint = draft.config.endpoints.find((item) => item.id === id); return endpoint ? endpointSupportsTools(endpoint) : false; }) : backups }); }} placeholder="按实际端点 ID 填写，可留空" /></Field></div>; })}</CardBody></Card>
    </fieldset>
    <Card><CardHeader><SectionTitle icon={ActivityIcon} title="真实运行状态" description="可见页每 20 秒轮询；离开页面会取消定时器和请求。没有活动负载时也只展示服务端返回的 0/unknown。" /></CardHeader><CardBody>{status ? <div className="space-y-4"><div className="flex flex-wrap items-center justify-between gap-2"><div className="flex flex-wrap gap-2"><Badge>{valueOrUnknown(status.active_strategy)}</Badge><span className="rounded-lg border border-[var(--border)] px-2 py-1 text-xs text-[var(--text-muted)]">队列 {valueOrUnknown(status.queue_depth)}</span><span className="rounded-lg border border-[var(--border)] px-2 py-1 text-xs text-[var(--text-muted)]">远程配额：{valueOrUnknown(status.remote_quota_note)}</span></div><Button type="button" variant="secondary" size="sm" onClick={onRefreshStatus}><RefreshCw className="h-3.5 w-3.5" />立即刷新</Button></div><div className="overflow-x-auto rounded-xl border border-[var(--border)]"><table className="w-full min-w-[880px] text-left text-xs"><thead className="bg-[var(--table-th-bg)] text-[var(--text-muted)]"><tr><th className="px-3 py-3">端点</th><th className="px-3 py-3">本地负载</th><th className="px-3 py-3">状态</th><th className="px-3 py-3">冷却</th><th className="px-3 py-3">最近错误</th><th className="px-3 py-3">远端额度</th></tr></thead><tbody>{status.endpoints_status.map((endpoint) => <tr key={endpoint.id} className="border-t border-[var(--border)]"><td className="px-3 py-3"><p className="font-medium">{endpoint.id}</p><p className="text-[var(--text-muted)]">{endpoint.model_label || endpoint.model_ref || "未知"}</p></td><td className="px-3 py-3">{valueOrUnknown(endpoint.current_active)} / {valueOrUnknown(endpoint.max_concurrency)}{endpoint.is_full ? <Badge tone="warning" className="ml-2">满载</Badge> : null}</td><td className="px-3 py-3"><Badge tone={statusTone(endpoint.status)}>{endpoint.status || "unknown"}</Badge></td><td className="px-3 py-3">{endpoint.cooldown_remaining_sec ? `${endpoint.cooldown_remaining_sec}s` : "0 / 未冷却"}</td><td className="max-w-[220px] px-3 py-3 text-[var(--text-muted)]">{endpoint.last_error || "无数据"}</td><td className="px-3 py-3 text-[var(--text-muted)]">{endpoint.remote_quota_observed || "未知"}</td></tr>)}</tbody></table></div>{status.last_dispatch_event && <LastDispatch event={status.last_dispatch_event} />}</div> : <ApiState label="运行状态无数据" error={statusError ?? "未知"} onRetry={onRefreshStatus} />}</CardBody></Card>
    <Card><CardHeader><CardTitle>最近调度记录</CardTitle><CardDescription>只显示服务端裁剪后的任务、端点、原因、状态和延迟，不含提示全文或密钥。</CardDescription></CardHeader><CardBody>{dispatchError ? <ApiState label="调度记录无数据" error={dispatchError} onRetry={onRefreshStatus} /> : dispatches.length === 0 ? <EmptyState title="没有 API 返回调度记录" detail="不绘制原型中的模拟曲线或虚构请求。" /> : <div className="overflow-x-auto rounded-xl border border-[var(--border)]"><table className="w-full min-w-[760px] text-left text-xs"><thead className="bg-[var(--table-th-bg)] text-[var(--text-muted)]"><tr><th className="px-3 py-3">时间</th><th className="px-3 py-3">任务 / 端点</th><th className="px-3 py-3">原因</th><th className="px-3 py-3">状态</th><th className="px-3 py-3">延迟</th><th className="px-3 py-3">错误</th></tr></thead><tbody>{dispatches.map((dispatch) => <tr key={dispatch.id} className="border-t border-[var(--border)] align-top"><td className="whitespace-nowrap px-3 py-3 text-[var(--text-muted)]">{formatDate(dispatch.created_at)}</td><td className="px-3 py-3"><p>{dispatch.task_type} · {dispatch.endpoint_id || "未知"}</p><p className="text-[var(--text-muted)]">{dispatch.model_ref || "未知"}</p></td><td className="max-w-[260px] px-3 py-3 text-[var(--text-muted)]">{dispatch.reason || "未知"}</td><td className="px-3 py-3"><Badge tone={dispatch.status === "ok" || dispatch.status === "success" ? "success" : dispatch.status === "failed" ? "danger" : "default"}>{dispatch.status || "未知"}</Badge></td><td className="px-3 py-3">{dispatch.latency_ms == null ? "未知" : `${dispatch.latency_ms} ms`}</td><td className="max-w-[220px] px-3 py-3 text-[var(--text-muted)]">{dispatch.error || "—"}</td></tr>)}</tbody></table></div>}</CardBody></Card>
    <SaveBar dirty={dirty} saving={saving} onSave={onSave} onCancel={onCancel} scope="模型池" />
  </div>;
}

function ActivityIcon(props: React.ComponentProps<typeof Clock3>) { return <Clock3 {...props} />; }
function LastDispatch({ event }: { event: AssistantLastDispatch }) { return <div className="rounded-xl border border-[var(--border)] bg-[var(--surface-2)] p-3 text-xs"><p className="font-medium">最近选路：{event.task_type} · {event.selected_endpoint_id || "未知"}</p><p className="mt-1 text-[var(--text-muted)]">{event.reason || "未知"} · {valueOrUnknown(event.status)} · {formatDate(event.timestamp)}</p></div>; }

function ToolsPanel({ tools, error, policy, onOpenSettings }: { tools: AssistantToolsResponse | null; error: string | null; policy: AssistantPolicy; onOpenSettings: () => void }) {
  return <div className="grid gap-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(280px,0.7fr)]"><Card><CardHeader><SectionTitle icon={Wrench} title="只读技能" description="没有真实工具运行 API，因此不显示‘试运行’或假的 Telegram 消息按钮。" /></CardHeader><CardBody>{tools ? <div className="space-y-3">{tools.tools.map((tool) => <div key={tool.name} className="rounded-xl border border-[var(--border)] p-4"><div className="flex flex-wrap items-center justify-between gap-2"><div className="flex items-center gap-2"><span className="font-medium">{TOOL_LABELS[tool.name as AssistantToolName] ?? tool.name}</span><Badge tone={tool.read_only ? "success" : "danger"}>{tool.read_only ? "只读" : "异常：非只读"}</Badge><Badge tone={tool.enabled ? "success" : "default"}>{tool.enabled ? "已启用" : "未启用"}</Badge></div><span className="text-xs text-[var(--text-muted)]">scope：{tool.scope || "未知"}</span></div>{tool.name === "webfetch_readonly" && <p className="mt-2 text-xs text-[var(--text-muted)]">只允许服务端约束的公开白名单 GET；失败、超时、截断会作为结构化结果返回，不代表读取成功。域名白名单在聊天与学习设置编辑。</p>}</div>)}<div className="rounded-xl border border-[var(--border)] bg-[var(--surface-2)] p-3 text-xs text-[var(--text-muted)]">write_tools：{tools.write_tools.length === 0 ? "空" : tools.write_tools.join("、")} · server_bound_scope：{String(tools.server_bound_scope)}</div></div> : <ApiState label="tools API 无数据" error={error ?? "未知"} />}</CardBody></Card><Card><CardHeader><CardTitle>当前策略 allowlist</CardTitle><CardDescription>来自 assistant settings policy；tools API 才能说明服务端实际启用能力。</CardDescription></CardHeader><CardBody className="space-y-3"><div className="space-y-2">{TOOL_NAMES.map((tool) => <div key={tool} className="flex items-center justify-between gap-3 text-sm"><span>{TOOL_LABELS[tool]}</span><Badge tone={policy.tool_allowlist.includes(tool) ? "success" : "default"}>{policy.tool_allowlist.includes(tool) ? "allow" : "deny"}</Badge></div>)}</div><div className="border-t border-[var(--border)] pt-3 text-xs text-[var(--text-muted)]"><p>域名白名单</p><p className="mt-1 break-words">{policy.allow_domains.length ? policy.allow_domains.join("、") : "空（无已配置域名）"}</p></div><Button type="button" variant="secondary" size="sm" onClick={onOpenSettings}>编辑 assistant settings</Button></CardBody></Card></div>;
}
