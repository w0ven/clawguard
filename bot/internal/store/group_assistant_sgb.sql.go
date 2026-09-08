package store

import (
	"context"
	"strings"
)

const assistantGlobalSettingsColumns = `id, version, model_roles, inbound_merge_window_sec, reply_total_timeout_sec,
	decision_context_items, keep_original_text, memory_recall_enabled, hot_window_compress_enabled,
	proactive_idle_minutes, proactive_quiet_start, proactive_quiet_end, proactive_check_interval_sec,
	tts_enabled, tts_http_timeout_sec, tts_max_text_length, tts_api_base, tts_app_id, tts_app_key_enc,
	tts_access_key_enc, tts_resource_id, tts_model, tts_speaker, tts_audio_format, tts_sample_rate,
	tts_bit_rate, tts_emotion, tts_emotion_scale, tts_speech_rate, tts_loudness_rate, tts_silence_duration_ms,
	sticker_fallback_file_ids, updated_by, created_at, updated_at`

func scanAssistantGlobalSettings(row interface{ Scan(...any) error }) (AssistantGlobalSettings, error) {
	var v AssistantGlobalSettings
	err := row.Scan(&v.ID, &v.Version, &v.ModelRoles, &v.InboundMergeWindowSec, &v.ReplyTotalTimeoutSec,
		&v.DecisionContextItems, &v.KeepOriginalText, &v.MemoryRecallEnabled, &v.HotWindowCompressEnabled,
		&v.ProactiveIdleMinutes, &v.ProactiveQuietStart, &v.ProactiveQuietEnd, &v.ProactiveCheckIntervalSec,
		&v.TTSEnabled, &v.TTSHTTPTimeoutSec, &v.TTSMaxTextLength, &v.TTSAPIBase, &v.TTSAppID, &v.TTSAppKeyEnc,
		&v.TTSAccessKeyEnc, &v.TTSResourceID, &v.TTSModel, &v.TTSSpeaker, &v.TTSAudioFormat, &v.TTSSampleRate,
		&v.TTSBitRate, &v.TTSEmotion, &v.TTSEmotionScale, &v.TTSSpeechRate, &v.TTSLoudnessRate, &v.TTSSilenceDurationMS,
		&v.StickerFallbackFileIDs, &v.UpdatedBy, &v.CreatedAt, &v.UpdatedAt)
	if err == nil && v.StickerFallbackFileIDs == nil {
		v.StickerFallbackFileIDs = []string{}
	}
	if err == nil && len(v.ModelRoles) == 0 {
		v.ModelRoles = []byte("{}")
	}
	return v, err
}

func (q *Queries) GetAssistantGlobalSettings(ctx context.Context) (AssistantGlobalSettings, error) {
	return scanAssistantGlobalSettings(q.db.QueryRow(ctx, `SELECT `+assistantGlobalSettingsColumns+` FROM group_assistant_global_settings WHERE id=1`))
}

