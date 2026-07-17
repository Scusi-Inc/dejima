// Package voicein is the host-side voice-dictation bridge: capture the host
// microphone, transcribe it locally (whisper.cpp — no cloud, no subscription,
// audio never leaves the machine), and hand the transcript back to the caller,
// which injects it into a contained agent's prompt. The container never gets
// audio access — only the transcribed TEXT crosses into the island — so this
// stays on the deny-all containment posture and works in exactly the
// tmux+ssh/remote case where Claude Code's native `/voice` explicitly gives up.
//
// Everything here is host-local and dependency-light: a recorder that writes a
// 16 kHz mono WAV (sox `rec`, or `ffmpeg`/`arecord` if already present) and a
// whisper.cpp CLI + a small English model (`base.en`, ~142 MB). `dejima voice
// install` provisions both; `Check` reports readiness so the TUI can nudge.
package voicein

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

var (
	// ErrUnsupported is returned when the host platform has no supported mic
	// capture path (Windows, for now). Callers degrade with a friendly hint.
	ErrUnsupported = errors.New("voice dictation isn't supported on this host platform yet")
	// ErrNotInstalled means the whisper.cpp CLI, a recorder, or the model is
	// missing — `dejima voice install` provisions them.
	ErrNotInstalled = errors.New("voice dictation isn't set up — run `dejima voice install`")
	// ErrNoSpeech means the recording transcribed to nothing (silence / no mic
	// input) — not an error to dump, a "say something" nudge.
	ErrNoSpeech = errors.New("no speech detected")
)

// DefaultModel is the whisper.cpp model we install by default: the English-only
// base model — ~142 MB, fast on CPU, Metal-accelerated on Apple Silicon, and
// accurate enough for dictating a message. Larger models (small.en, medium.en)
// can be dropped alongside and selected via DEJIMA_VOICE_MODEL.
const DefaultModel = "ggml-base.en.bin"

// modelBaseURL is the canonical whisper.cpp GGML model host (Hugging Face). The
// install step fetches DefaultModel from here into the per-user model dir.
const modelBaseURL = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/"

// modelSHA256 pins the expected SHA-256 of each model we fetch by name, so a
// tampered/MITM'd download (the transport is HTTPS-only, but weights aren't a
// signed binary) is rejected before it lands. A model we don't have a pin for
// (a custom DEJIMA_VOICE_MODEL) is downloaded without verification — the pin
// only tightens the default path, never blocks an operator-managed model.
var modelSHA256 = map[string]string{
	"ggml-base.en.bin": "a03779c86df3323075f5e796cb2ce5029f00ec8869eee3fdfb897afe36c6d002",
}

// whisperBins are the whisper.cpp CLI names we probe, newest-first: modern builds
// ship `whisper-cli`; older ones `main`/`whisper`. Homebrew's `whisper-cpp`
// formula installs `whisper-cli`.
var whisperBins = []string{"whisper-cli", "whisper-cpp", "whisper", "main"}

// recorders lists mic-capture tools we can drive, in preference order per OS.
// `rec` (sox) is what `dejima voice install` provisions, so it's preferred; the
// others are used when already present so we don't force a second install.
func recorders() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"rec", "ffmpeg"} // sox `rec`, then ffmpeg (avfoundation)
	case "linux":
		return []string{"rec", "arecord", "ffmpeg"}
	default:
		return nil
	}
}

// Supported reports whether this host platform has any mic-capture path.
func Supported() bool { return len(recorders()) > 0 }

// Dir is the per-user voice-dictation dir (~/.dejima/voice) holding models.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".dejima", "voice"), nil
}

// ModelPath resolves the selected model file. DEJIMA_VOICE_MODEL overrides the
// name (a bare name resolves under the model dir; an absolute path is used
// as-is, so an operator can point at a model they manage).
func ModelPath() (string, error) {
	name := strings.TrimSpace(os.Getenv("DEJIMA_VOICE_MODEL"))
	if name == "" {
		name = DefaultModel
	}
	if filepath.IsAbs(name) {
		return name, nil
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "models", name), nil
}

// lookPath / command are indirected so tests can stub tool discovery + exec.
var (
	lookPath = exec.LookPath
	command  = exec.Command
)

