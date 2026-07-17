package voicein

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCleanTranscript(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  Hello there.  ", "Hello there."},
		{"[BLANK_AUDIO]", ""},
		{"(silence) open the file (typing)", "open the file"},
		{"line one\n line two\n", "line one line two"},
		{"[ music ] refactor   the   parser", "refactor the parser"},
		{"", ""},
		{"   \n  ", ""},
		{"push to (main) branch", "push to branch"}, // parenthetical dropped (whisper non-speech convention)
		// Control bytes are stripped so nothing can break out of the bracketed-
		// paste envelope we inject into.
		{"hello\x1bworld", "helloworld"}, // bare ESC removed
		{"say\x07 hi", "say hi"},         // BEL removed, word boundary kept
		{"drop\x7fchar", "dropchar"},     // DEL removed
		{"tabs\tand\nnewlines", "tabs and newlines"},
	}
	for _, c := range cases {
		if got := cleanTranscript(c.in); got != c.want {
			t.Errorf("cleanTranscript(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCleanTranscriptStripsPasteTerminator: an embedded bracketed-paste
// terminator (ESC[201~) must not survive into the injected transcript, or a
// spoken-through sequence could close the paste envelope early.
func TestCleanTranscriptStripsPasteTerminator(t *testing.T) {
	for _, in := range []string{
		"hello \x1b[201~ world",
		"\x1b[201~rm -rf",
		"ok\x1b[200~nested\x1b[201~done",
	} {
		got := cleanTranscript(in)
		if strings.ContainsRune(got, '\x1b') {
			t.Errorf("cleanTranscript(%q) = %q still contains ESC", in, got)
		}
		if strings.Contains(got, "[201~") || strings.Contains(got, "[200~") {
			t.Errorf("cleanTranscript(%q) = %q still contains a paste marker body", in, got)
		}
	}
}

func TestRecorderArgsShape(t *testing.T) {
	// Every supported recorder must target a 16 kHz mono capture at the wav path.
	for _, tool := range []string{"rec", "arecord", "ffmpeg"} {
		args := recorderArgs(tool, "/tmp/x.wav")
		if len(args) == 0 {
			t.Fatalf("recorderArgs(%q) empty", tool)
		}
		if args[len(args)-1] != "/tmp/x.wav" {
			t.Errorf("recorderArgs(%q) last arg = %q, want the wav path", tool, args[len(args)-1])
		}
		if !contains(args, "16000") {
			t.Errorf("recorderArgs(%q) = %v, want a 16000 sample rate", tool, args)
		}
	}
	if recorderArgs("nope", "/tmp/x.wav") != nil {
		t.Error("recorderArgs for an unknown tool should be nil")
	}
}

func TestWhisperArgs(t *testing.T) {
	got := whisperArgs("/m/base.en.bin", "/tmp/x.wav")
	want := []string{"-m", "/m/base.en.bin", "-f", "/tmp/x.wav", "-nt", "-np"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("whisperArgs = %v, want %v", got, want)
	}
}

func TestPlanInstall(t *testing.T) {
	// Nothing present → install both tools + model.
	full := PlanInstall(Status{Supported: true, ModelPath: "/m/base.en.bin"})
	if !reflect.DeepEqual(full.BrewPackages, []string{"whisper-cpp", "sox"}) {
		t.Errorf("full plan brew = %v, want [whisper-cpp sox]", full.BrewPackages)
	}
	if full.ModelURL == "" || full.ModelDest != "/m/base.en.bin" {
		t.Errorf("full plan should download the model to the status path; got %+v", full)
	}
	if full.Empty() {
		t.Error("full plan should not be Empty")
	}

	// Everything present → empty plan (idempotent re-run).
	none := PlanInstall(Status{Supported: true, WhisperBin: "whisper-cli", Recorder: "rec", ModelPresent: true})
	if !none.Empty() {
		t.Errorf("nothing-missing plan should be Empty, got %+v", none)
	}

	// Only the model missing → no brew, just the download.
	modelOnly := PlanInstall(Status{Supported: true, WhisperBin: "whisper-cli", Recorder: "rec", ModelPath: "/m/x.bin"})
	if len(modelOnly.BrewPackages) != 0 || modelOnly.ModelURL == "" {
		t.Errorf("model-only plan = %+v, want just the download", modelOnly)
	}
}

func TestStatusReadyAndMissing(t *testing.T) {
	ready := Status{Supported: true, WhisperBin: "whisper-cli", Recorder: "rec", ModelPresent: true}
	if !ready.Ready() || len(ready.Missing()) != 0 {
		t.Errorf("ready status: Ready=%v Missing=%v", ready.Ready(), ready.Missing())
	}
	partial := Status{Supported: true, Recorder: "rec"} // whisper + model missing
	if partial.Ready() {
		t.Error("partial status should not be Ready")
	}
	if len(partial.Missing()) != 2 {
		t.Errorf("partial Missing = %v, want 2 items", partial.Missing())
	}
	unsup := Status{Supported: false}
	if unsup.Ready() || len(unsup.Missing()) != 1 {
		t.Errorf("unsupported status: Ready=%v Missing=%v", unsup.Ready(), unsup.Missing())
	}
}

func TestModelPathEnvOverride(t *testing.T) {
	// Absolute override is used verbatim.
	t.Setenv("DEJIMA_VOICE_MODEL", "/opt/models/small.en.bin")
	if p, _ := ModelPath(); p != "/opt/models/small.en.bin" {
		t.Errorf("abs override ModelPath = %q", p)
	}
	// Bare name resolves under the per-user model dir.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEJIMA_VOICE_MODEL", "medium.en.bin")
	p, err := ModelPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "medium.en.bin" || !filepath.IsAbs(p) {
		t.Errorf("bare-name override ModelPath = %q, want an abs path ending medium.en.bin", p)
	}
}

func TestCheckReadyWithStubs(t *testing.T) {
	// Stub tool discovery so Check() reports Ready without real binaries; place a
	// non-empty model file where ModelPath resolves.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DEJIMA_VOICE_MODEL", "")
	mp, _ := ModelPath()
	if err := os.MkdirAll(filepath.Dir(mp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mp, []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := lookPath
	lookPath = func(f string) (string, error) { return "/usr/local/bin/" + f, nil } // everything "found"
	defer func() { lookPath = old }()

	if !Supported() {
		t.Skip("recorder set empty on this GOOS; Check readiness not applicable")
	}
	st := Check()
	if !st.Ready() {
		t.Fatalf("Check() not Ready with stubbed tools + model: %+v", st)
	}
	if st.WhisperBin == "" || st.Recorder == "" || !st.ModelPresent {
		t.Errorf("Check() = %+v, want all components resolved", st)
	}
}

func TestPlanInstallPinsModelChecksum(t *testing.T) {
	// The default model carries its pinned checksum into the plan.
	def := PlanInstall(Status{Supported: true, ModelPath: "/home/u/.dejima/voice/models/ggml-base.en.bin"})
	if def.ModelSHA256 != modelSHA256["ggml-base.en.bin"] || def.ModelSHA256 == "" {
		t.Errorf("default plan ModelSHA256 = %q, want the pinned base.en sum", def.ModelSHA256)
	}
	// A custom (unpinned) model downloads without a checksum rather than blocking.
	custom := PlanInstall(Status{Supported: true, ModelPath: "/models/my-custom.bin"})
	if custom.ModelSHA256 != "" {
		t.Errorf("custom-model plan ModelSHA256 = %q, want empty (unverified)", custom.ModelSHA256)
	}
}

func TestDownloadModelChecksum(t *testing.T) {
	body := []byte("pretend-ggml-weights")
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	// Correct checksum → the model lands.
	dest := filepath.Join(t.TempDir(), "models", "ggml-base.en.bin")
	if err := downloadModel(context.Background(), srv.URL, dest, want); err != nil {
		t.Fatalf("download with correct checksum failed: %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != string(body) {
		t.Errorf("downloaded content = %q, want %q", got, body)
	}

	// Wrong checksum → refused, and nothing is left at dest (no half-trusted file).
	dest2 := filepath.Join(t.TempDir(), "models", "ggml-base.en.bin")
	err := downloadModel(context.Background(), srv.URL, dest2, "deadbeef")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("wrong-checksum download err = %v, want a checksum-mismatch error", err)
	}
	if _, statErr := os.Stat(dest2); statErr == nil {
		t.Error("a checksum-mismatched download must not leave a file at dest")
	}

	// Empty want → unverified download still works (custom model path).
	dest3 := filepath.Join(t.TempDir(), "custom.bin")
	if err := downloadModel(context.Background(), srv.URL, dest3, ""); err != nil {
		t.Fatalf("unverified download failed: %v", err)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
