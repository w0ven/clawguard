"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Card, CardHeader, CardTitle, CardBody } from "@/components/ui/card";
import { Tabs } from "@/components/ui/tabs";
import { Switch } from "@/components/ui/switch";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Select } from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from "@/components/ui/table";
import {
  apiFetch,
  deleteOldProfileCheckLogs,
  fetchProfileCheckLogs,
} from "@/lib/api";
import {
  formatArrayValue,
  getPathValue,
  hasPath,
  parseArrayValue,
  setPath,
} from "@/lib/policy";
import type {
  AuditEntry,
  GuardPolicy,
  Group,
  KeywordReplyRule,
  ProfileCheckLog,
} from "@/lib/types";
import { useToast } from "@/components/providers";
import { ScheduledMessagesEditor } from "@/components/scheduled-messages-editor";
import { Save, ChevronDown, ChevronRight } from "lucide-react";

type FieldKind =
  | "switch"
  | "text"
  | "number"
  | "textarea"
  | "username-list"
  | "readonly-number"
  | "select"
  | "template"
  | "model-ref"
  | "model-ref-list";

type FieldDescriptor = {
  path: string[];
  label: string;
  description: string;
  kind: FieldKind;
  tab: string;
  placeholder?: string;
  options?: Array<{ label: string; value: string }>;
};

export type LLMModelOption = {
  label: string;
  value: string;
};

