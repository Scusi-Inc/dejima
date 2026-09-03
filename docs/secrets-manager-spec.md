# Secrets manager — spec

Status: **designed, not built.** Target: v0.9.x.

Per-island storage for the access tokens agents' tools need — EAS, npm, scraper
API keys — so they live somewhere managed instead of in the repo, a shell
profile, or a chat message.

## The property this does and does not have

**It does not hide secrets from agents, and must never claim to.**

Every agent in an island runs as the same OS user with a shell. A value usable
by a tool in that container is readable by any agent in that container — via
`env`, `/proc/self/environ`, or just reading the mount. Any in-container
"hiding" is obfuscation.

This is not a dejima limitation. Vault, Doppler, Infisical and `chamber` all
work the same way: `doppler run -- eas build` puts the token in the child's
environment. The industry did not solve invisibility; it solved *value if
leaked* — short-lived credentials, narrow scope, audit, fast rotation.

Note that dejima's existing GitHub credential handling already works this way:
the token is materialized into `/opt/host/gh-config` and is readable in-island.
The secrets manager is consistent with that, not weaker than it.

So the honest value proposition:

- not in your repo, never committed
- not pasted into a chat or a shell profile
- one place to see what exists, rotate, and revoke
- scoped to one island, deleted with it
- management events audited

Naming follows from this. It is a **secrets manager**, not a vault or a lockbox
— those words imply a boundary that does not exist here. TUI copy states
plainly that agents in the island can read these values.

## Scope: per-island only

No host-level scope in v1.

The one credential an operator would plausibly want host-wide is the GitHub
token, and that already has a better home in `githubid` (owner-scoped,
per-island selection, gh-config materialization). A host-level secret store
would be a second, weaker way to do a solved problem — while adding a
cross-tenant leak surface on a multi-user host.

Dropping it also removes the precedence question entirely: with one scope there
is no shadowing, and setting an existing name is an **update**, not a conflict.

If a real host-wide need appears, `--shared` on an island secret is the smaller
next step.

## Permissions

Host terminals are, by design, uncontained shells on the daemon host. Anyone
holding one can already read `~/.dejima/`, set any variable, and edit files
directly — so gating secret writes behind an operator role buys nothing against
that caller. The secrets manager is hygiene and audit, not a boundary between
operators.

The gate that *is* meaningful:

| caller | secrets |
| --- | --- |
| owner / operator, for islands they own | read + write |
| **island token (an agent)** | **list names only — never write** |
| viewer | list names |

Agents must not write. Not because it would expose values — they are in the
environment anyway — but because an agent that can *set* a secret can plant a
value other agents will trust. Unlike the rest of this, that boundary is real
and enforceable, since island tokens are already island-scoped.

## Storage

Values go through the **existing keychain-backed store** in
`internal/secrets` — macOS Keychain via `security`, libsecret via
`secret-tool`, and a 0600 file when neither is usable. That fallback is
deliberate, not a failure mode: a `--system` daemon starts at boot *before any
login*, when the login keychain is still locked.

Non-sensitive bookkeeping lives separately, which keeps `List` cheap and
keychain-free:

```
keychain account "island:<name>:<KEY>"     ← the value
~/.dejima/secrets/islands/<name>/meta.json ← 0600: names, timestamps, set_by,
                                             fingerprint, require_approval
```

Values never leave the daemon: absent from API responses, `dejima inspect`,
island exports, the rescue bundle, and the ledger. `Meta` has no field for a
value, so no code path can serialize one outward by accident.

(An earlier draft of this spec planned a plain 0600 JSON store and deferred
encryption at rest. That was wrong — the keychain store already existed, so
using it is strictly better and costs nothing. `DEJIMA_SECRETS_BACKEND=file`
forces the file backend for tests and for hosts where a locked keychain is more
nuisance than protection.)

## Delivery into the island

A `KEY=VALUE` file inside a **directory** bind-mounted read-only, mirroring
`/opt/host/gh-config`:

```
~/.dejima/secrets/<island>/mount/  →  /opt/host/secrets.d/  (ro)
                          └─ secrets.env
```

The **directory** is mounted, not the file: a file bind binds the inode, and the
file is replaced by rename, so a container would read the original inode for its
whole life (every later set/remove silently invisible). `meta.json` stays outside
`mount/` so the bind carries only what is meant to cross.

**Parsed, never sourced.** A file sourced by bash *executes* what it reads, so a
value containing a backtick or `$(...)` would be command injection into every
new shell. The agent launcher (`/opt/dejima/agents/<type>/init.sh`) and the
shell profile read it through a parser that sets variables directly; values are
opaque data.

Bind mounts reflect host writes live, so rotation needs no container recreate.

### Restarts are unavoidable, so say so

A running process's environment cannot be changed. New shells and new panes pick
up a change immediately; an already-running agent will not see it until
restarted. The TUI states this prominently after any change, with a one-key
restart:

```
⚠ RESTART TERMINALS TO APPLY — EXPO_TOKEN is live in new shells;
  the running agent still has the old environment.  [R] restart agents
```

