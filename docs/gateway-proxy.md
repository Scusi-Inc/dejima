# Gateway proxy — spec

**Status:** specified, unbuilt. Owner: d4.

## The gap

`GatewayPort` (openclaw 18789, letta 8283, goose 3000) is used for exactly one
thing: building the `ssh -L` forward inside `dejima agent open`. **Nothing
exposes it over the API.** An API client can create a Home Island, start an
assistant brain, and have no route to its console at all.

The TUI never noticed because it reads the daemon directly and shells out to
`ssh`. Harbormaster is a GUI on the API, and **a browser cannot run `ssh -L`**,
so for it the gap is total rather than inconvenient.

## Why a proxy rather than exposing the port

Three problems, one route. That is the argument.

**Reachability.** `agent open` tunnels *from the machine running the CLI*. A
browser is not that machine. Publishing `gateway_url` only helps a client that
can dial it, which a web build cannot.

**The credential.** `handlers.go` pins a gateway auth token for openclaw and the
daemon never shares it. A client that solved reachability would still meet a
login screen for a password it cannot know. **A proxy fixes this for free**: the
daemon injects the token server-side and the client never holds it. That is
strictly better than returning the token on `AgentInfo` — it keeps a credential
out of a browser, which is where credentials go to leak.

**Readiness.** `gatewayReady` lives in `cmd/dejima/agent_open.go` — client-side,
with no API surface. Any other client reimplements it and drifts from us the
first time it changes. Behind a proxy it has one home that both clients read.

## Opaque, not modelled — decided

The daemon relays bytes. It does not model OpenClaw's sessions, verbs or state.

- Letta and Goose work the same day, because they already declare a
  `GatewayPort` and nothing else is required of them.
- OpenClaw is `2026.7.1-2` — young and moving. Modelling its API means chasing
  it, and every churn upstream becomes our bug.
- The usual argument for modelling is a Ledger that records *what* crossed
  rather than *that* something crossed. It does not apply here: this is the
  operator reaching **their own island**, not untrusted content crossing into a
  contained one. Port's direction is the one that needs semantic auditing.

"Dejima can drive an assistant — send it work, read its state" is a **separate
feature** on the roadmap, not a better proxy. Do not let the two merge.

## The route

```
GET|POST|PUT|DELETE  /v1/islands/{name}/agents/{id}/gateway/{path...}
```

Normal route auth. **Register it explicitly in `roleauth.go`** — an unlisted
route is owner-only by default, which is safe but almost certainly not the
intent for a console a non-owner operator should reach. Classify it consciously;
that table's comment says so.

**Not reachable by an island token.** `tokenauth.go` denies by default and this
must stay denied: an island reaching another island's assistant through the
daemon is exactly the containment break the token scoping exists to prevent.

## The part most likely to be got wrong

**WEBSOCKETS.** OpenClaw's Control UI holds a live connection; that is what
"Gateway connection lost" was about in the field. A plain `httputil.ReverseProxy`
over a `ResponseWriter` that does not support `Hijack` will serve the page and
silently fail the socket — HTTP works, the live connection does not, and the
symptom looks exactly like the framework being broken. We have already shipped
that misattribution once.

Precedent exists in this tree: `internal/api/audit.go:223` forwards `Hijack`
through its `statusRecorder` specifically so streaming and upgrade survive the
middleware, and `internal/api/session.go` already runs a real websocket
(`coder/websocket`). Follow both. **Verify the upgrade end to end** — a test that
only proves a GET returns 200 proves the half that was never in doubt.

## Readiness, exposed rather than reimplemented

Move the probe daemon-side and surface it. `gatewayReady` sends the same GET the
browser would and requires one byte back — any HTTP server answers *something*,
while ssh's accept-then-fail gives EOF with nothing read.

Keep the states distinct, and keep them **three**, not two:

| | Meaning |
| --- | --- |
| ready | something is serving on the gateway port |
| not ready | nothing is listening — starting, installing, or dead |
| unknown | the daemon could not ask |

And keep the **provider-key state separate from all of them**. A keyless gateway
serves `/health` perfectly and fails every task, so "nothing is listening" and
"listening but cannot reach a model" must never collapse into one signal.
`RequiresProviderKey` is registry data the daemon holds before any connection —
answer it as a preflight, not a probe.

**Do not wire a readiness indicator to OpenClaw's `/ready`.** Its docs are
explicit that a broken messaging channel returns 503 while the Control UI is
fine. That would ship a red light that does not mean what a user reads it as —
the defect we have spent this week removing from three other surfaces.

## Open questions, for whoever builds it

- **Streaming and size.** A console may stream. Do not buffer whole responses.
- **Timeouts.** The proxy must not inherit a short daemon-wide timeout that
  kills a long-lived connection; `defaultDockerTimeout` is not the right budget.
- **Ledger.** Operator-to-own-island is low stakes, so per-request entries are
  probably noise. One entry per *session* opened is likely right. Decide
  deliberately rather than by omission.
