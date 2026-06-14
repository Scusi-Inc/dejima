# Secure in-island routing

## The problem

The daemon's operator control socket (`paths.SocketPath()`) — the full,
unauthenticated control plane used by the local `dejima` CLI — was bind-mounted
**into every Linux island** at `/run/dejima/dejimad.sock`. Any process inside a
container could therefore reach the entire API: create/delete *any* island,
self-grant Port host-access scopes, push/read credentials, etc. That defeats
containment. (macOS never mounted the socket, so behavior also diverged by
platform.)

## The change (Option C — single, authenticated path)

- **The control socket is no longer mounted into containers.** It stays on the
  daemon host for the operator's local CLI only.
- **Every island reaches dejimad over one path:** the token-authenticated,
  host-internal TCP listener — `DEJIMA_HOST` + a per-island `DEJIMA_TOKEN`. That
  path is island-scoped in `internal/api/tokenauth.go` (default-deny: the control
  plane, lifecycle, attach surface, and Port-scope grant are all unreachable for
  an island token; only the island's own autonomy surface is).
- **The token listener is on by default** (loopback `127.0.0.1:7274`). An
  explicit `--token-tcp` that fails to bind is fatal; a failure of the *default*
  bind is best-effort — in-island telemetry/autonomy degrade to a no-op rather
  than bricking the daemon (e.g. on a port clash).
- **`agent-event` (the notify hooks) now flows over this path, authenticated.**
  The daemon attributes each event to the *token's* island, so an island can
  only emit telemetry for itself — the previous cross-island spoof is closed.
- Containers are created with `--add-host=host.docker.internal:host-gateway` so
  the route to the host-internal listener resolves.

## Platform note (native-Linux caveat — read before deploying on Linux)

This is proven on the **macOS / Docker Desktop / colima** deployment (Minion),
where `host.docker.internal` resolves to the host and can reach a loopback-bound
listener.

On **native Linux Docker** it is *not* fully solved: `host.docker.internal:host-gateway`
resolves to the bridge gateway (e.g. `172.17.0.1`), which **cannot** reach a
listener bound to `127.0.0.1` (loopback is host-only). So a native-Linux daemon
must bind the token listener somewhere the container can reach — the bridge
gateway IP (or `0.0.0.0` behind a host firewall) via `--token-tcp` — for
in-island telemetry/autonomy to work. The default loopback bind is correct for
the macOS deployment; first-class native-Linux support is a follow-up. Because
telemetry is best-effort, islands still function when the path is unreachable —
you just lose live agent-state events.

## What still uses the socket

Only the operator's local `dejima` CLI on the daemon host. It is full-control by
design and is never exposed to a container.
