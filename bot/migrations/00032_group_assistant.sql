-- +goose Up
-- Group assistant state is intentionally isolated from moderation policy, ai_decisions,
-- and user_trust. New rows are disabled until an administrator explicitly enables them.
CREATE TABLE group_assistant_policies (
    chat_id BIGINT PRIMARY KEY,
    version BIGINT NOT NULL DEFAULT 1,
    chat_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    learning_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    trigger_mode TEXT NOT NULL DEFAULT 'mention_or_reply',
    followup_window_sec INT NOT NULL DEFAULT 300,
    max_followup_turns INT NOT NULL DEFAULT 5,
    chat_model_ref TEXT NOT NULL DEFAULT '',
    learning_model_ref TEXT NOT NULL DEFAULT '',
    temperature DOUBLE PRECISION NOT NULL DEFAULT 0.3,
    system_prompt TEXT NOT NULL DEFAULT '',
    history_limit INT NOT NULL DEFAULT 30,
    retention_days INT NOT NULL DEFAULT 7,
    collection_policy TEXT NOT NULL DEFAULT 'history_7d_and_long_term_summary',
    tool_allowlist TEXT[] NOT NULL DEFAULT ARRAY['knowledge_query', 'conversation_recall', 'webfetch_readonly']::TEXT[],
    allow_domains TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    max_queue_depth INT NOT NULL DEFAULT 10,
    max_queue_wait_sec INT NOT NULL DEFAULT 15,
    updated_by BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (trigger_mode IN ('mention_or_reply', 'mention_only')),
    CHECK (collection_policy IN ('history_7d_and_long_term_summary')),
    CHECK (followup_window_sec BETWEEN 30 AND 3600),
    CHECK (max_followup_turns BETWEEN 1 AND 20),
    CHECK (temperature >= 0 AND temperature <= 2),
    CHECK (history_limit BETWEEN 1 AND 200),
    CHECK (retention_days BETWEEN 1 AND 30),
    CHECK (max_queue_depth BETWEEN 0 AND 100),
    CHECK (max_queue_wait_sec BETWEEN 1 AND 60)
);

CREATE TABLE group_assistant_pools (
    chat_id BIGINT PRIMARY KEY,
    version BIGINT NOT NULL DEFAULT 1,
    strategy TEXT NOT NULL DEFAULT 'primary-overflow',
    config JSONB NOT NULL DEFAULT '{"task_assignments":{"chat":"","learning":""},"endpoints":[]}'::JSONB,
    updated_by BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (strategy = 'primary-overflow')
);

CREATE TABLE group_assistant_messages (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    thread_id INT NOT NULL DEFAULT 0,
    telegram_message_id BIGINT NOT NULL,
    sender_id BIGINT NOT NULL DEFAULT 0,
    sender_name TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL,
    text TEXT NOT NULL,
    approved BOOLEAN NOT NULL DEFAULT TRUE,
    delivered BOOLEAN NOT NULL DEFAULT TRUE,
    content_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    source_type TEXT NOT NULL DEFAULT 'telegram_message',
    source_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (role IN ('user', 'assistant')),
    CHECK (length(text) BETWEEN 1 AND 20000),
    UNIQUE (chat_id, telegram_message_id, role)
);
CREATE INDEX idx_group_assistant_messages_history
    ON group_assistant_messages(chat_id, thread_id, created_at DESC)
    WHERE approved = TRUE AND delivered = TRUE;
CREATE INDEX idx_group_assistant_messages_expiry
    ON group_assistant_messages(expires_at);
CREATE INDEX idx_group_assistant_messages_search
    ON group_assistant_messages(chat_id, lower(text));