func (q *Queries) UpsertAssistantGlobalSettings(ctx context.Context, arg UpsertAssistantGlobalSettingsParams) (AssistantGlobalSettings, error) {
	if len(arg.ModelRoles) == 0 {
		arg.ModelRoles = []byte("{}")
	}
	if arg.StickerFallbackFileIDs == nil {
		arg.StickerFallbackFileIDs = []string{}
	}
	if arg.ExpectedVersion <= 0 {
		return scanAssistantGlobalSettings(q.db.QueryRow(ctx, `UPDATE group_assistant_global_settings SET
			model_roles=$1, inbound_merge_window_sec=$2, reply_total_timeout_sec=$3, decision_context_items=$4,
			keep_original_text=$5, memory_recall_enabled=$6, hot_window_compress_enabled=$7,
			proactive_idle_minutes=$8, proactive_quiet_start=$9, proactive_quiet_end=$10, proactive_check_interval_sec=$11,
			tts_enabled=$12, tts_http_timeout_sec=$13, tts_max_text_length=$14, tts_api_base=$15, tts_app_id=$16,
			tts_app_key_enc=$17, tts_access_key_enc=$18, tts_resource_id=$19, tts_model=$20, tts_speaker=$21,
			tts_audio_format=$22, tts_sample_rate=$23, tts_bit_rate=$24, tts_emotion=$25, tts_emotion_scale=$26,
			tts_speech_rate=$27, tts_loudness_rate=$28, tts_silence_duration_ms=$29, sticker_fallback_file_ids=$30,
			updated_by=$31, version=version+1, updated_at=NOW()
			WHERE id=1 RETURNING `+assistantGlobalSettingsColumns,
			arg.ModelRoles, arg.InboundMergeWindowSec, arg.ReplyTotalTimeoutSec, arg.DecisionContextItems,
			arg.KeepOriginalText, arg.MemoryRecallEnabled, arg.HotWindowCompressEnabled,
			arg.ProactiveIdleMinutes, arg.ProactiveQuietStart, arg.ProactiveQuietEnd, arg.ProactiveCheckIntervalSec,
			arg.TTSEnabled, arg.TTSHTTPTimeoutSec, arg.TTSMaxTextLength, arg.TTSAPIBase, arg.TTSAppID,
			arg.TTSAppKeyEnc, arg.TTSAccessKeyEnc, arg.TTSResourceID, arg.TTSModel, arg.TTSSpeaker,
			arg.TTSAudioFormat, arg.TTSSampleRate, arg.TTSBitRate, arg.TTSEmotion, arg.TTSEmotionScale,
			arg.TTSSpeechRate, arg.TTSLoudnessRate, arg.TTSSilenceDurationMS, arg.StickerFallbackFileIDs, arg.UpdatedBy))
	}
	return scanAssistantGlobalSettings(q.db.QueryRow(ctx, `UPDATE group_assistant_global_settings SET
		model_roles=$2, inbound_merge_window_sec=$3, reply_total_timeout_sec=$4, decision_context_items=$5,
		keep_original_text=$6, memory_recall_enabled=$7, hot_window_compress_enabled=$8,
		proactive_idle_minutes=$9, proactive_quiet_start=$10, proactive_quiet_end=$11, proactive_check_interval_sec=$12,
		tts_enabled=$13, tts_http_timeout_sec=$14, tts_max_text_length=$15, tts_api_base=$16, tts_app_id=$17,
		tts_app_key_enc=$18, tts_access_key_enc=$19, tts_resource_id=$20, tts_model=$21, tts_speaker=$22,
		tts_audio_format=$23, tts_sample_rate=$24, tts_bit_rate=$25, tts_emotion=$26, tts_emotion_scale=$27,
		tts_speech_rate=$28, tts_loudness_rate=$29, tts_silence_duration_ms=$30, sticker_fallback_file_ids=$31,
		updated_by=$32, version=version+1, updated_at=NOW()
		WHERE id=1 AND version=$1 RETURNING `+assistantGlobalSettingsColumns,
		arg.ExpectedVersion, arg.ModelRoles, arg.InboundMergeWindowSec, arg.ReplyTotalTimeoutSec, arg.DecisionContextItems,
		arg.KeepOriginalText, arg.MemoryRecallEnabled, arg.HotWindowCompressEnabled,
		arg.ProactiveIdleMinutes, arg.ProactiveQuietStart, arg.ProactiveQuietEnd, arg.ProactiveCheckIntervalSec,
		arg.TTSEnabled, arg.TTSHTTPTimeoutSec, arg.TTSMaxTextLength, arg.TTSAPIBase, arg.TTSAppID,
		arg.TTSAppKeyEnc, arg.TTSAccessKeyEnc, arg.TTSResourceID, arg.TTSModel, arg.TTSSpeaker,
		arg.TTSAudioFormat, arg.TTSSampleRate, arg.TTSBitRate, arg.TTSEmotion, arg.TTSEmotionScale,
		arg.TTSSpeechRate, arg.TTSLoudnessRate, arg.TTSSilenceDurationMS, arg.StickerFallbackFileIDs, arg.UpdatedBy))
}

