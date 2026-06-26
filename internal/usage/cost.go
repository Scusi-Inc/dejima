// Package usage computes the USD cost of an LLM token breakdown from a model
// price table. It is deliberately small and self-contained: the daemon can't
// see an agent's LLM calls (they're opaque outbound traffic), so token counts
// arrive from the agent's own usage hook and cost is derived here.
package usage

import "strings"

// Tokens is an Anthropic-shaped token breakdown for a session (cumulative).
// The fields mirror the `usage` block in a Claude Code transcript.
type Tokens struct {
	Input         int // fresh (uncached) input tokens
	CacheCreation int // tokens written to the prompt cache (billed > fresh input)
	CacheRead     int // tokens served from the prompt cache (billed << fresh input)
	Output        int // generated output tokens
}

// rate is a model family's per-million-token USD price for fresh input and
// output. Cache tiers derive from the input rate via Anthropic's standard
// multipliers below, so we only table the two base numbers per family.
type rate struct{ inPerM, outPerM float64 }

// Anthropic prompt-cache multipliers (relative to the fresh input rate): a
// 5-minute cache WRITE costs 1.25× input, a cache READ costs 0.10× input.
const (
	cacheWriteMult = 1.25
	cacheReadMult  = 0.10
	perMillion     = 1_000_000.0
)

// modelRates are Anthropic list prices (USD / million tokens) as of 2026-06.
//
// MAINTENANCE: list prices change — keep this dated and update it when they do.
// Matching is by lowercased SUBSTRING on the model id (so version/date suffixes
// like "claude-opus-4-8" still resolve to "opus"); first match wins, so order
// most-specific first if families ever overlap. A model that matches NOTHING
// returns ok=false from CostUSD, and the caller surfaces cost as "n/a" rather
// than a wrong number — we never fake a cost we can't actually compute.
var modelRates = []struct {
	match string
	rate  rate
}{
	{"opus", rate{inPerM: 15, outPerM: 75}},
	{"sonnet", rate{inPerM: 3, outPerM: 15}},
	{"haiku", rate{inPerM: 1, outPerM: 5}},
}

// CostUSD returns the dollar cost of t under model's price table. ok is false
// when the model isn't priced (the caller should render cost as n/a, keeping
// the token counts visible).
func CostUSD(model string, t Tokens) (cost float64, ok bool) {
	r, found := rateFor(model)
	if !found {
		return 0, false
	}
	cost = float64(t.Input)/perMillion*r.inPerM +
		float64(t.CacheCreation)/perMillion*r.inPerM*cacheWriteMult +
		float64(t.CacheRead)/perMillion*r.inPerM*cacheReadMult +
		float64(t.Output)/perMillion*r.outPerM
	return cost, true
}

func rateFor(model string) (rate, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return rate{}, false
	}
	for _, e := range modelRates {
		if strings.Contains(m, e.match) {
			return e.rate, true
		}
	}
	return rate{}, false
}