func firstOnPath(names []string) string {
	for _, n := range names {
		if _, err := lookPath(n); err == nil {
			return n
		}
	}
	return ""
}

// Status is the readiness of the voice-dictation toolchain — the seam the CLI's
// `voice status` and the TUI tip/settings read.
type Status struct {
	Supported    bool
	WhisperBin   string // resolved whisper.cpp CLI, "" if missing
	Recorder     string // resolved mic recorder, "" if missing
	ModelPath    string
	ModelPresent bool
}

// Ready reports whether a dictation can run right now (recorder + whisper + model
// all present on a supported host).
func (s Status) Ready() bool {
	return s.Supported && s.WhisperBin != "" && s.Recorder != "" && s.ModelPresent
}

// Missing lists the human-readable components still needed for a Ready toolchain
// (empty when Ready). Drives the install prompt + the "not set up yet" hint.
func (s Status) Missing() []string {
	if !s.Supported {
		return []string{"a supported host platform (macOS/Linux)"}
	}
	var m []string
	if s.WhisperBin == "" {
		m = append(m, "whisper.cpp CLI")
	}
	if s.Recorder == "" {
		m = append(m, "a microphone recorder (sox)")
	}
	if !s.ModelPresent {
		m = append(m, "the speech model ("+DefaultModel+")")
	}
	return m
}

// Check probes the toolchain without recording. Cheap; safe to call often (the
// TUI tip uses it to decide rotation weight).
func Check() Status {
	st := Status{Supported: Supported()}
	if !st.Supported {
		return st
	}
	st.WhisperBin = firstOnPath(whisperBins)
	st.Recorder = firstOnPath(recorders())
	if p, err := ModelPath(); err == nil {
		st.ModelPath = p
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() > 0 {
			st.ModelPresent = true
		}
	}
	return st
}

// InstallPlan is what `voice install` will do — computed from what's missing so
// a re-run is a clean no-op and we never reinstall a present tool.
type InstallPlan struct {
	BrewPackages []string // missing tools to `brew install` (whisper-cpp, sox)
	ModelURL     string   // "" when the model is already present
	ModelDest    string
	ModelSHA256  string // expected checksum of the download; "" = unverified (custom model)
}

// Empty reports whether there's nothing left to do.
func (p InstallPlan) Empty() bool { return len(p.BrewPackages) == 0 && p.ModelURL == "" }

// PlanInstall derives the install steps from a Status: only the missing brew
// packages, and the model download only when absent. Pure — unit-tested.
func PlanInstall(st Status) InstallPlan {
	var p InstallPlan
	if st.WhisperBin == "" {
		p.BrewPackages = append(p.BrewPackages, "whisper-cpp")
	}
	if st.Recorder == "" {
		p.BrewPackages = append(p.BrewPackages, "sox")
	}
	if !st.ModelPresent {
		p.ModelURL = modelBaseURL + DefaultModel
		p.ModelDest = st.ModelPath
		p.ModelSHA256 = modelSHA256[filepath.Base(st.ModelPath)]
	}
	return p
}

// recorderArgs builds the argv for a 16 kHz mono 16-bit WAV capture with the
// given tool, writing to wav. Recording runs until the process is signalled;
// each tool finalizes the WAV header on SIGINT/kill. Pure — unit-tested.
func recorderArgs(tool, wav string) []string {
	switch tool {
	case "rec": // sox
		return []string{"-q", "-c", "1", "-r", "16000", "-b", "16", wav}
	case "arecord":
		return []string{"-q", "-f", "S16_LE", "-c", "1", "-r", "16000", wav}
	case "ffmpeg":
		in := "default"
		if runtime.GOOS == "darwin" {
			in = ":default" // avfoundation default audio device
		}
		f := "alsa"
		if runtime.GOOS == "darwin" {
			f = "avfoundation"
		}
		return []string{"-hide_banner", "-loglevel", "error", "-f", f, "-i", in,
			"-ac", "1", "-ar", "16000", "-y", wav}
	default:
		return nil
	}
}

