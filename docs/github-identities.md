# GitHub identities

Islands clone and push using a **GitHub identity that lives on the daemon**, not
on whatever device you happen to be driving from. The daemon is the credential
owner because it's the thing that actually manages the repo — and because you
reconnect to Dejima from different devices, some of which (a phone, a fresh
laptop) have no `gh` of their own.

A daemon can hold **several** identities — say `work` and `personal` — and each
island picks **one** at creation time (or inherits the default). The daemon
materializes just that one identity into the island; an island never sees the
whole set.

## Why daemon-side, not client-side

- **Device independence.** Listing repos and cloning both run on the daemon, so
  you can create islands from any device — even one with no `gh` installed.
- **Multiple identities.** A single login can't be both your work and personal
  account. The daemon holds named identities; the island names which one.
- **It's where the repo is managed.** Clone/push happen on the daemon host, so
  that's the natural home for the credential.
- **Enterprise shape.** A server holding scoped service credentials is how a
  team/enterprise deployment wants to work.

The client's own `gh` is now only a convenience for *seeding* the daemon (below)
— it's never required to create an island.

## Setting up identities

Any one of these gets a daemon identity in place:

1. **On the daemon host directly** — `gh auth login`. With no Dejima identities
   configured, islands fall back to the host's `~/.config/gh`, so this alone
   already works for a single account.

2. **Inherit from a client that has `gh`** — from a machine where you're logged
   in:
   ```sh
   dejima auth push --github                 # push the active gh account
   dejima auth push --github --name work --default
   gh auth switch                            # then push the other account
   dejima auth push --github --name personal
   ```
   `--name` defaults to the GitHub login; `--default` marks the daemon default.
   This sends the token to the daemon over the same channel `dejima auth push`
   already uses for Claude credentials.

3. **Enterprise / scripted** — `PUT /v1/credentials/github/{name}` with
   `{login, token, host?, default?}`.

Check what the daemon holds:

```sh
dejima auth status
# …
# github identities:
#   * work       alockwood@github.com
#     personal   austin@github.com
#   (* = default; new islands use it unless they pick another)
```

GitHub Enterprise hosts are supported — set `host` on the identity (e.g.
`github.example.com`); the daemon talks to that host's `/api/v3`.

## Selecting an identity per island

- **TUI (`dejima` → new island → Browse GitHub):** pick the identity, then one
  of its repositories (fetched daemon-side). A single configured identity is
  selected automatically; none shows a hint pointing here.
- **CLI:** `dejima init --repo <url> --github-identity work`.

If you don't name one, the island uses the daemon's **default** identity, or —
when no identities are configured — the host's `~/.config/gh`. Naming an
identity that doesn't exist is rejected up front.

## How it reaches the island

At container creation the daemon resolves the island's identity and writes a
single-identity `hosts.yml` to a per-island directory
(`~/.dejima/secrets/github/islands/<name>/`, mode 0600), mounted read-only at
`/opt/host/gh-config`. The island's entrypoint runs `gh auth setup-git` against
it exactly as before — so only the chosen identity is present inside, and it's
re-materialized on `dejima reset`/recreate from the stored identity.

## Security note

Identities are stored on the daemon host (`~/.dejima/secrets/github/store.json`,
0600) and the selected one is materialized into the island's gh config. This is
the same posture as the existing Claude credential seed: the token is readable
inside the island it's mounted into, but each island only ever receives the one
identity it selected — not the others. Tightening this further (brokering the
credential over the island socket instead of mounting it, or minting per-repo
scoped tokens) is tracked as future hardening and reuses the same per-island
token machinery as the macOS autonomy path.

## Commit author vs. push identity

The push/clone identity comes from the selected GitHub identity. Commit
author name/email still come from the host `~/.gitconfig` mount, so a commit is
*authored* per your gitconfig and *pushed* as the chosen identity. For most
setups these line up; per-identity commit authorship is a possible follow-up.

## Owner-scoped identities (team self-serve)

Identities are **tenant-scoped**: a team member (operator) provides credentials
for **their own** private repos, usable only by **their own** islands — the host
owner is not in the loop, and one member's token can never reach another
member's island.

### Connecting GitHub (a member, self-serve)

Two paths, both self-scoping to the caller's tenant:

1. **Guided sign-in (device flow)** — `dejima github connect [name]`. Shows a
   short code + `https://github.com/login/device`; approve in the browser and the
   daemon captures the token — no PAT to hand-make. Requires the daemon to be
   configured with an OAuth app (below); otherwise it points you at the PAT path.
2. **Paste a token** — `dejima auth push --github [--name <name>]` pushes the
   machine's active `gh` login, or use a **fine-grained PAT** scoped to just the
   repos you need (the least-privilege option — tighter than the device flow's
   `repo` scope).

`dejima auth status` lists **your** identities (the host owner sees all). Only the
host owner may set an identity as the daemon `--default` or mark it `--shared` (a
team-wide org credential usable by every tenant).

### Enabling the guided device flow (operator, one-time)

The device flow needs a **GitHub OAuth App** (public client id — no client secret
for device flow):

1. GitHub → Settings → Developer settings → **OAuth Apps** → New OAuth App.
2. Enable **Device Flow** on the app.
3. Set **`DEJIMAD_GITHUB_CLIENT_ID`** to the app's client id and restart the daemon.

Until it's set, the device-flow endpoints return `501` and the **PAT path is the
only route** — the feature ships fully working without the app; the guided flow
lights up once it's registered. Scopes requested: `repo` (clone **and** push —
agents commit) + `read:org`, surfaced in the flow (never silent).

### Scoping & containment

- Each identity is stamped with its **owner** (the authenticated caller — never
  client-forged). All resolution goes through one chokepoint,
  `Store.ResolveForIsland`: a tenant island resolves only its owner's identities
  plus host-**shared** ones. An operator's token is **never** materialized into
  another tenant's island.
- The host's own `~/.config/gh` is a **host-island-only** fallback. A tenant
  island that resolves no identity gets **no** credential (recover with the
  self-serve paths above) rather than silently inheriting the host operator's
  login — the over-mount this scoping closes.
- **Security note (device-flow / captured tokens):** a connected token is
  materialized into the owner's **own** islands (the read-only `gh` config mount),
  so a compromised agent inside one of their islands could use it to reach GitHub
  — inherent to giving an island clone/push access, and **contained to that
  tenant** by `ResolveForIsland`. Prefer a fine-grained per-repo PAT to bound what
  a captured credential can do.
