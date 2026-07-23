# Site demo tapes (vhs)

Terminal GIFs for the website, scripted with [vhs](https://github.com/charmbracelet/vhs)
so they're reproducible and leak nothing real.

## Render

```
brew install vhs          # or: go install github.com/charmbracelet/vhs@latest
make build                # ensure ./bin/dejima is current, and on PATH
vhs assets/tapes/01-fleet-dashboard.tape
```

Each tape writes its GIF to `assets/<name>.gif` (the path the site `<img>` tags
point at). Re-run a tape to regenerate its GIF; they overwrite in place.

## The tapes

| tape | GIF | needs a daemon? |
| --- | --- | --- |
| `01-fleet-dashboard.tape` | `fleet-dashboard.gif` | **No** — `dejima --demo` |
| `02-secrets.tape` | `secrets.gif` | **No** — `dejima --demo` |
| `04-first-island.tape` | `first-island.gif` | **No** — `dejima --demo` |
| `03-file-to-agent.tape` | `file-to-agent.gif` | **Yes** — a real island + a local file |

`--demo` drives a synthetic fleet with no daemon and no real repos, paths, or
secrets — safe to publish as-is. `03` is the exception: it shows a real upload
into a live session, so it needs a running island (see the header in the tape
for setup). Render it on the host that has your fleet.

## Notes

- These scripts encode exact keystrokes and sleeps; they have NOT been rendered
  in CI, so eyeball the first run and nudge the `Sleep` timings if a step is cut
  off or lingers.
- Keep `Width`/`Height`/`FontSize` consistent across tapes so the GIFs read as
  one set on the page.
- The demo scaffolding lives in `cmd/dejima/tui_demo.go` (synthetic fleet, repo
  list, and secrets) behind the root `--demo` flag.
