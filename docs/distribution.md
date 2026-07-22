# Distribution & going public

How `dejima` reaches users, what's already wired up in this repo, and the
one-time manual steps that are yours to do (registrar DNS, GitHub repo/secret
creation, license choice). The install matrix users see lives in the
[README](../README.md#install).

## Channels at a glance

| Channel | User command | Delivers | Status |
|---------|--------------|----------|--------|
| Source curl | `curl -fsSL https://dejima.tech/install.sh \| bash` | full host (build + image + service) | ✅ live |
| Client curl | `curl -fsSL https://dejima.tech/install-client.sh \| bash` | CLI binary | ✅ live |
| Windows PS | `irm https://dejima.tech/install-client.ps1 \| iex` | CLI binary | ✅ live |
| `go install` | `go install github.com/aoos/dejima/cmd/dejima@latest` | CLI from source | ✅ live |
| Homebrew | `brew install aoos/dejima/dejima` | dejima + dejimad binaries | ⏳ needs tap repo + token |
| npm | `npm install -g dejima` | CLI binary | ⏳ needs `NPM_TOKEN` |

All binary channels download the same [GitHub Release](https://github.com/aoos/dejima/releases)
tarballs and checksum-verify against `SHA256SUMS`. The release pipeline
(`.github/workflows/release.yml` + `make release-binaries`) is live and fires on
every `v*` tag.

---

## ✅ Already done in-repo

- **Custom domain** — `CNAME` (`dejima.tech`) committed; all install/landing/API
  URLs point at it.
- **GitHub Pages** — enabled, serving `master:/(root)` at `aoos.github.io/dejima/`.
- **Release binaries** — tag-driven cross-compile of darwin/linux/windows ×
  arm64/amd64, published with `SHA256SUMS`.
- **Homebrew formula + auto-bump** — `scripts/gen-homebrew-formula.sh` is the
  source of truth; `homebrew/dejima.rb` is its output for the latest release. The
  `homebrew-tap` job in `release.yml` regenerates and pushes the formula to the
  tap on each tag.
- **npm CLI package** — `npm/` (postinstall download + checksum + bin shim). The
  `publish-npm-cli` job in `release.yml` stamps the version and publishes on each
  tag. The TS SDK was renamed to `@dejima/sdk` to free the bare `dejima` name.

## 🧑 Your one-time manual steps

### 1. Point `dejima.tech` DNS at GitHub Pages

At your registrar (Cloudflare Registrar gives at-cost pricing), add for the apex
(`@`) — re-verify the IPs at
[docs.github.com/pages](https://docs.github.com/en/pages/configuring-a-custom-domain-for-your-github-pages-site/managing-a-custom-domain-for-your-github-pages-site#configuring-an-apex-domain),
GitHub rotates them occasionally:

```
TYPE   NAME    VALUE
A      @       185.199.108.153
A      @       185.199.109.153
A      @       185.199.110.153
A      @       185.199.111.153
AAAA   @       2606:50c0:8000::153
AAAA   @       2606:50c0:8001::153
AAAA   @       2606:50c0:8002::153
AAAA   @       2606:50c0:8003::153
CNAME  www     aoos.github.io.
```

Then **Settings → Pages → Custom domain** → `dejima.tech` → Save, and tick
**Enforce HTTPS** once the cert provisions (minutes to an hour). The committed
`CNAME` makes Pages pick it up automatically.

Verify:

```bash
curl -fsSL https://dejima.tech/install.sh | head -5
```

### 2. Create the Homebrew tap + token

1. Create a public repo **`aoos/homebrew-dejima`** (Homebrew discovers taps by
   this exact name). Empty is fine — the release CI populates
   `Formula/dejima.rb`. To seed it now: copy this repo's `homebrew/dejima.rb` to
   `Formula/dejima.rb` there.
2. Create a token with `contents:write` on `aoos/homebrew-dejima` (a
   fine-grained PAT scoped to that one repo, or a classic PAT with `repo`) and add
   it as the **`HOMEBREW_TAP_TOKEN`** Actions secret on `aoos/dejima`.

Then `brew install aoos/dejima/dejima` works after the next tag (or immediately
if you seeded the formula). `brew install --HEAD aoos/dejima/dejima` builds from
source and needs neither.

### 3. Add the npm token

Create an npm automation token (npmjs.com → Access Tokens) for the account that
will own `dejima` + `@dejima/sdk`, and add it as the **`NPM_TOKEN`** Actions
secret on `aoos/dejima`. The `@dejima` scope must exist on that account/org
(create the org once on npm). The next tag then publishes both `dejima` (CLI) and
`@dejima/sdk`.

### 4. Pick a license

`LICENSE`, the formula, and `npm/package.json` all say `Pre-public-release`.
Choose a real license (e.g. Apache-2.0 / MIT for OSS, or a source-available
license) and set it in all three before publishing. Required for npm/brew
metadata and for any future homebrew-core submission.

### 5. (Fast-follow) Notarize the macOS binaries

The darwin binaries are unsigned, so Gatekeeper quarantines downloads; the
install scripts + brew + the npm installer strip the quarantine xattr as a
stopgap. To sign + notarize properly, follow
[`release-notarization.md`](release-notarization.md) (Apple cert + API key + the
macOS-runner workflow diff). Not a hard blocker for 0.1.0.

---

## Recommended order

1. **DNS for `dejima.tech`** → the canonical curl one-liners go brand-pure (~30 min).
2. **`NPM_TOKEN`** → `npm install -g dejima` goes live on the next tag (~10 min).
3. **Tap repo + `HOMEBREW_TAP_TOKEN`** → `brew install aoos/dejima/dejima` goes
   live on the next tag (~15 min).
4. **License** → unblocks the metadata; do before announcing.
5. **Notarization** → fast-follow once there are real macOS downloaders.
6. **Eventually**: submit to homebrew-core for a bare `brew install dejima` (months
   of stewardship; defer until v1.x has users).
