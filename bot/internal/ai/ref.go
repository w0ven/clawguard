package ai

import "strings"

type ModelRef string

func NewModelRef(provider, model string) ModelRef {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return ModelRef("")
	}
	return ModelRef(provider + ":" + model)
}

func (r ModelRef) Parse() (provider, model string, ok bool) {
	raw := strings.TrimSpace(string(r))
	if raw == "" {
		return "", "", false
	}
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	provider = strings.TrimSpace(parts[0])
	model = strings.TrimSpace(parts[1])
	if provider == "" || model == "" {
		return "", "", false
	}
	return provider, model, true
}

func (r ModelRef) String() string {
	return string(r)
}
