-- +goose Up
CREATE TABLE admins (
    id BIGSERIAL PRIMARY KEY,
    telegram_id BIGINT UNIQUE NOT NULL,
    username TEXT,
    first_name TEXT,
    photo_url TEXT,
    role TEXT NOT NULL DEFAULT 'admin',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    last_login_at TIMESTAMPTZ
);

CREATE TABLE groups (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT UNIQUE NOT NULL,
    title TEXT NOT NULL,
    type TEXT NOT NULL,
    member_count INT DEFAULT 0,
    enabled BOOLEAN DEFAULT TRUE,
    joined_at TIMESTAMPTZ DEFAULT NOW(),
    config JSONB NOT NULL DEFAULT '{}'
);

CREATE TABLE global_config (
    id INT PRIMARY KEY DEFAULT 1,
    config JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    CHECK (id = 1)
);

INSERT INTO global_config (id, config)
VALUES (1, '{}')
ON CONFLICT (id) DO NOTHING;

CREATE TABLE pending_verifications (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    username TEXT,
    first_name TEXT,
    method TEXT NOT NULL,
    payload JSONB DEFAULT '{}',
    join_message_id BIGINT,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (chat_id, user_id)
);

CREATE TABLE violations (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    username TEXT,
    rule TEXT NOT NULL,
    matched TEXT,
    action TEXT NOT NULL,
    message_text TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_violations_chat_created ON violations(chat_id, created_at DESC);

CREATE TABLE banned_users (
    user_id BIGINT PRIMARY KEY,
    reason TEXT,
    source TEXT NOT NULL,
    banned_at TIMESTAMPTZ DEFAULT NOW(),
    banned_by BIGINT
);

CREATE TABLE warnings (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    reason TEXT,
    issued_by BIGINT,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_warnings_chat_user ON warnings(chat_id, user_id);

CREATE TABLE config_audit (
    id BIGSERIAL PRIMARY KEY,
    scope TEXT NOT NULL,
    chat_id BIGINT,
    admin_id BIGINT NOT NULL,
    diff JSONB NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS config_audit;
DROP INDEX IF EXISTS idx_warnings_chat_user;
DROP TABLE IF EXISTS warnings;
DROP TABLE IF EXISTS banned_users;
DROP INDEX IF EXISTS idx_violations_chat_created;
DROP TABLE IF EXISTS violations;
DROP TABLE IF EXISTS pending_verifications;
DROP TABLE IF EXISTS global_config;
DROP TABLE IF EXISTS groups;
DROP TABLE IF EXISTS admins;
