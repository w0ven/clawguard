package bot

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/openclaw/clawguard/internal/store"
	"testing"
)

func TestNativeMigrationRejectsChangedProfileOrTarget(t *testing.T) {
	hash := sha256.Sum256([]byte("latest model profile"))
	expected := nativeMigrationProfile{PolicyVersion: 10, ProfileHash: hex.EncodeToString(hash[:]), Target: 258605875, SampleCount: 14, DistilledCount: 14}
	current := store.GroupAssistantPolicy{Version: 10, MimicProfileText: "latest model profile", MimicTargetUserID: 258605875, MimicSampleCount: 14, MimicDistilledAtCount: 14}
	if !nativeMigrationProfileMatches(expected, current) {
		t.Fatal("matching latest state rejected")
	}
	for _, mutate := range []func(*store.GroupAssistantPolicy){
		func(p *store.GroupAssistantPolicy) { p.Version = 11 },
		func(p *store.GroupAssistantPolicy) { p.MimicProfileText = "" },
		func(p *store.GroupAssistantPolicy) { p.MimicTargetUserID = 1 },
		func(p *store.GroupAssistantPolicy) { p.MimicSampleCount = 15 },
		func(p *store.GroupAssistantPolicy) { p.MimicDistilledAtCount = 0 },
	} {
		copy := current
		mutate(&copy)
		if nativeMigrationProfileMatches(expected, copy) {
			t.Fatal("changed live state accepted")
		}
	}
}
