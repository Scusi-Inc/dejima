package voicein

import "testing"

// Real `ffmpeg -list_devices true -f dshow -i dummy` output. ffmpeg writes this
// to stderr and exits non-zero, and every line is quoted — including the
// "Alternative name" lines, which are NOT selectable device names. Parsing has
// to key on the trailing (audio) marker, not on quotes.
const sampleDshowOutput = `[dshow @ 000001d1] "HD Pro Webcam C920" (video)
[dshow @ 000001d1]   Alternative name "@device_pnp_\\?\usb#vid_046d"
[dshow @ 000001d1] "Headset Microphone (Corsair HS70)" (audio)
[dshow @ 000001d1]   Alternative name "@device_cm_{33D9A762}\wave_{A1B2}"
[dshow @ 000001d1] "Microphone Array (Realtek(R) Audio)" (audio)
[dshow @ 000001d1]   Alternative name "@device_cm_{33D9A762}\wave_{C3D4}"
dummy: Immediate exit requested
`

func TestParseDshowAudioDevices(t *testing.T) {
	got := parseDshowAudioDevices(sampleDshowOutput)
	want := []string{
		"Headset Microphone (Corsair HS70)",
		"Microphone Array (Realtek(R) Audio)",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d devices %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("device %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// Video devices and the quoted "Alternative name" lines must never be offered
// as microphones — picking one produces a recorder that captures nothing.
func TestParseDshowIgnoresVideoAndAlternativeNames(t *testing.T) {
	for _, d := range parseDshowAudioDevices(sampleDshowOutput) {
		if d == "HD Pro Webcam C920" {
			t.Error("offered a video device as a microphone")
		}
		if len(d) > 8 && d[:8] == "@device_" {
			t.Errorf("offered an alternative-name string as a device: %q", d)
		}
	}
}

func TestParseDshowEmptyInput(t *testing.T) {
	if got := parseDshowAudioDevices(""); len(got) != 0 {
		t.Errorf("empty output should yield no devices, got %q", got)
	}
	if got := parseDshowAudioDevices("no devices here\n"); len(got) != 0 {
		t.Errorf("unrelated output should yield no devices, got %q", got)
	}
}

// Device names routinely contain spaces and parentheses, so the whole
// `audio=<name>` must be ONE argv element — splitting it makes ffmpeg reject
// the input.
func TestWindowsRecorderArgsKeepDeviceIntact(t *testing.T) {
	const dev = "Microphone Array (Realtek(R) Audio)"
	args := recorderArgsOn("windows", "ffmpeg", "out.wav", dev)

	var found bool
	for _, a := range args {
		if a == "audio="+dev {
			found = true
		}
	}
	if !found {
		t.Errorf("device name was not passed as a single audio= argument: %q", args)
	}
	if !contains(args, "dshow") {
		t.Errorf("windows capture must use the dshow input format: %q", args)
	}
}

// The other platforms address a system default and must not grow an empty
// device argument when none is supplied.
func TestRecorderArgsPerPlatform(t *testing.T) {
	mac := recorderArgsOn("darwin", "ffmpeg", "out.wav", "")
	if !contains(mac, "avfoundation") || !contains(mac, ":default") {
		t.Errorf("darwin args = %q, want avfoundation + :default", mac)
	}
	lin := recorderArgsOn("linux", "ffmpeg", "out.wav", "")
	if !contains(lin, "alsa") || !contains(lin, "default") {
		t.Errorf("linux args = %q, want alsa + default", lin)
	}
	for _, args := range [][]string{mac, lin} {
		for _, a := range args {
			if len(a) > 6 && a[:6] == "audio=" {
				t.Errorf("non-windows args should carry no audio= device: %q", args)
			}
		}
	}
	// Every platform must produce 16 kHz mono, which is what whisper expects.
	for name, args := range map[string][]string{
		"darwin": mac, "linux": lin,
		"windows": recorderArgsOn("windows", "ffmpeg", "out.wav", "Mic"),
	} {
		if !contains(args, "16000") || !contains(args, "1") {
			t.Errorf("%s args = %q, want 16 kHz mono", name, args)
		}
	}
}

// The saved device round-trips, and the env var overrides it for one run.
func TestSavedDeviceRoundTripAndOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir()) // windows home
	t.Setenv(DeviceEnvVar, "")

	if got := SavedDevice(); got != "" {
		t.Errorf("no device saved yet, got %q", got)
	}
	if err := SaveDevice("Headset Microphone (Corsair HS70)"); err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}
	if got := SavedDevice(); got != "Headset Microphone (Corsair HS70)" {
		t.Errorf("SavedDevice = %q, want the saved name", got)
	}

	t.Setenv(DeviceEnvVar, "Microphone Array (Realtek(R) Audio)")
	if got := SavedDevice(); got != "Microphone Array (Realtek(R) Audio)" {
		t.Errorf("env override ignored; got %q", got)
	}
}
