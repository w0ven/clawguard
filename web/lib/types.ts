export type Nullable<T> = T | null;

export type GuardPolicy = {
  verify: {
    enabled: boolean;
    method: string;
    timeout_seconds: number;
    fail_action: string;
    delete_join_message: boolean;
    welcome_message: {
      enabled: boolean;
      template: Nullable<string>;
      rules_link: string;
      delete_after_seconds: number;
      parse_mode?: string;
    };
    check_profile: boolean;
    profile_check_mode: string;
    profile_blacklist: string[];
  };
  filter: {
    non_text_messages: string;
    keywords: {
      enabled: boolean;
      list: string[];
      action: string;
      case_sensitive: boolean;
    };
    regex: {
      enabled: boolean;
      patterns: string[];
    };
    links: {
      enabled: boolean;
      whitelist: string[];
      action: string;
      exempt_admins: boolean;
    };
    usernames: {
      enabled: boolean;
      blacklist: string[];
    };
    new_user: {
      enabled: boolean;
      duration_hours: number;
      no_links: boolean;
      no_forwards: boolean;
      no_media: boolean;
      max_messages_per_minute: number;
    };
    other_bots_action: string;
    bot_whitelist: string[];
    ban_bot_inviter_on_violation: boolean;
    ban_sender_chats: boolean;
  };
  messages: {
    keyword_replies: KeywordReplyRule[];
  };
  anti_spam: {
    cas_enabled: boolean;
    rate_limit: {
      enabled: boolean;
      messages_per_10s: number;
      action: string;
    };
  };
  warnings: {
    enabled: boolean;
    max_warns: number;
    action_at_max: string;
    decay_days: number;
  };
  logging: {
    log_chat_id: Nullable<number>;
  };
  ai: {
    enabled: boolean;
    image_moderation_enabled: boolean;
    video_moderation_enabled: boolean;
    video_max_bytes: number;
    video_max_duration_sec: number;
    video_frame_count: number;
    video_concurrency: number;
    include_video_note: boolean;
    primary_model_ref?: string;
    fallback_model_refs?: string[];
    auto_degrade?: boolean;
    probe_enabled?: boolean;
    probe_interval_seconds?: number;
    primary_provider: string;
    primary_model: string;
    fallback_chain: string[];
    temperature: number;
    timeout_ms: number;
    max_retries: number;
    graduate_after_messages: number;
    graduate_after_days: number;
    per_user_daily_limit: number;
    skip_messages_shorter_than: number;
    batch_window_ms: number;
    cache_ttl_hours: number;
    custom_rules: string;
    thresholds: {
      ban: number;
      mute: number;
      warn: number;
      flag: number;
    };
    actions_by_category: Record<string, string>;
    trigger_keywords: string[];
    check_profile_on_message: boolean;
    profile_on_message_mode: string;
    bio_cache_ttl_minutes: number;
  };
  feedback: {
    delete_msg: ActionFeedback;
    mute: ActionFeedback;
    kick: ActionFeedback;
    ban: ActionFeedback;
    warn: ActionFeedback;
    verify_pass: ActionFeedback;
    verify_fail: ActionFeedback;
    cas_hit: ActionFeedback;
    trust_graduated: ActionFeedback;
    admin_action: ActionFeedback;
  };
};

export type ActionFeedback = {
  enabled: boolean;
  template: string;
  auto_delete_seconds: number;
  reply_to_message: boolean;
  parse_mode?: string;
};

export type KeywordReplyRule = {
  id: string;
  name: string;
  enabled: boolean;
  match_type: string;
  keywords: string[];
  case_sensitive: boolean;
  reply_text: string;
  auto_delete_seconds: number;
  cooldown_seconds: number;
  skip_admins?: boolean;
  parse_mode?: string;
  trigger_count?: number;
  last_triggered_at?: Nullable<string>;
};

export type Admin = {
  id: number;
  telegram_id: number;
  username: Nullable<string>;
  first_name: Nullable<string>;
  photo_url: Nullable<string>;
  role: string;
  notes: Nullable<string>;
  group_scope: number[];
  created_at: string;
  last_login_at: Nullable<string>;
};

