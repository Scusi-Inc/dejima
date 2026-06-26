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
