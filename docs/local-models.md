# Local models & agent tiers

**Status:** shipped complete on `feat/local-models` — catalog + backend +
`/v1/local/*` API (+ openapi) + `dejima local` CLI + ts/py SDK + agent
tiers/Aider + TUI status sub-page + `doctor` check + the provisioning-wizard
local-models step.
**Goal:** run open-weights models (Qwen-Coder, Mistral, Kimi K2, …) on your own
hardware and drive isolated Dejima agents with them — managed the same easy way
everything else in Dejima is, from the **TUI, CLI, or SDK**.

## Principles (the two rules that keep this bounded)

1. **The model is a shared *service*, not a per-island payload.** Weights load
   once into host RAM/VRAM behind an OpenAI-compatible endpoint; every island's
   agent is a thin API client. Per-island loading is impossible for big models
   (a ~1T-param MoE fits once on a Mac's unified memory) and pointless for small
   ones. On Apple silicon the server *must* run on the host — the colima Docker
   VM has no Metal passthrough — so islands reach it via `host.docker.internal`.
2. **Orchestrate Ollama; don't reimplement it.** Ollama (and vLLM / LM Studio)
   already do install / pull / serve / lifecycle + an OpenAI endpoint. Dejima's
   value is the *glue* Ollama doesn't provide: host-aware model curation,
   auto-registration as a provider, per-island egress grants, and one management
   surface across TUI/CLI/SDK.

This rides machinery that already exists:
- `internal/providercreds` stores `(provider, api_key, base_url)` and materializes
  `<PREFIX>_BASE_URL` per island (comment already lists "self-host") — so a local
  endpoint is a first-class provider today.
- Per-island egress policy (`PATCH /v1/islands/{name}/egress/policy`) can allow
  *only* the model endpoint and deny the rest.
- `internal/vmmem` already detects host RAM — reuse it for model sizing.

---

## Part A — Agent tiers

Today `claude-code` and `codex` are bundled + interactive; `goose`/`letta`/
`hermes` are registered handlers but headless and not bundled; `aider` is absent.
Make the tiering explicit rather than making everything first-class (first-class
is a maintenance contract: bundled, hooks wired, version-pinned, tested).

- **Tier 1 — bundled, interactive, hooks/skills/prefs wired:**
  `claude-code` (Anthropic shape) · `codex` (OpenAI shape) · **`aider`** (open,
  model-agnostic). This trio covers the whole matrix with three tools. Aider is
  the open anchor because it's interactive, mature, trivially OpenAI-compatible,
  and its **diff-based edit loop tolerates weaker local models** far better than
  a tool-call-heavy agent.
- **Tier 2 — registered + install-on-first-use:** `goose`, `letta`, `hermes`,
  others. Install into the writable prefix the first time they're picked (same
  pattern as `NPM_CONFIG_PREFIX`; add a pip prefix), cached for the island's
  life. No image bloat, still one-click.

**Code shape:** add to `handlers.Handler`:
```go
Bundled    bool     // tier-1: preinstalled in the image
InstallCmd []string // tier-2: run on first pick if the launch binary is missing
```
`dejima agent add` runs `InstallCmd` when the binary is absent. Add an `aider`
handler (`KindInteractive`, `Launch: "aider"`, `RequiresProviderKey`, providers
include `openai` + `local`).

---

## Part B — Managed local provider

A small backend interface, **Ollama as the default impl**, room for vLLM
(throughput, Linux/GPU) and LM Studio — don't marry one backend:
```go
type LocalBackend interface {
    Detect() (installed bool, running bool, endpoint string)
    Install(ctx) error            // install + start; idempotent
    Pull(ctx, model string) error // streams progress
    Remove(ctx, model string) error
    Models(ctx) ([]Model, error)  // pulled models
    Off(ctx) error
}
```
Plus a **host-aware curated catalog** (in `internal/localmodel`, sized off
`vmmem`): a *small* recommended set per RAM band, not a registry —
e.g. `≥64 GB → qwen2.5-coder:32b-q4 / mistral-small`; `~16 GB → *:8b`;
`<16 GB → *:3b`. Refreshed in code over time.

