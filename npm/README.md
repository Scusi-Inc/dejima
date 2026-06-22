# dejima (CLI)

The `dejima` command-line client, installable from npm. Drives a [Dejima](https://dejima.tech/)
host — create islands, connect to agents, run one-shot commands — from macOS, Linux, or Windows.

```bash
npm install -g dejima
dejima --version
```

On install, this package downloads the prebuilt `dejima` binary matching its
version from the [GitHub Release](https://github.com/aoos/dejima/releases),
checksum-verifies it, and puts a `dejima` command on your PATH. No Go toolchain
required.

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

- `DEJIMA_SKIP_DOWNLOAD=1` — skip the postinstall download (offline / sandboxed
  CI). Provide your own binary at runtime with `DEJIMA_BINARY=/path/to/dejima`.
- `DEJIMA_BINARY=/path/to/dejima` — run a specific binary instead of the
  downloaded one.

## Notes

- Requires Node 16+ and a `tar` on PATH (bundled on macOS, Linux, and Windows 10+).
- macOS binaries are currently unsigned; the installer strips the Gatekeeper
  quarantine attribute on download. Notarization is on the roadmap.
