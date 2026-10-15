package api

import (
	"strings"

	"github.com/aoos/dejima/internal/localmodel"
)

// launchModel is the model string an agent is launched with: the operator's
// choice, except that a curated local alias becomes the backend's own name.
//
// `dejima local pull qwen-coder-7b` stores the model in Ollama under its Ref,
// qwen2.5-coder:7b-instruct-q4_K_M. Nothing creates an Ollama entry called
// qwen-coder-7b. Yet the alias is what every surface offers: the catalog, the
// Local models page, and aider's own SuggestedModels ("local/qwen-coder"). The
// shims strip the provider prefix and hand the rest to the backend
// (aider: --model openai/${DEJIMA_MODEL#*/}; codex: model = "${DEJIMA_MODEL#*/}"),
// so an agent given the suggested model asked Ollama for a model it never had,
// and failed for a reason that had nothing to do with local models working.
//
// Resolved here, at launch, rather than when the agent is configured, so the
// stored choice stays the readable alias and a catalog change reaches existing
// agents on their next start. Anything not in the curated set passes through
// untouched: an operator who typed a raw Ollama tag meant exactly that tag.
func launchModel(provider, model string) string {
	const prefix = "local/"
	rest, hasPrefix := strings.CutPrefix(model, prefix)
	if !hasPrefix && strings.TrimSpace(provider) != "local" {
		return model
	}
	m, ok := localmodel.Lookup(rest)
	if !ok {
		return model
	}
	if hasPrefix {
		return prefix + m.Ref
	}
	return m.Ref
}
