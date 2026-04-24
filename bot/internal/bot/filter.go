package bot

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/config"
)

var bareURLPattern = regexp.MustCompile(`https?://[^\s]+`)

type FilterResult struct {
	Hit         bool
	Reason      string
	MatchedRule string
	Action      string
}

func checkMessage(_ context.Context, msg *tele.Message, policy config.FilterConfig, isAdmin bool) FilterResult {
	if msg == nil {
		return FilterResult{}
	}

	content := collectMessageContent(msg)
	searchContent := strings.ToLower(content)
	if policy.Keywords.CaseSensitive {
		searchContent = content
	}

	for _, keyword := range policy.Keywords.List {
		trimmed := strings.TrimSpace(keyword)
		if trimmed == "" {
			continue
		}
		needle := strings.ToLower(trimmed)
		if policy.Keywords.CaseSensitive {
			needle = trimmed
		}
		if policy.Keywords.Enabled && strings.Contains(searchContent, needle) {
			return FilterResult{
				Hit:         true,
				Reason:      "filter_keyword",
				MatchedRule: trimmed,
				Action:      policy.Keywords.Action,
			}
		}
	}

	if policy.Regex.Enabled {
		for _, pattern := range policy.Regex.Patterns {
			trimmed := strings.TrimSpace(pattern)
			if trimmed == "" {
				continue
			}
			expr, err := regexp.Compile(trimmed)
			if err != nil {
				continue
			}
			if expr.MatchString(content) {
				return FilterResult{
					Hit:         true,
					Reason:      "filter_regex",
					MatchedRule: trimmed,
				}
			}
		}
	}

	if policy.Usernames.Enabled {
		username := normalizeUsername(msg.Sender)
		for _, blocked := range policy.Usernames.Blacklist {
			if username == "" {
				break
			}
			if username == normalizeUsernameValue(blocked) {
				return FilterResult{
					Hit:         true,
					Reason:      "filter_username",
					MatchedRule: blocked,
				}
			}
		}
	}

	links := collectMessageLinks(msg)
	if len(links) == 0 || !policy.Links.Enabled || (isAdmin && policy.Links.ExemptAdmins) {
		return FilterResult{}
	}

	for _, rawLink := range links {
		host := normalizeURLHost(rawLink)
		if host == "" || !domainAllowed(host, policy.Links.Whitelist) {
			return FilterResult{
				Hit:         true,
				Reason:      "filter_link",
				MatchedRule: rawLink,
				Action:      policy.Links.Action,
			}
		}
	}

	return FilterResult{}
}

func collectMessageContent(msg *tele.Message) string {
	if msg == nil {
		return ""
	}
	if msg.Text != "" && msg.Caption != "" {
		return msg.Text + "\n" + msg.Caption
	}
	if msg.Text != "" {
		return msg.Text
	}
	return msg.Caption
}

func collectMessageLinks(msg *tele.Message) []string {
	if msg == nil {
		return nil
	}

	seen := map[string]struct{}{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
	}

	for _, entity := range msg.Entities {
		if entity.Type == tele.EntityURL {
			add(msg.EntityText(entity))
		}
		if entity.Type == tele.EntityTextLink {
			add(entity.URL)
		}
	}

	for _, entity := range msg.CaptionEntities {
		if entity.Type == tele.EntityURL {
			add(msg.EntityText(entity))
		}
		if entity.Type == tele.EntityTextLink {
			add(entity.URL)
		}
	}

	for _, match := range bareURLPattern.FindAllString(msg.Text, -1) {
		add(match)
	}
	for _, match := range bareURLPattern.FindAllString(msg.Caption, -1) {
		add(match)
	}

	links := make([]string, 0, len(seen))
	for link := range seen {
		links = append(links, link)
	}
	return links
}

func normalizeURLHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
}

func domainAllowed(host string, allowlist []string) bool {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	for _, domain := range allowlist {
		normalized := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), "www.")
		if normalized == "" {
			continue
		}
		if host == normalized || strings.HasSuffix(host, "."+normalized) {
			return true
		}
	}
	return false
}

func normalizeUsername(user *tele.User) string {
	if user == nil {
		return ""
	}
	return normalizeUsernameValue(user.Username)
}

func normalizeUsernameValue(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "@")
	return value
}
