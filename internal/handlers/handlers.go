// Package handlers is the declarative registry of agent types ("handlers") that
// Dejima can run inside an island. It centralizes knowledge that was previously
// spread across three places: the launch command (image/start.sh's case block),
// the on-disk state dir (the daemon's agent-state mount target), and whether an
// agent exposes an attach surface (the session handler's headless guard).
//
// Adding a first-class agent type becomes a registry entry here plus an install
// line in the image — not a Go change scattered across packages. Richer handler
// metadata (host-credential mounts, event-hook wiring, workspace templates) is
// captured informally in docs/agent-adapters.md and folded in over later phases.
package handlers

import "sort"

// Kind distinguishes how an agent runs inside an island.
type Kind string

const (
	// KindInteractive agents own a tmux session and are attachable.
	KindInteractive Kind = "interactive"
	// KindHeadless agents run as a supervised process with no attach surface.
	KindHeadless Kind = "headless"
)

// Headless is the reserved agent type whose command is user-supplied
// (AgentSpec.Cmd) rather than baked into the image.
const Headless = "headless"

// Shell is a plain interactive terminal (a bash login shell) in the island — a
// scratch shell you type into, not an AI agent. Attachable; runs on /workspace.
const Shell = "shell"

// Handler is the declarative descriptor for one agent type.
type Handler struct {
	// ID matches Project/AgentSpec.Type and the image/agents/<id> shim dir.
	ID string
	// Kind drives attachability and how the supervisor launches the agent.
	Kind Kind
	// Launch is the command run inside the container. Empty for headless, whose
	// command comes from the user (AgentSpec.Cmd).
	Launch string
	// StateDir is the home-dir state path persisted across restarts (e.g.
	// ~/.claude). Informational now that the whole /home/dejima is persisted.
	StateDir string
	// RequiresProviderKey reports that this framework reaches an LLM over a
	// provider API key (vs OAuth-seeded agents like claude-code/codex). It drives
	// the provider/model picker, credential injection, and the proactive
	// "missing-provider-auth" health state. The LLM-credential subsystem is
	// opt-in per handler: false leaves an agent on its existing auth path.
	RequiresProviderKey bool
	// SupportedProviders is an advisory allow-list of provider ids this framework
	// understands (empty = any), used to populate the picker — not enforced.
	SupportedProviders []string
	// SuggestedModels are example "provider/model" strings shown as picker hints.
	// They are NOT applied as a default: the user must pick explicitly.
	SuggestedModels []string
	// GatewayPort is the in-container loopback port a channel framework serves a
	// web UI / HTTP API on (0 = none). Used by `dejima agent open` to forward and
	// open it. 0 means there is no localhost UI to open (e.g. a messaging-only
	// gateway).
	GatewayPort int
}

// Attachable reports whether clients can attach to this handler's agents.
func (h Handler) Attachable() bool { return h.Kind == KindInteractive }

// NeedsProviderKey reports whether this handler requires an LLM provider key.
func (h Handler) NeedsProviderKey() bool { return h.RequiresProviderKey }

// registry is the built-in handler set. Custom agents not listed here are
// treated as generic interactive agents (the image's start.sh `*)` fallback
// runs the type string as a command); see Lookup.
var registry = map[string]Handler{
	"claude-code": {ID: "claude-code", Kind: KindInteractive, Launch: "claude", StateDir: "/home/dejima/.claude"},
	"codex":       {ID: "codex", Kind: KindInteractive, Launch: "codex --sandbox-policy=no-sandbox", StateDir: "/home/dejima/.codex"},
	Shell:         {ID: Shell, Kind: KindInteractive, Launch: "bash -l", StateDir: "/home/dejima"},
	// OpenClaw: a first-class headless assistant. Self-installs on first launch
	// (kept out of the base image to avoid bloating every island) and runs its
	// gateway from /workspace — which should hold the brain's config (the Home
	// Island model).
	//   --allow-unconfigured lets the gateway idle (wait for config) instead of
	//     exiting when /workspace has no brain config yet.
	//   --bind loopback is required *inside a container*: OpenClaw detects the
	//     container, defaults to bind=auto (0.0.0.0) "for port-forwarding
	//     compatibility", and then REFUSES to start on a non-loopback bind
	//     without auth ("Refusing to bind gateway to auto without auth"). Without
	//     this the gateway exits immediately on a fresh, auth-less island. Binding
	//     loopback makes it self-generate a runtime token and come up `ready`;
	//     widening the bind is the operator's job once auth/config is in place.
	"openclaw": {ID: "openclaw", Kind: KindHeadless,
		Launch:              "bash -lc 'command -v openclaw >/dev/null 2>&1 || npm install -g openclaw; openclaw gateway --allow-unconfigured --bind loopback'",
		StateDir:            "/home/dejima/.openclaw",
		RequiresProviderKey: true,
		SupportedProviders:  []string{"anthropic", "openai", "google"},
		SuggestedModels:     []string{"anthropic/claude-sonnet-4-6", "openai/gpt-5.5"},
		GatewayPort:         18789},
	Headless: {ID: Headless, Kind: KindHeadless, Launch: "", StateDir: "/home/dejima/.agent-state"},
}

// Lookup returns the registered handler for an agent type. ok is false for
// unknown (custom) types; callers should treat those as generic interactive
// agents, matching the image's behavior.
func Lookup(agentType string) (h Handler, ok bool) {
	h, ok = registry[agentType]
	return h, ok
}

// Attachable reports whether an agent type exposes an attach surface. Unknown
// (custom) types are assumed interactive/attachable, matching the image, which
// runs them under tmux.
func Attachable(agentType string) bool {
	if h, ok := registry[agentType]; ok {
		return h.Attachable()
	}
	return true
}

// All returns every registered handler, sorted by ID — for capability discovery
// (GET /v1/agent-types) and clients that populate a provider/model picker.
func All() []Handler {
	out := make([]Handler, 0, len(registry))
	for _, h := range registry {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
