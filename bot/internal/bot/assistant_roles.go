package bot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openclaw/clawguard/internal/store"
)

type assistantRoleSet struct {
	Main     store.AssistantModelRoleConfig `json:"main"`
	Decision store.AssistantModelRoleConfig `json:"decision"`
	Vision   store.AssistantModelRoleConfig `json:"vision"`
	Compress store.AssistantModelRoleConfig `json:"compress"`
	Vector   store.AssistantModelRoleConfig `json:"vector"`
}

func defaultAssistantRole(name string) store.AssistantModelRoleConfig {
	cfg := store.AssistantModelRoleConfig{Fallbacks: []string{}, Temperature: 0.7, MaxTokens: 2048, TimeoutSec: 12}
	switch name {
	case "decision":
		cfg.Temperature, cfg.MaxTokens, cfg.TimeoutSec = 0.1, 512, 6
	case "vision":
		cfg.TimeoutSec = 15
	case "compress":
		cfg.Temperature, cfg.MaxTokens = 0.3, 1024
	case "vector":
		cfg.Temperature, cfg.MaxTokens, cfg.TimeoutSec = 0, 0, 10
	}
	return cfg
}

func parseAssistantRoleSet(raw []byte) assistantRoleSet {
	out := assistantRoleSet{
		Main: defaultAssistantRole("main"), Decision: defaultAssistantRole("decision"),
		Vision: defaultAssistantRole("vision"), Compress: defaultAssistantRole("compress"), Vector: defaultAssistantRole("vector"),
	}
	if len(raw) == 0 {
		return out
	}
	var decoded map[string]store.AssistantModelRoleConfig
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return out
	}
	if role, ok := decoded["main"]; ok {
		out.Main = normalizeAssistantRole(role, "main")
	}
	if role, ok := decoded["decision"]; ok {
		out.Decision = normalizeAssistantRole(role, "decision")
	}
	if role, ok := decoded["vision"]; ok {
		out.Vision = normalizeAssistantRole(role, "vision")
	}
	if role, ok := decoded["compress"]; ok {
		out.Compress = normalizeAssistantRole(role, "compress")
	}
	if role, ok := decoded["vector"]; ok {
		out.Vector = normalizeAssistantRole(role, "vector")
	}
	return out
}

