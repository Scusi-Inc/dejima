package localmodel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/paths"
)

// In-island wiring for the default (Ollama) backend. Islands reach the host's
// inference server via host.docker.internal (already in the egress no-proxy
// path); Ollama exposes an OpenAI-compatible API under /v1 on port 11434.
const (
	// LocalProviderName is the providercreds entry auto-registered when a backend
	// is installed, so local models show up in the `v` model editor unprompted.
	LocalProviderName = "local"

	// OllamaEndpoint is the base URL an island uses to reach the host's Ollama.
	OllamaEndpoint = "http://host.docker.internal:11434/v1"
	// OllamaAllowHostPort is the egress-allowlist entry that lets an island reach
	// exactly that endpoint and nothing else.
	OllamaAllowHostPort = "host.docker.internal:11434"
)

// InstalledModel is a model already pulled into the backend on the host.
type InstalledModel struct {
	Ref   string `json:"ref"`             // backend tag, e.g. "qwen2.5-coder:32b-instruct-q4_K_M"
	Size  string `json:"size,omitempty"`  // human size from the backend, e.g. "20 GB"
	Alias string `json:"alias,omitempty"` // catalog alias, when the ref is one we curate
}

// Status is the backend's runtime state on the daemon host, plus the host-aware
// recommendation — everything the TUI/CLI needs to render "Local models" in one
// call.
type Status struct {
	Backend    Backend          `json:"backend"`
	Installed  bool             `json:"installed"` // backend binary present on the host
	Running    bool             `json:"running"`   // backend responding (models listable)
	Endpoint   string           `json:"endpoint"`  // in-island OpenAI-compatible base URL
	Models     []InstalledModel `json:"models"`
	HostRAMGiB int              `json:"host_ram_gib"`
	Recommend  Recommendation   `json:"recommend"`
	Provider   string           `json:"provider"` // the providercreds name islands use ("local")
}

// LocalBackend abstracts a host inference server so Ollama (default), vLLM, and
// LM Studio can slot in behind one interface. All methods run on the daemon
// HOST (where the GPU is), never inside an island.
type LocalBackend interface {
	Name() Backend
	// Detect reports whether the backend binary is installed and whether it's
	// currently responding.
	Detect(ctx context.Context) (installed, running bool)
	// Endpoint is the in-island base URL for the OpenAI-compatible API.
	Endpoint() string
	// AllowHostPort is the egress-allowlist entry islands need to reach Endpoint.
	AllowHostPort() string
	// List returns models already pulled on the host.
	List(ctx context.Context) ([]InstalledModel, error)
	// Pull downloads a model, streaming progress lines. Caller closes the reader.
	Pull(ctx context.Context, ref string) (io.ReadCloser, error)
	// Remove deletes a pulled model.
	Remove(ctx context.Context, ref string) error
	// Install streams a best-effort install of the backend on the host.
	Install(ctx context.Context) (io.ReadCloser, error)
	// Start brings the server up if it is not already answering, and waits for
	// it to answer. On the interface because an installed-but-stopped backend is
	// a state the daemon must be able to leave — it is where an operator landed
	// with no command on any surface to get out.
	Start(ctx context.Context) error
}

// Ollama is the default LocalBackend: the daemon shells out to the host `ollama`
// CLI. It's the simplest path and Mac-friendly (Metal); vLLM is the better
// default on a Linux/GPU daemon and can implement this same interface later.
type Ollama struct {
	bin string // resolved lazily; "ollama" by default
}

// NewOllama returns the default-configured Ollama backend.
func NewOllama() *Ollama { return &Ollama{bin: "ollama"} }

func (o *Ollama) Name() Backend         { return BackendOllama }
func (o *Ollama) Endpoint() string      { return OllamaEndpoint }
func (o *Ollama) AllowHostPort() string { return OllamaAllowHostPort }

// ollamaKnownPaths is where a macOS install actually lands. PATH alone is not
// enough to find it: these methods run in the DAEMON, and a system LaunchDaemon
// inherits a bare /usr/bin:/bin:/usr/sbin:/sbin — no Homebrew prefix. Without
// this, an operator who installs Ollama correctly still sees "not installed"
// forever, and the only visible symptom is a wrong answer.
var ollamaKnownPaths = []string{
	"/opt/homebrew/bin/ollama", // Homebrew, Apple silicon
	"/usr/local/bin/ollama",    // Homebrew on Intel; also where the .app links it
	"/Applications/Ollama.app/Contents/Resources/ollama",
}

