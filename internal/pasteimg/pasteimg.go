// Package pasteimg is the host-side clipboard-image capture seam for the
// host→island image-paste bridge. Capturing an image off the system clipboard
// is inherently platform-specific, so the capture itself lives behind a single
// Capture() function with one implementation per OS (build-tagged files). The
// rest of the bridge — staging the bytes into the island's intake dir and
// injecting a `Read <path>` line — is platform-neutral and lives in the CLI.
//
// When the running platform has no clipboard-image capture (or the clipboard
// holds no image), Capture returns ErrNoImage / ErrUnsupported and the caller
// degrades gracefully: the bridge is a convenience, never a hard dependency.
package pasteimg

import "errors"

// ErrUnsupported is returned on platforms without a clipboard-image capturer.
var ErrUnsupported = errors.New("clipboard image capture is not supported on this platform")

// ErrNoImage is returned when the platform supports capture but the clipboard
// currently holds no image (e.g. it holds text, or is empty).
var ErrNoImage = errors.New("no image on the clipboard")

// Image is a captured clipboard image: the raw encoded bytes plus the file
// extension (no dot, e.g. "png") describing their encoding.
type Image struct {
	Bytes []byte
	Ext   string
}

// Capture reads an image off the host system clipboard. It returns ErrNoImage
// when the clipboard holds no image and ErrUnsupported on platforms without a
// capturer. Implementations are selected by build tags (pasteimg_darwin.go,
// pasteimg_other.go).
func Capture() (Image, error) { return capture() }

// Supported reports whether this platform has a clipboard-image capturer at all,
// so callers can tailor help text without attempting a capture.
func Supported() bool { return supported }
