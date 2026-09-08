package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/openclaw/clawguard/internal/store"
	"strings"
	"time"
)

// A synchronous tool phase has no chat HTTP call in flight. Only its nested
// embedding may borrow this already-counted slot, scoped to this owner/group.
// The parent lease remains pinned and unavailable to other turns throughout.
type assistantToolReservationKey struct{}
type assistantToolReservation struct {
	chatID int64
	lease  *assistantLease
}

func (a *GroupAssistant) ownsToolReservation(ctx context.Context, chatID int64, task string, rt *assistantEndpointRuntime) bool {
	owner, _ := ctx.Value(assistantToolReservationKey{}).(assistantToolReservation)
	if task != "vector" || owner.chatID != chatID || owner.lease == nil || owner.lease.a != a || owner.lease.released || owner.lease.runtime != rt {
		return false
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.active > 0 && !time.Now().Before(rt.cooldownUntil)
}

func (l *assistantLease) discardReservation() {
	if !l.borrowed {
		l.runtime.releaseCanceledReservation()
	}
}

// All selection/reservation (including primary mode) is serialized locally.
// Reservations are keyed by normalized registry model identity, never role/group.
func (a *GroupAssistant) reserveAssistantCandidate(ctx context.Context, chatID int64, task string, cfg AssistantPoolConfig, tools bool, ids []string) (*assistantLease, error) {
	if strategy := cfg.TaskAssignments[task].Strategy; strategy != "" {
		cfg.Strategy = strategy
	}
	a.scheduleMu.Lock()
	defer a.scheduleMu.Unlock()
	if err := assistantAcquireContextError(ctx, a.ctx); err != nil {
		return nil, err
	}
	type candidate struct {
		lease  *assistantLease
		weight int
	}
	available := []candidate{}
	seen := map[string]bool{}
	reason := "primary_overflow_concurrency_full"
	signature := fmt.Sprintf("%d:%s", chatID, task)
	for idx, id := range ids {
		ep, found := endpointByID(cfg, id)
		if !found {
			continue
		}
		signature += fmt.Sprintf("|%s:%d", id, ep.Weight)
		ref, parsed := normalizeAssistantModelRef(ep.ModelRef)
		if !parsed || a.models == nil || a.providers == nil {
			continue
		}
		if seen[ref.String()] {
			continue
		}
		seen[ref.String()] = true
		model, exists := a.models.Get(ref)
		if !exists || !assistantModelCapable(model, task, tools) {
			if idx == 0 {
				reason = "primary_overflow_disabled_or_incompatible"
			}
			continue
		}
		provider, _, _ := ref.Parse()
		if p, ok := a.providers.GetByKey(provider); !ok || !p.Enabled {
			if idx == 0 {
				reason = "primary_overflow_provider_disabled"
			}
			continue
		}
		limit := ep.MaxConcurrency
		for _, other := range cfg.Endpoints {
			r, ok := normalizeAssistantModelRef(other.ModelRef)
			if ok && r == ref && other.MaxConcurrency > 0 && (limit < 1 || other.MaxConcurrency < limit) {
				limit = other.MaxConcurrency
			}
		}
		limit = a.effectiveEndpointLimit(ctx, ref.String(), limit)
		rt := a.endpointRuntime(ref.String(), limit)
		rt.setLimit(limit)
		borrowed := a.ownsToolReservation(ctx, chatID, task, rt)
		if !borrowed && !rt.tryAcquire(time.Now()) {
			if idx == 0 && rt.statusValue() == "cooldown" {
				reason = "primary_overflow_cooldown"
			}
			continue
		}
		selectedReason := "primary_selected"
		if idx > 0 {
			selectedReason = reason
		}
		lease := &assistantLease{a: a, ep: ep, ref: ref, runtime: rt, reason: selectedReason, borrowed: borrowed}
		if cfg.Strategy != "weighted" {
			if err := assistantAcquireContextError(ctx, a.ctx); err != nil {
				lease.discardReservation()
				return nil, err
			}
			return lease, nil
		}
		weight := ep.Weight
		if weight < 1 {
			weight = 1
		}
		available = append(available, candidate{lease: lease, weight: weight})
	}
	if len(available) == 0 {
		return nil, nil
	}
	if err := assistantAcquireContextError(ctx, a.ctx); err != nil {
		for _, c := range available {
			c.lease.discardReservation()
		}
		return nil, err
	}
	if a.weights == nil {
		a.weights = map[string]map[string]int{}
	}
	currents := a.weights[signature]
	if currents == nil {
		currents = map[string]int{}
		a.weights[signature] = currents
	}
	best, total := 0, 0
	for i, c := range available {
		key := c.lease.ref.String()
		currents[key] += c.weight
		total += c.weight
		if currents[key] > currents[available[best].lease.ref.String()] {
			best = i
		}
	}
	selected := available[best].lease
	currents[selected.ref.String()] -= total
	selected.reason = "weighted_selected"
	for i, c := range available {
		if i != best {
			c.lease.discardReservation()
		}
	}
	return selected, nil
}

func (a *AssistantTaskAssignment) UnmarshalJSON(raw []byte) error {
	// goose32 default rows represented empty task assignments as strings.
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "\"") {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		a.Primary = s
		a.Backups = []string{}
		return nil
	}
	type plain AssistantTaskAssignment
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	return decoder.Decode((*plain)(a))
}

// For inherited pools there is intentionally no second policy model route.
func assistantPoolPolicy(cfg AssistantPoolConfig, policy store.GroupAssistantPolicy) store.GroupAssistantPolicy {
	if cfg.InheritGlobal != nil && *cfg.InheritGlobal {
		policy.ChatModelRef = ""
		policy.LearningModelRef = ""
	}
	return policy
}
