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
	// DashboardTokenCmd, when set, is a command run INSIDE the container that prints
	// the framework's gateway auth token to stdout. `dejima agent open` runs it over
	// the façade and appends DashboardTokenSuffix (with the token substituted) to the
	// tunnel URL, so the browser auto-authenticates instead of landing on a connect
	// form. This is the framework-specific knowledge kept DECLARATIVE (like Launch),
	// so core's agent-open stays generic. Empty = open the gateway root.
	DashboardTokenCmd string
	// DashboardTokenSuffix is appended to the console URL with the literal "{token}"
	// replaced by the (url-escaped) token — e.g. "#token={token}" (OpenClaw reads it
	// from the URL fragment) or "?token={token}". Only meaningful with
	// DashboardTokenCmd.
	DashboardTokenSuffix string
	// Bundled marks a TIER-1 agent preinstalled in the island image (claude-code,
	// codex): no first-use install wait. Tier-2 agents (Bundled=false) self-install
	// on first launch instead — see InstallCmd and the self-installing Launch line.
	Bundled bool
	// UpdateCmd upgrades an already-installed agent IN PLACE, inside the island.
	//
	// It exists because the self-installing Launch lines are all shaped
	// `command -v X || install X` — install-if-missing, never update. So an
	// island pins whatever version it first installed, for the life of the
	// container. An operator watching OpenClaw's own UI offer "Update available:
	// v2026.8.1 (running v2026.7.1-2)" could not take it: that button hands off
	// to a service supervisor to restart the process, and Dejima launches the
	// agent directly in tmux, so the handoff target does not exist
	// ("managed-service-handoff-unavailable"). Neither side is wrong; there was
	// simply no path.
	//
	// EMPTY MEANS NOT UPDATABLE IN PLACE, and that is a real answer for the
	// bundled agents — claude-code ships in the island image, so its version is
	// the image's and `dejima upgrade` is the update. The API says that rather
	// than failing vaguely.
	UpdateCmd []string

	// InstallCmd is the tier-2 install command (informational: surfaced in the
	// picker as "installs on first use"). The actual install happens inside the
	// self-installing Launch line, matching the existing goose/letta/hermes pattern.
	InstallCmd []string
	// ResumeLaunch is the Launch variant used for a GRACEFUL, operator-initiated
	// restart (e.g. applying a new secret): it continues the agent's previous
	// conversation instead of a cold start. Empty means the framework has no
	// resume affordance, so a restart falls back to a normal (fresh) Launch.
	ResumeLaunch string
}

// LaunchFor returns the command to run for this handler, honoring resume when the
// handler supports it. Falls back to the normal Launch otherwise.
func (h Handler) LaunchFor(resume bool) string {
	if resume && h.ResumeLaunch != "" {
		return h.ResumeLaunch
	}
	return h.Launch
}

// Attachable reports whether clients can attach to this handler's agents.
func (h Handler) Attachable() bool { return h.Kind == KindInteractive }

// NeedsProviderKey reports whether this handler requires an LLM provider key.
func (h Handler) NeedsProviderKey() bool { return h.RequiresProviderKey }

