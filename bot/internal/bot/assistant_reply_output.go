package bot

// Adapted from Smart_Group_Bot bot/services/reply_output.py and
// bot/handlers/group.py, commit 82c3703daba218b36255132c9bf51ebc444c6480.
// Copyright (c) 2025 Sanite&Ava. MIT: assistant_prompts_sgb/LICENSE.
import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
)

const assistantReplySchema = "smart-group-bot.reply.v2"
const assistantReplyProtocol = `[REPLY_OUTPUT_PROTOCOL]
Default to one plain-text message. Blank lines do not split messages. Only a standalone [[SPLIT]] line outside a code fence splits messages; never split just for a chat-bubble effect.
For zero messages or per-message delivery control, the entire answer must be an unfenced JSON object with schema="smart-group-bot.reply.v2".
Example: {"schema":"smart-group-bot.reply.v2","messages":[{"text":"你好","delivery_mode":"reply","reply_to":"latest_input"}]}
messages may contain strings or objects. delivery_mode supports auto/reply/message. reply_to must use a supplied REPLY_TARGET_CANDIDATES alias, otherwise use auto.
To stay silent: {"schema":"smart-group-bot.reply.v2","should_reply":false}.
Do not describe the protocol. After tools the same output rules apply. Telegram delivery uses plain text, not rich web pages.`

type assistantReplySpec struct{ Text, DeliveryMode, ReplyTo string }

func assistantReplyBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "yes", "1":
			return true, true
		case "false", "no", "0":
			return false, true
		}
	}
	return false, false
}
func assistantReplyText(s string, dropMarkers bool) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	s = strings.Map(func(r rune) rune {
		if (r < 32 && r != '\n' && r != '\t') || r == 127 {
			return ' '
		}
		return r
	}, s)
	if dropMarkers {
		lines := []string{}
		for _, l := range strings.Split(s, "\n") {
			if !strings.EqualFold(strings.TrimSpace(l), "[[SPLIT]]") {
				lines = append(lines, l)
			}
		}
		s = strings.Join(lines, "\n")
	}
	return truncateAssistant(strings.TrimSpace(s), assistantMaxTelegramText)
}
func parseAssistantReplyOutput(raw string) []assistantReplySpec {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil
	}
	var data map[string]any
	valid := json.Unmarshal([]byte(text), &data) == nil && data["schema"] == assistantReplySchema
	control := false
	for _, k := range []string{"action", "disposition", "message", "messages", "reply", "response_type", "should_reply", "silent", "skip_reply", "text"} {
		if _, ok := data[k]; ok {
			control = true
		}
	}
	if valid && control {
		for _, k := range []string{"should_reply", "silent", "skip_reply"} {
			if b, ok := assistantReplyBool(data[k]); ok && ((k == "should_reply" && !b) || (k != "should_reply" && b)) {
				return nil
			}
		}
		for _, k := range []string{"action", "response_type", "disposition"} {
			switch strings.ToLower(fmt.Sprint(data[k])) {
			case "silent", "skip", "no_reply", "noreply", "no-response", "no_response":
				return nil
			}
		}
		extract := func(value any, max int) []assistantReplySpec {
			var list []any
			switch x := value.(type) {
			case []any:
				list = x
			case string:
				list = []any{x}
			case map[string]any:
				list = []any{x}
			}
			out := []assistantReplySpec{}
			for i, item := range list {
				if i >= max {
					break
				}
				spec := assistantReplySpec{DeliveryMode: "auto", ReplyTo: "auto"}
				switch x := item.(type) {
				case string:
					spec.Text = x
				case map[string]any:
					for _, k := range []string{"text", "content", "message", "reply"} {
						if v, ok := x[k].(string); ok && strings.TrimSpace(v) != "" {
							spec.Text = v
							break
						}
					}
					for _, k := range []string{"delivery_mode", "mode"} {
						if v, ok := x[k].(string); ok {
							spec.DeliveryMode = strings.ToLower(strings.TrimSpace(v))
							break
						}
					}
					for _, k := range []string{"reply_to", "reply_target", "target"} {
						if v, ok := x[k].(string); ok {
							spec.ReplyTo = truncateAssistant(strings.TrimSpace(v), 80)
							break
						}
					}
				}
				if spec.DeliveryMode != "reply" && spec.DeliveryMode != "message" {
					spec.DeliveryMode = "auto"
				}
				spec.Text = assistantReplyText(spec.Text, true)
				if spec.Text != "" {
					out = append(out, spec)
				}
			}
			return out
		}
		if out := extract(data["messages"], 8); len(out) > 0 {
			return out
		}
		for _, k := range []string{"message", "reply", "text"} {
			if out := extract(data[k], 1); len(out) > 0 {
				return out
			}
		}
		return nil
	}
	parts := []string{}
	lines := []string{}
	fence := false
	for _, l := range strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n") {
		trim := strings.TrimSpace(l)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			fence = !fence
		} else if !fence && strings.EqualFold(trim, "[[SPLIT]]") {
			parts = append(parts, strings.Join(lines, "\n"))
			lines = nil
			continue
		}
		lines = append(lines, l)
	}
	parts = append(parts, strings.Join(lines, "\n"))
	clean := []string{}
	for _, p := range parts {
		if s := assistantReplyText(p, false); s != "" {
			clean = append(clean, s)
		}
	}
	if len(clean) > 8 {
		clean = append(clean[:7], assistantReplyText(strings.Join(clean[7:], "\n\n"), false))
	}
	out := []assistantReplySpec{}
	for _, p := range clean {
		out = append(out, assistantReplySpec{Text: p, DeliveryMode: "auto", ReplyTo: "auto"})
	}
	return out
}

// SGB _next_pending_reply_flush_at: later arrivals can advance, never delay.
func assistantNextFlushAt(item assistantReplyBatchItem, size int, base time.Duration, now, current time.Time) time.Time {
	delay := 1800 * time.Millisecond
	switch {
	case item.direct:
		delay = 500 * time.Millisecond
	case size >= 3:
		delay = 900 * time.Millisecond
	case size == 2:
		delay = 1100 * time.Millisecond
	case item.msg != nil && item.msg.ReplyTo != nil || assistantReplyQuestion(item.text):
		delay = 1400 * time.Millisecond
	}
	if base < delay {
		delay = base
	}
	target := now.Add(delay)
	if !current.IsZero() && current.Before(target) {
		return current
	}
	return target
}
func assistantReplyQuestion(text string) bool {
	text = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)
	for _, v := range []string{"?", "？", "什么", "哪个", "哪款", "怎么", "咋", "如何", "为什么", "为啥", "推荐", "帮我", "有没有", "是不是", "行不行", "可不可以", "能不能", "最好用", "值不值得", "吗", "呢", "么", "嘛"} {
		if strings.Contains(text, v) {
			return true
		}
	}
	return false
}
