-- +goose Up
CREATE TABLE IF NOT EXISTS appeals (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL UNIQUE REFERENCES moderation_cases(id),
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    reason TEXT NOT NULL,
    reference_url TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    turnstile_verified BOOLEAN NOT NULL DEFAULT true,
    hcaptcha_verified BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS appeals_chat_status_time_idx ON appeals(chat_id,status,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS appeals_user_time_idx ON appeals(user_id,created_at DESC,id DESC);
CREATE TABLE IF NOT EXISTS appeal_challenges (
    token TEXT PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES moderation_cases(id),
    user_id BIGINT NOT NULL,
    state TEXT NOT NULL DEFAULT 'open' CHECK(state IN ('open','claimed','used')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '10 minutes'
);
CREATE INDEX IF NOT EXISTS appeal_challenges_expiry_idx ON appeal_challenges(expires_at);
-- Provider responses are never retained; hashes prevent local cross-challenge replay.
CREATE TABLE IF NOT EXISTS appeal_provider_tokens (
    provider TEXT NOT NULL CHECK (provider IN ('turnstile','hcaptcha')),
    token_hash BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '24 hours',
    PRIMARY KEY(provider,token_hash)
);
CREATE INDEX IF NOT EXISTS appeal_provider_tokens_expiry_idx ON appeal_provider_tokens(expires_at);
-- +goose Down
-- Preserve appeal decisions and evidence.
SELECT 1;