// registry is the built-in handler set. Custom agents not listed here are
// treated as generic interactive agents (the image's start.sh `*)` fallback
// runs the type string as a command); see Lookup.
var registry = map[string]Handler{
	"claude-code": {ID: "claude-code", Kind: KindInteractive, Launch: "claude", ResumeLaunch: "claude --continue", StateDir: "/home/dejima/.claude", Bundled: true},
	// `--sandbox danger-full-access`, NOT `--sandbox-policy=no-sandbox`. The latter
	// is not a Codex flag — it exits 2 with "unexpected argument", the tmux session
	// dies on the spot, and attaching lands the operator on a bare shell prompt
	// with no sign that an agent was ever meant to start. It has been wrong since
	// this registry was written.
	//
	// The island is the sandbox: Codex's own bubblewrap cannot run inside a
	// container (no unprivileged user namespaces — "bwrap: No permissions to
	// create a new namespace"), so its inner sandbox is disabled rather than left
	// to fail on every command. init.sh writes the same setting into config.toml;
	// passing it here too keeps the agent working when the operator has supplied
	// their own config, which init.sh deliberately leaves alone.
	"codex": {ID: "codex", Kind: KindInteractive, Launch: "codex --sandbox danger-full-access", StateDir: "/home/dejima/.codex", Bundled: true},
	// Aider: the open, model-agnostic tier-1 anchor (interactive). Its diff-based
	// edit loop tolerates weaker LOCAL models far better than a tool-call-heavy
	// agent — so it's the natural pairing for `dejima local`. Self-installs on
	// first launch (pip, kept out of the base image), sources the provider key,
	// and bridges providercreds' OPENAI_BASE_URL to aider/litellm's OPENAI_API_BASE
	// so a `local` provider works out of the box. Model comes from DEJIMA_MODEL
	// (provider-prefix stripped, routed via the openai/ litellm path).
	"aider": {
		ID: "aider", Kind: KindInteractive, StateDir: "/home/dejima/.aider",
		Launch: "bash -lc 'set -a; k=\"${DEJIMA_PROVIDER_KEY_FILE:-}\"; [ -f \"$k\" ] && . \"$k\"; set +a; " +
			"export OPENAI_API_BASE=\"${OPENAI_API_BASE:-${OPENAI_BASE_URL:-}}\"; " +
			"command -v aider >/dev/null 2>&1 || pipx install aider-chat; " +
			"if [ -n \"${DEJIMA_MODEL:-}\" ]; then exec aider --model \"openai/${DEJIMA_MODEL#*/}\"; else exec aider; fi'",
		RequiresProviderKey: true,
		SupportedProviders:  []string{"local", "openai", "anthropic", "google"},
		SuggestedModels:     []string{"local/qwen-coder", "openai/gpt-5.5", "anthropic/claude-sonnet-4-6"},
		InstallCmd:          []string{"pipx", "install", "aider-chat"},
		UpdateCmd:           []string{"pipx", "upgrade", "aider-chat"},
	},
	Shell: {ID: Shell, Kind: KindInteractive, Launch: "bash -l", StateDir: "/home/dejima"},
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
		UpdateCmd: []string{"npm", "install", "-g", "openclaw@latest"},
		// Source the daemon-materialized provider key (DEJIMA_PROVIDER_KEY_FILE →
		// `<PROVIDER>_API_KEY=…`) into the env before launch, like letta/goose —
		// OpenClaw reads OPENAI_API_KEY / ANTHROPIC_API_KEY / GEMINI_API_KEY from
		// the environment. Without this the gateway comes up but every task fails
		// with "No API key found for provider …". exec so signals reach it.
		// Before launching, pin a STABLE gateway auth token (the exact config the
		// gateway itself recommends: `gateway.auth.mode token` + `gateway.auth.token
		// …`). Otherwise a fresh runtime token is minted each start and only copied
		// to a clipboard — which doesn't exist in a container — so the console can
		// never authenticate. A generated value is kept in a file so it's stable
		// across restarts; DashboardTokenCmd reads it back via `openclaw config get`
		// to build the tokenized console URL.
		Launch:              "bash -lc 'set -a; k=\"${DEJIMA_PROVIDER_KEY_FILE:-}\"; [ -f \"$k\" ] && . \"$k\"; set +a; command -v openclaw >/dev/null 2>&1 || npm install -g openclaw; t=\"$HOME/.openclaw/.dejima-gateway-token\"; [ -s \"$t\" ] || { mkdir -p \"$HOME/.openclaw\"; head -c 18 /dev/urandom | base64 | tr -dc A-Za-z0-9 > \"$t\"; }; openclaw config set gateway.auth.mode token >/dev/null 2>&1 || true; openclaw config set gateway.auth.token \"$(cat \"$t\")\" >/dev/null 2>&1 || true; exec openclaw gateway --allow-unconfigured --bind loopback'",
		StateDir:            "/home/dejima/.openclaw",
		RequiresProviderKey: true,
		SupportedProviders:  []string{"anthropic", "openai", "google"},
		SuggestedModels:     []string{"anthropic/claude-sonnet-4-6", "openai/gpt-5.5"},
		GatewayPort:         18789,
		// Read the pinned token straight from the file the launch wrote it to —
		// NOT `openclaw config get gateway.auth.token`, which redacts secret values
		// and returns the literal "__OPENCLAW_REDACTED__" placeholder. Since the
		// launch sets gateway.auth.token to exactly this file's contents, the file is
		// the real token. `agent open` turns it into
		// http://localhost:<port>/#token=<token>; the Control UI reads the token from
		// the URL FRAGMENT (docs.openclaw.ai/web/dashboard), auto-connects, then
		// strips it.
		DashboardTokenCmd:    "bash -lc 'cat \"$HOME/.openclaw/.dejima-gateway-token\" 2>/dev/null'",
		DashboardTokenSuffix: "#token={token}"},
	// Letta — a stateful-agent framework with a REST API + web UI on 8283. Reads
	// its model key straight from the provider env var (OPENAI_API_KEY /
	// ANTHROPIC_API_KEY …), so the launch sources the daemon-materialized key file
	// (DEJIMA_PROVIDER_KEY_FILE → `<PROVIDER>_API_KEY=…`) before `letta server` —
	// no extra config shim needed. Self-installs via pip on first launch (kept out
	// of the base image, like openclaw). GatewayPort 8283 → `dejima agent open`.
	"letta": {ID: "letta", Kind: KindHeadless,
		UpdateCmd:           []string{"pip3", "install", "--user", "--upgrade", "letta"},
		Launch:              "bash -lc 'set -a; k=\"${DEJIMA_PROVIDER_KEY_FILE:-}\"; [ -f \"$k\" ] && . \"$k\"; set +a; command -v letta >/dev/null 2>&1 || pip3 install --user letta; exec letta server'",
		StateDir:            "/home/dejima/.letta",
		RequiresProviderKey: true,
		SupportedProviders:  []string{"anthropic", "openai", "google"},
		SuggestedModels:     []string{"anthropic/claude-sonnet-4-6", "openai/gpt-5.5"},
		GatewayPort:         8283},
	// Hermes — a messaging-bridge gateway (no localhost web UI), so GatewayPort 0
	// (injection-only: there is nothing for `dejima agent open` to forward). The
	// launch sources the provider key env; richer auth (`hermes auth add`) is a
	// framework concern left to the operator / a later shim.
	"hermes": {ID: "hermes", Kind: KindHeadless,
		Launch:              "bash -lc 'set -a; k=\"${DEJIMA_PROVIDER_KEY_FILE:-}\"; [ -f \"$k\" ] && . \"$k\"; set +a; command -v hermes >/dev/null 2>&1 || npm install -g @hermes-gateway/cli; exec hermes gateway'",
		UpdateCmd:           []string{"npm", "install", "-g", "@hermes-gateway/cli@latest"},
		StateDir:            "/home/dejima/.hermes",
		RequiresProviderKey: true,
		SupportedProviders:  []string{"anthropic", "openai", "google"},
		SuggestedModels:     []string{"anthropic/claude-sonnet-4-6"},
		GatewayPort:         0},
	// Goose — Block's agent with a web UI on 3000 (`goosed`). Beyond the provider
	// key it reads GOOSE_PROVIDER / GOOSE_MODEL, so the launch translates dejima's
	// framework-agnostic DEJIMA_PROVIDER / DEJIMA_MODEL ("provider/model") into
	// them. Self-installs via the official CLI installer on first launch.
	"goose": {ID: "goose", Kind: KindHeadless,
		// The vendor install script IS the updater; it is what Launch already runs.
		UpdateCmd:           []string{"bash", "-lc", "curl -fsSL https://github.com/block/goose/releases/download/stable/download_cli.sh | CONFIGURE=false bash"},
		Launch:              "bash -lc 'set -a; k=\"${DEJIMA_PROVIDER_KEY_FILE:-}\"; [ -f \"$k\" ] && . \"$k\"; set +a; [ -n \"${DEJIMA_PROVIDER:-}\" ] && export GOOSE_PROVIDER=\"$DEJIMA_PROVIDER\"; [ -n \"${DEJIMA_MODEL:-}\" ] && export GOOSE_MODEL=\"${DEJIMA_MODEL#*/}\"; command -v goosed >/dev/null 2>&1 || curl -fsSL https://github.com/block/goose/releases/download/stable/download_cli.sh | CONFIGURE=false bash; exec goosed'",
		StateDir:            "/home/dejima/.config/goose",
		RequiresProviderKey: true,
		SupportedProviders:  []string{"anthropic", "openai", "google"},
		SuggestedModels:     []string{"anthropic/claude-sonnet-4-6", "openai/gpt-5.5"},
		GatewayPort:         3000},
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

// UpdatableInPlace reports whether this agent can be upgraded inside a running
// island. False is not a failure: a BUNDLED agent's version is the island
// image's, so the update is `dejima upgrade`, and callers should say that
// instead of reporting the agent unupdatable.
func (h Handler) UpdatableInPlace() bool { return len(h.UpdateCmd) > 0 }
