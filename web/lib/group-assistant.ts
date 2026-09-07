import { apiFetch } from "@/lib/api";

export type AssistantToolName =
  | "knowledge_query"
  | "conversation_recall"
  | "webfetch_readonly";

export type AssistantMemoryValidScope =
  | "today"
  | "this_week"
  | "this_month"
  | "current_group"
  | "long_term"
  | "weekly"
  | "retention_window";

export type AssistantMemorySourceType =
  | "admin_base"
  | "admin_explicit"
  | "admin_explicit_correction"
  | "admin_conflict_accept"
  | "pinned_announcement"
  | "telegram_message"
  | "telegram_approved_message"
  | "telegram_admin_explicit_correction"
  | "telegram_edited_message"
  | "learned_fact"
  | "unknown"
  | string;

export type AssistantPolicy = {
  chat_id: number;
  version: number;
  chat_enabled: boolean;
  learning_enabled: boolean;
  trigger_mode: "mention_or_reply" | "mention_only" | string;
  followup_window_sec: number;
  max_followup_turns: number;
  chat_model_ref: string;
  learning_model_ref: string;
  temperature: number;
  system_prompt: string;
  history_limit: number;
  retention_days: number;
  collection_policy: string;
  tool_allowlist: AssistantToolName[];
  allow_domains: string[];
  max_queue_depth: number;
  max_queue_wait_sec: number;
  proactive_interject_enabled?: boolean;
  proactive_cold_topic_enabled?: boolean;
  cold_topic_idle_minutes?: number;
  cold_topic_quiet_start?: number;
  cold_topic_quiet_end?: number;
  mimic_target_user_id?: number;
  mimic_target_user_name?: string;
  mimic_profile_text?: string;
  mimic_sample_count?: number;
  mimic_distilled_at_count?: number;
  updated_by?: number | null;
  created_at?: string;
  updated_at?: string;
};

export type AssistantTaskAssignment = {
  primary: string;
  backups: string[];
};

export type AssistantPoolEndpoint = {
  id: string;
  name?: string;
  model_ref: string;
  role: "primary" | "backup" | string;
  priority: number;
  max_concurrency: number;
  timeout_ms: number;
  cooldown_duration_sec: number;
  supports_tools: boolean;
};

export type AssistantPoolConfig = {
  task_assignments: Record<string, AssistantTaskAssignment>;
  endpoints: AssistantPoolEndpoint[];
  max_queue_depth: number;
  max_queue_wait_sec: number;
};

export type AssistantPool = {
  version: number;
  strategy: "primary-overflow" | string;
  config: AssistantPoolConfig;
  updated_at?: string;
};

export type AssistantDefaults = {
  disabled: boolean;
  retention_days: number;
  history_retention: string;
  remote_quota: string;
};

export type AssistantPolicyWrite = {
  chat_enabled: boolean;
  learning_enabled: boolean;
  trigger_mode: string;
  followup_window_sec: number;
  max_followup_turns: number;
  chat_model_ref: string;
  learning_model_ref: string;
  temperature: number;
  system_prompt: string;
  history_limit: number;
  retention_days: number;
  collection_policy: string;
  tool_allowlist: AssistantToolName[];
  allow_domains: string[];
  max_queue_depth: number;
  max_queue_wait_sec: number;
  proactive_interject_enabled: boolean;
  proactive_cold_topic_enabled: boolean;
  cold_topic_idle_minutes: number;
  cold_topic_quiet_start: number;
  cold_topic_quiet_end: number;
  mimic_target_user_id: number;
  mimic_target_user_name: string;
  mimic_profile_text: string;
  mimic_sample_count: number;
  mimic_distilled_at_count: number;
};

export type AssistantReadiness = {
  can_chat?: boolean;
  blockers?: string[];
  chat_primary_model_ref?: string;
  tools_declared?: boolean;
  [key: string]: unknown;
};

export type AssistantRecentSender = {
  user_id: number;
  user_name: string;
  message_count?: number;
  last_seen_at?: string;
  [key: string]: unknown;
};

export type AssistantOverview = {
  policy: AssistantPolicy;
  model_pool: AssistantPool;
  defaults: AssistantDefaults;
  readiness?: AssistantReadiness;
};

export type AssistantEndpointStatus = {
  id: string;
  role: string;
  model_ref: string;
  provider_ref: string;
  model_label: string;
  current_active: number;
  max_concurrency: number;
  is_full: boolean;
  status: "unknown" | "healthy" | "cooldown" | "unhealthy" | "half_open" | string;
  cooldown_remaining_sec: number;
  last_error?: string;
  supports_tools: boolean;
  local_limit_label: string;
  remote_quota_observed: string;
};

export type AssistantLastDispatch = {
  timestamp: string;
  task_type: string;
  selected_endpoint_id: string;
  reason: string;
  status: string;
};

