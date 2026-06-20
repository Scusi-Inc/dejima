# Audit log

Dejima keeps a single append-only, **hash-chained** audit ledger on the daemon
host at `~/.dejima/ledger.jsonl` — outside every container's blast radius, so a
compromised island cannot rewrite its own history. Each entry chains to the
previous one (SHA-256, or HMAC-SHA-256 when a key is configured); any in-place
edit or deletion breaks the chain and is detected by verification.

There are two layers on the one chain:

1. **Brokered-operation records (always on).** Port scope grants/revokes, file
   Trades (`trade.read`/`trade.write`/`trade.deny`), and capability calls
   (`capability.*`). This is the tamper-evidence the Port spec requires before
   an island may be granted host access — it predates the operational layer and
   is written regardless of any flag.

2. **Operational records (opt-in).** API requests (`api.request`) and
   governance-relevant lifecycle events (`island.created`, `island.purged`,
   `island.agent-added`, `daemon.panic-engaged`, `container.crashed`, …). Turned
   on with `dejimad --audit`. Off by default.

## Enabling the operational log

```
dejimad --audit                       # record mutations + lifecycle
dejimad --audit --audit-reads         # also record read (GET) requests
dejimad --audit-hmac-key-file KEYFILE # key the whole chain with HMAC-SHA-256
```

(or the `DEJIMAD_AUDIT=1`, `DEJIMAD_AUDIT_READS=1`,
`DEJIMAD_AUDIT_HMAC_KEY_FILE=…` env vars.)

- The default records **state-changing** requests (POST/PUT/PATCH/DELETE) plus
  lifecycle events; `--audit-reads` adds GETs. Healthz/metrics polls, the audit
  read itself, agent-shim telemetry, and websocket attaches are never recorded.
- **HMAC** raises tamper-detection from "detectable" to "requires the key". The
  key is read from a file (never the command line, so it can't leak via `ps`).
  Set it on a **fresh** ledger — switching keying over a file that already holds
  entries will make verification report the older entries as broken.

## Reading, filtering, exporting

`dejima audit` reads the ledger; combine filters (AND):

```
dejima audit                                  # recent entries, newest at the bottom
dejima audit -n 50                            # last 50
dejima audit --island myrepo                  # one island
dejima audit --type port                      # prefix: matches port.grant, port.revoke
dejima audit --type api --decision denied     # denied API requests (auth failures etc.)
dejima audit --since 2026-06-20T00:00:00Z     # RFC3339 lower bound (also --until)
dejima audit --actor operator
dejima audit --verify                         # verify the hash chain; non-zero exit if tampered
dejima audit --export csv  -o audit.csv       # export the filtered records (also: jsonl)
dejima audit --export jsonl                   # stream to stdout
```

The same surface is the HTTP API: `GET /v1/audit` (`?island=&type=&actor=&`
`decision=&since=&until=&limit=&format=json|jsonl|csv`). **Verification always
covers the whole chain** regardless of filters — filters only narrow what is
returned, never what is verified (`total` vs `returned` in the JSON response).

## TUI viewer

Press **`A`** in the `dejima` dashboard to open the audit pane: the chain-
verification status (✓ intact / ⚠ tampered) and a scrollable view of recent
activity (denied entries highlighted). `j/k` scroll, `r` refresh, `esc` close.

## Identity (who/role)

`api.request` records carry an actor. On the trusted operator listeners (unix
socket, tailnet TCP) that is `operator`. Richer per-request identity (named
tokens + roles) is provided by the team-auth work and consumed here via the
`AuditIdentity` request-context seam (`internal/api/audit.go`); the team
activity feed is built on top of this operational log.