// resolveExe locates the ollama binary. ok=false means genuinely not installed,
// as opposed to installed-but-off-PATH.
func (o *Ollama) resolveExe() (string, bool) {
	name := o.bin
	if name == "" {
		name = "ollama"
	}
	if filepath.IsAbs(name) {
		return name, isExecutableFile(name)
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, true
	}
	for _, p := range ollamaKnownPaths {
		if isExecutableFile(p) {
			return p, true
		}
	}
	return name, false
}

func isExecutableFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

func (o *Ollama) exe() string {
	p, _ := o.resolveExe()
	return p
}

// Detect: installed = binary found (PATH or a known install location); running =
// `ollama list` succeeds (it talks to the local server, so a clean exit means
// the server is up).
func (o *Ollama) Detect(ctx context.Context) (installed, running bool) {
	if _, ok := o.resolveExe(); !ok {
		return false, false
	}
	installed = true
	if err := exec.CommandContext(ctx, o.exe(), "list").Run(); err == nil {
		running = true
	}
	return installed, running
}

// List parses `ollama list` and annotates any ref we curate with its alias.
func (o *Ollama) List(ctx context.Context) ([]InstalledModel, error) {
	out, err := exec.CommandContext(ctx, o.exe(), "list").Output()
	if err != nil {
		return nil, fmt.Errorf("ollama list: %w", err)
	}
	return parseOllamaList(string(out)), nil
}

// Pull streams `ollama pull <ref>` combined output. The model ref is validated
// by the caller (ValidateRef) before we ever build the command.
func (o *Ollama) Pull(ctx context.Context, ref string) (io.ReadCloser, error) {
	return streamCommand(exec.CommandContext(ctx, o.exe(), "pull", ref))
}