export type Group = {
  id: number;
  chat_id: number;
  title: string;
  type: string;
  member_count: number;
  enabled: boolean;
  joined_at: string;
  config: Record<string, unknown>;
};

export type AuthorizedGroup = {
  chat_id: number;
  title: string;
  authorized_at: string;
  authorized_by: Nullable<number>;
  enabled: boolean;
  notes: string;
};

export type SystemState = {
  id: number;
  ai_paused: boolean;
  actions_paused: boolean;
  frozen: boolean;
  ai_paused_reason: string;
  updated_at: string;
  updated_by: Nullable<number>;
};

export type Violation = {
  id: number;
  chat_id: number;
  user_id: number;
  username: Nullable<string>;
  rule: string;
  matched: Nullable<string>;
  action: string;
  message_text: Nullable<string>;
  created_at: string;
};

export type ProfileCheckLog = {
  id: number;
  chat_id: number;
  user_id: number;
  user_name: Nullable<string>;
  username: Nullable<string>;
  bio: Nullable<string>;
  check_mode: string;
  result: string;
  matched_rule: Nullable<string>;
  ai_confidence: Nullable<number>;
  ai_verdict: Nullable<string>;
  created_at: string;
};

export type Warning = {
  id: number;
  chat_id: number;
  user_id: number;
  reason: Nullable<string>;
  issued_by: Nullable<number>;
  created_at: string;
  consumed_at: Nullable<string>;
};

export type AuditEntry = {
  id: number;
  scope: string;
  chat_id: Nullable<number>;
  admin_id: number;
  action: string;
  before: unknown;
  after: unknown;
  diff: Nullable<string>;
  created_at: string;
};

export type AdminStats = {
  groups_count: number;
  active_verifications: number;
  today_violations: number;
};

export type HealthStatus = {
  db_latency_ms: number;
  redis_latency_ms: Nullable<number>;
  webhook_last_update_at: Nullable<string>;
  webhook_seconds_ago: Nullable<number>;
  ai_last_ok_at: Nullable<string>;
  ai_last_fail_at: Nullable<string>;
  ai_last_error: string;
  today_calls: number;
  uptime_seconds: number;
};

export type LiveEvent = {
  type: string;
  id: number;
  chat_id: number;
  user_id: number;
  title: string;
  detail: string;
  extra: string;
  created_at: string;
};

export type AIDecision = {
  id: number;
  chat_id: number;
  user_id: number;
  message_id: number;
  message_text: Nullable<string>;
  model: string;
  prompt_version: string;
  verdict: string;
  confidence: number;
  category: string;
  reason: Nullable<string>;
  action_taken: string;
  admin_override: Nullable<string>;
  latency_ms: number;
  scene: string;
  created_at: string;
};

export type UserTrust = {
  chat_id: number;
  user_id: number;
  username?: Nullable<string>;
  first_name?: Nullable<string>;
  last_name?: Nullable<string>;
  joined_at: string;
  updated_at: string;
  status: string;
  score: number;
  messages_checked: number;
  messages_clean: number;
  graduated_at: Nullable<string>;
  banned_at: Nullable<string>;
  banned_reason: Nullable<BannedReason>;
  notes: Nullable<string>;
};

export type BannedReason = {
  rule?: string;
  matched?: string;
  source?: string;
};

export type AICallDaily = {
  date: string;
  calls: number;
};

export type AICallPerModel = {
  model: string;
  calls: number;
};

export type AICallPerScene = {
  scene: string;
  calls: number;
};

export type AICallPerChat = {
  chat_id: number;
  title: Nullable<string>;
  calls: number;
};

export type AICallSummary = {
  today_calls: number;
  daily: AICallDaily[];
  per_model: AICallPerModel[];
  per_scene: AICallPerScene[];
  per_chat: AICallPerChat[];
};

export type AICacheStats = {
  hit: number;
  miss: number;
  rate: number;
};