export const fieldDescriptors: FieldDescriptor[] = [
  // ===== Verify =====
  {
    tab: "verify",
    path: ["verify", "enabled"],
    label: "启用验证",
    description: "新成员进群是否需要验证",
    kind: "switch",
  },
  {
    tab: "verify",
    path: ["verify", "method"],
    label: "验证方式",
    description: "选择人机验证方式",
    kind: "select",
    options: [
      { label: "按钮点击（+3秒延迟）", value: "button" },
      { label: "算术题四选一", value: "math" },
      { label: "算术题（图片防 OCR）", value: "math_image" },
      { label: "Emoji 四选一", value: "random" },
      { label: "Cloudflare Turnstile", value: "turnstile" },
    ],
  },
  {
    tab: "verify",
    path: ["verify", "timeout_seconds"],
    label: "超时时长 (秒)",
    description: "超过后按失败动作处理",
    kind: "number",
  },
  {
    tab: "verify",
    path: ["verify", "fail_action"],
    label: "超时动作",
    description: "验证失败/超时如何处理",
    kind: "select",
    options: [
      { label: "踢出 (Kick)", value: "kick" },
      { label: "封禁 (Ban)", value: "ban" },
      { label: "永久禁言", value: "mute_permanent" },
    ],
  },
  {
    tab: "verify",
    path: ["verify", "delete_join_message"],
    label: "删除进群提示",
    description: "自动删除系统进群消息",
    kind: "switch",
  },
  {
    tab: "verify",
    path: ["verify", "welcome_message", "enabled"],
    label: "启用欢迎语",
    description: "验证通过后发送欢迎消息",
    kind: "switch",
  },
  {
    tab: "verify",
    path: ["verify", "welcome_message", "template"],
    label: "欢迎语模板",
    description:
      "支持变量：{user_mention} {user_name} {group_title} {rules_link} {admin_list} {date} {time}",
    kind: "template",
    placeholder: "🎉 欢迎 {user_mention} 加入 {group_title}！",
  },
  {
    tab: "verify",
    path: ["verify", "welcome_message", "rules_link"],
    label: "群规链接",
    description: "模板中 {rules_link} 变量的值",
    kind: "text",
  },
  {
    tab: "verify",
    path: ["verify", "welcome_message", "delete_after_seconds"],
    label: "欢迎语自动删除 (秒)",
    description: "0 表示不删除",
    kind: "number",
  },
  {
    tab: "verify",
    path: ["verify", "welcome_message", "parse_mode"],
    label: "欢迎语解析模式",
    description: "消息格式：HTML / MarkdownV2 / Markdown",
    kind: "select",
    options: [
      { value: "markdownv2", label: "MarkdownV2" },
      { value: "html", label: "HTML" },
      { value: "markdown", label: "Markdown (legacy)" },
    ],
  },
  {
    tab: "verify",
    path: ["verify", "check_profile"],
    label: "检查用户简介",
    description: "检查用户 Bio（简介）中的敏感词",
    kind: "switch",
  },
  {
    tab: "verify",
    path: ["verify", "profile_check_mode"],
    label: "资料审核方式",
    description: "检查新用户简介的方式",
    kind: "select",
    options: [
      { value: "off", label: "不检查" },
      { value: "keyword", label: "关键词黑名单（默认）" },
      { value: "ai", label: "AI 自动判定" },
    ],
  },
  {
    tab: "verify",
    path: ["verify", "profile_blacklist"],
    label: "资料黑名单",
    description: "一行一条，或用逗号分隔敏感词",
    kind: "textarea",
  },

  // ===== Filter =====
  {
    tab: "filter",
    path: ["filter", "keywords", "enabled"],
    label: "关键词过滤",
    description: "命中关键词触发动作",
    kind: "switch",
  },
  {
    tab: "filter",
    path: ["filter", "keywords", "list"],
    label: "关键词列表",
    description: "一行一个，或用逗号分隔",
    kind: "textarea",
  },
  {
    tab: "filter",
    path: ["filter", "keywords", "action"],
    label: "关键词动作",
    description: "",
    kind: "select",
    options: [
      { label: "删除", value: "delete" },
      { label: "删除 + 警告", value: "delete_warn" },
      { label: "删除 + 禁言", value: "delete_mute" },
      { label: "删除 + 封禁", value: "delete_ban" },
    ],
  },
  {
    tab: "filter",
    path: ["filter", "keywords", "case_sensitive"],
    label: "区分大小写",
    description: "",
    kind: "switch",
  },
  {
    tab: "filter",
    path: ["filter", "regex", "enabled"],
    label: "正则过滤",
    description: "",
    kind: "switch",
  },
  {
    tab: "filter",
    path: ["filter", "regex", "patterns"],
    label: "正则规则",
    description: "一行一条，或用逗号分隔",
    kind: "textarea",
  },
  {
    tab: "filter",
    path: ["filter", "links", "enabled"],
    label: "链接过滤",
    description: "只允许白名单内的链接",
    kind: "switch",
  },
  {
    tab: "filter",
    path: ["filter", "links", "whitelist"],
    label: "链接白名单",
    description: "一行一个，或用逗号分隔域名",
    kind: "textarea",
  },
  {
    tab: "filter",
    path: ["filter", "links", "action"],
    label: "链接动作",
    description: "",
    kind: "select",
    options: [
      { label: "删除", value: "delete" },
      { label: "删除 + 警告", value: "delete_warn" },
      { label: "删除 + 禁言", value: "delete_mute" },
      { label: "删除 + 封禁", value: "delete_ban" },
    ],
  },
  {
    tab: "filter",
    path: ["filter", "links", "exempt_admins"],
    label: "豁免管理员",
    description: "管理员发链接不触发",
    kind: "switch",
  },
  {
    tab: "filter",
    path: ["filter", "non_text_messages"],
    label: "非文字消息策略",
    description: "名片/贴纸/GIF/图片等如何处理",
    kind: "select",
    options: [
      { value: "off", label: "不处理" },
      { value: "delete", label: "直接删除" },
      { value: "delete_warn", label: "删除并警告" },
      { value: "ai_review", label: "交给 AI 判定（推荐）" },
    ],
  },
  {
    tab: "filter",
    path: ["filter", "other_bots_action"],
    label: "其他 Bot 入群策略",
    description:
      "默认 audit：非白名单 Bot 入群后进入 AI 审核；kick/ban 为自动移出/封禁；off 为不处理。",
    kind: "select",
    options: [
      { value: "audit", label: "进入 AI 审核（推荐）" },
      { value: "kick", label: "自动移出" },
      { value: "ban", label: "自动封禁" },
      { value: "off", label: "不处理" },
    ],
  },
  {
    tab: "filter",
    path: ["filter", "bot_whitelist"],
    label: "Bot 白名单",
    description:
      "一行一个 Bot 用户名，支持 @foo 或 foo；后端保存时会统一去 @、小写。",
    kind: "username-list",
  },
  {
    tab: "filter",
    path: ["filter", "usernames", "enabled"],
    label: "用户名黑名单",
    description: "",
    kind: "switch",
  },
  {
    tab: "filter",
    path: ["filter", "usernames", "blacklist"],
    label: "黑名单列表",
    description: "一行一个，或用逗号分隔词",
    kind: "textarea",
  },
  {
    tab: "filter",
    path: ["filter", "new_user", "enabled"],
    label: "新人严管",
    description: "入群初期限制更严",
    kind: "switch",
  },
  {
    tab: "filter",
    path: ["filter", "new_user", "duration_hours"],
    label: "新人时长 (小时)",
    description: "",
    kind: "number",
  },
  {
    tab: "filter",
    path: ["filter", "new_user", "no_links"],
    label: "禁止链接",
    description: "",
    kind: "switch",
  },
  {
    tab: "filter",
    path: ["filter", "new_user", "no_forwards"],
    label: "禁止转发",
    description: "",
    kind: "switch",
  },
  {
    tab: "filter",
    path: ["filter", "new_user", "no_media"],
    label: "禁止媒体",
    description: "",
    kind: "switch",
  },
  {
    tab: "filter",
    path: ["filter", "new_user", "max_messages_per_minute"],
    label: "每分钟消息上限",
    description: "",
    kind: "number",
  },

  // ===== Warnings =====
  {
    tab: "warnings",
    path: ["warnings", "enabled"],
    label: "启用警告系统",
    description: "累积警告并升级处理",
    kind: "switch",
  },
  {
    tab: "warnings",
    path: ["warnings", "max_warns"],
    label: "最大警告数",
    description: "达到后触发升级动作",
    kind: "number",
  },
  {
    tab: "warnings",
    path: ["warnings", "action_at_max"],
    label: "升级动作",
    description: "",
    kind: "select",
    options: [
      { label: "踢出", value: "kick" },
      { label: "封禁", value: "ban" },
      { label: "禁言 5 分钟", value: "mute_5m" },
      { label: "禁言 1 小时", value: "mute_1h" },
    ],
  },
  {
    tab: "warnings",
    path: ["warnings", "decay_days"],
    label: "衰减天数",
    description: "警告多久后自动过期",
    kind: "number",
  },

  // ===== Anti-spam =====
  {
    tab: "anti-spam",
    path: ["anti_spam", "cas_enabled"],
    label: "CAS 联动",
    description: "新成员查询 Combot Anti-Spam",
    kind: "switch",
  },
  {
    tab: "anti-spam",
    path: ["anti_spam", "rate_limit", "enabled"],
    label: "速率限制",
    description: "",
    kind: "switch",
  },
  {
    tab: "anti-spam",
    path: ["anti_spam", "rate_limit", "messages_per_10s"],
    label: "每 10 秒消息数",
    description: "",
    kind: "number",
  },
  {
    tab: "anti-spam",
    path: ["anti_spam", "rate_limit", "action"],
    label: "超速动作",
    description: "例如 mute_5m",
    kind: "text",
  },

  // ===== Logging =====
  {
    tab: "logging",
    path: ["logging", "log_chat_id"],
    label: "日志群 Chat ID",
    description: "违规事件推送到此群，留空关闭",
    kind: "number",
  },

  // ===== AI =====
  {
    tab: "ai",
    path: ["ai", "enabled"],
    label: "启用 AI 审核",
    description: "过滤器未命中时交由 AI 判定",
    kind: "switch",
  },
  {
    tab: "ai",
    path: ["ai", "image_moderation_enabled"],
    label: "启用图片审核",
    description: "下载图片并交给视觉模型审核，会增加 AI 调用量",
    kind: "switch",
  },
  {
    tab: "ai",
    path: ["ai", "video_moderation_enabled"],
    label: "启用视频审核",
    description: "对未毕业用户的视频/GIF 抽帧后交给视觉模型审核（默认关，灰度开）",
    kind: "switch",
  },
  {
    tab: "ai",
    path: ["ai", "include_video_note"],
    label: "审核圆形短视频（VideoNote）",
    description: "审查价值低、成本高，默认关闭",
    kind: "switch",
  },
  {
    tab: "ai",
    path: ["ai", "video_max_bytes"],
    label: "视频最大体积 (字节)",
    description: "超过则跳过抽帧。Telegram Bot API 上限约 20 MB = 20971520",
    kind: "number",
  },
  {
    tab: "ai",
    path: ["ai", "video_max_duration_sec"],
    label: "视频最大时长 (秒)",
    description: "超过则跳过抽帧",
    kind: "number",
  },
  {
    tab: "ai",
    path: ["ai", "video_frame_count"],
    label: "抽帧张数",
    description: "建议 1-3，GIF 自动覆盖为 1",
    kind: "number",
  },
  {
    tab: "ai",
    path: ["ai", "video_concurrency"],
    label: "并发抽帧上限",
    description: "同时进行的视频抽帧数。建议 2-4",
    kind: "number",
  },
  {
    tab: "ai",
    path: ["ai", "primary_model_ref"],
    label: "主模型（新）",
    description: "从模型注册表选择，格式 provider:model",
    kind: "model-ref",
  },
  {
    tab: "ai",
    path: ["ai", "fallback_model_refs"],
    label: "Fallback 链（新）",
    description: "可多选并排序，格式 provider:model",
    kind: "model-ref-list",
  },
  {
    tab: "ai",
    path: ["ai", "auto_degrade"],
    label: "自动降级不健康模型",
    description: "探活判定不健康时自动跳过",
    kind: "switch",
  },
  {
    tab: "ai",
    path: ["ai", "temperature"],
    label: "Temperature",
    description: "建议 0-0.3",
    kind: "number",
  },
  {
    tab: "ai",
    path: ["ai", "timeout_ms"],
    label: "请求超时 (ms)",
    description: "",
    kind: "number",
  },
  {
    tab: "ai",
    path: ["ai", "max_retries"],
    label: "最大重试",
    description: "",
    kind: "number",
  },
  {
    tab: "ai",
    path: ["ai", "graduate_after_messages"],
    label: "毕业 clean 消息数",
    description: "未毕业用户必须通过 AI 审核的 clean 消息数，默认 5。",
    kind: "number",
  },
  {
    tab: "ai",
    path: ["ai", "graduate_after_days"],
    label: "毕业天数（已废弃）",
    description:
      "兼容旧配置保留；当前毕业只看 AI clean 消息数，不再按天数自动 trusted。",
    kind: "readonly-number",
  },
  {
    tab: "ai",
    path: ["ai", "per_user_daily_limit"],
    label: "每用户日上限",
    description: "",
    kind: "number",
  },
  {
    tab: "ai",
    path: ["ai", "skip_messages_shorter_than"],
    label: "短消息跳过",
    description: "少于此长度不送 AI",
    kind: "number",
  },
  {
    tab: "ai",
    path: ["ai", "batch_window_ms"],
    label: "合批窗口 (ms)",
    description: "",
    kind: "number",
  },
  {
    tab: "ai",
    path: ["ai", "cache_ttl_hours"],
    label: "缓存时长 (小时)",
    description: "相同消息 hash 复用",
    kind: "number",
  },
  {
    tab: "ai",
    path: ["ai", "custom_rules"],
    label: "自定义规则",
    description: "只会追加到固定 system prompt 末尾",
    kind: "template",
  },
  {
    tab: "ai",
    path: ["ai", "trigger_keywords"],
    label: "AI 触发关键词",
    description: "已毕业用户命中这些关键词时触发 AI 审核（一行一个，模糊匹配）",
    kind: "textarea",
  },
  {
    tab: "ai",
    path: ["ai", "check_profile_on_message"],
    label: "未毕业用户发言前审核简介",
    description:
      "new/suspicious 用户每条消息前重新审核其 bio，防止入群后偷改简介加广告。命中直接 ban",
    kind: "switch",
  },
  {
    tab: "ai",
    path: ["ai", "profile_on_message_mode"],
    label: "发言前简介审核模式",
    description: "keyword=用 ProfileBlacklist 关键词命中；ai=调 AI 判定",
    kind: "text",
  },
  {
    tab: "ai",
    path: ["ai", "bio_cache_ttl_minutes"],
    label: "Bio 缓存分钟数",
    description:
      "缓存 bio，节流 Telegram getChat 调用；0=不缓存每次现查；建议 10-30 分钟",
    kind: "number",
  },
  {
    tab: "ai",
    path: ["ai", "thresholds", "ban"],
    label: "Ban 阈值",
    description: "0-1，超过此分直接 Ban",
    kind: "number",
  },
  {
    tab: "ai",
    path: ["ai", "thresholds", "mute"],
    label: "Mute 阈值",
    description: "0-1",
    kind: "number",
  },
  {
    tab: "ai",
    path: ["ai", "thresholds", "warn"],
    label: "Warn 阈值",
    description: "0-1",
    kind: "number",
  },
  {
    tab: "ai",
    path: ["ai", "thresholds", "flag"],
    label: "Flag 阈值",
    description: "0-1",
    kind: "number",
  },
];

const tabs = [
  { value: "basic", label: "基础" },
  { value: "verify", label: "验证" },
  { value: "filter", label: "过滤" },
  { value: "replies", label: "关键词回复" },
  { value: "warnings", label: "警告" },
  { value: "anti-spam", label: "反垃圾" },
  { value: "ai", label: "AI" },
  { value: "feedback", label: "动作反馈" },
  { value: "logging", label: "日志" },
  { value: "audit", label: "审计" },
  { value: "scheduled", label: "定时消息" },
];

type Props = {
  group: Group;
  mergedPolicy: GuardPolicy;
};

type ProfileCheckLogFilters = {
  search: string;
  result: string;
  mode: string;
};