func (o *Ollama) Remove(ctx context.Context, ref string) error {
	if out, err := exec.CommandContext(ctx, o.exe(), "rm", ref).CombinedOutput(); err != nil {
		return fmt.Errorf("ollama rm %s: %w: %s", ref, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ErrInstallNeedsTerminal reports that this host's Ollama install cannot be
// driven from the daemon, and names the commands that do work.
//
// On macOS the official script copies Ollama.app into /Applications and then
// SUDOs to link the CLI onto PATH. These methods run inside the daemon, which
// has no controlling terminal, so that sudo dies on "a terminal is required to
// read the password" — every time, unfixably from here. It was reported from a
// Mac mini as a bare "ERROR: exit status 1" after a completed 100% download,
// which reads as a network flake rather than the one thing it is.
//
// "UNFIXABLY FROM HERE" WAS TRUE OF THE OFFICIAL SCRIPT AND WAS READ AS TRUE OF
// macOS. Homebrew needs no sudo — it refuses to run under it, and installs into
// a prefix the invoking user owns — so a daemon running as that user can drive
// `brew install ollama` with no terminal and no password. The message had been
// telling operators to run, by hand, a command the daemon could have run itself.
//
// This error now means the narrower thing it always should have: this host has
// no usable Homebrew for the daemon to use. Which is real — no brew installed,
// or a daemon running as root, where brew refuses.
var ErrInstallNeedsTerminal = errors.New(
	"can't install Ollama from the daemon on this Mac: the official installer needs sudo, " +
		"and no Homebrew is usable from here (either it isn't installed, or this daemon runs as root, " +
		"which Homebrew refuses).\n" +
		"Install it on the DAEMON HOST — not the machine you are typing on — then re-run " +
		"`dejima local install` (it will detect it and just register the provider):\n" +
		"  brew install ollama && brew services start ollama\n" +
		"or download the app from https://ollama.com/download and open it once")

// Install runs Ollama's official install script. It's best-effort and explicitly
// user-invoked (`dejima local install`); we never auto-install.
func (o *Ollama) Install(ctx context.Context) (io.ReadCloser, error) {
	return o.installOn(ctx, runtime.GOOS)
}

// installOn is Install with the platform injected, so the macOS refusal is
// assertable from a Linux test runner — which is the only place it ever runs.
func (o *Ollama) installOn(ctx context.Context, goos string) (io.ReadCloser, error) {
	if goos == "darwin" {
		return o.installDarwin(ctx)
	}
	// The official one-liner is idempotent and handles platform detection.
	return streamCommand(exec.CommandContext(ctx, "sh", "-c",
		"curl -fsSL https://ollama.com/install.sh | sh"))
}

// installDarwin installs Ollama through Homebrew, which needs no sudo and no
// terminal, and falls back to the hand-install instructions when brew is not
// usable from here.
//
// The refusal this replaces was correct about the OFFICIAL installer and was
// applied to the whole platform. brew is the one the message itself recommended,
// and it is drivable: Homebrew installs into a user-owned prefix and REFUSES to
// run under sudo, so there is no password to type.
//
// Two states keep the old path, and both are real rather than defensive:
// no brew on this host, and a daemon running as root (`brew` exits immediately
// with "Don't run this as root!", so attempting it would replace a clear message
// with a confusing one).
func (o *Ollama) installDarwin(ctx context.Context) (io.ReadCloser, error) {
	brew, ok := findBrew()
	if !ok || geteuid() == 0 {
		return nil, ErrInstallNeedsTerminal
	}
	return streamCommand(exec.CommandContext(ctx, "sh", "-c", darwinBrewScript(brew)))
}

// darwinBrewScript is the install, as a pure function so its content is
// assertable without running brew — there is no Mac in CI, and a test that only
// checks "did not refuse" would pass on a script that installs nothing.
//
// `brew services start` is the supported way to run it and survives a reboot. It
// can fail where the daemon has no launchd user domain to bootstrap into, and an
// install that succeeds while leaving nothing listening is the exact shape this
// package keeps hitting — so it falls back to starting the server directly. The
// `|| {` block rather than `&&`: a services failure must not fail the install.
func darwinBrewScript(brew string) string {
	return fmt.Sprintf(`set -e
%[1]q install ollama
%[1]q services start ollama || echo "brew services could not start it here; the daemon will start the server itself"
echo "ollama installed via Homebrew"`, brew)
}

// brewCandidates are the two Homebrew prefixes plus whatever is on PATH. A
// daemon started by launchd has a minimal PATH that usually does NOT include
// /opt/homebrew/bin, so looking only at PATH would report "no brew" on a Mac
// that plainly has it — which is the same false-negative the refusal above was.
var brewCandidates = []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"}

// findBrew resolves an executable brew, or reports that there is none.
var findBrew = func() (string, bool) {
	for _, p := range brewCandidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, true
		}
	}
	if p, err := exec.LookPath("brew"); err == nil {
		return p, true
	}
	return "", false
}

// geteuid is indirected so the root branch is reachable from a test that is not
// running as root — the branch would otherwise only ever execute on a machine
// nobody runs the suite on.
var geteuid = os.Geteuid

// Start launches the backend server DETACHED and waits for it to answer.
//
// An install that leaves nothing listening is the shape this package keeps
// producing: the operator gets "install finished" and a status line reading
// `installed (not running)`, with no command anywhere to fix it.
//
// Detached because the daemon that starts it may itself be restarted, and
// because `brew services` cannot always be used: on a Mac reached over SSH,
// `launchctl enable gui/501/...` fails with "Domain does not support specified
// action" (exit 125), which is what an operator actually hit. Starting it
// ourselves is the fallback that has to work, so it uses setsid and releases the
// process rather than nohup-and-hope.
//
// A no-op when the server is already answering, so it is safe to call on every
// install and from a `start` that an operator runs twice.
func (o *Ollama) Start(ctx context.Context) error {
	if _, running := o.Detect(ctx); running {
		return nil
	}
	exe, ok := o.resolveExe()
	if !ok {
		return errors.New("the backend is not installed, so there is nothing to start")
	}
	// STDIO MUST BE DETACHED, always. Left nil, the child inherits ours — and a
	// server that outlives us then holds our stdout open forever. That is not
	// theoretical: it hung `go test` on the first run of this code, because the
	// harness waits for the pipe rather than for the process.
	//
	// The daemon's stdout is its log; a released child writing there, or holding
	// it open across a daemon restart, is the same defect with worse blast
	// radius. A log file when we can open one, /dev/null when we cannot.
	logPath, logFile := serverLog()
	if logFile == nil {
		var err error
		if logFile, err = os.OpenFile(os.DevNull, os.O_WRONLY, 0); err != nil {
			return fmt.Errorf("cannot detach the server's output: %w", err)
		}
		logPath = ""
	}
	defer logFile.Close()

	// NOT CommandContext. ctx bounds how long we WAIT for the server, not how
	// long the server lives — binding the process to it would kill the thing we
	// just started the moment this function returns.
	cmd := exec.Command(exe, "serve")
	cmd.SysProcAttr = detachAttrs()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s serve: %w", exe, err)
	}
	// Release, so this process is not the child's parent for wait purposes and
	// the server outlives us.
	_ = cmd.Process.Release()

	// Wait for it to ANSWER, not merely to have been spawned. "I started a
	// process" and "the backend is up" are different claims, and reporting the
	// first as the second is how `installed (not running)` reached a screen that
	// had just said the install finished.
	// A real deadline. This was `deadline := time.Now().After` — a METHOD VALUE
	// bound to the instant it was created, so every later call compared against
	// that same frozen `now` and the loop could not expire. It reads like a
	// clock and is a snapshot. Caught by the never-answers test hanging until the
	// harness killed it, which is the only reason it was not shipped.
	deadline := time.Now().Add(serverStartBudget)
	for time.Now().Before(deadline) {
		if _, running := o.Detect(ctx); running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(serverStartPoll):
		}
	}
	if logPath != "" {
		return fmt.Errorf("started %s but it did not answer within %s — its output is in %s",
			exe, serverStartBudget, logPath)
	}
	return fmt.Errorf("started %s but it did not answer within %s", exe, serverStartBudget)
}

