-- +goose Up
ALTER TABLE appeals ADD COLUMN IF NOT EXISTS decision TEXT CHECK (decision IN ('approve','uphold'));
ALTER TABLE appeals ADD COLUMN IF NOT EXISTS public_note TEXT NOT NULL DEFAULT '';
ALTER TABLE appeals ADD COLUMN IF NOT EXISTS reviewer_id BIGINT REFERENCES admins(id);
ALTER TABLE appeals ADD COLUMN IF NOT EXISTS decision_at TIMESTAMPTZ;
ALTER TABLE appeals ADD COLUMN IF NOT EXISTS execution_result TEXT NOT NULL DEFAULT '';
ALTER TABLE appeals ADD COLUMN IF NOT EXISTS review_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE appeals ADD COLUMN IF NOT EXISTS execution_token TEXT;
ALTER TABLE appeals ADD COLUMN IF NOT EXISTS execution_lease_until TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS appeals_status_time_idx ON appeals(status,created_at DESC,id DESC);
CREATE TABLE IF NOT EXISTS appeal_warning_reversals (appeal_id BIGINT PRIMARY KEY REFERENCES appeals(id), warning_id BIGINT NOT NULL UNIQUE REFERENCES warnings(id), created_at TIMESTAMPTZ NOT NULL DEFAULT now());
ALTER TABLE moderation_notifications DROP CONSTRAINT IF EXISTS moderation_notifications_case_id_key;
ALTER TABLE moderation_notifications ADD COLUMN IF NOT EXISTS event_kind TEXT NOT NULL DEFAULT 'moderation_case' CHECK(event_kind IN ('moderation_case','appeal_result'));
ALTER TABLE moderation_notifications ADD COLUMN IF NOT EXISTS appeal_id BIGINT REFERENCES appeals(id);
ALTER TABLE moderation_notifications ADD COLUMN IF NOT EXISTS event_version INTEGER;
ALTER TABLE moderation_notifications ADD COLUMN IF NOT EXISTS event_text TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS moderation_notification_case_unique ON moderation_notifications(case_id) WHERE event_kind='moderation_case';
CREATE UNIQUE INDEX IF NOT EXISTS moderation_notification_appeal_version_unique ON moderation_notifications(appeal_id,event_version) WHERE event_kind='appeal_result';
ALTER TABLE ai_unban_review_claims DROP CONSTRAINT ai_unban_review_claims_state_check;
ALTER TABLE ai_unban_review_claims ADD CONSTRAINT ai_unban_review_claims_state_check CHECK(state IN ('claimed','dispatching','telegram_done','completed','conflict'));
ALTER TABLE ai_mute_review_claims DROP CONSTRAINT ai_mute_review_claims_state_check;
ALTER TABLE ai_mute_review_claims ADD CONSTRAINT ai_mute_review_claims_state_check CHECK(state IN ('claimed','dispatching','telegram_done','completed','conflict'));

-- +goose Down
-- Review and audit data must survive rollback.
SELECT 1;
