package bot

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/messagefmt"
)

func (s *Service) tryKeywordReply(ctx context.Context, msg *tele.Message, policy config.GuardPolicy) (matched bool, err error) {
	if msg == nil || msg.Chat == nil || msg.Sender == nil {
		return false, nil
	}
	if len(policy.Messages.KeywordReplies) == 0 {
		return false, nil
	}

	text := strings.TrimSpace(collectMessageContent(msg))
	if text == "" {
		return false, nil
	}

	// 懒查管理员状态，只有命中 skip_admins 规则时才查
	var adminChecked bool
	var senderIsAdmin bool
	checkAdmin := func() bool {
		if adminChecked {
			return senderIsAdmin
		}
		adminChecked = true
		ok, checkErr := s.isChatAdmin(ctx, msg.Chat.ID, msg.Sender.ID)
		if checkErr != nil {
			s.logger.Warn("keyword reply: check admin failed", zap.Error(checkErr))
			return false
		}
		senderIsAdmin = ok
		return ok
	}

	for _, rule := range policy.Messages.KeywordReplies {
		if !rule.Enabled {
			continue
		}
		ruleID := strings.TrimSpace(rule.ID)
		replyText := strings.TrimSpace(rule.ReplyText)
		if ruleID == "" || replyText == "" {
			continue
		}

		// 规则级：管理员豁免
		if rule.SkipAdmins && checkAdmin() {
			continue
		}

		keyword, hit := s.matchKeywordReplyRule(rule, text)
		if !hit {
			continue
		}

		if rule.CooldownSeconds > 0 && s.redis != nil {
			cdKey := fmt.Sprintf("kwreply:cd:%d:%s", msg.Chat.ID, ruleID)
			ok, redisErr := s.redis.SetNX(ctx, cdKey, "1", time.Duration(rule.CooldownSeconds)*time.Second).Result()
			if redisErr != nil {
				s.logger.Warn("keyword reply cooldown unavailable, suppress reply fail-closed", zap.Error(redisErr), zap.Int64("chat_id", msg.Chat.ID), zap.String("rule_id", ruleID))
				return true, nil
			}
			if !ok {
				return true, nil
			}
		}

		rendered := renderKeywordReply(msg, keyword, replyText, rule.ParseMode)
		parseMode := resolveParseMode(rule.ParseMode)
		sent, sendErr := s.sendThrottled(ctx, msg.Chat, rendered, &tele.SendOptions{
			ParseMode:             parseMode,
			DisableWebPagePreview: true,
			ReplyTo:               msg,
		})
		if sendErr != nil {
			return false, fmt.Errorf("send keyword reply: %w", sendErr)
		}

		if s.redis != nil {
			countKey := fmt.Sprintf("kwreply:count:%d:%s", msg.Chat.ID, ruleID)
			lastKey := fmt.Sprintf("kwreply:last:%d:%s", msg.Chat.ID, ruleID)
			nowUnix := time.Now().Unix()
			pipe := s.redis.TxPipeline()
			pipe.Incr(ctx, countKey)
			pipe.Set(ctx, lastKey, strconv.FormatInt(nowUnix, 10), 0)
			if _, redisErr := pipe.Exec(ctx); redisErr != nil {
				s.logger.Warn("update keyword reply stats failed", zap.Error(redisErr), zap.Int64("chat_id", msg.Chat.ID), zap.String("rule_id", ruleID))
			}
		}

		if rule.AutoDeleteSeconds > 0 && sent != nil {
			s.scheduleKeywordReplyDelete(msg.Chat, sent, rule)
		}

		return true, nil
	}

	return false, nil
}

func (s *Service) matchKeywordReplyRule(rule config.KeywordReplyRule, messageText string) (string, bool) {
	matchType := strings.TrimSpace(strings.ToLower(rule.MatchType))
	if matchType == "" {
		matchType = "fuzzy"
	}

	haystack := messageText
	if !rule.CaseSensitive {
		haystack = strings.ToLower(haystack)
	}

	for _, rawKeyword := range rule.Keywords {
		keyword := strings.TrimSpace(rawKeyword)
		if keyword == "" {
			continue
		}

		switch matchType {
		case "exact":
			needle := keyword
			if !rule.CaseSensitive {
				needle = strings.ToLower(needle)
			}
			if haystack == needle {
				return keyword, true
			}
		case "regex":
			pattern := keyword
			if !rule.CaseSensitive {
				pattern = "(?i)" + pattern
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				s.logger.Warn("compile keyword reply regex failed", zap.Error(err), zap.String("rule_id", rule.ID), zap.String("rule_name", rule.Name), zap.String("keyword", keyword))
				continue
			}
			if re.MatchString(messageText) {
				return keyword, true
			}
		case "", "fuzzy":
			needle := keyword
			if !rule.CaseSensitive {
				needle = strings.ToLower(needle)
			}
			if strings.Contains(haystack, needle) {
				return keyword, true
			}
		default:
			needle := keyword
			if !rule.CaseSensitive {
				needle = strings.ToLower(needle)
			}
			if strings.Contains(haystack, needle) {
				return keyword, true
			}
		}
	}

	return "", false
}

func resolveParseMode(mode string) string {
	return messagefmt.ResolveParseMode(mode)
}