function formatTime(ts: string) {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return "-";
  return d.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function formatBeijingTime(ts: string) {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return "-";
  const parts = new Intl.DateTimeFormat("zh-CN", {
    timeZone: "Asia/Shanghai",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).formatToParts(d);
  const get = (type: string) =>
    parts.find((part) => part.type === type)?.value ?? "";
  return `${get("month")}-${get("day")} ${get("hour")}:${get("minute")}`;
}

function truncateText(value: string, maxLength: number) {
  if (value.length <= maxLength) {
    return value;
  }
  return `${value.slice(0, maxLength)}...`;
}

export function GroupConfigEditor({ group, mergedPolicy }: Props) {
  const { pushToast } = useToast();
  const [activeTab, setActiveTab] = useState("verify");
  const [initialConfig, setInitialConfig] = useState<Record<string, unknown>>(
    group.config ?? {},
  );
  const [draft, setDraft] = useState<Record<string, unknown>>(
    group.config ?? {},
  );
  const [mergedPolicyState, setMergedPolicyState] =
    useState<GuardPolicy>(mergedPolicy);
  const [audit, setAudit] = useState<AuditEntry[]>([]);
  const [auditExpanded, setAuditExpanded] = useState<Set<number>>(new Set());
  const [saving, setSaving] = useState(false);
  const [profileCheckLogs, setProfileCheckLogs] = useState<ProfileCheckLog[]>(
    [],
  );
  const [profileCheckTotal, setProfileCheckTotal] = useState(0);
  const [profileCheckPage, setProfileCheckPage] = useState(1);
  const [profileCheckLoading, setProfileCheckLoading] = useState(false);
  const [profileCheckDeleting, setProfileCheckDeleting] = useState(false);
  const [profileCheckRefreshKey, setProfileCheckRefreshKey] = useState(0);
  const [profileCheckFilters, setProfileCheckFilters] =
    useState<ProfileCheckLogFilters>({
      search: "",
      result: "",
      mode: "",
    });
  const [llmModelOptions, setLLMModelOptions] = useState<LLMModelOption[]>([]);
  const [llmOptionsError, setLLMOptionsError] = useState<string | null>(null);

  useEffect(() => {
    setInitialConfig(group.config ?? {});
    setDraft(group.config ?? {});
  }, [group.config]);

  useEffect(() => {
    setMergedPolicyState(mergedPolicy);
  }, [mergedPolicy]);

  useEffect(() => {
    setProfileCheckPage(1);
  }, [group.chat_id]);

  useEffect(() => {
    let alive = true;
    apiFetch<{
      models?: Array<{
        provider_key: string;
        model_key: string;
        label?: string;
        enabled?: boolean;
      }>;
    }>("/api/admin/llm/models")
      .then((payload) => {
        if (!alive) {
          return;
        }
        const options = (payload.models ?? []).map((model) => {
          const value = `${model.provider_key}:${model.model_key}`;
          const label = `${value} · ${model.label || model.model_key}`;
          return { value, label };
        });
        setLLMModelOptions(options);
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

  useEffect(() => {
    apiFetch<{ audit: AuditEntry[] }>(
      `/api/admin/audit?chat_id=${group.chat_id}&limit=50`,
    )
      .then((p) => setAudit(p.audit ?? []))
      .catch((e) =>
        pushToast(e instanceof Error ? e.message : "加载失败", "error"),
      );
  }, [group.chat_id, pushToast]);

  useEffect(() => {
    if (activeTab !== "verify") {
      return;
    }

    const timer = window.setTimeout(() => {
      setProfileCheckLoading(true);
      fetchProfileCheckLogs(group.chat_id, {
        page: profileCheckPage,
        pageSize: 20,
        search: profileCheckFilters.search,
        result: profileCheckFilters.result,
        mode: profileCheckFilters.mode,
      })
        .then((payload) => {
          setProfileCheckLogs(payload.items ?? []);
          setProfileCheckTotal(payload.total ?? 0);
        })
        .catch((error) => {
          setProfileCheckLogs([]);
          setProfileCheckTotal(0);
          pushToast(
            error instanceof Error ? error.message : "加载失败",
            "error",
          );
        })
        .finally(() => setProfileCheckLoading(false));
    }, 250);

    return () => window.clearTimeout(timer);
  }, [
    activeTab,
    group.chat_id,
    profileCheckFilters.mode,
    profileCheckFilters.result,
    profileCheckFilters.search,
    profileCheckPage,
    profileCheckRefreshKey,
    pushToast,
  ]);

  const isDirty = useMemo(
    () => JSON.stringify(initialConfig) !== JSON.stringify(draft),
    [draft, initialConfig],
  );

  const tabFields = fieldDescriptors
    .filter((f) => f.tab === activeTab)
    .filter((f) => {
      // hide profile_blacklist unless profile_check_mode === "keyword"
      if (f.path.join(".") === "verify.profile_blacklist") {
        const mode = String(
          getEffectiveValue(draft, mergedPolicyState, [
            "verify",
            "profile_check_mode",
          ]) ?? "keyword",
        );
        return mode === "keyword";
      }
      return true;
    });

  const welcomePreview = renderWelcomePreview(
    getEffectiveValue(draft, mergedPolicyState, [
      "verify",
      "welcome_message",
      "template",
    ]),
    {
      groupTitle: group.title,
      groupId: group.chat_id,
      memberCount: group.member_count,
      rulesLink: String(
        getEffectiveValue(draft, mergedPolicyState, [
          "verify",
          "welcome_message",
          "rules_link",
        ]) ?? "",
      ),
    },
  );
  const keywordRepliesInherited = !hasPath(draft, [
    "messages",
    "keyword_replies",
  ]);
  const keywordRepliesValue = getEffectiveValue(draft, mergedPolicyState, [
    "messages",
    "keyword_replies",
  ]);
  const mergedKeywordReplies = (getPathValue(mergedPolicyState, [
    "messages",
    "keyword_replies",
  ]) ?? []) as KeywordReplyRule[];
  const keywordReplies = (
    Array.isArray(keywordRepliesValue)
      ? (keywordRepliesValue as KeywordReplyRule[])
      : []
  ).map((rule) => {
    const stats = mergedKeywordReplies.find((r) => r.id === rule.id);
    return stats
      ? {
          ...rule,
          trigger_count: stats.trigger_count ?? rule.trigger_count,
          last_triggered_at: stats.last_triggered_at ?? rule.last_triggered_at,
        }
      : rule;
  });
  const checkProfileEnabled = Boolean(
    getEffectiveValue(draft, mergedPolicyState, ["verify", "check_profile"]) ??
    false,
  );

  function updateField(
    field: FieldDescriptor,
    rawValue: string | boolean | string[],
  ) {
    const next = structuredClone(draft) as Record<string, unknown>;
    let value: unknown = rawValue;

    if (field.kind === "number") {
      const parsed = Number(rawValue);
      value = Number.isFinite(parsed) ? parsed : 0;
    }
    if (field.kind === "textarea") {
      value = parseArrayValue(String(rawValue));
    }
    if (field.kind === "username-list") {
      value = parseArrayValue(String(rawValue)).map((item) =>
        item.replace(/^@+/, "").trim().toLowerCase(),
      );
    }
    if (field.kind === "model-ref-list") {
      value = Array.isArray(rawValue)
        ? rawValue.map((item) => item.trim()).filter(Boolean)
        : parseArrayValue(String(rawValue));
    }
    if (field.kind === "template" || field.kind === "text") {
      value = String(rawValue);
    }
    if (field.kind === "model-ref") {
      value = String(rawValue);
    }
    if (
      field.path.join(".") === "logging.log_chat_id" &&
      String(rawValue).trim() === ""
    ) {
      value = null;
    }

    setPath(next, field.path, value);
    setDraft(next);
  }

  function toggleInherited(field: FieldDescriptor, checked: boolean) {
    const next = structuredClone(draft) as Record<string, unknown>;
    if (checked) {
      removePath(next, field.path);
      setDraft(next);
      return;
    }
    const mergedValue = structuredClone(
      getPathValue(mergedPolicyState, field.path),
    );
    setPath(next, field.path, mergedValue);
    setDraft(next);
  }

  async function saveConfig() {
    setSaving(true);
    try {
      const p = await apiFetch<{
        group: Group;
        merged_policy: GuardPolicy;
      }>(`/api/admin/groups/${group.chat_id}/config`, {
        method: "PUT",
        body: JSON.stringify(draft),
      });
      setInitialConfig(p.group.config ?? {});
      setDraft(p.group.config ?? {});
      setMergedPolicyState(p.merged_policy);
      pushToast("已保存", "success");
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "保存失败", "error");
    } finally {
      setSaving(false);
    }
  }

  function toggleAudit(id: number) {
    const next = new Set(auditExpanded);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    setAuditExpanded(next);
  }

  function updateProfileCheckFilters(
    patch: Partial<ProfileCheckLogFilters>,
    resetPage = true,
  ) {
    setProfileCheckFilters((current) => ({ ...current, ...patch }));
    if (resetPage) {
      setProfileCheckPage(1);
    }
  }

  async function cleanupProfileCheckLogs(days: number) {
    if (!window.confirm(`确认清理 ${days} 天前的简介检查日志？`)) {
      return;
    }

    setProfileCheckDeleting(true);
    try {
      const payload = await deleteOldProfileCheckLogs(days);
      pushToast(`已清理 ${payload.deleted} 条日志`, "success");
      setProfileCheckPage(1);
      setProfileCheckRefreshKey((current) => current + 1);
    } catch (error) {
      pushToast(error instanceof Error ? error.message : "清理失败", "error");
    } finally {
      setProfileCheckDeleting(false);
    }
  }

  return (
    <div className="space-y-5 pb-20">
      <Tabs tabs={tabs} value={activeTab} onValueChange={setActiveTab} />

      {activeTab === "basic" && (
        <div className="grid gap-4 md:grid-cols-2">
          <Card>
            <CardHeader>
              <CardTitle>群信息</CardTitle>
            </CardHeader>
            <CardBody>
              <dl className="grid gap-3 text-sm">
                {[
                  ["群标题", group.title],
                  ["Chat ID", String(group.chat_id)],
                  ["类型", group.type],
                  ["成员数", String(group.member_count)],
                ].map(([k, v]) => (
                  <div
                    key={k}
                    className="flex justify-between border-b border-[var(--border)] pb-2 last:border-0 last:pb-0"
                  >
                    <dt className="text-[var(--text-muted)]">{k}</dt>
                    <dd className="font-medium tabular-nums">{v}</dd>
                  </div>
                ))}
              </dl>
            </CardBody>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>原始覆盖配置</CardTitle>
            </CardHeader>
            <CardBody>
              <pre className="max-h-96 overflow-x-auto rounded-md border border-[var(--border)] bg-[var(--surface-2)] p-3 text-xs leading-relaxed">
                {JSON.stringify(group.config ?? {}, null, 2)}
              </pre>
            </CardBody>
          </Card>

          <Card className="md:col-span-2">
            <CardHeader>
              <CardTitle>合并后生效策略</CardTitle>
            </CardHeader>
            <CardBody>
              <pre className="max-h-[400px] overflow-x-auto rounded-md border border-[var(--border)] bg-[var(--surface-2)] p-3 text-xs leading-relaxed">
                {JSON.stringify(mergedPolicyState, null, 2)}
              </pre>
            </CardBody>
          </Card>
        </div>
      )}

      {activeTab !== "basic" &&
        activeTab !== "audit" &&
        activeTab !== "scheduled" && (
          <div className="space-y-3">
            {tabFields.map((field) => {
              const inherited = !hasPath(draft, field.path);
              const value = getEffectiveValue(
                draft,
                mergedPolicyState,
                field.path,
              );
              return (
                <Card key={field.path.join(".")}>
                  <CardBody>
                    <div className="flex items-start justify-between gap-4 mb-3">
                      <div className="min-w-0">
                        <p className="text-sm font-medium">{field.label}</p>
                        {field.description && (
                          <p className="mt-0.5 text-xs text-[var(--text-muted)]">
                            {field.description}
                          </p>
                        )}
                      </div>
                      <label className="inline-flex items-center gap-1.5 text-xs text-[var(--text-muted)] whitespace-nowrap cursor-pointer select-none">
                        <input
                          type="checkbox"
                          checked={inherited}
                          onChange={(e) =>
                            toggleInherited(field, e.target.checked)
                          }
                          className="rounded border-[var(--border)]"
                        />
                        继承全局
                      </label>
                    </div>
                    <FieldControl
                      field={field}
                      value={value}
                      inherited={inherited}
                      modelOptions={llmModelOptions}
                      modelOptionsError={llmOptionsError}
                      onChange={updateField}
                    />
                  </CardBody>
                </Card>
              );
            })}

            {activeTab === "verify" && (
              <ProfileCheckLogsSection
                enabled={checkProfileEnabled}
                items={profileCheckLogs}
                total={profileCheckTotal}
                page={profileCheckPage}
                loading={profileCheckLoading}
                deleting={profileCheckDeleting}
                filters={profileCheckFilters}
                onSearchChange={(search) =>
                  updateProfileCheckFilters({ search }, true)
                }
                onResultChange={(result) =>
                  updateProfileCheckFilters({ result }, true)
                }
                onModeChange={(mode) =>
                  updateProfileCheckFilters({ mode }, true)
                }
                onPageChange={setProfileCheckPage}
                onCleanup={cleanupProfileCheckLogs}
              />
            )}

            {activeTab === "verify" && (
              <Card>
                <CardHeader>
                  <CardTitle>欢迎语预览</CardTitle>
                  <p className="text-xs text-[var(--text-muted)] mt-0.5">
                    示例用户「可乐」，群名即当前群
                  </p>
                </CardHeader>
                <CardBody>
                  <div
                    className="rounded-md border border-[var(--border)] bg-[var(--surface-2)] p-4 text-sm leading-7"
                    dangerouslySetInnerHTML={{ __html: welcomePreview }}
                  />
                </CardBody>
              </Card>
            )}

            {activeTab === "feedback" && (
              <FeedbackSection
                value={
                  (getEffectiveValue(draft, mergedPolicyState, ["feedback"]) as
                    | Record<string, ActionFeedbackValue>
                    | undefined) ?? {}
                }
                onChange={(next) => {
                  const cur = structuredClone(draft) as Record<string, unknown>;
                  setPath(cur, ["feedback"], next);
                  setDraft(cur);
                }}
              />
            )}

            {activeTab === "replies" && (
              <KeywordReplySection
                rules={keywordReplies}
                inherited={keywordRepliesInherited}
                onToggleInherited={(checked) => {
                  const next = structuredClone(draft) as Record<
                    string,
                    unknown
                  >;
                  if (checked) {
                    removePath(next, ["messages", "keyword_replies"]);
                  } else {
                    setPath(
                      next,
                      ["messages", "keyword_replies"],
                      structuredClone(
                        getPathValue(mergedPolicyState, [
                          "messages",
                          "keyword_replies",
                        ]) ?? [],
                      ),
                    );
                  }
                  setDraft(next);
                }}
                onChange={(rules) => {
                  const next = structuredClone(draft) as Record<
                    string,
                    unknown
                  >;
                  setPath(next, ["messages", "keyword_replies"], rules);
                  setDraft(next);
                }}
              />
            )}
          </div>
        )}

      {activeTab === "scheduled" && <ScheduledMessagesEditor group={group} />}

      {activeTab === "audit" && (
        <Card>
          <CardHeader>
            <CardTitle>最近变更 ({audit.length})</CardTitle>
          </CardHeader>
          {audit.length === 0 ? (
            <div className="px-5 py-12 text-center text-sm text-[var(--text-muted)]">
              无变更记录
            </div>
          ) : (
            <div className="divide-y divide-[var(--border)]">
              {audit.map((e) => {
                const open = auditExpanded.has(e.id);
                return (
                  <div key={e.id} className="px-5 py-3">
                    <button
                      type="button"
                      onClick={() => toggleAudit(e.id)}
                      className="flex w-full items-center gap-3 text-left"
                    >
                      {open ? (
                        <ChevronDown className="h-4 w-4 text-[var(--text-muted)]" />
                      ) : (
                        <ChevronRight className="h-4 w-4 text-[var(--text-muted)]" />
                      )}
                      <span className="text-[var(--text-muted)] tabular-nums text-sm whitespace-nowrap">
                        {formatTime(e.created_at)}
                      </span>
                      <code className="text-xs">{e.action}</code>
                      <span className="ml-auto text-xs text-[var(--text-muted)]">
                        admin {e.admin_id}
                      </span>
                    </button>
                    {open && (
                      <div className="mt-3 ml-7 grid gap-3 sm:grid-cols-2">
                        <div>
                          <p className="text-xs text-[var(--text-muted)] mb-1">
                            Before
                          </p>
                          <pre className="overflow-x-auto rounded-md border border-[var(--border)] bg-[var(--surface-2)] p-3 text-xs leading-relaxed">
                            {formatStructuredValue(e.before)}
                          </pre>
                        </div>
                        <div>
                          <p className="text-xs text-[var(--text-muted)] mb-1">
                            After
                          </p>
                          <pre className="overflow-x-auto rounded-md border border-[var(--border)] bg-[var(--surface-2)] p-3 text-xs leading-relaxed">
                            {formatStructuredValue(e.after)}
                          </pre>
                        </div>
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </Card>
      )}

      {/* sticky save */}
      {activeTab !== "scheduled" && (
        <div className="fixed bottom-6 right-6 z-20">
          <Button
            onClick={saveConfig}
            disabled={!isDirty || saving}
            variant={isDirty ? "primary" : "secondary"}
            size="lg"
            className="shadow-lg"
          >
            <Save className="h-4 w-4" />
            {saving ? "保存中…" : isDirty ? "保存配置" : "已保存"}
          </Button>
        </div>
      )}
    </div>
  );
}

function ProfileCheckLogsSection({
  enabled,
  items,
  total,
  page,
  loading,
  deleting,
  filters,
  onSearchChange,
  onResultChange,
  onModeChange,
  onPageChange,
  onCleanup,
}: {
  enabled: boolean;
  items: ProfileCheckLog[];
  total: number;
  page: number;
  loading: boolean;
  deleting: boolean;
  filters: ProfileCheckLogFilters;
  onSearchChange: (value: string) => void;
  onResultChange: (value: string) => void;
  onModeChange: (value: string) => void;
  onPageChange: (value: number) => void;
  onCleanup: (days: number) => void;
}) {
  const pageSize = 20;
  const totalPages = Math.max(1, Math.ceil(total / pageSize));

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
          <div>
            <CardTitle>📋 简介检查日志</CardTitle>
            <p className="mt-0.5 text-xs text-[var(--text-muted)]">
              展示每次 `checkProfile` 的执行结果，支持搜索、筛选和按天清理。
            </p>
          </div>
          {!enabled ? (
            <Badge>资料检查已关闭</Badge>
          ) : (
            <Badge>{`共 ${total} 条`}</Badge>
          )}
        </div>
      </CardHeader>
      {!enabled ? (
        <CardBody>
          <div className="rounded-md border border-dashed border-[var(--border)] bg-[var(--surface-2)] px-4 py-6 text-sm text-[var(--text-muted)]">
            资料检查已关闭
          </div>
        </CardBody>
      ) : (
        <CardBody className="space-y-4">
          <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_180px_180px]">
            <Input
              value={filters.search}
              onChange={(e) => onSearchChange(e.target.value)}
              placeholder="搜索用户名 / Bio"
            />
            <Select
              value={filters.result}
              onChange={(e) => onResultChange(e.target.value)}
              options={[
                { label: "全部结果", value: "" },
                { label: "通过", value: "pass" },
                { label: "命中", value: "hit" },
                { label: "跳过", value: "skip" },
                { label: "错误", value: "error" },
              ]}
            />
            <Select
              value={filters.mode}
              onChange={(e) => onModeChange(e.target.value)}
              options={[
                { label: "全部模式", value: "" },
                { label: "入群关键词 (keyword)", value: "keyword" },
                { label: "入群AI (ai)", value: "ai" },
                {
                  label: "发言前关键词 (on_message_keyword)",
                  value: "on_message_keyword",
                },
                { label: "发言前AI (on_message_ai)", value: "on_message_ai" },
              ]}
            />
          </div>

          <Table>
            <TableHead>
              <TableRow>
                <TableHeaderCell>时间</TableHeaderCell>
                <TableHeaderCell>用户</TableHeaderCell>
                <TableHeaderCell>Bio</TableHeaderCell>
                <TableHeaderCell>模式</TableHeaderCell>
                <TableHeaderCell>结果</TableHeaderCell>
                <TableHeaderCell>详情</TableHeaderCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {loading ? (
                <TableRow>
                  <TableCell
                    colSpan={6}
                    className="text-center text-[var(--text-muted)]"
                  >
                    加载中...
                  </TableCell>
                </TableRow>
              ) : items.length === 0 ? (
                <TableRow>
                  <TableCell
                    colSpan={6}
                    className="text-center text-[var(--text-muted)]"
                  >
                    暂无日志
                  </TableCell>
                </TableRow>
              ) : (
                items.map((item) => {
                  const bio = item.bio ?? "";
                  const username = item.username?.trim() ?? "";
                  const userLabel = [item.user_name?.trim(), username]
                    .filter(Boolean)
                    .join(" ");
                  const detail = buildProfileCheckLogDetail(item);

                  return (
                    <TableRow key={item.id}>
                      <TableCell className="whitespace-nowrap tabular-nums text-[var(--text-muted)]">
                        {formatBeijingTime(item.created_at)}
                      </TableCell>
                      <TableCell className="max-w-[180px]">
                        <div className="truncate font-medium">
                          {userLabel || `用户 ${item.user_id}`}
                        </div>
                        <div className="text-xs text-[var(--text-muted)]">
                          ID {item.user_id}
                        </div>
                      </TableCell>
                      <TableCell className="max-w-[320px]" title={bio || "-"}>
                        <span className="block truncate">
                          {bio ? truncateText(bio, 50) : "-"}
                        </span>
                      </TableCell>
                      <TableCell>
                        <Badge
                          tone={item.check_mode === "ai" ? "info" : "default"}
                        >
                          {item.check_mode}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        <Badge tone={profileCheckResultTone(item.result)}>
                          {profileCheckResultLabel(item.result)}
                        </Badge>
                      </TableCell>
                      <TableCell className="max-w-[240px]" title={detail}>
                        <span className="block truncate">{detail}</span>
                      </TableCell>
                    </TableRow>
                  );
                })
              )}
            </TableBody>
          </Table>

          <div className="flex flex-col gap-3 border-t border-[var(--border)] pt-4 md:flex-row md:items-center md:justify-between">
            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant="outline"
                onClick={() => onCleanup(30)}
                disabled={deleting}
              >
                {deleting ? "清理中..." : "清理 30 天前日志"}
              </Button>
            </div>
            <div className="flex items-center gap-2 self-end md:self-auto">
              <span className="text-xs text-[var(--text-muted)]">
                {`第 ${Math.min(page, totalPages)} / ${totalPages} 页`}
              </span>
              <Button
                type="button"
                variant="secondary"
                size="sm"
                onClick={() => onPageChange(Math.max(1, page - 1))}
                disabled={page <= 1 || loading}
              >
                上一页
              </Button>
              <Button
                type="button"
                variant="secondary"
                size="sm"
                onClick={() => onPageChange(Math.min(totalPages, page + 1))}
                disabled={page >= totalPages || loading}
              >
                下一页
              </Button>
            </div>
          </div>
        </CardBody>
      )}
    </Card>
  );
}

function profileCheckResultTone(
  result: string,
): "success" | "danger" | "default" | "warning" {
  switch (result) {
    case "pass":
      return "success";
    case "hit":
      return "danger";
    case "error":
      return "warning";
    default:
      return "default";
  }
}

function profileCheckResultLabel(result: string) {
  switch (result) {
    case "pass":
      return "通过";
    case "hit":
      return "命中";
    case "skip":
      return "跳过";
    case "error":
      return "错误";
    default:
      return result || "-";
  }
}

function buildProfileCheckLogDetail(item: ProfileCheckLog) {
  if (item.check_mode === "ai") {
    const parts = [item.ai_verdict ?? ""];
    if (typeof item.ai_confidence === "number") {
      parts.push(`${Math.round(item.ai_confidence * 100)}%`);
    }
    if (item.result === "hit" && item.matched_rule) {
      parts.push(item.matched_rule);
    }
    return parts.filter(Boolean).join(" | ") || "-";
  }
  if (item.result === "hit") {
    return item.matched_rule ?? "-";
  }
  return "-";
}

type ActionFeedbackValue = {
  enabled: boolean;
  template: string;
  auto_delete_seconds: number;
  reply_to_message: boolean;
  parse_mode?: string;
};

type FeedbackActionMeta = {
  key: string;
  label: string;
  description: string;
  vars: string;
  defaultTemplate: string;
  defaultEnabled: boolean;
  defaultAutoDelete: number;
  defaultReplyToMsg: boolean;
};

const FEEDBACK_ACTIONS: FeedbackActionMeta[] = [
  {
    key: "delete_msg",
    label: "删除消息",
    description: "命中过滤/AI 时删除消息的反馈（默认关闭，避免刷屏）",
    vars: "{user} {user_mention} {reason} {group}",
    defaultTemplate: "🧹 {user} 的消息被清扫员扫进垃圾桶了（{reason}）",
    defaultEnabled: false,
    defaultAutoDelete: 15,
    defaultReplyToMsg: false,
  },
  {
    key: "mute",
    label: "禁言",
    description: "禁言用户时的反馈",
    vars: "{user} {user_mention} {duration} {reason}",
    defaultTemplate:
      "🤐 {user} 被贴了 {duration} 的封口胶。冷静一下，思考人生（{reason}）",
    defaultEnabled: true,
    defaultAutoDelete: 60,
    defaultReplyToMsg: false,
  },
  {
    key: "kick",
    label: "踢出",
    description: "踢出用户时的反馈",
    vars: "{user} {user_mention} {reason}",
    defaultTemplate: "👢 {user} 被一脚踹出门外。下次再来请文明发言（{reason}）",
    defaultEnabled: true,
    defaultAutoDelete: 60,
    defaultReplyToMsg: false,
  },
  {
    key: "ban",
    label: "封禁",
    description: "封禁用户时的反馈",
    vars: "{user} {user_mention} {reason}",
    defaultTemplate: "🚓 {user} 被正义执法，带走调查。理由：{reason}",
    defaultEnabled: true,
    defaultAutoDelete: 60,
    defaultReplyToMsg: false,
  },
  {
    key: "warn",
    label: "警告",
    description: "警告累加时的反馈（显示当前/上限）",
    vars: "{user} {user_mention} {current} {limit} {reason}",
    defaultTemplate:
      "⚠️ 警告一下 {user}，累计 {current}/{limit}。再犯就请你吃手铐了（{reason}）",
    defaultEnabled: true,
    defaultAutoDelete: 60,
    defaultReplyToMsg: true,
  },
  {
    key: "verify_pass",
    label: "验证通过",
    description: "新人验证通过时的反馈（默认关闭）",
    vars: "{user} {user_mention} {group}",
    defaultTemplate:
      "✅ {user} 通过安检，欢迎入群！麻烦看下群规，别让我下次在违规榜上见到你",
    defaultEnabled: false,
    defaultAutoDelete: 15,
    defaultReplyToMsg: false,
  },
  {
    key: "verify_fail",
    label: "验证失败/超时",
    description:
      "新人验证失败或超时被踢时的反馈。{reason} 会注入具体原因（超时未完成验证 / 答题错误 / 个人简介违规：xxx）",
    vars: "{user} {user_mention} {reason}",
    defaultTemplate:
      "⌛ {user} 验证未通过（{reason}），已被礼送出境。真人欢迎重新申请加群",
    defaultEnabled: true,
    defaultAutoDelete: 60,
    defaultReplyToMsg: false,
  },
  {
    key: "cas_hit",
    label: "CAS 命中",
    description: "用户命中 CAS 黑名单被封时的反馈",
    vars: "{user} {user_mention} {reason}",
    defaultTemplate: "🚫 {user} 已被 CAS 全网通缉，直接押送离场",
    defaultEnabled: true,
    defaultAutoDelete: 60,
    defaultReplyToMsg: false,
  },
  {
    key: "trust_graduated",
    label: "信任毕业",
    description: "用户成为老用户时的反馈（默认关闭）",
    vars: "{user} {user_mention} {days} {messages}",
    defaultTemplate: "🎓 恭喜 {user} 通过见习期，正式成为群里的老油条",
    defaultEnabled: false,
    defaultAutoDelete: 30,
    defaultReplyToMsg: false,
  },
  {
    key: "admin_action",
    label: "管理员操作",
    description: "/warn /ban /mute 等管理员命令后的反馈",
    vars: "{admin} {admin_mention} {user} {user_mention} {action} {reason}",
    defaultTemplate: "👮 {admin} 对 {user} 执行了「{action}」。理由：{reason}",
    defaultEnabled: true,
    defaultAutoDelete: 0,
    defaultReplyToMsg: false,
  },
];

function defaultFeedback(meta?: FeedbackActionMeta): ActionFeedbackValue {
  if (!meta) {
    return {
      enabled: false,
      template: "",
      auto_delete_seconds: 30,
      reply_to_message: false,
    };
  }
  return {
    enabled: meta.defaultEnabled,
    template: meta.defaultTemplate,
    auto_delete_seconds: meta.defaultAutoDelete,
    reply_to_message: meta.defaultReplyToMsg,
  };
}

export function FeedbackSection({
  value,
  onChange,
}: {
  value: Record<string, ActionFeedbackValue>;
  onChange: (next: Record<string, ActionFeedbackValue>) => void;
}) {
  // 首次渲染时把缺失的动作补齐默认值，避免保存时 template 为空
  useEffect(() => {
    const next = { ...value };
    let changed = false;
    for (const meta of FEEDBACK_ACTIONS) {
      if (!next[meta.key]) {
        next[meta.key] = defaultFeedback(meta);
        changed = true;
      }
    }
    if (changed) {
      onChange(next);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function updateOne(key: string, patch: Partial<ActionFeedbackValue>) {
    const meta = FEEDBACK_ACTIONS.find((x) => x.key === key);
    const cur = { ...(value[key] ?? defaultFeedback(meta)) };
    const next = { ...cur, ...patch };
    onChange({ ...value, [key]: next });
  }

  return (
    <div className="space-y-3">
      {FEEDBACK_ACTIONS.map((item) => {
        const v = value[item.key] ?? defaultFeedback(item);
        return (
          <Card key={item.key}>
            <CardHeader className="flex-row items-center justify-between">
              <div>
                <CardTitle>{item.label}</CardTitle>
                <p className="mt-0.5 text-xs text-[var(--text-muted)]">
                  {item.description}
                </p>
              </div>
              <Switch
                checked={v.enabled}
                onChange={(checked) =>
                  updateOne(item.key, { enabled: checked })
                }
              />
            </CardHeader>
            {v.enabled && (
              <CardBody className="space-y-3">
                <div className="space-y-1.5">
                  <div className="flex items-center justify-between">
                    <label className="text-xs font-medium text-[var(--text-muted)]">
                      回复模板（可用变量：{item.vars}）
                    </label>
                    <button
                      type="button"
                      className="text-xs text-[var(--accent)] hover:underline"
                      onClick={() =>
                        updateOne(item.key, { template: item.defaultTemplate })
                      }
                    >
                      重置为默认
                    </button>
                  </div>
                  <Textarea
                    value={v.template || item.defaultTemplate}
                    onChange={(e) =>
                      updateOne(item.key, { template: e.target.value })
                    }
                    className="min-h-[80px] font-mono text-xs"
                    placeholder={item.defaultTemplate}
                  />
                </div>
                <div className="grid gap-3 md:grid-cols-2">
                  <div className="space-y-1.5">
                    <label className="text-xs font-medium text-[var(--text-muted)]">
                      自动撤回秒数（0 = 不撤回）
                    </label>
                    <Input
                      type="number"
                      value={v.auto_delete_seconds}
                      onChange={(e) =>
                        updateOne(item.key, {
                          auto_delete_seconds: Number(e.target.value) || 0,
                        })
                      }
                    />
                  </div>
                  <div className="flex items-end">
                    <label className="flex items-center gap-2 text-sm">
                      <Switch
                        checked={v.reply_to_message}
                        onChange={(checked) =>
                          updateOne(item.key, { reply_to_message: checked })
                        }
                      />
                      <span>Reply 违规消息</span>
                    </label>
                  </div>
                </div>
                <div className="space-y-1.5">
                  <label className="text-xs font-medium text-[var(--text-muted)]">
                    解析模式
                  </label>
                  <select
                    className="w-full rounded-md border border-[var(--border)] bg-[var(--bg-card)] px-3 py-2 text-sm"
                    value={v.parse_mode || "markdownv2"}
                    onChange={(e) =>
                      updateOne(item.key, { parse_mode: e.target.value })
                    }
                  >
                    <option value="markdownv2">MarkdownV2</option>
                    <option value="html">HTML</option>
                    <option value="markdown">Markdown (legacy)</option>
                  </select>
                </div>
              </CardBody>
            )}
          </Card>
        );
      })}
    </div>
  );
}

type KeywordReplySectionProps = {
  rules: KeywordReplyRule[];
  inherited?: boolean;
  onToggleInherited?: (checked: boolean) => void;
  onChange: (rules: KeywordReplyRule[]) => void;
};

export function KeywordReplySection({
  rules,
  inherited = false,
  onToggleInherited,
  onChange,
}: KeywordReplySectionProps) {
  const [searchQuery, setSearchQuery] = useState("");
  const [expandedIds, setExpandedIds] = useState<string[]>([]);

  function toggleExpand(id: string) {
    setExpandedIds((prev) =>
      prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id],
    );
  }

  const filteredRules = rules.filter((rule) => {
    if (!searchQuery.trim()) return true;
    const q = searchQuery.toLowerCase();
    return (
      (rule.name || "").toLowerCase().includes(q) ||
      (rule.keywords || []).some((kw) => kw.toLowerCase().includes(q)) ||
      (rule.reply_text || "").toLowerCase().includes(q)
    );
  });

  function updateRule(id: string, patch: Partial<KeywordReplyRule>) {
    onChange(
      rules.map((rule) => (rule.id === id ? { ...rule, ...patch } : rule)),
    );
  }

  function deleteRule(id: string) {
    onChange(rules.filter((rule) => rule.id !== id));
  }

  function addRule() {
    onChange([
      ...rules,
      {
        id: crypto.randomUUID(),
        name: "",
        enabled: true,
        match_type: "fuzzy",
        keywords: [],
        case_sensitive: false,
        reply_text: "",
        auto_delete_seconds: 0,
        cooldown_seconds: 0,
        skip_admins: true,
        trigger_count: 0,
        last_triggered_at: null,
      },
    ]);
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex items-start justify-between gap-4">
          <div>
            <CardTitle>关键词回复</CardTitle>
            <p className="mt-1 text-xs text-[var(--text-muted)]">
              命中关键词后自动回复。管理员消息不触发。
            </p>
          </div>
          {onToggleInherited ? (
            <label className="inline-flex items-center gap-1.5 text-xs text-[var(--text-muted)] whitespace-nowrap cursor-pointer select-none">
              <input
                type="checkbox"
                checked={inherited}
                onChange={(e) => onToggleInherited(e.target.checked)}
                className="rounded border-[var(--border)]"
              />
              继承全局
            </label>
          ) : null}
        </div>
      </CardHeader>
      <CardBody className="space-y-4">
        {rules.length === 0 ? (
          <div className="rounded-md border border-dashed border-[var(--border)] bg-[var(--surface-2)] px-4 py-8 text-center text-sm text-[var(--text-muted)]">
            暂无规则
          </div>
        ) : null}
        {rules.length > 0 ? (
          <input
            type="text"
            placeholder="搜索规则名称、关键词、回复内容…"
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className="rounded-md border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm w-full"
          />
        ) : null}
        {filteredRules.length === 0 && rules.length > 0 ? (
          <div className="rounded-md border border-dashed border-[var(--border)] bg-[var(--surface-2)] px-4 py-6 text-center text-sm text-[var(--text-muted)]">
            无匹配规则
          </div>
        ) : (
          filteredRules.map((rule, index) => {
            const isExpanded = expandedIds.includes(rule.id || `idx-${index}`);
            return (
              <div
                key={rule.id || index}
                className="rounded-xl border border-[var(--border)] bg-[var(--surface-2)]"
              >
                <div
                  className="flex flex-wrap items-center gap-2 cursor-pointer select-none px-4 py-2.5"
                  onClick={() => toggleExpand(rule.id || `idx-${index}`)}
                >
                  <span className="min-w-[220px] flex-1 truncate text-sm font-medium">
                    {rule.name || "未命名规则"}
                  </span>
                  <Badge tone={rule.enabled ? "info" : "default"}>
                    {rule.enabled ? "已启用" : "已停用"}
                  </Badge>
                  <Badge>{`命中 ${rule.trigger_count ?? 0}`}</Badge>
                  <Badge>{`最后触发 ${rule.last_triggered_at ? formatTime(rule.last_triggered_at) : "-"}`}</Badge>
                  <div className="ml-auto flex items-center gap-3">
                    <label
                      className="inline-flex items-center gap-2 text-sm text-[var(--text)]"
                      onClick={(e) => e.stopPropagation()}
                    >
                      <Switch
                        checked={rule.enabled}
                        disabled={inherited}
                        onChange={(value) =>
                          updateRule(rule.id, { enabled: value })
                        }
                      />
                    </label>
                    <Button
                      type="button"
                      variant="secondary"
                      disabled={inherited}
                      onClick={(e) => {
                        e.stopPropagation();
                        deleteRule(rule.id);
                      }}
                      className="!px-2 !py-1 text-xs"
                    >
                      删除
                    </Button>
                    <span className="text-xs text-[var(--text-muted)] select-none">
                      {isExpanded ? "▼" : "▶"}
                    </span>
                  </div>
                </div>
                {isExpanded && (
                  <div className="px-4 pb-4 space-y-4">
                    <div className="grid gap-4 md:grid-cols-2">
                      <div className="space-y-2">
                        <p className="text-xs font-medium text-[var(--text-muted)]">
                          规则名称
                        </p>
                        <Input
                          disabled={inherited}
                          value={rule.name}
                          onChange={(e) =>
                            updateRule(rule.id, { name: e.target.value })
                          }
                          placeholder="规则名称"
                        />
                      </div>
                      <div className="space-y-2">
                        <p className="text-xs font-medium text-[var(--text-muted)]">
                          状态
                        </p>
                        <div className="flex items-center gap-2">
                          <Badge tone={rule.enabled ? "info" : "default"}>
                            {rule.enabled ? "已启用" : "已停用"}
                          </Badge>
                          <Badge>{`命中 ${rule.trigger_count ?? 0}`}</Badge>
                          <Badge>{`最后触发 ${rule.last_triggered_at ? formatTime(rule.last_triggered_at) : "-"}`}</Badge>
                        </div>
                      </div>
                    </div>

                    <div className="grid gap-4 md:grid-cols-2">
                      <div className="space-y-2">
                        <p className="text-xs font-medium text-[var(--text-muted)]">
                          匹配类型
                        </p>
                        <Select
                          disabled={inherited}
                          value={rule.match_type}
                          onChange={(e) =>
                            updateRule(rule.id, { match_type: e.target.value })
                          }
                          options={[
                            { label: "模糊", value: "fuzzy" },
                            { label: "精准", value: "exact" },
                            { label: "正则", value: "regex" },
                          ]}
                        />
                      </div>
                      <label className="flex items-center gap-3 rounded-md border border-[var(--border)] bg-[var(--surface)] px-3 py-2 text-sm">
                        <Switch
                          checked={rule.case_sensitive}
                          disabled={inherited}
                          onChange={(value) =>
                            updateRule(rule.id, { case_sensitive: value })
                          }
                        />
                        <span>大小写敏感</span>
                      </label>
                      <div className="space-y-2">
                        <p className="text-xs font-medium text-[var(--text-muted)]">
                          解析模式
                        </p>
                        <Select
                          disabled={inherited}
                          value={rule.parse_mode || "markdownv2"}
                          onChange={(e) =>
                            updateRule(rule.id, { parse_mode: e.target.value })
                          }
                          options={[
                            { label: "MarkdownV2", value: "markdownv2" },
                            { label: "HTML", value: "html" },
                            { label: "Markdown", value: "markdown" },
                          ]}
                        />
                      </div>
                    </div>

                    <div className="space-y-2">
                      <p className="text-xs font-medium text-[var(--text-muted)]">
                        关键词
                      </p>
                      <ArrayTextarea
                        disabled={inherited}
                        value={
                          Array.isArray(rule.keywords) ? rule.keywords : []
                        }
                        onChange={(raw) =>
                          updateRule(rule.id, {
                            keywords: parseArrayValue(
                              raw,
                              rule.match_type !== "regex",
                            ),
                          })
                        }
                        className="min-h-[110px] font-mono text-xs"
                        placeholder={"支持换行、,、，分隔\n例如：签到\n早安"}
                      />
                    </div>

                    <div className="space-y-2">
                      <p className="text-xs font-medium text-[var(--text-muted)]">
                        回复内容
                      </p>
                      <Textarea
                        disabled={inherited}
                        value={rule.reply_text}
                        onChange={(e) =>
                          updateRule(rule.id, { reply_text: e.target.value })
                        }
                        className="min-h-[140px] font-mono text-xs"
                        placeholder="支持变量 {user} {group} {keyword}，HTML 格式"
                      />
                    </div>

                    <div className="grid gap-4 md:grid-cols-2">
                      <div className="space-y-2">
                        <p className="text-xs font-medium text-[var(--text-muted)]">
                          冷却秒数
                        </p>
                        <Input
                          disabled={inherited}
                          type="number"
                          value={rule.cooldown_seconds}
                          onChange={(e) =>
                            updateRule(rule.id, {
                              cooldown_seconds: Number(e.target.value) || 0,
                            })
                          }
                        />
                      </div>
                      <div className="space-y-2">
                        <p className="text-xs font-medium text-[var(--text-muted)]">
                          自动删除秒数
                        </p>
                        <Input
                          disabled={inherited}
                          type="number"
                          value={rule.auto_delete_seconds}
                          onChange={(e) =>
                            updateRule(rule.id, {
                              auto_delete_seconds: Number(e.target.value) || 0,
                            })
                          }
                        />
                      </div>
                    </div>

                    <div>
                      <label className="flex items-center gap-2 text-sm">
                        <Switch
                          checked={rule.skip_admins !== false}
                          disabled={inherited}
                          onChange={(v) =>
                            updateRule(rule.id, { skip_admins: v })
                          }
                        />
                        <span>不对管理员触发</span>
                      </label>
                    </div>
                  </div>
                )}
              </div>
            );
          })
        )}

        <Button type="button" disabled={inherited} onClick={addRule}>
          + 添加规则
        </Button>
      </CardBody>
    </Card>
  );
}

export function ArrayTextarea({
  value,
  onChange,
  disabled,
  className,
  placeholder,
}: {
  value: string[];
  onChange: (raw: string) => void;
  disabled?: boolean;
  className?: string;
  placeholder?: string;
}) {
  // 维护原始字符串，让换行/逗号/全角逗号等"分隔符输入"留在文本里
  const [text, setText] = useState(() => formatArrayValue(value));
  // 标记最近一次从本地 onChange 推出的 array，用于去抖外部回流
  const lastEmitted = useRef<string[]>(Array.isArray(value) ? [...value] : []);

  useEffect(() => {
    const incoming = Array.isArray(value) ? value : [];
    // 只有当外部 array 与我最近 emit 的 array 不一致时才覆盖本地 text
    // （这样"本地打了分隔符 → emit 同样 array → 回流"不会清掉分隔符）
    const same =
      incoming.length === lastEmitted.current.length &&
      incoming.every((v, i) => v === lastEmitted.current[i]);
    if (!same) {
      lastEmitted.current = [...incoming];
      setText(formatArrayValue(incoming));
    }
  }, [value]);

  return (
    <Textarea
      disabled={disabled}
      placeholder={placeholder}
      value={text}
      onChange={(e) => {
        const v = e.target.value;
        setText(v);
        // 同步记录 emit 出去的 array（parse 结果）
        lastEmitted.current = v
          .split(/[\n,，]/)
          .map((item) => item.trim())
          .filter(Boolean);
        onChange(v);
      }}
      className={className}
    />
  );
}

export function ModelRefSelect({
  value,
  disabled,
  options,
  placeholder,
  onChange,
}: {
  value: string;
  disabled?: boolean;
  options: LLMModelOption[];
  placeholder?: string;
  onChange: (value: string) => void;
}) {
  if (options.length === 0) {
    return (
      <Input
        disabled={disabled}
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
      />
    );
  }

  return (
    <Select
      disabled={disabled}
      value={value}
      onChange={(e) => onChange(e.target.value)}
    >
      <option value="">请选择模型</option>
      {options.map((option) => (
        <option key={option.value} value={option.value}>
          {option.label}
        </option>
      ))}
    </Select>
  );
}

export function ModelRefListControl({
  value,
  disabled,
  options,
  onChange,
}: {
  value: string[];
  disabled?: boolean;
  options: LLMModelOption[];
  onChange: (value: string[]) => void;
}) {
  const [pending, setPending] = useState("");

  useEffect(() => {
    if (disabled) {
      setPending("");
    }
  }, [disabled]);

  const canAdd = pending.trim() !== "" && !value.includes(pending.trim());

  function addValue(nextValue?: string) {
    const raw = (nextValue ?? pending).trim();
    if (!raw || value.includes(raw)) {
      return;
    }
    onChange([...value, raw]);
    setPending("");
  }

  function removeValue(target: string) {
    onChange(value.filter((item) => item !== target));
  }

  function moveValue(index: number, delta: number) {
    const nextIndex = index + delta;
    if (nextIndex < 0 || nextIndex >= value.length) {
      return;
    }
    const next = [...value];
    const [item] = next.splice(index, 1);
    next.splice(nextIndex, 0, item);
    onChange(next);
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap gap-2">
        {value.length === 0 && (
          <span className="text-xs text-[var(--text-muted)]">
            暂无 fallback 模型
          </span>
        )}
        {value.map((item, index) => (
          <div
            key={`${item}-${index}`}
            className="flex items-center gap-1 rounded-md border border-[var(--border)] bg-[var(--surface-2)] px-2 py-1 text-xs"
          >
            <span className="font-mono">{item}</span>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={disabled || index === 0}
              onClick={() => moveValue(index, -1)}
            >
              ↑
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={disabled || index === value.length - 1}
              onClick={() => moveValue(index, 1)}
            >
              ↓
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={disabled}
              onClick={() => removeValue(item)}
            >
              删除
            </Button>
          </div>
        ))}
      </div>
      <div className="flex flex-col gap-2 md:flex-row">
        <div className="flex-1">
          <ModelRefSelect
            value={pending}
            disabled={disabled}
            options={options}
            placeholder="provider:model"
            onChange={setPending}
          />
        </div>
        <Button
          type="button"
          variant="secondary"
          disabled={disabled || !canAdd}
          onClick={() => addValue()}
        >
          添加
        </Button>
      </div>
    </div>
  );
}

function FieldControl({
  field,
  value,
  inherited,
  modelOptions,
  modelOptionsError,
  onChange,
}: {
  field: FieldDescriptor;
  value: unknown;
  inherited: boolean;
  modelOptions: LLMModelOption[];
  modelOptionsError: string | null;
  onChange: (field: FieldDescriptor, v: string | boolean | string[]) => void;
}) {
  if (field.kind === "switch") {
    return (
      <Switch
        checked={Boolean(value)}
        disabled={inherited}
        onChange={(v) => onChange(field, v)}
      />
    );
  }

  if (field.kind === "text") {
    return (
      <Input
        disabled={inherited}
        placeholder={field.placeholder}
        value={typeof value === "string" || value === null ? (value ?? "") : ""}
        onChange={(e) => onChange(field, e.target.value)}
      />
    );
  }

  if (field.kind === "number") {
    const numericValue =
      typeof value === "number"
        ? value
        : value == null || Number.isNaN(Number(value))
          ? ""
          : Number(value);
    return (
      <Input
        disabled={inherited}
        type="number"
        value={numericValue}
        onChange={(e) => onChange(field, e.target.value)}
      />
    );
  }

  if (field.kind === "textarea" || field.kind === "username-list") {
    return (
      <ArrayTextarea
        disabled={inherited}
        value={Array.isArray(value) ? (value as string[]) : []}
        onChange={(s) => onChange(field, s)}
        className="font-mono text-xs"
      />
    );
  }

  if (field.kind === "readonly-number") {
    const numericValue =
      typeof value === "number"
        ? value
        : value == null || Number.isNaN(Number(value))
          ? ""
          : Number(value);
    return (
      <Input
        disabled
        type="number"
        value={numericValue}
        aria-readonly="true"
      />
    );
  }

  if (field.kind === "template") {
    return (
      <Textarea
        disabled={inherited}
        placeholder={field.placeholder}
        value={typeof value === "string" || value === null ? (value ?? "") : ""}
        onChange={(e) => onChange(field, e.target.value)}
        className="min-h-[120px] font-mono text-xs"
      />
    );
  }

  if (field.kind === "select" && field.options) {
    return (
      <Select
        disabled={inherited}
        value={typeof value === "string" ? value : field.options[0]?.value}
        options={field.options}
        onChange={(e) => onChange(field, e.target.value)}
      />
    );
  }

  if (field.kind === "model-ref") {
    return (
      <div className="space-y-2">
        <ModelRefSelect
          value={typeof value === "string" ? value : ""}
          disabled={inherited}
          options={modelOptions}
          placeholder="provider:model"
          onChange={(nextValue) => onChange(field, nextValue)}
        />
        {modelOptionsError && (
          <p className="text-xs text-[var(--warning)]">
            模型列表加载失败，已降级为文本输入：{modelOptionsError}
          </p>
        )}
      </div>
    );
  }

  if (field.kind === "model-ref-list") {
    return (
      <div className="space-y-2">
        <ModelRefListControl
          value={Array.isArray(value) ? (value as string[]) : []}
          disabled={inherited}
          options={modelOptions}
          onChange={(nextValue) => onChange(field, nextValue)}
        />
        {modelOptionsError && (
          <p className="text-xs text-[var(--warning)]">
            模型列表加载失败，已降级为文本输入：{modelOptionsError}
          </p>
        )}
      </div>
    );
  }

  return null;
}

function getEffectiveValue(
  draft: Record<string, unknown>,
  mergedPolicy: GuardPolicy,
  path: string[],
) {
  return hasPath(draft, path)
    ? getPathValue(draft, path)
    : getPathValue(mergedPolicy, path);
}

function removePath(target: Record<string, unknown>, path: string[]) {
  const chain: Array<Record<string, unknown>> = [];
  let current: Record<string, unknown> | undefined = target;

  for (let i = 0; i < path.length - 1; i += 1) {
    if (!current) return;
    chain.push(current);
    current = current[path[i]] as Record<string, unknown> | undefined;
  }
  if (!current) return;

  delete current[path[path.length - 1]];

  for (let i = path.length - 2; i >= 0; i -= 1) {
    const parent = chain[i];
    const key = path[i];
    const value = parent[key];
    if (
      value &&
      typeof value === "object" &&
      !Array.isArray(value) &&
      Object.keys(value as Record<string, unknown>).length === 0
    ) {
      delete parent[key];
    }
  }
}

function formatStructuredValue(value: unknown) {
  if (value == null) return "—";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function renderWelcomePreview(
  templateValue: unknown,
  context: {
    groupTitle: string;
    groupId: number;
    memberCount: number;
    rulesLink: string;
  },
) {
  const esc = (v: string) =>
    v.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");

  const template =
    typeof templateValue === "string" && templateValue.trim() !== ""
      ? templateValue
      : "🎉 欢迎 {user_mention} 加入 {group_title}！\n\n👋 看一下群规，交流愉快。\n\n🔗 群规：{rules_link}\n📱 有问题私聊管理员：{admin_list}";

  let result = esc(template);
  const replacements: Record<string, string> = {
    "{user_mention}": `<a href="tg://user?id=6425070392" class="text-[var(--accent)]">可乐</a>`,
    "{user_id}": "6425070392",
    "{user_name}": "可乐",
    "{group_title}": esc(context.groupTitle),
    "{group_id}": String(context.groupId),
    "{member_count}": String(context.memberCount),
    "{rules_link}": esc(context.rulesLink || "https://example.com/rules"),
    "{admin_list}": "@kele_admin @mod_team",
    "{date}": "2026-04-19",
    "{time}": "02:45",
  };
  Object.entries(replacements).forEach(([k, v]) => {
    result = result.replaceAll(k, v);
  });
  return result.replaceAll("\n", "<br />");
}
