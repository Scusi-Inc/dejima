// Package localmodel is the host-side brain for Dejima's managed local-model
// support: a curated, hardware-aware catalog of open-weights models plus the
// backend-detection glue that lets islands drive them as a shared inference
// service. It deliberately does NOT reimplement a model registry — the pull /
// serve / lifecycle is delegated to a backend (Ollama by default); this package
// is the curation + recommendation + detection layer Dejima adds on top.
//
// See docs/local-models.md for the design of record.
package localmodel

import "sort"

// Backend identifies a local inference-server implementation. Ollama is the
// default; the set is open so vLLM (throughput, Linux/GPU) and LM Studio can
// slot in behind the same LocalBackend interface.
type Backend string

const (
	BackendOllama   Backend = "ollama"
	BackendVLLM     Backend = "vllm"
	BackendLMStudio Backend = "lmstudio"
)

// DefaultBackend is what a bare `dejima local install` provisions.
const DefaultBackend = BackendOllama

// Model is one curated open-weights model Dejima knows how to recommend and
// pull. Ref is the backend-native pull reference (an Ollama tag today); Alias is
// the short, backend-agnostic handle users type (`dejima local pull qwen-coder`).
type Model struct {
	Alias     string `json:"alias"`       // dejima handle, e.g. "qwen-coder"
	Ref       string `json:"ref"`         // backend pull ref, e.g. "qwen2.5-coder:32b-instruct-q4_K_M"
	Params    string `json:"params"`      // human parameter size, e.g. "32B"
	MinRAMGiB int    `json:"min_ram_gib"` // host RAM (GiB) to run it comfortably (weights + KV headroom)
	Coding    bool   `json:"coding"`      // suited to agentic / coding work
	Note      string `json:"note"`        // one-line description
}

// Catalog is the curated set — deliberately small, refreshed in code over time
// rather than mirroring a full model registry. Ordered small→large so callers
// can scan for the biggest that fits. MinRAMGiB is the *total host RAM* to run
// the model comfortably at the referenced quantization, leaving room for the OS
// and a working set — not just the weight footprint.
var Catalog = []Model{
	{Alias: "qwen-coder-3b", Ref: "qwen2.5-coder:3b-instruct-q4_K_M", Params: "3B", MinRAMGiB: 8, Coding: true,
		Note: "Smallest coding model — runs on laptops; light autocomplete/edits."},
	{Alias: "qwen-coder-7b", Ref: "qwen2.5-coder:7b-instruct-q4_K_M", Params: "7B", MinRAMGiB: 16, Coding: true,
		Note: "Solid small coder; good default for 16 GB machines."},
	{Alias: "mistral-small", Ref: "mistral-small:24b-instruct-2501-q4_K_M", Params: "24B", MinRAMGiB: 32, Coding: true,
		Note: "Strong general + coding model at a mid footprint."},
	{Alias: "qwen-coder", Ref: "qwen2.5-coder:32b-instruct-q4_K_M", Params: "32B", MinRAMGiB: 36, Coding: true,
		Note: "Best open coder that fits a workstation; the recommended default."},
	{Alias: "llama-70b", Ref: "llama3.3:70b-instruct-q4_K_M", Params: "70B", MinRAMGiB: 48, Coding: true,
		Note: "Large general model; strong reasoning, heavier to run (fits a 64 GB box)."},
	{Alias: "kimi-k2", Ref: "kimi-k2:q4_K_M", Params: "~1T MoE", MinRAMGiB: 256, Coding: true,
		Note: "Frontier-class open MoE — needs a big rig / server, not a laptop."},
}

// Lookup resolves a user-typed handle to a catalog model. It matches the Alias
// first, then the full Ref (so `dejima local pull qwen2.5-coder:32b-...` also
// works). ok is false for anything not in the curated set — callers may still
// pass an arbitrary Ref straight to the backend, but it won't be size-checked.
func Lookup(handle string) (Model, bool) {
	for _, m := range Catalog {
		if m.Alias == handle || m.Ref == handle {
			return m, true
		}
	}
	return Model{}, false
}

// Recommendation is the host-aware answer to "what should I run here?": the
// models that fit this machine (largest-first) and a single top pick.
type Recommendation struct {
	HostRAMGiB int     `json:"host_ram_gib"`
	Fits       []Model `json:"fits"` // catalog models that fit, largest-first
	Top        *Model  `json:"top"`  // the recommended default (nil if nothing fits)
}

// RecommendFor curates the catalog for a host with ramGiB total RAM. A model
// fits when ramGiB meets its MinRAMGiB (which already folds in OS + working-set
// headroom). Top is the largest coding-capable model that fits — the best local
// coder this box can run.
func RecommendFor(ramGiB int) Recommendation {
	var fits []Model
	for _, m := range Catalog {
		if m.MinRAMGiB <= ramGiB {
			fits = append(fits, m)
		}
	}
	// Largest-first so the UI leads with the most capable option.
	sort.SliceStable(fits, func(i, j int) bool { return fits[i].MinRAMGiB > fits[j].MinRAMGiB })

	rec := Recommendation{HostRAMGiB: ramGiB, Fits: fits}
	for i := range fits {
		if fits[i].Coding {
			rec.Top = &fits[i]
			break
		}
	}
	return rec
}
