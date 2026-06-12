# `dejima update` — dual-mode self-update (design)

Dejima installs two ways, so it updates two ways:

- **source mode** — a dev box or a self-hosted server with the repo checked out,
  installed via `make install`. Build version is `dev` or a git-describe string
  (`v0.1.9-6-gHASH`, `…-dirty`).
- **release mode** — a client that installed a tagged binary (install.sh / a
  release asset). Build version is a clean semver tag (`v0.1.9`).

The mode is inferred from the build version (`selfupdate.DetectMode`): a clean
`vX.Y.Z` with no suffix ⇒ release; anything else ⇒ source. (`version.IsRelease`
alone isn't enough — it ignores the `-N-gHASH`/`-dirty` suffix a checkout build
carries, so `DetectMode` also rejects any suffix.)

## Shipped (read-only foundation)

`internal/selfupdate` + `dejima update`:

- `DetectMode()` — source vs release.
- `LatestRelease(ctx)` — newest tag from the GitHub releases API.
- `Check(ctx)` / `Evaluate()` — compare current vs latest (via `version.Compare`).
- `dejima update [--check]` — reports `current` / `latest` / `mode` / whether an
  update is available, and (without `--check`) prints the manual steps for the
  detected mode. **No mutation.** This is the honest, safe surface today.

## Deferred (the mutating slices — each wants its own review)

These replace running software, so they ship behind explicit review, not
auto-applied:

1. **Source-mode apply.** `git pull --ff-only` in the checkout → `make install`
   → `dejima service restart`. Decisions: locate the checkout (record it at
   install time, or require running from it?); refuse if the tree is dirty / not
   fast-forwardable; daemon-restart UX (in-flight sessions).
2. **Release-mode apply.** Resolve the right asset for `GOOS/GOARCH` from the
   latest release, download, **verify** (checksum/signature — a self-replacing
   binary MUST verify provenance), atomically replace the on-disk binary
   (write-tmp + rename, handle the running-binary-busy case), then
   `dejima service restart`. Decisions: checksum vs signature; where `dejimad`'s
   binary lives vs `dejima`'s; privilege (a `--system` install may need sudo).
3. **Daemon coordination.** `dejima` and `dejimad` are two binaries; an update
   must move them together and restart the service so `api_version` skew
   (`version.APIVersion`) doesn't strand a new client against an old daemon.
4. **`--apply` / `--yes` flags** on `dejima update` to opt into the above once
   built, with a dry-run default.

Security note: slice 2's download-and-replace is the highest-risk surface in the
product — an unverified fetch is remote code execution by design. It must not
ship without provenance verification and should go through `/security-review`.
