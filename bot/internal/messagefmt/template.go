package messagefmt

import (
	"html"
	"sort"
	"strings"

	tele "gopkg.in/telebot.v3"
)

type RenderedTemplate struct {
	Text      string
	ParseMode string
}

func ResolveParseMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "markdown", "md":
		return tele.ModeMarkdownV2
	case "markdownv2", "mdv2":
		return tele.ModeMarkdownV2
	case "html":
		return tele.ModeHTML
	default:
		return tele.ModeMarkdownV2
	}
}

func RenderMessageTemplate(template string, parseMode string, structuralVars map[string]string, plainVars map[string]string) RenderedTemplate {
	resolvedMode := ResolveParseMode(parseMode)

	var text string
	switch resolvedMode {
	case tele.ModeHTML:
		text = replaceTemplatePlaceholders(template, structuralVars, plainVars, html.EscapeString)
	case tele.ModeMarkdown:
		text = replaceTemplatePlaceholdersMarkdownV2(template, structuralVars, plainVars)
		resolvedMode = tele.ModeMarkdownV2
	case tele.ModeMarkdownV2:
		text = replaceTemplatePlaceholdersMarkdownV2(template, structuralVars, plainVars)
	default:
		text = replaceTemplatePlaceholdersMarkdownV2(template, structuralVars, plainVars)
		resolvedMode = tele.ModeMarkdownV2
	}

	return RenderedTemplate{
		Text:      text,
		ParseMode: resolvedMode,
	}
}

func replaceTemplatePlaceholders(template string, structuralVars map[string]string, plainVars map[string]string, escapePlain func(string) string) string {
	// Substitute only placeholders in the original template, never placeholders
	// embedded in a user-provided value. Deterministic longest-first matching
	// also avoids map-order-dependent output when names overlap.
	values := make(map[string]string, len(structuralVars)+len(plainVars))
	for key, value := range plainVars {
		if key != "" {
			values[key] = escapePlain(value)
		}
	}
	for key, value := range structuralVars {
		if key != "" {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	pairs := make([]string, 0, 2*len(keys))
	for _, key := range keys {
		pairs = append(pairs, key, values[key])
	}
	return strings.NewReplacer(pairs...).Replace(template)
}

func replaceTemplatePlaceholdersMarkdownV2(template string, structuralVars map[string]string, plainVars map[string]string) string {
	return replaceTemplatePlaceholders(template, structuralVars, plainVars, EscapeMarkdownV2Literal)
}

// Plain user data must escape its own backslashes; a trailing backslash must
// not escape the template's closing delimiter or mutate its markup.
func EscapeMarkdownV2Literal(s string) string {
	var b strings.Builder
	for _, ch := range s {
		if ch == '\\' || isMarkdownV2Special(ch) {
			b.WriteByte('\\')
		}
		b.WriteRune(ch)
	}
	return b.String()
}

var markdownV2Specials = []string{"_", "*", "[", "]", "(", ")", "~", "`", ">", "#", "+", "-", "=", "|", "{", "}", ".", "!"}

func EscapeMarkdownV2Chars(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 2)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if ch == '\\' && i+1 < len(runes) {
			b.WriteRune(ch)
			i++
			b.WriteRune(runes[i])
			continue
		}
		if ch == '\x00' {
			b.WriteRune(ch)
			continue
		}
		need := false
		for _, sp := range markdownV2Specials {
			if string(ch) == sp {
				need = true
				break
			}
		}
		if need {
			b.WriteRune('\\')
		}
		b.WriteRune(ch)
	}
	return b.String()
}
