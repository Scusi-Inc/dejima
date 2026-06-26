package usage

import (
	"math"
	"testing"
)

func TestCostUSD_KnownModels(t *testing.T) {
	// 1M fresh input + 1M output on Opus = 15 + 75 = 90.
	got, ok := CostUSD("claude-opus-4-8", Tokens{Input: 1_000_000, Output: 1_000_000})
	if !ok {
		t.Fatal("opus should be priced")
	}
	if !approx(got, 90.0) {
		t.Errorf("opus cost = %v, want 90", got)
	}

	// Cache tiers: 1M cache-write = 15*1.25 = 18.75; 1M cache-read = 15*0.10 = 1.50.
	got, _ = CostUSD("claude-opus-4-8", Tokens{CacheCreation: 1_000_000, CacheRead: 1_000_000})
	if !approx(got, 18.75+1.50) {
		t.Errorf("opus cache cost = %v, want 20.25", got)
	}

	// Sonnet substring match with a version suffix.
	got, ok = CostUSD("claude-sonnet-4-6", Tokens{Input: 1_000_000, Output: 1_000_000})
	if !ok || !approx(got, 3+15) {
		t.Errorf("sonnet cost = %v ok=%v, want 18", got, ok)
	}
}

func TestCostUSD_UnknownModelIsNotFaked(t *testing.T) {
	if _, ok := CostUSD("some-other-llm", Tokens{Input: 1000, Output: 1000}); ok {
		t.Error("unknown model must return ok=false so cost renders n/a, not a fake number")
	}
	if _, ok := CostUSD("", Tokens{Input: 1000}); ok {
		t.Error("empty model must return ok=false")
	}
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