export type AssistantStatus = {
  chat_id: number;
  active_strategy: string;
  endpoints_status: AssistantEndpointStatus[];
  queue_depth: number;
  last_dispatch_event?: AssistantLastDispatch | null;
  remote_quota_note: string;
};

export type AssistantTool = {
  name: AssistantToolName | string;
  read_only: boolean;
  enabled: boolean;
  scope: string;
  allow_domains?: string[];
};

export type AssistantToolsResponse = {
  chat_id: number;
  tools: AssistantTool[];
  write_tools: string[];
  server_bound_scope: boolean;
};

export type AssistantMemorySource = {
  source_type?: AssistantMemorySourceType;
  type?: string;
  source_message_id?: number | null;
  source_chat_id?: number | null;
  message_id?: number | null;
  chat_id?: number | null;
  operator_id?: number | null;
  operator_name?: string;
  snippet?: string;
  created_at?: string | null;
  verified?: string;
  currently_verified?: boolean;
};

export type AssistantMemory = {
  id: number;
  chat_id: number;
  subject: string;
  content: string;
  memory_type: "base" | "learned" | "pending" | string;
  authority_level: string;
  valid_scope: string;
  source: AssistantMemorySource;
  expires_at: string;
  active: boolean;
  forgotten_at?: string | null;
  version: number;
  created_at: string;
  updated_at: string;
};

export type AssistantMemoryVersion = {
  id: number;
  memory_id: number;
  version: number;
  content: string;
  memory_type: string;
  authority_level: string;
  valid_scope: string;
  source_type: string;
  source_message_id?: number | null;
  source_snippet: string;
  changed_by?: number | null;
  change_kind: string;
  created_at: string;
};

export type AssistantConflictSource = {
  type?: AssistantMemorySourceType;
  source_type?: AssistantMemorySourceType;
  message_id?: number | null;
  source_message_id?: number | null;
  chat_id?: number | null;
  source_chat_id?: number | null;
  snippet?: string;
};

export type AssistantConflict = {
  id: number;
  chat_id: number;
  memory_id?: number | null;
  subject: string;
  candidate_content: string;
  candidate_scope: string;
  candidate_authority: string;
  source: AssistantConflictSource;
  status: string;
  resolved_by?: number | null;
  resolved_at?: string | null;
  created_at: string;
};

export type AssistantHistoryMessage = {
  id: number;
  chat_id: number;
  thread_id: number;
  telegram_message_id: number;
  sender_id: number;
  sender_name: string;
  role: string;
  text: string;
  approved: boolean;
  delivered: boolean;
  expires_at: string;
  source: { type: string; id: string };
  created_at: string;
};

export type AssistantDispatch = {
  id: number;
  chat_id: number;
  request_id: string;
  task_type: string;
  endpoint_id: string;
  model_ref: string;
  reason: string;
  status: string;
  error?: string;
  latency_ms?: number | null;
  created_at: string;
};

export type RegistryModel = {
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
  tools_declared?: boolean;
  supports_tools_declared?: boolean;
  tool_capability?: "declared" | "unsupported" | "unspecified" | string;
  tool_support?: "declared" | "unsupported" | "unspecified" | string;
  capability_tags: string[];
  priority: number;
};

export function assistantPath(chatId: number | string, suffix = "") {
  return `/api/admin/groups/${encodeURIComponent(String(chatId))}/assistant${suffix}`;
}

export function fetchAssistantOverview(chatId: number) {
  return apiFetch<AssistantOverview>(assistantPath(chatId));
}

export function saveAssistantPolicy(
  chatId: number,
  policy: AssistantPolicyWrite,
  expectedVersion: number,
) {
  return apiFetch<{ policy: AssistantPolicy }>(assistantPath(chatId), {
    method: "PUT",
    body: JSON.stringify({ ...policy, expected_version: expectedVersion }),
  });
}

export function fetchAssistantPool(chatId: number) {
  return apiFetch<AssistantPool>(assistantPath(chatId, "/model-pool"));
}

export function saveAssistantPool(
  chatId: number,
  pool: Omit<AssistantPool, "version" | "updated_at" | "config"> & AssistantPoolConfig,
  expectedVersion: number,
) {
  return apiFetch<AssistantPool>(assistantPath(chatId, "/model-pool"), {
    method: "PUT",
    body: JSON.stringify({
      expected_version: expectedVersion,
      strategy: pool.strategy,
      task_assignments: pool.task_assignments,
      endpoints: pool.endpoints,
      max_queue_depth: pool.max_queue_depth,
      max_queue_wait_sec: pool.max_queue_wait_sec,
    }),
  });
}

