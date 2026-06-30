# dejima (CLI)

The `dejima` command-line client, installable from npm. Drives a [Dejima](https://dejima.tech/)
host — create islands, connect to agents, run one-shot commands — from macOS, Linux, or Windows.

```bash
npm install -g dejima
dejima --version
```

The prebuilt `dejima` binary for your platform ships inside a per-platform
package (`@dejima/cli-<platform>-<arch>`) that npm installs automatically as an
optional dependency — only the one matching your OS/CPU. There's **no install
script** (so it works under npm 11's default script-blocking), and no Go
toolchain required; the `dejima` command lands on your PATH.

## What this installs (and what it doesn't)

This package ships the **CLI client only** — the cross-platform binary you use to
drive a host. That's all most people on a laptop need:

```bash
export DEJIMA_HOST=your-host.tailnet.ts.net:7273
dejima ls
dejima connect <island>
```

It does **not** install the daemon (`dejimad`) or the island Docker image. Those
run only on a Unix **host** (your Mac mini / Linux box) and need Docker. Set a
host up with:

```bash
curl -fsSL https://dejima.tech/install.sh | bash   # full host: binaries + image + service
# or
brew install aoos/dejima/dejima                     # binaries via Homebrew
```

See <https://dejima.tech/> for the full picture.

## Other install channels

| Command | Gets you |
|---------|----------|
| `npm install -g dejima` | the CLI client (this package) |
| `brew install aoos/dejima/dejima` | CLI + daemon binaries |
| `curl -fsSL https://dejima.tech/install.sh \| bash` | full host (binaries + image + service) |

## Environment knobs

- `DEJIMA_BINARY=/path/to/dejima` — run a specific binary instead of the bundled
  platform one (offline installs, `npm i --no-optional`, or a binary you built).

## Notes

- Requires Node 16+.
- macOS binaries are currently unsigned. When downloaded via npm, Gatekeeper may
  quarantine them; if macOS blocks the binary, clear it with
  `xattr -d com.apple.quarantine "$(npm root -g)/dejima/node_modules/@dejima/cli-darwin-arm64/bin/dejima"`
  (adjust the arch). Notarization is on the roadmap.
