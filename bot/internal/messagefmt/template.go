package messagefmt

import (
	"html"
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
	rendered := template
	for key, value := range structuralVars {
		rendered = strings.ReplaceAll(rendered, key, value)
	}
	for key, value := range plainVars {
		rendered = strings.ReplaceAll(rendered, key, escapePlain(value))
	}
	return rendered
}

func replaceTemplatePlaceholdersMarkdownV2(template string, structuralVars map[string]string, plainVars map[string]string) string {
	return replaceTemplatePlaceholders(template, structuralVars, plainVars, EscapeMarkdownV2Chars)
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
