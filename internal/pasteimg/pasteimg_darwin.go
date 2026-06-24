//go:build darwin

package pasteimg

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const supported = true

// capture pulls a PNG off the macOS clipboard via AppleScript (osascript),
// which is always present on macOS — no third-party `pngpaste` needed. The
// script coerces the clipboard to PNG and writes it to a temp file; we read the
// bytes back and clean up. If the clipboard holds no image, the coercion fails
// and we report ErrNoImage rather than an opaque osascript error.
func capture() (Image, error) {
	tmp, err := os.CreateTemp("", "dejima-paste-*.png")
	if err != nil {
		return Image{}, err
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)

	// «class PNGf» is the clipboard's PNG flavor. `the clipboard as «class PNGf»`
	// throws if no PNG flavor is present, which is exactly the "no image" case.
	script := fmt.Sprintf(
		`set png to (the clipboard as «class PNGf»)`+"\n"+
			`set f to open for access POSIX file %q with write permission`+"\n"+
			`set eof f to 0`+"\n"+
			`write png to f`+"\n"+
			`close access f`, path)

	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		// osascript reports a coercion failure when there's no image flavor.
		if isNoImageErr(string(out)) {
			return Image{}, ErrNoImage
		}
		return Image{}, fmt.Errorf("osascript clipboard capture: %w: %s", err, strings.TrimSpace(string(out)))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Image{}, err
	}
	if len(b) == 0 {
		return Image{}, ErrNoImage
	}
	return Image{Bytes: b, Ext: "png"}, nil
}

// isNoImageErr recognizes the osascript errors that mean "the clipboard has no
// image to coerce" so the caller can degrade with a friendly message.
func isNoImageErr(out string) bool {
	o := strings.ToLower(out)
	return strings.Contains(o, "can't make") || // can't make «class PNGf» into the expected type
		strings.Contains(o, "can't get") ||
		strings.Contains(o, "no clip") ||
		strings.Contains(o, "-1700") // errAECoercionFail
}