export function fetchAssistantStatus(chatId: number, signal?: AbortSignal) {
  return apiFetch<AssistantStatus>(assistantPath(chatId, "/status"), { signal });
}

export function fetchAssistantTools(chatId: number) {
  return apiFetch<AssistantToolsResponse>(assistantPath(chatId, "/tools"));
}

export function fetchAssistantRecentSenders(chatId: number, signal?: AbortSignal) {
  return apiFetch<{ chat_id: number; senders: AssistantRecentSender[] } | AssistantRecentSender[]>(
    assistantPath(chatId, "/recent-senders"),
    { signal },
  );
}

export function fetchAssistantMemories(
  chatId: number,
  params: { query?: string; includeInactive?: boolean; limit?: number } = {},
  signal?: AbortSignal,
) {
  const query = new URLSearchParams();
  if (params.query?.trim()) query.set("q", params.query.trim());
  if (params.includeInactive) query.set("include_inactive", "true");
  query.set("limit", String(params.limit ?? 200));
  return apiFetch<{ chat_id: number; memories: AssistantMemory[]; retention_notice: string }>(
    `${assistantPath(chatId, "/memories")}?${query.toString()}`,
    { signal },
  );
}

export function createAssistantMemory(
  chatId: number,
  input: {
    subject: string;
    content: string;
    valid_scope: AssistantMemoryValidScope;
    expires_at?: string | null;
    source_type: "admin_base";
    source_snippet: string;
  },
) {
  return apiFetch<{ memory: AssistantMemory }>(assistantPath(chatId, "/memories"), {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export function fetchAssistantMemory(chatId: number, memoryId: number, signal?: AbortSignal) {
  return apiFetch<{ memory: AssistantMemory }>(assistantPath(chatId, `/memories/${memoryId}`), { signal });
}

export function fetchAssistantMemoryVersions(chatId: number, memoryId: number, signal?: AbortSignal) {
  return apiFetch<{ versions: AssistantMemoryVersion[] }>(
    assistantPath(chatId, `/memories/${memoryId}/versions`),
    { signal },
  );
}

export function updateAssistantMemory(
  chatId: number,
  memoryId: number,
  input: {
    expected_version: number;
    subject: string;
    content: string;
    valid_scope: AssistantMemoryValidScope;
    expires_at?: string | null;
    source_snippet: string;
  },
) {
  return apiFetch<{ memory: AssistantMemory }>(assistantPath(chatId, `/memories/${memoryId}`), {
    method: "PUT",
    body: JSON.stringify(input),
  });
}

export function forgetAssistantMemory(chatId: number, memoryId: number) {
  return apiFetch<{ forgotten: boolean; remote_telegram_deleted: boolean; notice: string }>(
    assistantPath(chatId, `/memories/${memoryId}/forget`),
    { method: "POST", body: JSON.stringify({}) },
  );
}

export function fetchAssistantConflicts(chatId: number, status = "pending", signal?: AbortSignal) {
  const query = new URLSearchParams({ status, limit: "200" });
  return apiFetch<{ conflicts: AssistantConflict[] }>(
    `${assistantPath(chatId, "/conflicts")}?${query.toString()}`,
    { signal },
  );
}

export type AssistantConflictResolution =
  | { accept: false }
  | {
      accept: true;
      expected_memory_version: number;
      resolution_mode: "admin_explicit_correction";
    };

export function resolveAssistantConflict(
  chatId: number,
  conflictId: number,
  resolution: AssistantConflictResolution,
) {
  return apiFetch<{ conflict: AssistantConflict }>(
    assistantPath(chatId, `/conflicts/${conflictId}/resolve`),
    { method: "POST", body: JSON.stringify(resolution) },
  );
}

export function fetchAssistantHistory(
  chatId: number,
  params: { query?: string; threadId?: string; senderId?: string; limit?: number } = {},
  signal?: AbortSignal,
) {
  const query = new URLSearchParams();
  if (params.query?.trim()) query.set("q", params.query.trim());
  if (params.threadId?.trim()) query.set("thread_id", params.threadId.trim());
  if (params.senderId?.trim()) query.set("sender_id", params.senderId.trim());
  query.set("limit", String(params.limit ?? 100));
  return apiFetch<{
    chat_id: number;
    history: AssistantHistoryMessage[];
    retention_days: number;
    expired_auto_removed: boolean;
  }>(`${assistantPath(chatId, "/history")}?${query.toString()}`, { signal });
}

export function fetchAssistantDispatches(chatId: number, limit = 50, signal?: AbortSignal) {
  return apiFetch<{ dispatches: AssistantDispatch[] }>(
    `${assistantPath(chatId, "/dispatches")}?limit=${limit}`,
    { signal },
  );
}

export function fetchRegistryModels() {
  return apiFetch<{ models: RegistryModel[] }>("/api/admin/llm/models");
}