CREATE TABLE group_assistant_memories (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    subject TEXT NOT NULL,
    content TEXT NOT NULL,
    memory_type TEXT NOT NULL,
    authority_level TEXT NOT NULL,
    valid_scope TEXT NOT NULL DEFAULT '',
    source_type TEXT NOT NULL,
    source_message_id BIGINT,
    source_chat_id BIGINT,
    source_operator_id BIGINT,
    source_operator_name TEXT NOT NULL DEFAULT '',
    source_snippet TEXT NOT NULL DEFAULT '',
    source_created_at TIMESTAMPTZ,
    source_verified TEXT NOT NULL DEFAULT 'verified',
    source_content_hash TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    forgotten_at TIMESTAMPTZ,
    forgotten_by BIGINT,
    dedupe_hash TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (memory_type IN ('base', 'learned', 'pending')),
    CHECK (authority_level IN ('admin_base', 'admin_explicit', 'pinned_announcement', 'learned_fact')),
    CHECK (source_verified IN ('verified', 'unknown', 'invalid')),
    CHECK (length(subject) BETWEEN 1 AND 200),
    CHECK (length(content) BETWEEN 1 AND 4000),
    CHECK (length(valid_scope) <= 300),
    CHECK (length(source_snippet) <= 1000),
    CHECK (length(source_content_hash) <= 128),
    UNIQUE (chat_id, dedupe_hash)
);
CREATE INDEX idx_group_assistant_memories_recall
    ON group_assistant_memories(chat_id, active, expires_at, subject);
CREATE INDEX idx_group_assistant_memories_conflicts
    ON group_assistant_memories(chat_id, memory_type, active);

CREATE TABLE group_assistant_memory_versions (
    id BIGSERIAL PRIMARY KEY,
    memory_id BIGINT NOT NULL REFERENCES group_assistant_memories(id) ON DELETE CASCADE,
    version BIGINT NOT NULL,
    content TEXT NOT NULL,
    memory_type TEXT NOT NULL,
    authority_level TEXT NOT NULL,
    valid_scope TEXT NOT NULL DEFAULT '',
    source_type TEXT NOT NULL,
    source_message_id BIGINT,
    source_snippet TEXT NOT NULL DEFAULT '',
    changed_by BIGINT,
    change_kind TEXT NOT NULL DEFAULT 'update',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (memory_id, version)
);
CREATE INDEX idx_group_assistant_memory_versions_memory
    ON group_assistant_memory_versions(memory_id, version DESC);

CREATE TABLE group_assistant_conflicts (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    memory_id BIGINT,
    subject TEXT NOT NULL,
    candidate_content TEXT NOT NULL,
    candidate_scope TEXT NOT NULL DEFAULT '',
    candidate_authority TEXT NOT NULL,
    source_type TEXT NOT NULL,
    source_message_id BIGINT,
    source_chat_id BIGINT,
    source_snippet TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    resolved_by BIGINT,
    CHECK (length(subject) BETWEEN 1 AND 200),
    CHECK (length(candidate_content) BETWEEN 1 AND 4000),
    CHECK (length(candidate_scope) <= 300),
    CHECK (length(source_snippet) <= 1000),
    resolved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (status IN ('pending', 'accepted', 'rejected'))
);
CREATE INDEX idx_group_assistant_conflicts_chat
    ON group_assistant_conflicts(chat_id, status, created_at DESC);

CREATE TABLE group_assistant_dispatches (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    request_id TEXT NOT NULL,
    task_type TEXT NOT NULL,
    endpoint_id TEXT NOT NULL DEFAULT '',
    model_ref TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    error_text TEXT NOT NULL DEFAULT '',
    latency_ms INT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_group_assistant_dispatches_chat
    ON group_assistant_dispatches(chat_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS group_assistant_dispatches;
DROP TABLE IF EXISTS group_assistant_conflicts;
DROP TABLE IF EXISTS group_assistant_memory_versions;
DROP TABLE IF EXISTS group_assistant_memories;
DROP TABLE IF EXISTS group_assistant_messages;
DROP TABLE IF EXISTS group_assistant_pools;
DROP TABLE IF EXISTS group_assistant_policies;
