# Adding `meta` as a provider — a plan, not a change

Muse Code ships as a first-class agent without this. It authenticates by OIDC
device login and inherits a pushed seed, so it works today with no provider
entry at all. This document is what it would take to *also* give Meta's Model
API a provider slot, and why you might not want to.

Written 2026-09-12, against v0.9.12.

## What a provider is here

`providercreds.Provider` is `{Name, APIKey, BaseURL, EnvVar, UpdatedAt}`. The
daemon materialises the key into a file and passes its path as
`DEJIMA_PROVIDER_KEY_FILE`; an agent's launch line sources it:

```
set -a; k="${DEJIMA_PROVIDER_KEY_FILE:-}"; [ -f "$k" ] && . "$k"; set +a
```

The file contains `<ENVVAR>=…`. `EnvVar` defaults to `UPPER(Name)+"_API_KEY"`
and can be overridden — which matters here, see below.

## The five places a new provider touches

1. **`internal/providercreds`** — nothing structural. A provider is data, not
   code; `Put` takes any name. No change needed.
2. **`internal/handlers/handlers.go`** — `SupportedProviders` on each agent that
   could use it, and `SuggestedModels` entries like `meta/<model>`.
3. **`cmd/dejima/tui_providers.go`** — `providerCandidates` decides which
   providers the Settings page offers before any key exists. A provider nobody
   has a key for must still be offerable or it can never be set up.
4. **`cmd/dejima/tui_agentpick.go` + the adder's key step** — the inline
   "this agent needs a key" flow (`agentKeyGap`, `adderKeyProviders`). Only
   fires for agents with `RequiresProviderKey`.
5. **The picker's key-gap guidance** — the text that names which providers an
   agent accepts.

## The two facts that decide the shape

**The env var is not `META_API_KEY`.** Third-party documentation gives Meta's
Model API key as `MODEL_API_KEY`. The default derivation would produce
`META_API_KEY` and silently hand the agent a variable it does not read — a
working key, a correct-looking Settings row, and every task failing with "no
credentials". `Provider.EnvVar` exists for exactly this and must be set
explicitly to `MODEL_API_KEY`.

**That env var is unverified.** It comes from third-party guides, not from
Meta, and it is NOT present in the launcher script
(`api.meta.ai/muse-launcher.sh`), which only implements the device-code flow.
Before any of this is built, someone has to confirm that a `MODEL_API_KEY` in
the environment actually authenticates a `muse` run. That is a ten-minute test
on a machine with a key, and doing it first is the difference between a day's
work and a day's work that ships a dead variable.

## Why it may not be worth doing

Muse Code's inference runs on Meta's infrastructure and the agent authenticates
itself. Unlike aider or letta — where the provider key IS how the agent reaches
a model, and where the user genuinely picks between openai/anthropic/local — a
`meta` provider would configure exactly one agent to reach exactly one service
that it can already reach by logging in.

The case *for* doing it is consistency: every other key-requiring agent appears
in Settings → Provider keys, and an operator who looks for muse there and finds
nothing has to learn that this one is different. That is a real cost, just a
smaller one than a dead `META_API_KEY` would be.

## Recommended order, if it goes ahead

1. Verify `MODEL_API_KEY` works for a headless `muse` run. Stop here if not.
2. Add the provider with an explicit `EnvVar: "MODEL_API_KEY"`, and a test that
   asserts the derived default is NOT used — the failure is silent otherwise.
3. Add `meta` to `providerCandidates` so it can be set before a key exists.
4. Only then set `RequiresProviderKey` on the muse handler, and add the
   key-file sourcing to its launch line. Doing this step earlier makes the
   agent demand a key it cannot use.

Step 4 last is the load-bearing ordering: `RequiresProviderKey` changes the
add-agent flow, so flipping it before the key path works turns a working agent
into one that interrupts every add with a step that leads nowhere.
