package bot

import (
	"html"
	"strings"

	tele "gopkg.in/telebot.v3"
)

type RenderedTemplate struct {
	Text      string
	ParseMode string
}

func RenderMessageTemplate(template string, parseMode string, structuralVars map[string]string, plainVars map[string]string) RenderedTemplate {
	resolvedMode := resolveParseMode(parseMode)

	var text string
	switch resolvedMode {
	case tele.ModeHTML:
		text = replaceTemplatePlaceholders(template, structuralVars, plainVars, html.EscapeString)
	case tele.ModeMarkdown:
		text = replaceTemplatePlaceholders(template, structuralVars, plainVars, func(s string) string { return s })
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
	return replaceTemplatePlaceholders(template, structuralVars, plainVars, mdv2EscapeChars)
}
