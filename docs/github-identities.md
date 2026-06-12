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