// whisperArgs builds the whisper.cpp CLI argv to transcribe wav with model,
// printing plain text (no timestamps) to stdout. Pure — unit-tested.
func whisperArgs(model, wav string) []string {
	return []string{"-m", model, "-f", wav, "-nt", "-np"}
}

// cleanTranscript normalizes whisper.cpp output into a single, injection-safe
// line: drops bracketed/parenthesized non-speech tags ([BLANK_AUDIO], (silence)),
// strips control bytes — including ESC, so a stray bracketed-paste terminator
// (ESC[201~) or any terminal escape can never break out of the paste envelope we
// inject into — and collapses whitespace/newlines. whisper emits natural-language
// text so this is belt-and-suspenders, but it hardens the inject seam. Pure —
// unit-tested.
func cleanTranscript(raw string) string {
	var b strings.Builder
	skipDepth := 0
	for _, r := range raw {
		switch r {
		case '[', '(':
			skipDepth++
		case ']', ')':
			if skipDepth > 0 {
				skipDepth--
			}
		default:
			if skipDepth > 0 {
				continue
			}
			// Drop non-whitespace control bytes (ESC, DEL, …); real whitespace
			// (\n\t\r) passes through for strings.Fields to collapse, so word
			// boundaries survive.
			if unicode.IsControl(r) && !unicode.IsSpace(r) {
				continue
			}
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// Transcribe runs whisper.cpp on a 16 kHz mono WAV and returns the cleaned text.
// ErrNotInstalled if the CLI or model is absent; ErrNoSpeech if it transcribes
// to nothing.
func Transcribe(ctx context.Context, wav string) (string, error) {
	bin := firstOnPath(whisperBins)
	if bin == "" {
		return "", ErrNotInstalled
	}
	model, err := ModelPath()
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(model); err != nil || fi.IsDir() || fi.Size() == 0 {
		return "", ErrNotInstalled
	}
	out, err := exec.CommandContext(ctx, bin, whisperArgs(model, wav)...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("whisper.cpp transcription failed: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("run whisper.cpp: %w", err)
	}
	text := cleanTranscript(string(out))
	if text == "" {
		return "", ErrNoSpeech
	}
	return text, nil
}

// Install provisions the plan: brew-installs the missing tools, then downloads
// the model. Progress/log lines are written to out. Idempotent (an Empty plan is
// a no-op). brew is required for the package step (macOS / Linuxbrew); without
// it the caller should print the manual package names.
func Install(ctx context.Context, plan InstallPlan, out io.Writer) error {
	if len(plan.BrewPackages) > 0 {
		if _, err := lookPath("brew"); err != nil {
			return fmt.Errorf("need Homebrew to install %s — get it at https://brew.sh, or install them yourself",
				strings.Join(plan.BrewPackages, " "))
		}
		fmt.Fprintf(out, "Installing %s via Homebrew…\n", strings.Join(plan.BrewPackages, " "))
		cmd := exec.CommandContext(ctx, "brew", append([]string{"install"}, plan.BrewPackages...)...)
		cmd.Stdout, cmd.Stderr = out, out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("brew install %s: %w", strings.Join(plan.BrewPackages, " "), err)
		}
	}
	if plan.ModelURL != "" {
		fmt.Fprintf(out, "Downloading speech model %s (~142 MB, one time)…\n", DefaultModel)
		if err := downloadModel(ctx, plan.ModelURL, plan.ModelDest, plan.ModelSHA256); err != nil {
			return fmt.Errorf("download model: %w", err)
		}
		fmt.Fprintf(out, "  ✓ model at %s\n", plan.ModelDest)
	}
	return nil
}

// downloadModel streams url to dest atomically (temp + rename) so an interrupted
// download never leaves a half-written model that later looks "present". When
// wantSHA is non-empty, the stream is hashed and a mismatch aborts the install
// (nothing is renamed into place) — closing the tamper/MITM gap on the fetch.
func downloadModel(ctx context.Context, url, dest, wantSHA string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("model host returned %s", resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".model-*.part")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if wantSHA != "" {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, wantSHA) {
			return fmt.Errorf("model checksum mismatch (got %s, want %s) — refusing a tampered download", got, wantSHA)
		}
	}
	return os.Rename(tmpName, dest)
}