func renderKeywordReply(msg *tele.Message, keyword string, template string, parseMode string) string {
	groupTitle := "私聊"
	if msg != nil && msg.Chat != nil && !msg.Private() {
		groupTitle = strings.TrimSpace(msg.Chat.Title)
		if groupTitle == "" {
			groupTitle = "私聊"
		}
	}

	userRepl := ""
	if msg.Sender != nil {
		switch resolveParseMode(parseMode) {
		case tele.ModeMarkdownV2:
			userRepl = mentionMarkdownV2(msg.Sender)
		case tele.ModeMarkdown:
			userRepl = mentionMarkdownLegacy(msg.Sender)
		default:
			userRepl = mentionHTML(msg.Sender)
		}
	}

	rendered := RenderMessageTemplate(template, parseMode, map[string]string{
		"{user}": userRepl,
	}, map[string]string{
		"{group}":   groupTitle,
		"{keyword}": keyword,
	})
	return rendered.Text
}

// escapeMarkdownV2 转义 MarkdownV2 特殊字符，但保留用户写的格式标记：
// *bold*, _italic_, __underline__, ~strikethrough~, ||spoiler||,
// `code`, ` + "```" + `pre` + "```" + `, [text](url), > blockquote
func escapeMarkdownV2(text string) string {
	type ph struct {
		key      string
		original string
	}
	var phs []ph
	idx := 0
	protect := func(s string) string {
		key := fmt.Sprintf("\x00PH%d\x00", idx)
		idx++
		phs = append(phs, ph{key: key, original: s})
		return key
	}

	patterns := []*regexp.Regexp{
		regexp.MustCompile("(?s)```[\\s\\S]*?```"),
		regexp.MustCompile("[`][^`]+[`]"),
		regexp.MustCompile(`!\[([^\]]*?)\]\(([^)]+)\)`),
		regexp.MustCompile(`\[([^\]]+?)\]\(([^)]+)\)`),
		regexp.MustCompile(`\|\|[^|]+\|\|`),
		regexp.MustCompile(`__[^_]+__`),
		regexp.MustCompile(`~[^~]+~`),
		regexp.MustCompile(`\*[^*]+\*`),
		regexp.MustCompile(`_[^_]+_`),
	}

	result := text
	for _, pat := range patterns {
		result = pat.ReplaceAllStringFunc(result, protect)
	}

	lines := strings.Split(result, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, ">") {
			rest := strings.TrimPrefix(line, ">")
			if strings.HasPrefix(rest, " ") {
				lines[i] = "> " + mdv2EscapeChars(strings.TrimPrefix(rest, " "))
			} else {
				lines[i] = ">" + mdv2EscapeChars(rest)
			}
		} else {
			lines[i] = mdv2EscapeChars(line)
		}
	}
	result = strings.Join(lines, "\n")

	for j := len(phs) - 1; j >= 0; j-- {
		result = strings.ReplaceAll(result, phs[j].key, phs[j].original)
	}
	return result
}

var mdv2Specials = []string{"_", "*", "[", "]", "(", ")", "~", "`", ">", "#", "+", "-", "=", "|", "{", "}", ".", "!"}

func mdv2EscapeChars(s string) string {
	return messagefmt.EscapeMarkdownV2Chars(s)
}

func (s *Service) scheduleKeywordReplyDelete(chat *tele.Chat, sent *tele.Message, rule config.KeywordReplyRule) {
	s.runDelayed(time.Duration(rule.AutoDeleteSeconds)*time.Second, func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Warn("keyword reply auto delete panic", zap.Any("panic", recovered), zap.String("rule_id", rule.ID))
			}
		}()
		if sent == nil || chat == nil {
			return
		}
		if err := s.deleteDelayedMessage(sent); err != nil {
			s.logger.Warn("auto delete keyword reply failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int("message_id", sent.ID), zap.String("rule_id", rule.ID))
		}
	})
}

func (s *Service) MergeKeywordReplyStats(ctx context.Context, chatID int64, rules []config.KeywordReplyRule) []config.KeywordReplyRule {
	return mergeKeywordReplyStats(ctx, s.redis, chatID, rules)
}

func mergeKeywordReplyStats(ctx context.Context, rdb redis.Cmdable, chatID int64, rules []config.KeywordReplyRule) []config.KeywordReplyRule {
	if len(rules) == 0 || rdb == nil {
		if rules == nil {
			return []config.KeywordReplyRule{}
		}
		return rules
	}

	merged := make([]config.KeywordReplyRule, len(rules))
	copy(merged, rules)

	pipe := rdb.Pipeline()
	countCmds := make([]*redis.StringCmd, len(merged))
	lastCmds := make([]*redis.StringCmd, len(merged))
	for i, rule := range merged {
		ruleID := strings.TrimSpace(rule.ID)
		if ruleID == "" {
			continue
		}
		countCmds[i] = pipe.Get(ctx, fmt.Sprintf("kwreply:count:%d:%s", chatID, ruleID))
		lastCmds[i] = pipe.Get(ctx, fmt.Sprintf("kwreply:last:%d:%s", chatID, ruleID))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return merged
	}

	for i := range merged {
		if countCmds[i] != nil {
			if raw, err := countCmds[i].Result(); err == nil {
				if parsed, parseErr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); parseErr == nil {
					merged[i].TriggerCount = parsed
				}
			}
		}
		if lastCmds[i] != nil {
			if raw, err := lastCmds[i].Result(); err == nil {
				if parsed, parseErr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); parseErr == nil && parsed > 0 {
					ts := time.Unix(parsed, 0).UTC().Format(time.RFC3339)
					merged[i].LastTriggeredAt = &ts
				}
			}
		}
	}

	return merged
}
