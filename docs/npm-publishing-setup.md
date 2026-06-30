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

1. **Create the `@dejima` npm scope.** As the npm account that owns `dejima`,
   create an organization named `dejima` (npmjs.com → *Add Organization* →
   name `dejima`). Scoped packages publish public with `--access public` (the
   workflow already passes it); a free org publishes public packages.

2. **Create a granular access token** with **read-write** on:
   - the `@dejima` scope (all `@dejima/*` packages), and
   - the existing unscoped `dejima` package.

   npmjs.com → *Access Tokens* → *Generate New Token* → *Granular Access Token*.

3. **Point the repo's `NPM_TOKEN` secret at it.** If `NPM_TOKEN` is already set
   for the old flow, replace it with this token so it can publish the scope.
   GitHub → repo *Settings* → *Secrets and variables* → *Actions* → *Secrets*.

4. **Flip the publish gate on.** Add a repo **variable** (not a secret):
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