func (q *Queries) ListAssistantPromptOverrides(ctx context.Context, chatID int64) ([]AssistantPromptOverride, error) {
	rows, err := q.db.Query(ctx, `SELECT chat_id, prompt_key, content, updated_at
		FROM group_assistant_prompt_overrides WHERE chat_id=$1 ORDER BY prompt_key`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AssistantPromptOverride, 0)
	for rows.Next() {
		var item AssistantPromptOverride
		if err := rows.Scan(&item.ChatID, &item.PromptKey, &item.Content, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (q *Queries) GetAssistantPromptOverride(ctx context.Context, chatID int64, key string) (AssistantPromptOverride, error) {
	var item AssistantPromptOverride
	err := q.db.QueryRow(ctx, `SELECT chat_id, prompt_key, content, updated_at
		FROM group_assistant_prompt_overrides WHERE chat_id=$1 AND prompt_key=$2`, chatID, key).
		Scan(&item.ChatID, &item.PromptKey, &item.Content, &item.UpdatedAt)
	return item, err
}

func (q *Queries) UpsertAssistantPromptOverride(ctx context.Context, chatID int64, key, content string) (AssistantPromptOverride, error) {
	var item AssistantPromptOverride
	err := q.db.QueryRow(ctx, `INSERT INTO group_assistant_prompt_overrides (chat_id, prompt_key, content, updated_at)
		VALUES ($1,$2,$3,NOW())
		ON CONFLICT (chat_id, prompt_key) DO UPDATE SET content=EXCLUDED.content, updated_at=NOW()
		RETURNING chat_id, prompt_key, content, updated_at`, chatID, key, content).
		Scan(&item.ChatID, &item.PromptKey, &item.Content, &item.UpdatedAt)
	return item, err
}

func (q *Queries) DeleteAssistantPromptOverride(ctx context.Context, chatID int64, key string) error {
	_, err := q.db.Exec(ctx, `DELETE FROM group_assistant_prompt_overrides WHERE chat_id=$1 AND prompt_key=$2`, chatID, key)
	return err
}

const assistantStickerSampleColumns = `id, chat_id, file_id, source_message_id, query, emoji, set_name, aliases,
	seen_count, sent_count, source, created_at, last_seen_at, last_sent_at`

func scanAssistantStickerSample(row interface{ Scan(...any) error }) (AssistantStickerSample, error) {
	var v AssistantStickerSample
	err := row.Scan(&v.ID, &v.ChatID, &v.FileID, &v.SourceMessageID, &v.Query, &v.Emoji, &v.SetName, &v.Aliases,
		&v.SeenCount, &v.SentCount, &v.Source, &v.CreatedAt, &v.LastSeenAt, &v.LastSentAt)
	if err == nil && v.Aliases == nil {
		v.Aliases = []string{}
	}
	return v, err
}

func (q *Queries) ListAssistantStickerSamples(ctx context.Context, chatID int64, limit int32) ([]AssistantStickerSample, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := q.db.Query(ctx, `SELECT `+assistantStickerSampleColumns+`
		FROM group_assistant_sticker_samples WHERE chat_id=$1
		ORDER BY last_seen_at DESC NULLS LAST, last_sent_at DESC NULLS LAST, sent_count DESC, seen_count DESC, id DESC
		LIMIT $2`, chatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AssistantStickerSample, 0)
	for rows.Next() {
		item, scanErr := scanAssistantStickerSample(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (q *Queries) UpsertAssistantStickerSample(ctx context.Context, arg UpsertAssistantStickerSampleParams) (AssistantStickerSample, error) {
	query := strings.TrimSpace(arg.Query)
	if len([]rune(query)) > 240 {
		query = string([]rune(query)[:240])
	}
	return scanAssistantStickerSample(q.db.QueryRow(ctx, `INSERT INTO group_assistant_sticker_samples
		(chat_id, file_id, source_message_id, query, emoji, set_name, aliases, seen_count, sent_count, source, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,ARRAY[]::TEXT[],1,0,$7,NOW())
		ON CONFLICT (chat_id, file_id) DO UPDATE SET
			emoji=CASE WHEN EXCLUDED.emoji<>'' THEN EXCLUDED.emoji ELSE group_assistant_sticker_samples.emoji END,
			set_name=CASE WHEN EXCLUDED.set_name<>'' THEN EXCLUDED.set_name ELSE group_assistant_sticker_samples.set_name END,
			query=CASE WHEN EXCLUDED.query<>'' THEN EXCLUDED.query ELSE group_assistant_sticker_samples.query END,
			source_message_id=COALESCE(EXCLUDED.source_message_id, group_assistant_sticker_samples.source_message_id),
			aliases=CASE WHEN group_assistant_sticker_samples.query<>'' AND EXCLUDED.query<>'' AND group_assistant_sticker_samples.query<>EXCLUDED.query
				THEN array_append(group_assistant_sticker_samples.aliases, group_assistant_sticker_samples.query)
				ELSE group_assistant_sticker_samples.aliases END,
			seen_count=group_assistant_sticker_samples.seen_count+1,
			last_seen_at=NOW()
		RETURNING `+assistantStickerSampleColumns,
		arg.ChatID, arg.FileID, arg.SourceMessageID, query, strings.TrimSpace(arg.Emoji), strings.TrimSpace(arg.SetName), strings.TrimSpace(arg.Source)))
}

func (q *Queries) MarkAssistantStickerSent(ctx context.Context, chatID int64, fileID string) error {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return nil
	}
	_, err := q.db.Exec(ctx, `UPDATE group_assistant_sticker_samples
		SET sent_count=sent_count+1, last_sent_at=NOW() WHERE chat_id=$1 AND file_id=$2`, chatID, fileID)
	return err
}

func (q *Queries) TrimAssistantStickerSamples(ctx context.Context, chatID int64, maxKeep int32) error {
	if maxKeep <= 0 {
		maxKeep = 100
	}
	_, err := q.db.Exec(ctx, `DELETE FROM group_assistant_sticker_samples WHERE chat_id=$1 AND id IN (
		SELECT id FROM group_assistant_sticker_samples WHERE chat_id=$1
		ORDER BY last_seen_at DESC NULLS LAST, last_sent_at DESC NULLS LAST, sent_count DESC, seen_count DESC, id DESC
		OFFSET $2
	)`, chatID, maxKeep)
	return err
}

func ValidAssistantPromptKey(key string) bool {
	switch strings.TrimSpace(key) {
	case "persona", "casual", "decision", "proactive_topic", "style_distill":
		return true
	default:
		return false
	}
}
