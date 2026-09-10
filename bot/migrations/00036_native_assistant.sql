-- +goose Up
-- CG remains the event-authorization and delivery owner. Native SQLite is the
-- active assistant memory domain. This is a durable transport/provenance log,
-- never an alternative chat/recall index. No existing data/config is reset here.
CREATE TABLE assistant_native_scopes (
    chat_id BIGINT NOT NULL, thread_id INT NOT NULL DEFAULT 0,
    generation BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY(chat_id,thread_id)
);
CREATE TABLE assistant_native_events (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL, thread_id INT NOT NULL DEFAULT 0,
    telegram_message_id BIGINT NOT NULL, revision TEXT NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('message','edit','invalidate','forget')),
    generation BIGINT NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','accepted','done','uncertain')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(chat_id,thread_id,telegram_message_id,revision,kind)
);
CREATE INDEX assistant_native_events_scope ON assistant_native_events(chat_id,thread_id,id);
CREATE TABLE assistant_native_deliveries (
    id BIGSERIAL PRIMARY KEY, turn_id TEXT NOT NULL,
    chat_id BIGINT NOT NULL, thread_id INT NOT NULL,
    method TEXT NOT NULL, telegram_message_id BIGINT,
    purpose TEXT NOT NULL DEFAULT 'reply' CHECK(purpose IN ('reply','progress')),
    status TEXT NOT NULL CHECK(status IN ('attempting','delivered','failed','uncertain')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX assistant_native_delivery_owner ON assistant_native_deliveries(chat_id,thread_id,telegram_message_id);

-- +goose Down
-- Keep transport receipts and provenance through application rollback. Schema
-- downgrade is unnecessary; the legacy engine ignores these additive tables.
SELECT 1;