func normalizeAssistantRole(role store.AssistantModelRoleConfig, name string) store.AssistantModelRoleConfig {
	base := defaultAssistantRole(name)
	role.ModelRef = strings.TrimSpace(role.ModelRef)
	if role.TimeoutSec <= 0 {
		role.TimeoutSec = base.TimeoutSec
	}
	if role.MaxTokens < 0 {
		role.MaxTokens = base.MaxTokens
	}
	if role.Fallbacks == nil {
		role.Fallbacks = []string{}
	}
	cleaned := make([]string, 0, len(role.Fallbacks))
	seen := map[string]struct{}{}
	for _, item := range role.Fallbacks {
		item = strings.TrimSpace(item)
		if item == "" || item == role.ModelRef {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		cleaned = append(cleaned, item)
	}
	role.Fallbacks = cleaned
	return role
}

func (roles assistantRoleSet) effective(name string) store.AssistantModelRoleConfig {
	var role store.AssistantModelRoleConfig
	switch name {
	case "main":
		return roles.Main
	case "decision":
		role = roles.Decision
	case "vision":
		role = roles.Vision
	case "compress":
		role = roles.Compress
	case "vector":
		role = roles.Vector
	}
	if strings.TrimSpace(role.ModelRef) == "" {
		inherited := roles.Main
		inherited.Fallbacks = append([]string(nil), roles.Main.Fallbacks...)
		if role.TimeoutSec > 0 {
			inherited.TimeoutSec = role.TimeoutSec
		}
		if role.MaxTokens > 0 {
			inherited.MaxTokens = role.MaxTokens
		}
		if name == "decision" && role.Temperature == 0 {
			inherited.Temperature = 0.1
		}
		return inherited
	}
	return role
}

func marshalAssistantRoleSet(roles assistantRoleSet) []byte {
	payload := map[string]store.AssistantModelRoleConfig{
		"main": roles.Main, "decision": roles.Decision, "vision": roles.Vision,
		"compress": roles.Compress, "vector": roles.Vector,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

func assistantRoleEndpointID(task, modelRef string) string {
	digest := sha256.Sum256([]byte(task + "\x00" + strings.TrimSpace(modelRef)))
	return fmt.Sprintf("role-%s-%x", task, digest[:6])
}

func applyAssistantRolesToPool(cfg AssistantPoolConfig, roles assistantRoleSet, policy store.GroupAssistantPolicy) AssistantPoolConfig {
	if cfg.Strategy == "" {
		cfg.Strategy = roles.Main.Strategy
		if cfg.Strategy == "" {
			cfg.Strategy = "primary-overflow"
		}
	}
	if cfg.TaskAssignments == nil {
		cfg.TaskAssignments = map[string]AssistantTaskAssignment{}
	}
	existing := make(map[string]struct{}, len(cfg.Endpoints))
	for _, ep := range cfg.Endpoints {
		existing[ep.ID] = struct{}{}
	}
	addRole := func(task string, role store.AssistantModelRoleConfig, requireTools bool) {
		// Legacy explicit assignments remain complete group overrides, never a
		// hidden partial merge with a newer global primary/fallback chain.
		if cfg.TaskAssignments[task].Primary != "" {
			return
		}
		refs := make([]string, 0, 1+len(role.Fallbacks))
		if strings.TrimSpace(role.ModelRef) != "" {
			refs = append(refs, strings.TrimSpace(role.ModelRef))
		}
		refs = append(refs, role.Fallbacks...)
		if len(refs) == 0 {
			return
		}
		ids := make([]string, 0, len(refs))
		timeoutMS := int(role.TimeoutSec * 1000)
		if timeoutMS < 1000 {
			timeoutMS = 6000
		}
		if timeoutMS > 120000 {
			timeoutMS = 120000
		}
		for i, ref := range refs {
			id := assistantRoleEndpointID(task, ref)
			if _, ok := existing[id]; !ok {
				roleName := "backup"
				if i == 0 {
					roleName = "primary"
				}
				opts := role.ModelOptions[ref]
				if opts.Weight == 0 {
					opts.Weight = 1
				}
				if opts.MaxConcurrency == 0 {
					opts.MaxConcurrency = 2
				}
				if opts.TimeoutMs == 0 {
					opts.TimeoutMs = timeoutMS
				}
				if opts.CooldownSeconds == 0 {
					opts.CooldownSeconds = 30
				}
				cfg.Endpoints = append(cfg.Endpoints, AssistantPoolEndpoint{
					ID: id, Name: ref, ModelRef: ref, Role: roleName, Priority: i, Weight: opts.Weight,
					MaxConcurrency: opts.MaxConcurrency, TimeoutMs: opts.TimeoutMs, CooldownSeconds: opts.CooldownSeconds,
				})
				existing[id] = struct{}{}
			}
			ids = append(ids, id)
		}
		assignment := cfg.TaskAssignments[task]
		if strings.TrimSpace(assignment.Primary) == "" {
			assignment.Primary = ids[0]
			assignment.Strategy = role.Strategy
			temperature := role.Temperature
			assignment.Temperature = &temperature
			assignment.MaxTokens = role.MaxTokens
			if len(ids) > 1 {
				assignment.Backups = append([]string(nil), ids[1:]...)
			}
			cfg.TaskAssignments[task] = assignment
		}
	}
	main := roles.effective("main")
	if strings.TrimSpace(policy.ChatModelRef) != "" {
		main.ModelRef = strings.TrimSpace(policy.ChatModelRef)
	}
	addRole("chat", main, true)
	for _, task := range []string{"decision", "vision", "compress", "vector"} {
		role := map[string]store.AssistantModelRoleConfig{"decision": roles.Decision, "vision": roles.Vision, "compress": roles.Compress, "vector": roles.Vector}[task]
		if role.ModelRef != "" {
			addRole(task, role, false)
		}
	}
	if strings.TrimSpace(policy.LearningModelRef) != "" {
		addRole("learning", store.AssistantModelRoleConfig{ModelRef: policy.LearningModelRef, TimeoutSec: 12, Fallbacks: []string{}}, false)
	}
	for _, task := range []string{"learning", "decision", "vision", "compress", "vector"} {
		if cfg.TaskAssignments[task].Primary == "" {
			child := cfg.TaskAssignments[task]
			if role, ok := map[string]store.AssistantModelRoleConfig{"decision": roles.Decision, "vision": roles.Vision, "compress": roles.Compress, "vector": roles.Vector}[task]; ok {
				if child.MaxTokens == 0 {
					child.MaxTokens = role.MaxTokens
				}
				if child.Temperature == nil {
					temp := role.Temperature
					child.Temperature = &temp
				}
				if child.Strategy == "" {
					child.Strategy = role.Strategy
				}
			}
			cfg.TaskAssignments[task] = inheritAssistantAssignment(child, cfg.TaskAssignments["chat"])
		}
	}
	return cfg
}

func (a *GroupAssistant) loadRuntimeSettings(ctx context.Context) store.AssistantGlobalSettings {
	if a == nil || a.queries == nil {
		return store.AssistantGlobalSettings{InboundMergeWindowSec: 5, ReplyTotalTimeoutSec: 45, DecisionContextItems: 5, MemoryRecallEnabled: true, TTSAudioFormat: "ogg_opus", TTSSampleRate: 48000, TTSEmotionScale: 4, TTSHTTPTimeoutSec: 20, TTSMaxTextLength: 500, TTSAPIBase: "https://openspeech.bytedance.com", TTSResourceID: "seed-tts-2.0", StickerFallbackFileIDs: []string{}}
	}
	settings, err := a.queries.GetAssistantGlobalSettings(ctx)
	if err != nil {
		if !errorsIsNoRows(err) {
			a.logger.Debug("load assistant global settings failed")
		}
		return store.AssistantGlobalSettings{InboundMergeWindowSec: 5, ReplyTotalTimeoutSec: 45, DecisionContextItems: 5, MemoryRecallEnabled: true, TTSAudioFormat: "ogg_opus", TTSSampleRate: 48000, TTSEmotionScale: 4, TTSHTTPTimeoutSec: 20, TTSMaxTextLength: 500, TTSAPIBase: "https://openspeech.bytedance.com", TTSResourceID: "seed-tts-2.0", StickerFallbackFileIDs: []string{}}
	}
	return settings
}

func errorsIsNoRows(err error) bool {
	return err != nil && (err == pgx.ErrNoRows || strings.Contains(err.Error(), "no rows"))
}

func assistantMergeWindow(settings store.AssistantGlobalSettings) time.Duration {
	if settings.InboundMergeWindowSec < 0 {
		return assistantReplyMergeWindow
	}
	return time.Duration(settings.InboundMergeWindowSec * float64(time.Second))
}

func assistantReplyTimeout(settings store.AssistantGlobalSettings) time.Duration {
	if settings.ReplyTotalTimeoutSec < 5 {
		return 45 * time.Second
	}
	return time.Duration(settings.ReplyTotalTimeoutSec * float64(time.Second))
}

func assistantDecisionHistoryLimit(settings store.AssistantGlobalSettings) int {
	if settings.DecisionContextItems < 0 {
		return 5
	}
	if settings.DecisionContextItems > 20 {
		return 20
	}
	return int(settings.DecisionContextItems)
}
