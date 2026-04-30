package bot

import (
	"github.com/openclaw/clawguard/internal/messagefmt"
)

type RenderedTemplate = messagefmt.RenderedTemplate

func RenderMessageTemplate(template string, parseMode string, structuralVars map[string]string, plainVars map[string]string) RenderedTemplate {
	return messagefmt.RenderMessageTemplate(template, parseMode, structuralVars, plainVars)
}
