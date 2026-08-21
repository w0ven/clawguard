package api

import (
	"encoding/json"
	"strings"

	"go.uber.org/zap"
)

const diagnosticRingEmoji = "💍"

type rawUpdateEnvelope struct {
	ID            int               `json:"update_id"`
	Message       *rawUpdateMessage `json:"message"`
	EditedMessage *rawUpdateMessage `json:"edited_message"`
}

type rawUpdateMessage struct {
	ID                 int             `json:"message_id"`
	Text               string          `json:"text"`
	Caption            string          `json:"caption"`
	ForwardOrigin      json.RawMessage `json:"forward_origin"`
	ForwardFrom        json.RawMessage `json:"forward_from"`
	ForwardSenderName  string          `json:"forward_sender_name"`
	ReplyToMessage     json.RawMessage `json:"reply_to_message"`
	ExternalReply      json.RawMessage `json:"external_reply"`
	Quote              json.RawMessage `json:"quote"`
	LinkPreviewOptions json.RawMessage `json:"link_preview_options"`
	Photo              json.RawMessage `json:"photo"`
	Video              json.RawMessage `json:"video"`
	Document           json.RawMessage `json:"document"`
	Animation          json.RawMessage `json:"animation"`
	Audio              json.RawMessage `json:"audio"`
	Voice              json.RawMessage `json:"voice"`
	Sticker            json.RawMessage `json:"sticker"`
}

func (s *Server) logRawTelegramUpdateIfTarget(raw []byte) {
	if !s.cfg.LogRawUpdates || len(raw) == 0 {
		return
	}

	summary, ok := rawTelegramUpdateDiagnosticSummary(raw)
	if !ok {
		return
	}

	s.logger.Info(
		"telegram raw update diagnostic",
		zap.Any("summary", summary),
	)
}

func rawTelegramUpdateDiagnosticSummary(raw []byte) (map[string]any, bool) {
	var update rawUpdateEnvelope
	if err := json.Unmarshal(raw, &update); err != nil {
		return nil, false
	}

	messageType := "message"
	msg := update.Message
	if msg == nil {
		messageType = "edited_message"
		msg = update.EditedMessage
	}
	if msg == nil {
		return nil, false
	}

	text := msg.Text
	if text == "" {
		text = msg.Caption
	}

	hasForward := len(msg.ForwardOrigin) > 0 || len(msg.ForwardFrom) > 0 || strings.TrimSpace(msg.ForwardSenderName) != ""
	hasContext := len(msg.ReplyToMessage) > 0 || len(msg.ExternalReply) > 0 || len(msg.Quote) > 0 || len(msg.LinkPreviewOptions) > 0
	shortTextWithContext := runeLen(text) <= 8 && hasContext
	hasRing := strings.Contains(text, diagnosticRingEmoji)
	if !hasForward && !shortTextWithContext && !hasRing {
		return nil, false
	}

	summary := map[string]any{
		"update_id":                update.ID,
		"message_type":             messageType,
		"message_id":               msg.ID,
		"text_len":                 runeLen(msg.Text),
		"caption_len":              runeLen(msg.Caption),
		"has_forward_origin":       len(msg.ForwardOrigin) > 0,
		"has_forward_from":         len(msg.ForwardFrom) > 0,
		"has_forward_sender_name":  strings.TrimSpace(msg.ForwardSenderName) != "",
		"has_reply_to_message":     len(msg.ReplyToMessage) > 0,
		"has_external_reply":       len(msg.ExternalReply) > 0,
		"has_quote":                len(msg.Quote) > 0,
		"has_link_preview_options": len(msg.LinkPreviewOptions) > 0,
		"has_photo":                len(msg.Photo) > 0,
		"has_video":                len(msg.Video) > 0,
		"has_document":             len(msg.Document) > 0,
		"has_animation":            len(msg.Animation) > 0,
		"has_audio":                len(msg.Audio) > 0,
		"has_voice":                len(msg.Voice) > 0,
		"has_sticker":              len(msg.Sticker) > 0,
		"reply_to_summary":         rawMessageFieldSummary(msg.ReplyToMessage),
		"external_reply_summary":   rawMessageFieldSummary(msg.ExternalReply),
		"matched_forward":          hasForward,
		"matched_short_context":    shortTextWithContext,
		"matched_ring":             hasRing,
	}
	return summary, true
}

func runeLen(s string) int {
	return len([]rune(s))
}

func rawMessageFieldSummary(raw json.RawMessage) map[string]bool {
	if len(raw) == 0 {
		return nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return map[string]bool{"present": true}
	}

	return map[string]bool{
		"present":                  true,
		"has_text":                 len(fields["text"]) > 0,
		"has_caption":              len(fields["caption"]) > 0,
		"has_link_preview_options": len(fields["link_preview_options"]) > 0,
		"has_photo":                len(fields["photo"]) > 0,
		"has_video":                len(fields["video"]) > 0,
		"has_document":             len(fields["document"]) > 0,
	}
}
