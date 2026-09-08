-- +goose Up
-- Assistant SGB parity: group TTS/sticker settings, global roles/prompts/TTS, sticker samples.
-- 00032/00033 assistant memory and policy semantics stay authoritative.

ALTER TABLE group_assistant_policies
    ADD COLUMN tts_mode TEXT NOT NULL DEFAULT 'off',
    ADD COLUMN sticker_fallback_file_ids TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    ADD COLUMN proactive_task_brief TEXT NOT NULL DEFAULT '',
    ADD CONSTRAINT group_assistant_policies_tts_mode_check CHECK (tts_mode IN ('off', 'on', 'always')),
    ADD CONSTRAINT group_assistant_policies_sticker_fallback_check CHECK (cardinality(sticker_fallback_file_ids) <= 50),
    ADD CONSTRAINT group_assistant_policies_task_brief_check CHECK (length(proactive_task_brief) <= 2000);

CREATE TABLE group_assistant_global_settings (
    id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    version BIGINT NOT NULL DEFAULT 1,
    model_roles JSONB NOT NULL DEFAULT '{}'::JSONB,
    inbound_merge_window_sec DOUBLE PRECISION NOT NULL DEFAULT 0.4,
    reply_total_timeout_sec DOUBLE PRECISION NOT NULL DEFAULT 45,
    decision_context_items INT NOT NULL DEFAULT 5,
    keep_original_text BOOLEAN NOT NULL DEFAULT TRUE,
    memory_recall_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    hot_window_compress_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    proactive_idle_minutes INT NOT NULL DEFAULT 180,
    proactive_quiet_start INT NOT NULL DEFAULT 0,
    proactive_quiet_end INT NOT NULL DEFAULT 8,
    proactive_check_interval_sec DOUBLE PRECISION NOT NULL DEFAULT 60,
    tts_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    tts_http_timeout_sec DOUBLE PRECISION NOT NULL DEFAULT 20,
    tts_max_text_length INT NOT NULL DEFAULT 500,
    tts_api_base TEXT NOT NULL DEFAULT 'https://openspeech.bytedance.com',
    tts_app_id TEXT NOT NULL DEFAULT '',
    tts_app_key_enc TEXT NOT NULL DEFAULT '',
    tts_access_key_enc TEXT NOT NULL DEFAULT '',
    tts_resource_id TEXT NOT NULL DEFAULT 'seed-tts-2.0',
    tts_model TEXT NOT NULL DEFAULT '',
    tts_speaker TEXT NOT NULL DEFAULT '',
    tts_audio_format TEXT NOT NULL DEFAULT 'ogg_opus',
    tts_sample_rate INT NOT NULL DEFAULT 48000,
    tts_bit_rate INT NOT NULL DEFAULT 96000,
    tts_emotion TEXT NOT NULL DEFAULT '',
    tts_emotion_scale INT NOT NULL DEFAULT 4,
    tts_speech_rate INT NOT NULL DEFAULT 0,
    tts_loudness_rate INT NOT NULL DEFAULT 0,
    tts_silence_duration_ms INT NOT NULL DEFAULT 0,
    sticker_fallback_file_ids TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    updated_by BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (inbound_merge_window_sec >= 0 AND inbound_merge_window_sec <= 60),
    CHECK (reply_total_timeout_sec >= 5 AND reply_total_timeout_sec <= 120),
    CHECK (decision_context_items BETWEEN 0 AND 20),
    CHECK (proactive_idle_minutes >= 180),
    CHECK (proactive_quiet_start BETWEEN 0 AND 23),
    CHECK (proactive_quiet_end BETWEEN 0 AND 23),
    CHECK (proactive_check_interval_sec >= 15 AND proactive_check_interval_sec <= 3600),
    CHECK (tts_http_timeout_sec >= 1 AND tts_http_timeout_sec <= 300),
    CHECK (tts_max_text_length BETWEEN 1 AND 10000),
    CHECK (length(tts_api_base) <= 1000),
    CHECK (length(tts_app_id) <= 255),
    CHECK (length(tts_resource_id) <= 255),
    CHECK (length(tts_model) <= 255),
    CHECK (length(tts_speaker) <= 255),
    CHECK (length(tts_audio_format) <= 64),
    CHECK (tts_sample_rate BETWEEN 8000 AND 192000),
    CHECK (tts_bit_rate BETWEEN 0 AND 512000),
    CHECK (length(tts_emotion) <= 64),
    CHECK (tts_emotion_scale BETWEEN 1 AND 5),
    CHECK (tts_speech_rate BETWEEN -100 AND 100),
    CHECK (tts_loudness_rate BETWEEN -100 AND 100),
    CHECK (tts_silence_duration_ms BETWEEN 0 AND 10000),
    CHECK (cardinality(sticker_fallback_file_ids) <= 50)
);

INSERT INTO group_assistant_global_settings (id) VALUES (1);

-- chat_id=0 is the global default overlay. Empty content means use embedded prompt.
CREATE TABLE group_assistant_prompt_overrides (
    chat_id BIGINT NOT NULL DEFAULT 0,
    prompt_key TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (chat_id, prompt_key),
    CHECK (prompt_key IN ('persona', 'casual', 'decision', 'proactive_topic', 'style_distill')),
    CHECK (length(content) <= 20000)
);

CREATE TABLE group_assistant_sticker_samples (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    file_id TEXT NOT NULL,
    source_message_id BIGINT,
    query TEXT NOT NULL DEFAULT '',
    emoji TEXT NOT NULL DEFAULT '',
    set_name TEXT NOT NULL DEFAULT '',
    aliases TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    seen_count INT NOT NULL DEFAULT 0,
    sent_count INT NOT NULL DEFAULT 0,
    source TEXT NOT NULL DEFAULT 'group_message',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ,
    last_sent_at TIMESTAMPTZ,
    UNIQUE (chat_id, file_id),
    CHECK (length(file_id) BETWEEN 1 AND 255),
    CHECK (length(query) <= 240),
    CHECK (length(emoji) <= 32),
    CHECK (length(set_name) <= 128),
    CHECK (seen_count >= 0),
    CHECK (sent_count >= 0)
);
CREATE INDEX idx_group_assistant_sticker_samples_chat
    ON group_assistant_sticker_samples(chat_id, last_seen_at DESC NULLS LAST, id DESC);

-- +goose Down
DROP TABLE IF EXISTS group_assistant_sticker_samples;
DROP TABLE IF EXISTS group_assistant_prompt_overrides;
DROP TABLE IF EXISTS group_assistant_global_settings;
ALTER TABLE group_assistant_policies
    DROP CONSTRAINT IF EXISTS group_assistant_policies_task_brief_check,
    DROP CONSTRAINT IF EXISTS group_assistant_policies_sticker_fallback_check,
    DROP CONSTRAINT IF EXISTS group_assistant_policies_tts_mode_check,
    DROP COLUMN IF EXISTS proactive_task_brief,
    DROP COLUMN IF EXISTS sticker_fallback_file_ids,
    DROP COLUMN IF EXISTS tts_mode;
