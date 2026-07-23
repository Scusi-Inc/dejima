package voicein

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Windows capture goes through ffmpeg's dshow input, which — unlike macOS
// avfoundation and Linux ALSA — has no "default" device. It wants the literal
// name, e.g. `audio=Headset Microphone (Corsair HS70)`. A typical Windows box
// has several (headset, webcam array, laptop built-in), so there is no safe
// automatic choice: this package enumerates them and remembers the operator's
// pick.

// DeviceEnvVar overrides the stored device for one run.
const DeviceEnvVar = "DEJIMA_VOICE_DEVICE"

// deviceFile is where the chosen capture device is remembered, under Dir().
const deviceFile = "device"

// dshowAudioLine matches ffmpeg's device dump, which looks like:
//
//	[dshow @ 0000...] "Headset Microphone (HS70)" (audio)
//	[dshow @ 0000...]   Alternative name "@device_cm_{...}"
//
// Only the quoted friendly name on an (audio) line is wanted; the "Alternative
// name" lines are also quoted, hence the trailing-marker requirement.
var dshowAudioLine = regexp.MustCompile(`"([^"]+)"\s*\(audio\)`)

// parseDshowAudioDevices pulls the audio capture device names out of ffmpeg's
// `-list_devices true` output, in the order ffmpeg reported them. Pure, so the
// parsing is testable without ffmpeg or Windows.
func parseDshowAudioDevices(ffmpegOutput string) []string {
	var out []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(ffmpegOutput))
	for sc.Scan() {
		m := dshowAudioLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		name := strings.TrimSpace(m[1])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// ListDevices returns the microphones this host can capture from.
//
// Only Windows needs it — macOS and Linux address a default device — so
// elsewhere it returns nothing and callers keep using "default".
func ListDevices(ctx context.Context) ([]string, error) {
	if !needsNamedDevice() {
		return nil, nil
	}
	ff, err := lookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg not found — it provides microphone capture on Windows")
	}
	// ffmpeg writes the device dump to stderr and exits non-zero (the dummy
	// input never opens), so the error is expected and the output is the point.
	cmd := exec.CommandContext(ctx, ff, "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()

	devices := parseDshowAudioDevices(stderr.String())
	if len(devices) == 0 {
		return nil, fmt.Errorf("no microphones found (ffmpeg -list_devices reported none)")
	}
	return devices, nil
}

// SaveDevice remembers the chosen capture device.
func SaveDevice(name string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, deviceFile), []byte(strings.TrimSpace(name)+"\n"), 0o600)
}

// SavedDevice returns the remembered capture device ("" when unset). The env
// override wins so a run can target another mic without disturbing the default.
func SavedDevice() string {
	if v := strings.TrimSpace(os.Getenv(DeviceEnvVar)); v != "" {
		return v
	}
	dir, err := Dir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, deviceFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// resolveDevice picks the capture device for a recording.
//
// Platforms with a system default need no choice. Where a name IS required, a
// saved pick wins; failing that we fall back to the first device ffmpeg lists,
// so a first run works before anyone has visited the picker — with the caveat
// that "first" on Windows is arbitrary, which is why the picker exists.
func resolveDevice(ctx context.Context) (string, error) {
	if !needsNamedDevice() {
		return "", nil
	}
	if d := SavedDevice(); d != "" {
		return d, nil
	}
	devices, err := ListDevices(ctx)
	if err != nil {
		return "", err
	}
	return devices[0], nil
}
