-- name: InsertAIDecision :one
INSERT INTO ai_decisions (
    chat_id,
    user_id,
    message_id,
    message_text,
    provider_id,
    model_id,
    model,
    prompt_version,
    verdict,
    confidence,
    category,
    reason,
    action_taken,
    admin_override,
    latency_ms,
    scene
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
RETURNING id, chat_id, user_id, message_id, message_text, provider_id, model_id, model, prompt_version, verdict, confidence, category, reason, action_taken, admin_override, latency_ms, scene, created_at;

-- name: ListAIDecisions :many
SELECT id, chat_id, user_id, message_id, message_text, provider_id, model_id, model, prompt_version, verdict, confidence, category, reason, action_taken, admin_override, latency_ms, scene, created_at
FROM ai_decisions
WHERE ($1::BIGINT IS NULL OR chat_id = $1)
  AND ($2::BIGINT IS NULL OR user_id = $2)
  AND (
    $3::TEXT = ''
    OR verdict = $3
    OR ($3::TEXT = 'normal' AND verdict = 'clean')
  )
  AND ($4::TEXT = '' OR COALESCE(admin_override, '') = $4)
  AND ($5::TEXT = '' OR category = $5)
  AND ($6::TEXT = '' OR action_taken = $6)
  AND ($7::TEXT = '' OR scene = $7)
  AND ($8::TIMESTAMPTZ IS NULL OR created_at >= $8)
  AND ($9::TIMESTAMPTZ IS NULL OR created_at <= $9)
ORDER BY created_at DESC
LIMIT $10;

-- name: GetAIDecisionByID :one
SELECT id, chat_id, user_id, message_id, message_text, provider_id, model_id, model, prompt_version, verdict, confidence, category, reason, action_taken, admin_override, latency_ms, scene, created_at
FROM ai_decisions
WHERE id = $1;

-- name: UpdateAIDecisionOverride :one
UPDATE ai_decisions
SET admin_override = $2
WHERE id = $1
RETURNING id, chat_id, user_id, message_id, message_text, provider_id, model_id, model, prompt_version, verdict, confidence, category, reason, action_taken, admin_override, latency_ms, scene, created_at;
