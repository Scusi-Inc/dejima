# npm publishing setup (operator, one-time)

The `dejima` npm package ships its binary inside per-platform packages
(`@dejima/cli-<platform>-<arch>`) declared as `optionalDependencies` — **no
install script**, so `npm install dejima` works under npm 11's default
script-blocking. (The old `postinstall` downloader was silently blocked by npm
11, leaving the CLI non-functional.)

Until the steps below are done, the release workflow **builds and dry-run
validates** the packages every release but does **not** publish — the publish is
held by a gate, so nothing reaches npm by accident.

## What gets published

Per release: six platform packages + the main launcher.

| Package | Carries |
|---|---|
| `@dejima/cli-darwin-arm64` / `-darwin-x64` | macOS binary |
| `@dejima/cli-linux-arm64` / `-linux-x64` | Linux binary |
| `@dejima/cli-win32-arm64` / `-win32-x64` | Windows `dejima.exe` |
| `dejima` | `bin/dejima.js` launcher; resolves the matching platform package |

All carry the same release version; `dejima`'s `optionalDependencies` pin the
platform packages to that exact version.

## One-time setup

1. **The `@dejima` scope already exists** — `@dejima/sdk` and the unscoped
   `dejima` are published under the same account (npm org `dejima`, owned by the
   `scusi-inc` account). So there's **no org to create**; the new
   `@dejima/cli-*` packages publish into the existing scope.

2. **Make sure `NPM_TOKEN` can publish the new packages.** `@dejima/cli-*` are
   brand-new package names, so the token's grant must cover the **whole `@dejima`
   scope** (not just the specific `@dejima/sdk` + `dejima` packages). Check the
   token at npmjs.com → *Access Tokens*: if it's a Granular token scoped to
   individual packages, regenerate it with **read-write on the `@dejima` scope**
   (all packages) + the unscoped `dejima` package, and update the **`NPM_TOKEN`**
   Actions secret (GitHub → repo *Settings* → *Secrets and variables* →
   *Actions* → *Secrets*). A classic automation token already covers everything.
   (The current token expires 2026-09-20 — rotate before then regardless.)

3. **Flip the publish gate on.** Add a repo **variable** (not a secret):
   - `NPM_PLATFORM_PUBLISH` = `enabled`

   Settings → *Secrets and variables* → *Actions* → *Variables* → *New variable*.
   With both `NPM_TOKEN` set and this variable `enabled`, the `publish-npm-cli`
   job publishes; otherwise it stays in dry-run.

## First publish

Either cut the next release tag, or re-run the `publish-npm-cli` job of the most
recent release run (Actions → the release run → *Re-run jobs*). The job:

1. rebuilds the release archives, checksum-verifies each binary,
2. publishes the six platform packages **first** (so the main package's optional
   deps resolve), then the main `dejima` package.

## Verify (the bug this fixes)

On a clean machine with **npm 11+** (where the old package broke):

```bash
npm install -g dejima
dejima --version          # must print a version — proves the binary resolved
```

No `npm install-scripts approve` prompt, no manual step. On an unsupported
platform the launcher prints a clear error and points at the curl/brew channels.

## Notes

- Holding the gate pauses npm publishing for releases until step 4; curl + brew
  are unaffected.
- Local dry-run of the packaging: `make release-binaries VERSION=v0.0.0-test &&
  node npm/scripts/build-platform-packages.mjs v0.0.0-test dist` builds the
  packages under `npm/platforms/` (gitignored) without publishing.
- `DEJIMA_BINARY=/path/to/dejima` overrides the resolved binary (offline / CI /
  `npm i --no-optional`).
