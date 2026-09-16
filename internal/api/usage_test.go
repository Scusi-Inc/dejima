package api

import (
	"testing"
	"time"

	"github.com/aoos/dejima/internal/events"
)

func TestAgentUsageFromPayload(t *testing.T) {
	ts := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	u, ok := agentUsageFromPayload(map[string]any{
		"input_tokens":                float64(1000),
		"cache_creation_input_tokens": float64(500),
		"cache_read_input_tokens":     float64(2000),
		"output_tokens":               float64(300),
		"model":                       "claude-opus-4-8",
		"source":                      "claude-code",
	}, ts)
	if !ok {
		t.Fatal("expected ok")
	}
	// InputTokens aggregates fresh + cache so input+output == total.
	if u.InputTokens != 3500 || u.OutputTokens != 300 || u.TotalTokens != 3800 {
		t.Errorf("tokens = in %d out %d total %d, want 3500/300/3800", u.InputTokens, u.OutputTokens, u.TotalTokens)
	}
	if u.InputTokens+u.OutputTokens != u.TotalTokens {
		t.Error("invariant input+output==total broken")
	}
	if u.Source != "claude-code" || !u.AsOf.Equal(ts) {
		t.Errorf("source/as_of = %q/%v", u.Source, u.AsOf)
	}
	if u.CostUSD == nil {
		t.Fatal("opus is priced — cost should be set")
	}
	// 1000*15 + 500*15*1.25 + 2000*15*0.10 + 300*75, all /1e6.
	want := (1000*15 + 500*15*1.25 + 2000*15*0.10 + 300*75) / 1_000_000.0
	if *u.CostUSD < want-1e-9 || *u.CostUSD > want+1e-9 {
		t.Errorf("cost = %v, want %v", *u.CostUSD, want)
	}
}

func TestAgentUsageFromPayload_UnknownModelNoCost(t *testing.T) {
	u, ok := agentUsageFromPayload(map[string]any{
		"input_tokens": float64(10), "output_tokens": float64(5), "model": "mystery-llm",
	}, time.Now())
	if !ok {
		t.Fatal("expected ok (tokens present)")
	}
	if u.CostUSD != nil {
		t.Error("unknown model must leave cost nil (n/a), not fake a number")
	}
	if u.TotalTokens != 15 {
		t.Errorf("total = %d, want 15", u.TotalTokens)
	}
}

func TestAgentUsageFromPayload_EmptyIsIgnored(t *testing.T) {
	if _, ok := agentUsageFromPayload(nil, time.Now()); ok {
		t.Error("nil payload should be ignored")
	}
	if _, ok := agentUsageFromPayload(map[string]any{"model": "claude-opus"}, time.Now()); ok {
		t.Error("all-zero tokens should be ignored (don't clobber a real snapshot)")
	}
}

func TestMaybeUpdateAgentUsage_RoundTrip(t *testing.T) {
	s := &Server{agentUsage: map[string]AgentUsage{}}
	s.maybeUpdateAgentUsage(events.Event{
		Type: events.TypeAgentUsage, Island: "isl", Agent: "a1",
		Timestamp: time.Now().UTC(),
		Payload:   map[string]any{"input_tokens": float64(7), "output_tokens": float64(3), "model": "claude-haiku-4-5"},
	})
	got := s.agentUsageOf("isl", "a1")
	if got == nil {
		t.Fatal("usage not stored")
	}
	if got.TotalTokens != 10 {
		t.Errorf("total = %d, want 10", got.TotalTokens)
	}
	// A non-usage event must not touch the usage map.
	s.maybeUpdateAgentUsage(events.Event{Type: events.TypeAgentTaskComplete, Island: "isl", Agent: "a2"})
	if s.agentUsageOf("isl", "a2") != nil {
		t.Error("non-usage event should not create a usage entry")
	}
}

// The model id arrived with every usage report from the day the hook was
// written — agentUsageFromPayload has always read it, to price the turn — and
// then went nowhere, because AgentUsage had no field to put it in. Nothing was
// missing upstream; the value was being computed with and discarded.
//
// Pinned for both a priced and an UNPRICED model: cost is the only thing that
// ever consumed this value, so a model we cannot price is exactly where a
// "derive it from the cost" shortcut would silently lose it again.
func TestAgentUsageCarriesTheReportedModel(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model string
	}{
		{"a priced model", "claude-opus-4-8"},
		{"a model with no price table entry", "mystery-llm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, ok := agentUsageFromPayload(map[string]any{
				"input_tokens": float64(10), "output_tokens": float64(5),
				"model": tc.model, "source": "claude-code",
			}, time.Now())
			if !ok {
				t.Fatal("expected ok (tokens present)")
			}
			if u.Model != tc.model {
				t.Errorf("Model = %q, want %q — the TUI has no other source for "+
					"which model is actually answering", u.Model, tc.model)
			}
		})
	}

	// An adapter that reports tokens but no model must not invent one. A blank
	// here renders as the framework name, which is honest; a placeholder would
	// read as a fact.
	u, ok := agentUsageFromPayload(map[string]any{
		"input_tokens": float64(10), "output_tokens": float64(5), "source": "codex",
	}, time.Now())
	if !ok {
		t.Fatal("expected ok (tokens present)")
	}
	if u.Model != "" {
		t.Errorf("Model = %q, want empty — nothing reported a model", u.Model)
	}
}
