package api

import (
	"testing"

	"github.com/openclaw/clawguard/internal/store"
)

func TestSummarizeAICallsByCurrentModelsCollapsesHistoricalModels(t *testing.T) {
	perModel := []store.AICallPerModel{
		{Model: "sub2api:gpt-5.5", Calls: 7},
		{Model: "aw:gpt-5.4", Calls: 5},
		{Model: "", Calls: 2},
		{Model: "current:gpt-5", Calls: 11},
		{Model: "current/gpt-5", Calls: 3},
	}
	currentRefs := map[string]struct{}{
		"current:gpt-5": {},
	}

	got := summarizeAICallsByCurrentModels(perModel, currentRefs)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (%+v)", len(got), got)
	}
	if got[0].Model != "current:gpt-5" || got[0].Calls != 14 || !got[0].Current || got[0].Historical {
		t.Fatalf("current row = %+v, want current:gpt-5 current row", got[0])
	}
	if got[1].Model != historicalAICallModelLabel || got[1].Calls != 14 || got[1].Current || !got[1].Historical {
		t.Fatalf("historical row = %+v, want collapsed historical calls", got[1])
	}
}
