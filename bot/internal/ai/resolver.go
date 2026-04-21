package ai

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

// StatsReader reads current health snapshots for the resolver. Implementations
// must be safe for concurrent reads. The resolver caches results for a short
// window to keep moderation hot-path cheap.
type StatsReader interface {
	ListLLMModelStats(ctx context.Context) ([]store.LlmModelStat, error)
}

type Resolver struct {
	models ModelRegistry
	stats  StatsReader

	mu           sync.RWMutex
	healthByID   map[int64]bool
	loadedAt     time.Time
	cacheTTL     time.Duration
}

func NewResolver(models ModelRegistry) *Resolver {
	return &Resolver{
		models:   models,
		cacheTTL: 5 * time.Second,
	}
}

// WithStats wires the stats reader used for auto-degrade. When not set, the
// resolver behaves exactly as in PR1 (no health filtering).
func (r *Resolver) WithStats(stats StatsReader) *Resolver {
	r.stats = stats
	return r
}

// snapshot returns the latest healthByID map, refreshing from the store at
// most once per cacheTTL window. Callers that want a completely fresh read can
// use Refresh(ctx).
func (r *Resolver) snapshot(ctx context.Context) map[int64]bool {
	if r.stats == nil {
		return nil
	}
	r.mu.RLock()
	fresh := time.Since(r.loadedAt) < r.cacheTTL && r.healthByID != nil
	if fresh {
		out := r.healthByID
		r.mu.RUnlock()
		return out
	}
	r.mu.RUnlock()

	// slow path: refresh
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.loadedAt) < r.cacheTTL && r.healthByID != nil {
		return r.healthByID
	}
	rows, err := r.stats.ListLLMModelStats(ctx)
	if err != nil {
		// Preserve the previous snapshot on error rather than losing all
		// health info. If we have no prior snapshot, return nil so the
		// resolver skips filtering.
		return r.healthByID
	}
	next := make(map[int64]bool, len(rows))
	for _, row := range rows {
		next[row.ModelID] = row.Healthy
	}
	r.healthByID = next
	r.loadedAt = time.Now()
	return next
}

// Refresh forces an immediate stats reload (ignores cache). Used by prober
// after it writes a transition to make sure the next moderation call sees the
// new state.
func (r *Resolver) Refresh(ctx context.Context) {
	if r.stats == nil {
		return
	}
	r.mu.Lock()
	r.loadedAt = time.Time{}
	r.mu.Unlock()
	_ = r.snapshot(ctx)
}

func (r *Resolver) BuildChain(policy config.AIPolicy, caps []string) ([]ModelRef, error) {
	requiredCaps := append([]string(nil), policy.CapabilityRequirements...)
	requiredCaps = append(requiredCaps, caps...)

	rawRefs := make([]ModelRef, 0, 1+len(policy.FallbackModelRefs)+len(policy.FallbackChain))
	if ref := normalizePolicyRef(policy.PrimaryModelRef); ref != "" {
		rawRefs = append(rawRefs, ref)
	} else if ref := NewModelRef(policy.PrimaryProvider, policy.PrimaryModel); ref != "" {
		rawRefs = append(rawRefs, ref)
	}
	for _, item := range policy.FallbackModelRefs {
		if ref := normalizePolicyRef(item); ref != "" {
			rawRefs = append(rawRefs, ref)
		}
	}
	for _, item := range policy.FallbackChain {
		if ref := normalizePolicyRef(item); ref != "" {
			rawRefs = append(rawRefs, ref)
		}
	}

	seen := map[ModelRef]struct{}{}
	chain := make([]ModelRef, 0, len(rawRefs))
	// First pass: enabled + capability filter (no health).
	type candidate struct {
		ref     ModelRef
		modelID int64
	}
	candidates := make([]candidate, 0, len(rawRefs))
	for _, ref := range rawRefs {
		if _, ok := seen[ref]; ok {
			continue
		}
		model, ok := r.models.Get(ref)
		if !ok || !model.Enabled || !hasCapabilities(model.CapabilityTags, requiredCaps) {
			continue
		}
		seen[ref] = struct{}{}
		candidates = append(candidates, candidate{ref: ref, modelID: model.ID})
	}
	if len(rawRefs) > 0 && len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled models available for policy chain")
	}

	// Second pass: optionally drop unhealthy when auto_degrade is on.
	// If every candidate is unhealthy, fall back to the full chain so
	// moderation keeps trying rather than throwing errors.
	if policy.AutoDegrade && r.stats != nil {
		health := r.snapshot(context.Background())
		if len(health) > 0 {
			healthy := make([]ModelRef, 0, len(candidates))
			for _, c := range candidates {
				// Unknown model id (never probed) is treated as healthy.
				if h, ok := health[c.modelID]; ok && !h {
					continue
				}
				healthy = append(healthy, c.ref)
			}
			if len(healthy) > 0 {
				return healthy, nil
			}
			// All unhealthy: degrade gracefully to full chain.
		}
	}

	for _, c := range candidates {
		chain = append(chain, c.ref)
	}
	return chain, nil
}

func normalizePolicyRef(raw string) ModelRef {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if provider, model, ok := ModelRef(raw).Parse(); ok {
		return NewModelRef(provider, model)
	}
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 {
		return ""
	}
	return NewModelRef(parts[0], parts[1])
}
