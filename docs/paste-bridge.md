# Host→island image-paste bridge (`dejima paste`)

A one-shot bridge to get an image off your **host clipboard** into an island and
in front of an agent, without saving a file and `cp`-ing it by hand.

```
dejima paste <island>[/<agent>]
```

1. **Capture** an image off the host system clipboard.
2. **Stage** the bytes into the island's intake dir.
3. **Inject** a `Read <path>` line into the target agent's prompt.

## Reusing the intake dir (no new channel)

The image lands under the island's Port intake tree —
`/home/dejima/intake/paste/clip-<timestamp>.png` — the same `~/intake/<scope>/…`
convention `dejima port intake` writes brokered host files into (see
`docs/port-island-spec.md`). `/home/dejima` is writable by the island's `dejima`
user; a top-level `/intake` is not (the container root is root-owned). Pastes get
their own `paste` scope dir so they're distinguishable from brokered host-file
intakes.

Staging uses the existing `PUT /v1/islands/{name}/files/{path}` endpoint
(`dejima cp`'s write path) and injection uses the existing exec endpoint to run
the same `tmux send-keys` the mailbox wake path uses. **No new API route, no Port
grant required, no grant routes touched** — a paste is a host-operator action,
not a brain-driven Trade, so it doesn't need a scope grant.

## Host capture seam (`internal/pasteimg`)

Capturing an image off the clipboard is platform-specific, so it lives behind a
single `pasteimg.Capture()` with one build-tagged implementation per OS:

- **macOS** (`pasteimg_darwin.go`): `osascript` coerces the clipboard to
  `«class PNGf»` and writes the PNG to a temp file — no third-party `pngpaste`
  dependency. An empty / non-image clipboard surfaces as `ErrNoImage`.
- **everything else** (`pasteimg_other.go`): returns `ErrUnsupported`.

The bridge **degrades gracefully**: on an unsupported host or an empty clipboard
it prints actionable advice ("save the image and use `dejima cp`") rather than
failing opaquely. Capture is a convenience, never a hard dependency.

## Flags

- `--agent <id>` — target a specific agent (default: the island's first
  interactive/attachable agent — the one with a prompt to inject into).
- `--no-inject` — stage the image but don't inject the `Read` line (you'll be
  told the path to reference).

## Where it doesn't auto-capture

On Linux/Windows hosts there is no capturer yet; `dejima paste` tells you to:

```
dejima cp ./img.png <island>:/home/dejima/intake/paste/img.png
```

…then tell the agent `Read /home/dejima/intake/paste/img.png`. Adding a
capturer for another host OS is just a new build-tagged `capture()` in
`internal/pasteimg`.
