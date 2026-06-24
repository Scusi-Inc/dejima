//go:build !darwin

package pasteimg

const supported = false

// capture is the fallback for platforms without a clipboard-image capturer.
// The bridge degrades gracefully: callers surface ErrUnsupported as advice
// (e.g. "save the image and use `dejima cp`") rather than failing hard.
func capture() (Image, error) { return Image{}, ErrUnsupported }
