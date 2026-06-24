package pasteimg

import (
	"errors"
	"runtime"
	"testing"
)

// TestCaptureUnsupportedDegrades asserts that on a non-darwin host (this CI
// runs on linux) Capture degrades to ErrUnsupported rather than panicking or
// returning bytes — the graceful-degradation contract the bridge relies on.
func TestCaptureUnsupportedDegrades(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin has a real capturer; this asserts the fallback path")
	}
	if Supported() {
		t.Fatalf("Supported() = true on %s, want false", runtime.GOOS)
	}
	img, err := Capture()
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Capture err = %v, want ErrUnsupported", err)
	}
	if img.Bytes != nil {
		t.Fatalf("Capture returned bytes on an unsupported platform")
	}
}
