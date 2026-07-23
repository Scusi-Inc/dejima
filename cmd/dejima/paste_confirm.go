package main

import (
	"os"
	"strings"
)

// pendingUpload holds a detected file-path paste awaiting the operator's decision
// in a plain shell: [u]/[y] uploads it to the agent, any other key forwards the
// path as text (nothing is ever silently uploaded).
type pendingUpload struct {
	path      string // client-local file to upload on confirm
	bracketed []byte // the original bracketed paste, forwarded as text on decline
}

// pastePolicy is how a pasted existing-file PATH is handled.
type pastePolicy int

const (
	pasteAsText  pastePolicy = iota // forward the path as text — no upload
	pasteConfirm                    // ask before uploading (plain shell only)
	pasteUpload                     // upload immediately + inject the in-island path
)

// pasteUploadEnabled reports whether pasting a local file PATH may upload the file
// (the drag-drop convenience). Default on; DEJIMA_PASTE_UPLOAD=off/none/0/false/no
// disables it, so a pasted path is always text (explicit upload stays available
// via the attach chord / `dejima attach`).
func pasteUploadEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DEJIMA_PASTE_UPLOAD"))) {
	case "off", "none", "0", "false", "no":
		return false
	default:
		return true
	}
}

// pasteDropPolicy decides how a pasted existing-file PATH is handled. Uploading is
// never the silent default: when auto-upload is disabled, or a full-screen TUI is
// attached (where a confirm prompt can't be drawn without corrupting its screen —
// the reason the notice bug existed), the path is just text. Only a plain shell
// gets the confirm-before-upload affordance.
func pasteDropPolicy(altScreenActive bool) pastePolicy {
	if !pasteUploadEnabled() {
		return pasteAsText
	}
	if altScreenActive {
		// Inside an agent's full-screen TUI a [u] confirm can't be drawn without
		// corrupting its screen — but a dragged FILE is a deliberate act, and the
		// host path pasted as text is useless in the container anyway. So upload
		// it and inject the in-island path, no prompt. (The scanner only fires on
		// paths that resolve to a real local file, so a stray text paste isn't
		// mistaken for a drop.)
		return pasteUpload
	}
	return pasteConfirm
}
