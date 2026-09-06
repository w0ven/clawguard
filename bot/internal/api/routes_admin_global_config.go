package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/redact"
	"github.com/openclaw/clawguard/internal/store"
)

const (
	globalConfigSectionDocument = "document"
	globalConfigSectionPrompt   = "prompt"
	globalConfigSectionAdKiller = "adkiller"
	globalConfigSectionLLM      = "llm"
)

var (
	errGlobalConfigInvalidBody       = errors.New("invalid global config body")
	errGlobalConfigVersionRequired   = errors.New("global config version is required")
	errGlobalConfigVersionConflict   = errors.New("global config version conflict; reload current config, keep your draft, and retry")
	errGlobalConfigSectionUnknown    = errors.New("unknown global config section")
	errGlobalConfigSectionIncomplete = errors.New("global config section payload is incomplete")
)

type globalConfigPutRequest struct {
	Section string
	Version *int64
	Config  []byte
}

type globalConfigVersionConflictError struct {
	Current store.GlobalConfig
}

func (e *globalConfigVersionConflictError) Error() string {
	return errGlobalConfigVersionConflict.Error()
}

func (e *globalConfigVersionConflictError) Unwrap() error {
	return errGlobalConfigVersionConflict
}

type globalConfigValidationError struct {
	err error
}

func (e globalConfigValidationError) Error() string {
	if e.err == nil {
		return "invalid global config"
	}
	return e.err.Error()
}

func (e globalConfigValidationError) Unwrap() error {
	return e.err
}

func serializeGlobalConfig(item store.GlobalConfig) map[string]any {
	cfg := item.Config
	if len(cfg) == 0 {
		cfg = []byte("{}")
	}
	return map[string]any{
		"config":     json.RawMessage(cfg),
		"version":    item.Version,
		"updated_at": item.UpdatedAt,
	}
}

func parseGlobalConfigPutRequest(r io.Reader) (globalConfigPutRequest, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return globalConfigPutRequest{}, fmt.Errorf("%w: invalid json body", errGlobalConfigInvalidBody)
	}
	return parseGlobalConfigPutBody(raw)
}

func parseGlobalConfigPutBody(raw []byte) (globalConfigPutRequest, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return globalConfigPutRequest{}, fmt.Errorf("%w: invalid json body", errGlobalConfigInvalidBody)
	}
	_, hasSection := obj["section"]
	_, hasConfig := obj["config"]
	if !hasSection || !hasConfig {
		return globalConfigPutRequest{}, errGlobalConfigVersionRequired
	}

	var section string
	if err := json.Unmarshal(obj["section"], &section); err != nil {
		return globalConfigPutRequest{}, fmt.Errorf("%w: invalid section", errGlobalConfigInvalidBody)
	}
	section = strings.TrimSpace(section)
	switch section {
	case globalConfigSectionDocument, globalConfigSectionPrompt, globalConfigSectionAdKiller, globalConfigSectionLLM:
	default:
		return globalConfigPutRequest{}, fmt.Errorf("%w: %s", errGlobalConfigSectionUnknown, section)
	}

	version, err := parseOptionalGlobalConfigVersion(obj["version"])
	if err != nil {
		return globalConfigPutRequest{}, err
	}
	if section == globalConfigSectionDocument && version == nil {
		return globalConfigPutRequest{}, errGlobalConfigVersionRequired
	}

	config, err := normalizeGlobalConfigJSON(obj["config"])
	if err != nil {
		return globalConfigPutRequest{}, err
	}
	return globalConfigPutRequest{Section: section, Version: version, Config: config}, nil
}

func parseOptionalGlobalConfigVersion(raw json.RawMessage) (*int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return &n, nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil && f == float64(int64(f)) {
		v := int64(f)
		return &v, nil
	}
	return nil, fmt.Errorf("%w: invalid version", errGlobalConfigInvalidBody)
}

func normalizeGlobalConfigJSON(raw json.RawMessage) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return nil, fmt.Errorf("%w: config must be an object", errGlobalConfigInvalidBody)
	}
	sanitizeKeywordReplyRuntimeFields(payload)
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal json body", errGlobalConfigInvalidBody)
	}
	return out, nil
}

func applyGlobalConfigPut(current store.GlobalConfig, req globalConfigPutRequest) ([]byte, error) {
	switch req.Section {
	case globalConfigSectionDocument:
		return applyGlobalConfigDocument(current, req)
	case globalConfigSectionPrompt:
		return mergePromptSection(current.Config, req.Config)
	case globalConfigSectionAdKiller:
		return mergeAdKillerSection(current.Config, req.Config)
	case globalConfigSectionLLM:
		return mergeLLMSection(current.Config, req.Config)
	default:
		return nil, fmt.Errorf("%w: %s", errGlobalConfigSectionUnknown, req.Section)
	}
}

func applyGlobalConfigDocument(current store.GlobalConfig, req globalConfigPutRequest) ([]byte, error) {
	if req.Version == nil {
		return nil, errGlobalConfigVersionRequired
	}
	if *req.Version != current.Version {
		return nil, &globalConfigVersionConflictError{Current: current}
	}
	if err := rejectDangerousGlobalConfigTruncation(current.Config, req.Config); err != nil {
		return nil, err
	}
	return req.Config, nil
}

