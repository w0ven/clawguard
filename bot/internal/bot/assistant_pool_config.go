package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
	"strings"
)

// EffectivePool is the single read path for execution, readiness and admin UI.
// nil inherit_global is a legacy explicit pool; no row is global inheritance.
// Returning an effective projection never rewrites legacy data.
func (a *GroupAssistant) EffectivePool(ctx context.Context, chatID int64) (AssistantPoolConfig, string, error) {
	stored, err := a.Pool(ctx, chatID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return AssistantPoolConfig{}, "", err
	}
	var cfg AssistantPoolConfig
	if err == nil {
		cfg, err = decodeAssistantPool(stored.Config)
		if err != nil {
			return cfg, "", err
		}
	}
	roles := parseAssistantRoleSet(a.loadRuntimeSettings(ctx).ModelRoles)
	policy, policyErr := a.Policy(ctx, chatID)
	if policyErr != nil && !errors.Is(policyErr, pgx.ErrNoRows) {
		return cfg, "", policyErr
	}
	if (cfg.InheritGlobal != nil && *cfg.InheritGlobal) || (len(cfg.Endpoints) == 0 && policy.ChatModelRef == "" && policy.LearningModelRef == "") {
		inherit := true
		cfg = applyAssistantRolesToPool(AssistantPoolConfig{InheritGlobal: &inherit}, roles, store.GroupAssistantPolicy{})
		return cfg, "global", nil
	}
	if cfg.InheritGlobal == nil {
		if cfg.Strategy == "" {
			cfg.Strategy = "primary-overflow"
		}
		return applyAssistantRolesToPool(cfg, roles, policy), "legacy_group", nil
	}
	// Modern explicit pools own their task routes; empty subroles inherit main.
	if cfg.TaskAssignments == nil {
		cfg.TaskAssignments = map[string]AssistantTaskAssignment{}
	}
	for _, task := range []string{"learning", "decision", "vision", "compress", "vector"} {
		if cfg.TaskAssignments[task].Primary == "" {
			cfg.TaskAssignments[task] = inheritAssistantAssignment(cfg.TaskAssignments[task], cfg.TaskAssignments["chat"])
		}
	}
	return cfg, "group", nil
}
func inheritAssistantAssignment(child, parent AssistantTaskAssignment) AssistantTaskAssignment {
	child.Inherited = true
	child.Primary, child.Backups = parent.Primary, append([]string(nil), parent.Backups...)
	if child.Strategy == "" {
		child.Strategy = parent.Strategy
	}
	if child.Temperature == nil {
		child.Temperature = parent.Temperature
	}
	if child.MaxTokens == 0 {
		child.MaxTokens = parent.MaxTokens
	}
	return child
}

func (a *GroupAssistant) GlobalPool(raw []byte) AssistantPoolConfig {
	return applyAssistantRolesToPool(AssistantPoolConfig{}, parseAssistantRoleSet(raw), store.GroupAssistantPolicy{})
}
func (a *GroupAssistant) ValidateRoleConfigs(roles map[string]store.AssistantModelRoleConfig) error {
	known := map[string]string{"main": "chat", "decision": "decision", "vision": "vision", "compress": "compress", "vector": "vector"}
	for name, role := range roles {
		task, ok := known[name]
		if !ok {
			return fmt.Errorf("未知助手角色：%s", name)
		}
		if role.Strategy != "" && role.Strategy != "primary-overflow" && role.Strategy != "weighted" {
			return fmt.Errorf("无效负载策略")
		}
		if role.TimeoutSec < 0 || role.TimeoutSec > 120 || role.Temperature < 0 || role.Temperature > 2 || role.MaxTokens < 0 || role.MaxTokens > 32768 {
			return fmt.Errorf("角色参数超出范围")
		}
		if strings.TrimSpace(role.ModelRef) == "" && len(role.Fallbacks) > 0 {
			return fmt.Errorf("请先选择%s主模型，再添加备用", name)
		}
		if len(role.Fallbacks) > 15 {
			return fmt.Errorf("每角色最多16个模型")
		}
		seen := map[string]bool{}
		for _, raw := range append([]string{role.ModelRef}, role.Fallbacks...) {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			ref, ok := normalizeAssistantModelRef(raw)
			if !ok {
				return fmt.Errorf("模型引用无效")
			}
			if seen[ref.String()] {
				return fmt.Errorf("同一角色不能重复引用模型")
			}
			seen[ref.String()] = true
			if err := a.ValidateTaskModelRef(raw, task); err != nil {
				return err
			}
		}
		for raw, o := range role.ModelOptions {
			if _, ok := normalizeAssistantModelRef(raw); !ok {
				return fmt.Errorf("模型参数引用无效")
			}
			if o.Weight < 0 || o.Weight > 1000 || o.MaxConcurrency < 0 || o.MaxConcurrency > 100 || o.TimeoutMs < 0 || o.TimeoutMs > 120000 || (o.TimeoutMs > 0 && o.TimeoutMs < 1000) || o.CooldownSeconds < 0 || o.CooldownSeconds > 3600 {
				return fmt.Errorf("权重、并发、超时或冷却超出范围")
			}
		}
	}
	return nil
}
func assistantModelCapable(model ai.Model, task string, tools bool) bool {
	if !model.Enabled || (tools && !model.SupportsTools) {
		return false
	}
	if task == "vision" && !model.SupportsVision {
		return false
	}
	if task == "vector" {
		for _, tag := range model.CapabilityTags {
			if strings.EqualFold(tag, "embedding") || strings.EqualFold(tag, "embeddings") {
				return true
			}
		}
		return false
	}
	return true
}
func (a *GroupAssistant) ValidateTaskModelRef(raw, task string) error {
	if err := a.ValidateModelRef(raw, task == "chat"); err != nil {
		return fmt.Errorf("模型不可用于%s：%w", task, err)
	}
	ref, _ := normalizeAssistantModelRef(raw)
	model, _ := a.models.Get(ref)
	if !assistantModelCapable(model, task, task == "chat") {
		return fmt.Errorf("模型 %s 未声明%s能力，请在现有模型管理核实能力后选择", raw, map[string]string{"vision": "视觉", "vector": "embedding"}[task])
	}
	return nil
}
func EncodeAssistantPool(cfg AssistantPoolConfig) []byte { raw, _ := json.Marshal(cfg); return raw }
