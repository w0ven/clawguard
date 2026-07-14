-- +goose Up
ALTER TABLE pending_verifications
    ADD COLUMN next_attempt_at TIMESTAMPTZ,
    ADD COLUMN lease_until TIMESTAMPTZ,
    ADD COLUMN lease_owner TEXT,
    ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN last_error TEXT;

UPDATE pending_verifications
SET next_attempt_at = expires_at
WHERE next_attempt_at IS NULL;

ALTER TABLE pending_verifications
    ALTER COLUMN next_attempt_at SET NOT NULL;

-- Keep inserts and conflict updates compatible with binaries from before this
-- migration so an image rollback does not break verification writes.
-- +goose StatementBegin
CREATE FUNCTION set_pending_verification_next_attempt_at()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.next_attempt_at IS NULL OR (
        TG_OP = 'UPDATE'
        AND NEW.expires_at IS DISTINCT FROM OLD.expires_at
        AND NEW.next_attempt_at IS NOT DISTINCT FROM OLD.next_attempt_at
    ) THEN
        NEW.next_attempt_at := NEW.expires_at;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER pending_verifications_next_attempt_at_compat
BEFORE INSERT OR UPDATE ON pending_verifications
FOR EACH ROW
EXECUTE FUNCTION set_pending_verification_next_attempt_at();

CREATE INDEX idx_pending_verifications_due
ON pending_verifications(next_attempt_at, lease_until, chat_id);

-- +goose Down
DROP TRIGGER IF EXISTS pending_verifications_next_attempt_at_compat ON pending_verifications;
DROP FUNCTION IF EXISTS set_pending_verification_next_attempt_at();

DROP INDEX IF EXISTS idx_pending_verifications_due;

ALTER TABLE pending_verifications
    DROP COLUMN IF EXISTS last_error,
    DROP COLUMN IF EXISTS attempt_count,
    DROP COLUMN IF EXISTS lease_owner,
    DROP COLUMN IF EXISTS lease_until,
    DROP COLUMN IF EXISTS next_attempt_at;