Installing a backend **auto-registers a provider** named `local`
(`base_url → host.docker.internal:11434/v1`) via existing `providercreds`, so it
appears in the `v` model editor and `dejima provider ls` with zero manual steps,
and **auto-grants island egress** to just that endpoint.

---

## Part C — The management surface (TUI · CLI · SDK · API)

### CLI — a `dejima local` family (mirrors `dejima provider` style)
```
dejima local                       # status: backend · endpoint · running? · models · which islands use it
dejima local install [--backend ollama|vllm|lmstudio]
dejima local models                # pulled models + recommendations for THIS host's RAM
dejima local pull <model|alias>    # e.g. `dejima local pull qwen-coder` (curated alias) → ollama pull
dejima local rm <model>
dejima local use <model> [--agent <island/id>]   # set default local model (or per agent)
dejima local off                   # stop/disable the backend
```

### API (owner-gated, like image build)
```
GET    /v1/local                       # status
POST   /v1/local/install               # {backend}
GET    /v1/local/models                # {pulled:[…], recommended:[…]}  (RAM-aware)
POST   /v1/local/models/{name}/pull    # streams progress (like image build)
DELETE /v1/local/models/{name}
POST   /v1/local/off
```

### SDK (ts + py, mirroring the API)
`localStatus()` · `localInstall(backend)` · `listLocalModels()` ·
`pullLocalModel(name)` · `removeLocalModel(name)` · `localOff()`.

### TUI (rides the new contextual settings menu)
- **Dejima settings → "Local models"** sub-page: backend status, an
  install/enable toggle, the pulled-model list with pull/remove, a
  "recommended for your RAM" section, and set-default. (Slots in beside editor /
  connection-target / voice / github.)
- **Per-agent `v` model editor:** once the `local` provider is registered, its
  pulled models appear as first-class picks automatically — no editor changes
  needed beyond surfacing the model list.
- **Setup/provision branch:** a "**Models: cloud / local / both**" step (natural
  fork off the existing guided per-agent provider-key step). Choosing *local*
  installs Ollama, pulls a recommended model, registers the provider, and grants
  egress — inside the wizard.

### Doctor & prefs
- `doctor` gains: backend installed? running? default model pulled? fits RAM?
- Prefs (in Dejima settings): default local model, autostart, backend choice.

---

## The caveat to keep front-and-center

Local open models are **meaningfully weaker at *agentic* coding** than Claude /
GPT today — reliable long-horizon tool-calling is the gap. Position local as
**privacy / cost / offline**, not frontier parity. This is *why* Aider is the
tier-1 open anchor: it degrades gracefully where tool-heavy agents face-plant.
When a local model is selected, the UX should nudge toward Aider and simpler
workflows rather than let tool-use-heavy skills silently underperform.

---

## Phased delivery

1. ✅ **Foundation (`internal/localmodel`)** — Ollama backend detection + a
   host-aware curated catalog (reuses `vmmem`), pure Go + unit tests.
2. ✅ **Daemon API + CLI** — `/v1/local/*` endpoints, `dejima local` verbs, auto
   provider registration. (No egress grant needed: `host.docker.internal` is
   already in the egress NO_PROXY set, so islands reach the host endpoint direct.)
3. ✅ **TUI** — read-only "Local models" settings sub-page; local models surface
   in the `v` model editor automatically once the `local` provider is registered.
   (Streaming install/pull stay in the CLI, where streaming belongs.)
4. ✅ **Agent tiers** — `Handler.Bundled`/`InstallCmd`, the `aider` handler
   (self-installs via pipx), surfaced on the agent-types API.
5. ✅ **Rest** — ts+py SDK, `doctor` check, the provisioning-wizard "local models"
   step, and openapi.yaml entries (with surface tests + waivers) for `/v1/local/*`.