func mergePromptSection(currentRaw, patchRaw []byte) ([]byte, error) {
	current, err := jsonObjectOrEmpty(currentRaw)
	if err != nil {
		return nil, err
	}
	patch, err := jsonObjectOrEmpty(patchRaw)
	if err != nil {
		return nil, err
	}
	patchAI, ok := objectField(patch, "ai")
	if !ok {
		return nil, fmt.Errorf("%w: prompt section requires ai", errGlobalConfigSectionIncomplete)
	}
	currentAI := ensureObject(current, "ai")
	copied := false
	for _, key := range []string{"message_rules", "bio_rules", "custom_rules"} {
		if value, exists := patchAI[key]; exists {
			currentAI[key] = value
			copied = true
		}
	}
	if !copied {
		return nil, fmt.Errorf("%w: prompt section requires message_rules or bio_rules", errGlobalConfigSectionIncomplete)
	}
	migratePromptCustomRules(currentAI)
	return json.Marshal(current)
}

func migratePromptCustomRules(ai map[string]any) {
	if ai == nil {
		return
	}
	if promptRuleNonEmpty(ai["message_rules"]) && promptRuleNonEmpty(ai["bio_rules"]) {
		ai["custom_rules"] = ""
	}
}

func promptRuleNonEmpty(value any) bool {
	text, ok := value.(string)
	return ok && text != ""
}

func mergeAdKillerSection(currentRaw, patchRaw []byte) ([]byte, error) {
	current, err := jsonObjectOrEmpty(currentRaw)
	if err != nil {
		return nil, err
	}
	patch, err := jsonObjectOrEmpty(patchRaw)
	if err != nil {
		return nil, err
	}
	patchAI, ok := objectField(patch, "ai")
	if !ok {
		return nil, fmt.Errorf("%w: adkiller section requires ai.adkiller", errGlobalConfigSectionIncomplete)
	}
	adkiller, exists := patchAI["adkiller"]
	if !exists {
		return nil, fmt.Errorf("%w: adkiller section requires ai.adkiller", errGlobalConfigSectionIncomplete)
	}
	currentAI := ensureObject(current, "ai")
	currentAI["adkiller"] = adkiller
	return json.Marshal(current)
}

func mergeLLMSection(currentRaw, patchRaw []byte) ([]byte, error) {
	current, err := jsonObjectOrEmpty(currentRaw)
	if err != nil {
		return nil, err
	}
	patch, err := jsonObjectOrEmpty(patchRaw)
	if err != nil {
		return nil, err
	}
	patchAI, ok := objectField(patch, "ai")
	if !ok {
		return nil, fmt.Errorf("%w: llm section requires ai probe fields", errGlobalConfigSectionIncomplete)
	}
	currentAI := ensureObject(current, "ai")
	copied := false
	for _, key := range []string{"auto_degrade", "probe_enabled", "probe_interval_seconds"} {
		if value, exists := patchAI[key]; exists {
			currentAI[key] = value
			copied = true
		}
	}
	if !copied {
		return nil, fmt.Errorf("%w: llm section requires probe fields", errGlobalConfigSectionIncomplete)
	}
	return json.Marshal(current)
}

func jsonObjectOrEmpty(raw []byte) (map[string]any, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return map[string]any{}, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("%w: config must be an object", errGlobalConfigInvalidBody)
	}
	if obj == nil {
		obj = map[string]any{}
	}
	return obj, nil
}

func ensureObject(parent map[string]any, key string) map[string]any {
	if child, ok := objectField(parent, key); ok {
		return child
	}
	child := map[string]any{}
	parent[key] = child
	return child
}

func (s *Server) handlePutGlobalConfig(c echo.Context) error {
	admin, _ := currentAdmin(c)
	if !adminHasGlobalAccess(admin) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "global config out of scope"})
	}

	req, err := parseGlobalConfigPutRequest(c.Request().Body)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": redact.ErrorString(err)})
	}

	queries := s.botService.Queries()
	var (
		before  []byte
		updated store.GlobalConfig
	)
	err = queries.Transact(c.Request().Context(), func(tx *store.Queries) error {
		current, err := tx.GetGlobalConfigForUpdate(c.Request().Context())
		if err != nil {
			return err
		}
		before = append([]byte(nil), current.Config...)
		next, err := applyGlobalConfigPut(current, req)
		if err != nil {
			return err
		}
		if err := validateGlobalPolicyUpdate(c.Request().Context(), tx, next); err != nil {
			return globalConfigValidationError{err: err}
		}
		updated, err = tx.UpdateGlobalConfig(c.Request().Context(), store.UpdateGlobalConfigParams{Config: next})
		return err
	})
	if err != nil {
		return writeGlobalConfigPutError(c, err)
	}

	if err := s.writeAudit(c.Request().Context(), admin, "global", nil, "update_global_config", before, updated.Config); err != nil {
		s.logger.Warn("write global config audit failed")
	}
	if err := s.botService.RefreshAllGuardPolicySnapshots(c.Request().Context()); err != nil {
		s.logger.Warn("refresh guard policy snapshots after global config update failed", zap.Error(err))
	}

	return c.JSON(http.StatusOK, serializeGlobalConfig(updated))
}

func writeGlobalConfigPutError(c echo.Context, err error) error {
	var conflict *globalConfigVersionConflictError
	if errors.As(err, &conflict) {
		payload := serializeGlobalConfig(conflict.Current)
		payload["error"] = redact.ErrorString(errGlobalConfigVersionConflict)
		return c.JSON(http.StatusConflict, payload)
	}
	var validation globalConfigValidationError
	if errors.As(err, &validation) ||
		errors.Is(err, errDangerousGlobalConfigTruncation) ||
		errors.Is(err, errGlobalConfigVersionRequired) ||
		errors.Is(err, errGlobalConfigSectionUnknown) ||
		errors.Is(err, errGlobalConfigInvalidBody) ||
		errors.Is(err, errGlobalConfigSectionIncomplete) {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": redact.ErrorString(err)})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load global config failed"})
	}
	return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update global config failed"})
}
