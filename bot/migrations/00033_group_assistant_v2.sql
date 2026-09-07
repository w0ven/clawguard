-- +goose Up
-- Group assistant v2 settings and speech-style samples. 00032 remains immutable.
ALTER TABLE group_assistant_policies
    ADD COLUMN proactive_interject_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN proactive_cold_topic_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN cold_topic_idle_minutes INT NOT NULL DEFAULT 180,
    ADD COLUMN cold_topic_quiet_start INT NOT NULL DEFAULT 0,
    ADD COLUMN cold_topic_quiet_end INT NOT NULL DEFAULT 8,
    ADD COLUMN mimic_target_user_id BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN mimic_target_user_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN mimic_profile_text TEXT NOT NULL DEFAULT '',
    ADD COLUMN mimic_sample_count INT NOT NULL DEFAULT 0,
    ADD COLUMN mimic_distilled_at_count INT NOT NULL DEFAULT 0,
    ADD CONSTRAINT group_assistant_policies_cold_idle_check CHECK (cold_topic_idle_minutes >= 180),
    ADD CONSTRAINT group_assistant_policies_quiet_start_check CHECK (cold_topic_quiet_start BETWEEN 0 AND 23),
    ADD CONSTRAINT group_assistant_policies_quiet_end_check CHECK (cold_topic_quiet_end BETWEEN 0 AND 23),
    ADD CONSTRAINT group_assistant_policies_mimic_target_check CHECK (mimic_target_user_id >= 0),
    ADD CONSTRAINT group_assistant_policies_mimic_profile_check CHECK (length(mimic_profile_text) <= 1200),
    ADD CONSTRAINT group_assistant_policies_mimic_sample_count_check CHECK (mimic_sample_count BETWEEN 0 AND 1000),
    ADD CONSTRAINT group_assistant_policies_mimic_distilled_count_check CHECK (mimic_distilled_at_count BETWEEN 0 AND 1000);

CREATE TABLE group_assistant_style_samples (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (user_id <> 0),
    CHECK (length(content) BETWEEN 1 AND 4000)
);
CREATE INDEX idx_group_assistant_style_samples_chat_user
    ON group_assistant_style_samples(chat_id, user_id, created_at DESC, id DESC);

-- +goose Down
DROP TABLE IF EXISTS group_assistant_style_samples;
ALTER TABLE group_assistant_policies
    DROP CONSTRAINT IF EXISTS group_assistant_policies_mimic_distilled_count_check,
    DROP CONSTRAINT IF EXISTS group_assistant_policies_mimic_sample_count_check,
    DROP CONSTRAINT IF EXISTS group_assistant_policies_mimic_profile_check,
    DROP CONSTRAINT IF EXISTS group_assistant_policies_mimic_target_check,
    DROP CONSTRAINT IF EXISTS group_assistant_policies_quiet_end_check,
    DROP CONSTRAINT IF EXISTS group_assistant_policies_quiet_start_check,
    DROP CONSTRAINT IF EXISTS group_assistant_policies_cold_idle_check,
    DROP COLUMN IF EXISTS mimic_distilled_at_count,
    DROP COLUMN IF EXISTS mimic_sample_count,
    DROP COLUMN IF EXISTS mimic_profile_text,
    DROP COLUMN IF EXISTS mimic_target_user_name,
    DROP COLUMN IF EXISTS mimic_target_user_id,
    DROP COLUMN IF EXISTS cold_topic_quiet_end,
    DROP COLUMN IF EXISTS cold_topic_quiet_start,
    DROP COLUMN IF EXISTS cold_topic_idle_minutes,
    DROP COLUMN IF EXISTS proactive_cold_topic_enabled,
    DROP COLUMN IF EXISTS proactive_interject_enabled;
