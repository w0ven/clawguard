-- +goose Up
-- Additive assistant-only adaptation; preserve policy flags, originals, learned
-- facts, explicit prompts and all stored merge-window values.
ALTER TABLE group_assistant_pools DROP CONSTRAINT group_assistant_pools_strategy_check;
ALTER TABLE group_assistant_pools ADD CONSTRAINT group_assistant_pools_strategy_check CHECK (strategy IN ('primary-overflow','weighted'));
ALTER TABLE group_assistant_global_settings ALTER COLUMN inbound_merge_window_sec SET DEFAULT 5;

ALTER TABLE group_assistant_policies ALTER COLUMN history_limit SET DEFAULT 500;
ALTER TABLE group_assistant_policies DROP CONSTRAINT group_assistant_policies_history_limit_check;
ALTER TABLE group_assistant_policies ADD CONSTRAINT group_assistant_policies_history_limit_check CHECK (history_limit BETWEEN 1 AND 500);

-- PostgreSQL's default FTS parser does not segment Chinese. Persist real CJK
-- character/bigram and Latin word tokens; the same tokenizer serves queries.
-- +goose StatementBegin
CREATE FUNCTION assistant_lexical_tokens(input_text TEXT) RETURNS TEXT[]
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE AS $$
DECLARE segment TEXT; tokens TEXT[] := ARRAY[]::TEXT[]; i INTEGER;
BEGIN
  FOR segment IN SELECT m[1] FROM regexp_matches(lower(input_text), '[a-z0-9_]+|[一-鿿]+', 'g') AS m LOOP
    IF segment ~ '^[一-鿿]' THEN
      FOR i IN 1..char_length(segment) LOOP
        tokens := array_append(tokens, substring(segment FROM i FOR 1));
        IF i < char_length(segment) THEN tokens := array_append(tokens, substring(segment FROM i FOR 2)); END IF;
      END LOOP;
    ELSE tokens := array_append(tokens, segment);
    END IF;
  END LOOP;
  RETURN ARRAY(SELECT DISTINCT t FROM unnest(tokens) AS t ORDER BY t);
END $$;
-- +goose StatementEnd
ALTER TABLE group_assistant_messages ADD COLUMN lexical_tokens TEXT[] GENERATED ALWAYS AS (assistant_lexical_tokens(text)) STORED;
CREATE INDEX idx_assistant_message_lexical ON group_assistant_messages USING GIN (lexical_tokens) WHERE approved AND delivered;
CREATE TABLE group_assistant_message_vectors (
    message_id BIGINT NOT NULL REFERENCES group_assistant_messages(id) ON DELETE CASCADE,
    model_ref TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    embedding DOUBLE PRECISION[] NOT NULL,
    indexed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (message_id, model_ref),
    CHECK (cardinality(embedding) BETWEEN 1 AND 65536)
);
CREATE INDEX idx_assistant_vectors_model ON group_assistant_message_vectors(model_ref, message_id);
-- +goose StatementBegin
CREATE FUNCTION assistant_revoke_stale_vectors() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.content_hash IS DISTINCT FROM NEW.content_hash OR NOT NEW.approved OR NOT NEW.delivered THEN
    DELETE FROM group_assistant_message_vectors WHERE message_id=NEW.id;
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER assistant_message_vector_validity AFTER UPDATE OF content_hash, approved, delivered ON group_assistant_messages FOR EACH ROW EXECUTE FUNCTION assistant_revoke_stale_vectors();

-- +goose Down
-- Rollback deliberately refuses to erase persistent indexes or silently turn a
-- weighted pool into primary mode. Rolling back requires an explicit decision.
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'assistant source refactor is additive; automatic destructive rollback is not supported'; END $$;
-- +goose StatementEnd
