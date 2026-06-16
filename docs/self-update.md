# `dejima update` — dual-mode self-update

Dejima installs two ways, so it updates two ways:

- **source mode** — a dev box or a self-hosted server with the repo checked out,
  installed via `make install`. Build version is `dev` or a git-describe string
  (`v0.1.13-6-gHASH`, `…-dirty`).
- **release mode** — a client that installed a tagged binary (install.sh / a
  release asset). Build version is a clean semver tag (`v0.1.13`).

The mode is inferred from the build version (`selfupdate.DetectMode`): a clean
`vX.Y.Z` with no suffix ⇒ release; anything else ⇒ source. (`version.IsRelease`
alone isn't enough — it ignores the `-N-gHASH`/`-dirty` suffix a checkout build
carries, so `DetectMode` also rejects any suffix.)

## `dejima update` applies by default

**`dejima update` updates in place.** "Update means update" — the bare command
upgrades; `--check` is the look-don't-touch escape hatch. (There is no
`--apply`/`--yes`; those were removed.)

- **`dejima update`** — checks for a newer release and, if one exists, applies it.
- **`dejima update --check`** — reports `current` / `latest` / `mode` / whether an
  update is available, and does nothing else.

### Release-mode apply (`selfupdate.ApplyReleaseSelf`)
Resolves the asset for this `GOOS/GOARCH` from the latest release, downloads it,
**verifies it against the release `SHA256SUMS`** (a self-replacing binary must
verify provenance — an unverified fetch would be RCE by design), and atomically
replaces the on-disk binary (`ReplaceExecutable`: stage a temp file, rename the
running binary aside, rename the new one into place, roll back on failure — this
also handles Windows, where a running `.exe` can be renamed but not overwritten).
`client.json` and all other config are never touched.

### Source-mode apply (`selfupdate.ApplySource`)
`git pull --ff-only` in the checkout → `make install` → `dejima service restart`.
The checkout is found from the cwd (walk up for the dejima `go.mod`) or
`--source <dir>`. Safety: a dirty tree is refused (no clobbering local work) and
`--ff-only` refuses a diverged history (no auto-merge).

### Daemon self-update (`POST /v1/admin/update`, `internal/api/admin_update.go`)
`dejima` and `dejimad` are two binaries that must move together. The daemon
updates *itself* on request (operator-only; reachable from the TUI's `U` key,
including against a remote daemon): it runs the same source/release apply, then
restarts in the right launchd/systemd domain so `api_version`
(`version.APIVersion`) skew can't strand a new client against an old daemon. On a
headless macOS **system** install the privileged steps go through a scoped
`/etc/sudoers.d/dejima` drop-in installed by `dejima service install --system`,
so the restart needs no TTY.

## Bootstrapping note

The apply-by-default behavior shipped in **v0.1.13**. Updating *from* an earlier
release (≤ v0.1.12), whose `dejima update` was check-only, uses that older
binary's syntax for the one-time hop — after which plain `dejima update` is the
only command you need.
