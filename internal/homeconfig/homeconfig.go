// Package homeconfig holds the config scaffolds `dejima home configure` writes
// into a Home Island's workspace to get an assistant brain from "booted but
// unconfigured and idling" to "configured and doing work".
//
// The scaffolds are intentionally data-only and framework-keyed (openclaw today;
// Hermes/Letta slot in later) so the command stays a thin writer. They are a
// starting point an operator edits in the island workspace — never a complete,
// secret-bearing config. Secrets are never scaffolded to disk in plaintext; see
// SECRETS.md in the scaffold and `dejima home configure`'s printed guidance.
package homeconfig

import (
	"fmt"
	"sort"
)

// File is one scaffold entry: a path relative to the island workspace and its
// initial contents.
type File struct {
	// Path is relative to /workspace inside the island (e.g.
	// "openclaw.config.toml"). No leading slash.
	Path string
	// Body is the initial file contents.
	Body []byte
	// ConfigFile marks the framework's primary config file — the one
	// `dejima home doctor` checks for and whose presence flips an island from
	// "unconfigured" to "configured".
	ConfigFile bool
}

// Frameworks lists the assistant frameworks with a known scaffold.
func Frameworks() []string {
	out := make([]string, 0, len(templates))
	for k := range templates {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ConfigPath returns the framework's primary config file path (relative to the
// workspace), or "" if the framework is unknown. `dejima home doctor` uses it to
// probe whether the brain is configured.
func ConfigPath(framework string) string {
	for _, f := range templates[framework] {
		if f.ConfigFile {
			return f.Path
		}
	}
	return ""
}

// Template returns the scaffold files for a framework. An unknown framework
// returns (nil, error) listing the known ones.
func Template(framework string) ([]File, error) {
	files, ok := templates[framework]
	if !ok {
		return nil, fmt.Errorf("no scaffold for framework %q (known: %v)", framework, Frameworks())
	}
	return files, nil
}

// templates is the per-framework scaffold. Keep these minimal and heavily
// commented — they are a starting point the operator fills in, not a finished
// config, and they must contain no secrets.
var templates = map[string][]File{
	"openclaw": {
		{
			Path:       "openclaw.config.toml",
			ConfigFile: true,
			Body: []byte(`# OpenClaw config — scaffolded by ` + "`dejima home configure`" + `.
# This is a STARTING POINT. Edit it in the island workspace, then commit it to
# the brain's config repo so it survives a rebuild. Channel credentials do NOT
# go here in plaintext — see SECRETS.md.

[gateway]
# Drop --allow-unconfigured from the launch once this file exists and is filled
# in; until then the gateway idles waiting for config.
name = "home-brain"

# Channels the brain reads. These are UNTRUSTED inbound surfaces (the prime
# prompt-injection vector) — which is exactly why the brain runs contained in a
# Home Island rather than native on the host. Enable only what you need.
[channels]
# slack = { enabled = false }
# email = { enabled = false }

# How the brain reaches your files and spins up work. In Dejima the brain has NO
# raw host access: it reads host files only through the Port (scoped + ledgered)
# and creates Project Islands via the daemon. See docs/using-openclaw.md.
[dejima]
# Grant scopes from the host with:  dejima port grant <island> <path>:ro
# Inside the island the brain shells out to the dejima CLI, which authenticates
# with its injected DEJIMA_TOKEN automatically.
use_port = true
`),
		},
		{
			Path: ".gitignore",
			Body: []byte(`# Never commit secrets to the brain config repo.
*.env
secrets/
.openclaw/secrets*
*.key
*.pem
`),
		},
		{
			Path: "SECRETS.md",
			Body: []byte(`# Channel credentials for this brain

Secrets are **never** written here in plaintext and are **never** scaffolded to
disk. The brain reads untrusted channels (chat, email) — its credentials are the
keys to those accounts, so they stay out of the config repo. Two safe paths:

## 1. Set them inside the island (ephemeral, not in git)

    dejima exec <island> -- openclaw secrets set slack <token>

Runs in the island's home volume; survives restarts, never touches the repo.

## 2. Broker a creds file through the Port (audited)

Put the file on the host, grant a read-only scope, intake it — every crossing is
recorded in the tamper-evident ledger (` + "`dejima audit`" + `):

    dejima port grant  <island> ~/brain-secrets:ro
    dejima port intake <island> brain-secrets:creds.env

Then point the brain at /home/dejima/intake/brain-secrets/creds.env.

Either way the secret crosses a boundary you control and can audit — never a raw
host mount.
`),
		},
	},
}