## Validation

Names must match `^[A-Za-z_][A-Za-z0-9_]*$` — anything else breaks the file
regardless.

A **case-insensitive deny-list** blocks names that would turn "add a secret"
into code execution or a containment bypass. `http_proxy` and `HTTP_PROXY` are
both live, hence case-insensitive.

| family | names |
| --- | --- |
| loader injection | prefixes `LD_`, `DYLD_`, `GLIBC_` |
| shell hooks | prefix `BASH_`; `ENV`, `IFS`, `PS4`, `SHELLOPTS`, `CDPATH` |
| interpreter hooks | `PYTHONPATH`, `PYTHONSTARTUP`, `PYTHONHOME`, `NODE_OPTIONS`, `PERL5LIB`, `PERL5OPT`, `RUBYOPT`, `RUBYLIB`, `JAVA_TOOL_OPTIONS`, `_JAVA_OPTIONS` |
| command hijack | `PATH`, `EDITOR`, `VISUAL`, `PAGER`, `TERMINFO`, `TERMCAP`, `HOSTALIASES`, `TMPDIR` |
| git execution | `GIT_SSH`, `GIT_SSH_COMMAND`, `GIT_EXTERNAL_DIFF`, `GIT_PAGER`, `GIT_EDITOR`, `GIT_CONFIG`, `GIT_CONFIG_GLOBAL`, `GIT_PROXY_COMMAND`, `GIT_DIR`, `GIT_WORK_TREE` |
| identity | `HOME`, `USER`, `LOGNAME`, `SHELL` |
| **dejima containment** | prefix `DEJIMA_`; `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, `NO_PROXY` |

The last row is the dejima-specific one and the reason this is not merely a
footgun guard: dejima sets `HTTPS_PROXY` to route island egress through the
proxy. A secret by that name silently disables egress observability and
`dejima egress allow/deny`. That is the containment story being switched off by
a config field.

Prefixes are deliberately narrow. `NODE_OPTIONS` is denied but `NODE_ENV` is
not; `GIT_` is not a blanket prefix because `GITHUB_TOKEN` must stay legal.

## Display

Values are never shown after entry — GitHub's model. No leading characters
either: prefixes are often the identifying, high-entropy part and are exactly
what leaks into screenshots.

```
EXPO_TOKEN   set 2026-07-22 by aoos   rotated 2026-08-01   fp:4f2a91c8
```

The fingerprint is the first 8 hex of SHA-256, available on request. It leaks
nothing and lets an operator confirm a stored secret matches their copy by
hashing it locally.

## Log masking

Known values are redacted from `dejima logs` output. Cheap, and it targets the
likeliest real leak: a tool echoing its own configuration.

## Lifecycle

- **Purge** — secrets are deleted with the island; `--purge-all` removes
  `~/.dejima` including all of them. The confirm prompt says so.
- **Exports/backups** — excluded. `docker cp` of `/workspace` ignores
  `.gitignore` and copies literally, so anything in the workspace lands in a
  backup tarball. Secrets never go in the workspace.

## Surfaces

```
dejima secret set <island> NAME          # prompts, echo off
dejima secret set <island> NAME --stdin  # keeps it out of shell history + ps
dejima secret ls <island>                # names, timestamps; never values
dejima secret rm <island> NAME
```

- **API** — `GET/PUT/DELETE /v1/islands/{name}/secrets[/{key}]`, values never in
  a response. `openapi.yaml` + TS SDK updated in step with it.
- **TUI** — a Secrets pane per island: list, add, rotate, remove; masked always;
  the restart banner; honest copy about agent readability.
- **Primer** — a Secrets section in `image/island-primer.md`, so an agent of any
  type learns the convention with no dejima-specific integration. This is what
  answers "how does the agent know where to look" — the same mechanism that
  already teaches `dejima msg` and the Port.

## Deferred — brokered access

Per-use approval gating and per-use logging are **one feature, not two.**

Under environment injection there is no read event to observe: `eas` reads
`EXPO_TOKEN` out of its own process memory and nothing crosses the daemon. So
"agent X used EXPO_TOKEN at 15:42" is unobtainable by construction.

Both become possible only if the agent must *ask* — `dejima secret get NAME`
over the island token API. That single change unlocks:

- per-use audit records
- per-use approval, reusing the pending-actions machinery in
  [`action-gate-spec.md`](action-gate-spec.md)
- rate limiting

Cost: tools do not fetch natively, so the agent must wire it
(`export EXPO_TOKEN=$(dejima secret get EXPO_TOKEN)`) — which puts the value
back in that shell's environment. Brokering removes *ambient* exposure and buys
the audit trail; it does not make the value unreadable.

`require_approval` is stored from v1 so this lands without a migration.

## Build order

1. `internal/secrets` — store, validation, deny-list, fingerprints, timestamps,
   tests
2. Delivery — mount, parser, agent launcher, live rotation
3. Surfaces — CLI, API, openapi, SDK, TUI pane, primer, log masking

Steps 1–2 first: the store and deny-list are what hurt to change once real
secrets exist.