// Injectable so the timeout path is testable without waiting out a real budget.
var (
	serverStartBudget = 30 * time.Second
	serverStartPoll   = time.Second
)

// serverLog opens the backend's log beside the daemon's own state, so a server
// that dies on startup leaves something to read. Best-effort: no log is worse
// than a log, but it is not a reason to refuse to start.
func serverLog() (string, *os.File) {
	root, err := paths.Root()
	if err != nil {
		return "", nil
	}
	p := filepath.Join(root, "ollama-server.log")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", nil
	}
	return p, f
}

// parseOllamaList turns `ollama list` tabular output into InstalledModels.
// Format: "NAME  ID  SIZE  MODIFIED" header then rows; NAME is field 0, and
// SIZE is a "<n> <unit>" pair we stitch back together when present.
func parseOllamaList(out string) []InstalledModel {
	var models []InstalledModel
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.EqualFold(fields[0], "NAME") {
			continue // header or blank
		}
		m := InstalledModel{Ref: fields[0]}
		// SIZE is typically fields[2]+" "+fields[3] ("20 GB"); tolerate variation.
		if len(fields) >= 4 {
			m.Size = fields[2] + " " + fields[3]
		}
		if cat, ok := Lookup(m.Ref); ok {
			m.Alias = cat.Alias
		}
		models = append(models, m)
	}
	return models
}

// streamCommand starts cmd with stdout+stderr merged into a single reader the
// caller drains and closes (mirrors the runtime's ExecStream shape).
func streamCommand(cmd *exec.Cmd) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return nil, err
	}
	go func() {
		err := cmd.Wait()
		_ = pw.CloseWithError(err) // surfaces a non-zero exit to the reader
	}()
	return &pipeReadCloser{r: pr}, nil
}

type pipeReadCloser struct{ r *io.PipeReader }

func (p *pipeReadCloser) Read(b []byte) (int, error) { return p.r.Read(b) }
func (p *pipeReadCloser) Close() error               { return p.r.Close() }

// ValidateRef guards a model ref before it reaches a shell/exec arg. Refs are
// backend tags like "qwen2.5-coder:32b-instruct-q4_K_M" — alnum plus a small
// punctuation set; anything else is rejected rather than escaped.
func ValidateRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("empty model ref")
	}
	if len(ref) > 200 {
		return fmt.Errorf("model ref too long")
	}
	for _, r := range ref {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("._:-/+", r):
		default:
			return fmt.Errorf("invalid character %q in model ref", r)
		}
	}
	return nil
}

// ResolveRef maps a user-typed handle (a curated alias or a raw backend ref) to
// the ref to pull. A curated alias resolves to its pinned ref; anything else is
// passed through verbatim after validation, so power users can pull uncurated
// models. ok reports whether the handle matched the curated catalog.
func ResolveRef(handle string) (ref string, curated bool, err error) {
	if m, ok := Lookup(handle); ok {
		return m.Ref, true, nil
	}
	if err := ValidateRef(handle); err != nil {
		return "", false, err
	}
	return handle, false, nil
}
