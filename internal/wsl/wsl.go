// Package wsl lets a Windows-native `dejima` client drive a `dejimad` running
// inside a WSL2 distro.
//
// Windows can't host Dejima: dejimad needs a Unix host with Docker (see
// scripts/setup.sh, which exits on anything but Darwin/Linux, and
// internal/service, which only knows launchd + systemd). WSL2 *is* such a host
// — a real Linux kernel with a real Docker — so a Windows user can run the
// whole stack locally after all, with the daemon one virtualization boundary
// away.
//
// The transport is deliberately the cheapest thing that preserves the security
// model: we shell out to `wsl.exe -d <distro> -- socat STDIO UNIX-CONNECT:…`
// and wrap that process's stdio as a net.Conn. dejimad needs no new listener,
// no TCP bind, and no relaxation of the tailnet pin; its 0600 Unix socket
// remains the only operator surface, reachable exactly by whoever can already
// run commands as that user inside the distro.
//
// A connection target for this path is spelled `wsl://<distro>` and is stored
// in a client profile like any other host.
package wsl

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// Scheme prefixes a WSL connection target: "wsl://dejima".
const Scheme = "wsl://"

// DefaultDistro is the distro name `dejima wsl setup` creates and that the
// first-run flow offers. Named for the project so it's obvious in `wsl -l -v`
// and can't be confused with a distro the user keeps for other work.
const DefaultDistro = "dejima"

// socketExpr is the shell expression that resolves the daemon socket inside the
// distro. It stays a `sh -c` expression (rather than a literal path) because the
// WSL user's home isn't knowable from the Windows side — the distro may have
// been created with any username.
const socketExpr = `exec socat STDIO UNIX-CONNECT:"$HOME/.dejima/dejimad.sock"`

// execCommand indirects exec.Command so tests can substitute a fake `wsl.exe`.
var execCommand = exec.Command

// IsHost reports whether a connection host names a WSL distro rather than a
// TCP address.
func IsHost(host string) bool {
	return strings.HasPrefix(strings.TrimSpace(host), Scheme)
}

// Distro extracts the distro name from a `wsl://<distro>` host, or "" if host
// isn't a WSL target. A bare "wsl://" yields DefaultDistro so the shorthand
// works.
func Distro(host string) string {
	host = strings.TrimSpace(host)
	if !IsHost(host) {
		return ""
	}
	name := strings.Trim(strings.TrimPrefix(host, Scheme), "/")
	if name == "" {
		return DefaultDistro
	}
	return name
}

// Host renders a distro name as a connection target.
func Host(distro string) string {
	distro = strings.TrimSpace(distro)
	if distro == "" {
		distro = DefaultDistro
	}
	return Scheme + distro
}

// Supported reports whether this platform can reach a WSL distro at all. WSL
// interop exists only on Windows; everywhere else a `wsl://` target is a
// mistake worth naming rather than a dial to attempt.
func Supported() bool { return runtime.GOOS == "windows" }

// InstallHint is the exact command to install WSL, in ONE place because three
// user-facing sites print it and a fourth (the website) copies it. They had
// already drifted: the binary said `wsl --install` while the site said
// `--no-distribution`, so the same operator got two different instructions
// depending on where they read.
//
// --no-distribution is the right one HERE, specifically. Plain `wsl --install`
// also downloads a default Ubuntu and puts a username/password prompt in front
// of the operator — a distro Dejima never uses, because `dejima wsl setup`
// creates its own (see createDistro). Telling someone to install a distro we
// then ignore costs a download, a reboot-time prompt, and the belief that the
// thing they set up is the thing we run.
//
// The flag needs a reasonably recent wsl.exe. Where it is rejected, the fallback
// is the plain form, a reboot, and then `wsl --update` to get a wsl.exe new
// enough for everything after — d4 walked that path on a real box, which is why
// the update step is here rather than inferred.
//
// The update step lives in THIS constant rather than only on the website
// because d5 asserts the page's command block is a substring of this string,
// read out of this file at check time. A step that exists only on the page is
// invisible to that check and can drift silently — which is the whole failure
// this constant was created to end.
const InstallHint = "wsl --install --no-distribution\n" +
	"      (older wsl.exe: wsl --install, reboot, then wsl --update)"

// ErrUnsupported is returned when a `wsl://` target is used off Windows.
var ErrUnsupported = errors.New("wsl:// targets work only on Windows (WSL interop); use a host:port address here")

// Dial opens a connection to dejimad's Unix socket inside distro by piping
// through `wsl.exe … socat`. The returned conn owns the subprocess and kills it
// on Close.
//
// ctx bounds the *handshake* (spawning wsl.exe), not the connection's lifetime:
// http.Transport pools connections past the request whose context triggered the
// dial, so binding the process to that context would kill live pooled conns.
func Dial(ctx context.Context, distro string) (net.Conn, error) {
	if !Supported() {
		return nil, ErrUnsupported
	}
	if strings.TrimSpace(distro) == "" {
		distro = DefaultDistro
	}
	cmd := execCommand("wsl.exe", "-d", distro, "--", "sh", "-c", socketExpr)
	isolateConsole(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("wsl stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("wsl stdout: %w", err)
	}
	errBuf := &syncBuffer{}
	cmd.Stderr = errBuf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("run wsl.exe -d %s: %w (is WSL installed? `wsl --status`)", distro, err)
	}
	if err := ctx.Err(); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, err
	}

	return newProcConn(cmd, stdin, stdout, errBuf, distro), nil
}

// newProcConn wires a started subprocess's stdio into a net.Conn. Split out of
// Dial so tests can build one over a fake wsl.exe without going through the
// Windows-only guard.
//
// It uses net.Pipe (not the raw os.Pipe ends) so the conn supports real
// deadlines, which http.Transport and the websocket client both set; two
// goroutines pump between the synchronous pipe and the subprocess's stdio.
func newProcConn(cmd *exec.Cmd, stdin io.WriteCloser, stdout io.Reader, errBuf *syncBuffer, distro string) *procConn {
	local, remote := net.Pipe()
	pc := &procConn{
		Conn:   local,
		cmd:    cmd,
		stderr: errBuf,
		distro: distro,
		reaped: make(chan struct{}),
	}
	go func() {
		_, _ = io.Copy(stdin, remote)
		_ = stdin.Close()
	}()
	go func() {
		_, err := io.Copy(remote, stdout)
		pc.setReadErr(err)
		// Reap BEFORE unblocking the reader, not after.
		//
		// The whole point of this type is that a caller sees "socat isn't
		// installed" instead of "EOF". That diagnosis is read out of the
		// subprocess's stderr — which exec copies into errBuf on its own
		// goroutine, and only guarantees complete once Wait returns. Closing
		// `remote` here hands the caller its EOF immediately, so without this the
		// caller can be told the connection ended before anyone has read what the
		// subprocess said about why. It then gets the bare EOF this code exists to
		// replace, on the exact path a Windows user with a half-finished setup
		// hits first.
		//
		// io.Copy has returned, so all reads from the stdout pipe are done — which
		// is the ordering cmd.Wait requires of a StdoutPipe caller.
		pc.drain(drainBudget)
		_ = remote.Close()
	}()
	return pc
}

// drainBudget bounds the wait above. A subprocess that has closed stdout is
// normally already exiting, so this costs microseconds; the bound is only here
// so that a child which closes stdout and then lingers cannot wedge the reader
// forever. Losing the diagnosis is bad, never surfacing the EOF is worse.
//
// A var, not a const, only so a test can set it to zero and reproduce the
// pre-fix behaviour — see TestLateHelperReallyRacesTheDiagnosis. Nothing in
// production writes it.
var drainBudget = 2 * time.Second

// drain waits for the subprocess to be reaped, giving up after d.
func (c *procConn) drain(d time.Duration) {
	go c.reap()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-c.reaped:
	case <-timer.C:
	}
}

// reap waits on the subprocess exactly once and closes reaped. Both the stdout
// pump (on a clean exit) and Close (after killing) call it, so it must be
// idempotent: a second cmd.Wait returns an error and races the first.
func (c *procConn) reap() {
	c.reapOnce.Do(func() {
		_ = c.cmd.Wait()
		close(c.reaped)
	})
}

// procConn is a net.Conn backed by a wsl.exe subprocess's stdio.
type procConn struct {
	net.Conn // local end of the net.Pipe pair

	cmd    *exec.Cmd
	stderr *syncBuffer
	distro string
	// reaped closes once the subprocess has been waited on, so Close's cleanup
	// is observable (tests assert no wsl.exe is left behind; a long TUI session
	// would otherwise accumulate one per dropped connection).
	reaped chan struct{}

	reapOnce sync.Once

	mu      sync.Mutex
	readErr error
	closed  bool
}

func (c *procConn) setReadErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr == nil {
		c.readErr = err
	}
}

// Read surfaces what actually went wrong. A missing `socat`, a stopped distro,
// or a distro name that doesn't exist all present to net/http as a bare EOF
// mid-response; wsl.exe's own diagnostics went to stderr (in UTF-16), so we
// splice them into the error instead of letting the caller see
// "unexpected EOF".
func (c *procConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err != nil && n == 0 {
		if msg := c.failure(); msg != "" {
			return n, fmt.Errorf("wsl distro %q: %s", c.distro, msg)
		}
	}
	return n, err
}

// failure renders the subprocess's stderr as a diagnosis, "" when it said
// nothing useful.
func (c *procConn) failure() string {
	raw := decodeWSLText(c.stderr.Bytes())
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Collapse to one line; wsl.exe likes to wrap.
	raw = strings.Join(strings.Fields(raw), " ")
	return classifyStderr(raw)
}

// classifyStderr turns the subprocess's stderr into a diagnosis.
//
// Split out from failure() so it can be tested against real stderr text without
// standing up a subprocess — the previous rule was wrong for an input nothing
// exercised, and the only reason it survived was that the test fed it the one
// string it got right.
//
// THE RULE THAT WAS WRONG: `contains "socat" && (contains "not found" ||
// contains "No such file")`. When the DAEMON'S SOCKET is missing, socat runs
// perfectly and reports:
//
//	socat[123] E connect(5, AF=1 "/root/.dejima/dejimad.sock", 45): No such file or directory
//
// which contains both "socat" and "No such file". So a running socat that could
// not find the daemon was reported as socat not being installed. An operator
// then installed socat — already present, newest version — and got the same
// message, with the actual cause (dejimad not running) never named. The correct
// diagnosis was the very next branch, unreachable because this one matched first.
//
// The word "socat" appearing in socat's own output is evidence that socat EXISTS.
func classifyStderr(raw string) string {
	// socat prefixes its own diagnostics with "socat[<pid>]". Anything carrying
	// that is socat RUNNING and failing, which settles the question of whether
	// it is installed.
	ranSocat := strings.Contains(raw, "socat[")

	// Only a SHELL saying it could not find the binary means it is absent.
	// Match the phrasings shells actually produce, not the mere co-occurrence
	// of a program name and a file error.
	notFound := strings.Contains(raw, "socat: not found") ||
		strings.Contains(raw, "socat: command not found") ||
		strings.Contains(raw, "executable file not found")

	if !ranSocat && notFound {
		return "socat isn't installed in the distro — run `dejima wsl setup` (or `sudo apt install socat` inside it)"
	}
	if strings.Contains(strings.ToLower(raw), "no such file or directory") {
		return "dejimad isn't running in the distro (no socket at ~/.dejima/dejimad.sock) — run `dejima wsl setup`"
	}
	return raw
}

// Close tears down the pipe and the subprocess. Killing is correct here: socat
// has no clean shutdown protocol over stdio and the connection is already gone.
func (c *procConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	err := c.Conn.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	// Reap so we don't leak zombies across a long TUI session. The pumps hold the
	// pipes; Wait closes them, which is what we want on a dead conn. Shared with
	// the stdout pump's drain — whichever gets there first does the Wait.
	go c.reap()
	return err
}

// Reaped returns a channel closed once Close has waited on the subprocess.
func (c *procConn) Reaped() <-chan struct{} { return c.reaped }

func (c *procConn) LocalAddr() net.Addr  { return wslAddr(c.distro) }
func (c *procConn) RemoteAddr() net.Addr { return wslAddr(c.distro) }

type wslAddr string

func (a wslAddr) Network() string { return "wsl" }
func (a wslAddr) String() string  { return Scheme + string(a) }

// syncBuffer is a bytes.Buffer safe for the concurrent write (exec's stderr
// copier) / read (our error path) we do to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

// decodeWSLText converts wsl.exe's output to UTF-8. wsl.exe emits its *own*
// diagnostics (distro not found, WSL not installed) as UTF-16LE, while text
// forwarded from inside the distro is already UTF-8 — so we sniff rather than
// assume, and pass through anything that's already valid UTF-8 with no NULs.
func decodeWSLText(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	// BOM is definitive.
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		return decodeUTF16LE(b[2:])
	}
	if utf8.Valid(b) && !bytes.ContainsRune(b, 0) {
		return string(b)
	}
	// Heuristic: UTF-16LE ASCII text is every-other-byte NUL.
	if len(b) >= 2 && bytes.IndexByte(b, 0) == 1 {
		return decodeUTF16LE(b)
	}
	return string(bytes.ReplaceAll(b, []byte{0}, nil))
}

func decodeUTF16LE(b []byte) string {
	if len(b)%2 == 1 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return string(utf16.Decode(u))
}

// ---------------------------------------------------------------------------
// Inspection — what `dejima wsl status`, `doctor`, and the setup flow read.
// ---------------------------------------------------------------------------

// Distribution is one entry from `wsl.exe -l -v`.
type Distribution struct {
	Name    string
	State   string // "Running" / "Stopped"
	Version int    // 1 or 2; only 2 has a real kernel + Docker
	Default bool
}

// Available reports whether WSL interop is usable at all: Windows, with
// wsl.exe on PATH.
func Available() bool {
	if !Supported() {
		return false
	}
	if _, err := exec.LookPath("wsl.exe"); err != nil {
		return false
	}
	// FINDING wsl.exe IS NOT THE SAME QUESTION AS "IS WSL INSTALLED".
	//
	// Windows ships wsl.exe in System32 whether or not the feature is enabled.
	// On a box without it, the launcher is a stub that prints "The Windows
	// Subsystem for Linux is not installed" and exits non-zero. So LookPath
	// succeeded, this reported available, requireWSLPlatform's clear message
	// ("Install it: an admin PowerShell, then `wsl --install`, then reboot")
	// never fired, and the operator got a raw passthrough of
	//
	//     wsl -l -v: The Windows Subsystem for Linux is not installed.
	//
	// from three layers down. The guard was not missing; it was not reached.
	//
	// So ask WSL itself. `--status` is the cheap probe: it answers on a healthy
	// install and emits the not-installed text on a stub. Note this deliberately
	// does NOT treat a non-zero exit as absence — `--status` also exits non-zero
	// on an install with no distro yet, which is a state List() already
	// distinguishes and which `dejima wsl setup` exists to fix.
	return featurePresent()
}

// featurePresent asks WSL whether it is actually there.
//
// Split from Available so it is REACHABLE FROM A TEST: Available's two guards
// (Supported, LookPath) are both false on the machines that run the tests, so a
// test of Available exercises the early returns and never the logic that was
// wrong. Mutating this function back to `return true` must fail something.
func featurePresent() bool {
	cmd := execCommand("wsl.exe", "--status")
	isolateConsole(cmd)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	_ = cmd.Run()
	return !reportsNotInstalled(decodeWSLText(out.Bytes()) + decodeWSLText(errOut.Bytes()))
}

// reportsNotInstalled recognises the stub launcher's answer.
//
// Matched on the message rather than the exit code, because the exit code cannot
// tell "WSL is absent" from "WSL is installed with no distro" — opposite
// situations with opposite next steps: install the feature and reboot, versus
// run `dejima wsl setup`. Telling someone with a working WSL to reinstall
// Windows features is the failure this function exists to avoid.
//
// Localized Windows installs translate this string. A translated message
// therefore reads as "installed", and the operator gets the raw passthrough they
// get today rather than a wrong instruction — the safe direction to be wrong in.
func reportsNotInstalled(text string) bool {
	t := strings.ToLower(text)
	return strings.Contains(t, "windows subsystem for linux is not installed") ||
		strings.Contains(t, "wsl is not installed")
}

// List enumerates installed distros. An empty list with no error means WSL is
// installed but has no distro yet — the state a fresh Windows box is in after
// `wsl --install` reboots.
func List(ctx context.Context) ([]Distribution, error) {
	if !Supported() {
		return nil, ErrUnsupported
	}
	cmd := execCommand("wsl.exe", "-l", "-v")
	isolateConsole(cmd)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		// `wsl -l -v` exits non-zero when no distro is installed; that's a state,
		// not a failure.
		txt := decodeWSLText(out.Bytes()) + decodeWSLText(errOut.Bytes())
		if strings.Contains(txt, "no installed distributions") || strings.Contains(txt, "has no installed distributions") {
			return nil, nil
		}
		if strings.TrimSpace(txt) != "" {
			return nil, fmt.Errorf("wsl -l -v: %s", strings.Join(strings.Fields(txt), " "))
		}
		return nil, fmt.Errorf("wsl -l -v: %w", err)
	}
	return parseDistroList(decodeWSLText(out.Bytes())), nil
}

// parseDistroList reads the fixed-column output of `wsl -l -v`:
//
//	  NAME      STATE           VERSION
//	* dejima    Running         2
func parseDistroList(text string) []Distribution {
	var out []Distribution
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		def := strings.HasPrefix(strings.TrimSpace(line), "*")
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "*"))
		if len(fields) < 3 {
			continue
		}
		// Skip the header row (localized installs still use these tokens in
		// English on the version column, so key off the literal header).
		if strings.EqualFold(fields[0], "NAME") {
			continue
		}
		d := Distribution{Name: fields[0], State: fields[1], Default: def}
		// VERSION is the last field.
		switch fields[len(fields)-1] {
		case "2":
			d.Version = 2
		case "1":
			d.Version = 1
		}
		out = append(out, d)
	}
	return out
}

// Report is a health read of one distro as a Dejima host.
type Report struct {
	Distro    string
	Exists    bool
	Version   int
	Running   bool
	HasSocat  bool
	HasDocker bool // docker CLI present AND the engine answers
	HasDejima bool // dejimad binary installed
	SocketUp  bool // ~/.dejima/dejimad.sock exists
}

// Ready reports whether this distro can serve as a working local host.
func (r Report) Ready() bool {
	return r.Exists && r.Version == 2 && r.HasSocat && r.HasDocker && r.HasDejima && r.SocketUp
}

// Probe inspects a distro without changing anything. Each check is a single
// `sh -c` inside the distro; a distro that isn't running will be started by WSL
// on the first one (that's expected and is why setup is idempotent).
func Probe(ctx context.Context, distro string) (Report, error) {
	if !Supported() {
		return Report{}, ErrUnsupported
	}
	if strings.TrimSpace(distro) == "" {
		distro = DefaultDistro
	}
	r := Report{Distro: distro}
	dists, err := List(ctx)
	if err != nil {
		return r, err
	}
	for _, d := range dists {
		if strings.EqualFold(d.Name, distro) {
			r.Exists, r.Version, r.Running = true, d.Version, strings.EqualFold(d.State, "Running")
			break
		}
	}
	if !r.Exists {
		return r, nil
	}
	// One round-trip: echo a token per satisfied condition.
	const script = `
		command -v socat   >/dev/null 2>&1 && echo socat
		command -v dejimad >/dev/null 2>&1 && echo dejimad
		docker info        >/dev/null 2>&1 && echo docker
		[ -S "$HOME/.dejima/dejimad.sock" ] && echo socket
		exit 0`
	out, err := run(ctx, distro, script)
	if err != nil {
		return r, err
	}
	for _, tok := range strings.Fields(out) {
		switch tok {
		case "socat":
			r.HasSocat = true
		case "dejimad":
			r.HasDejima = true
		case "docker":
			r.HasDocker = true
		case "socket":
			r.SocketUp = true
		}
	}
	return r, nil
}

// run executes a shell script inside the distro and returns its stdout.
func run(ctx context.Context, distro, script string) (string, error) {
	cmd := execCommand("wsl.exe", "-d", distro, "--", "sh", "-c", homePreamble+script)
	isolateConsole(cmd)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("wsl.exe -d %s: %w", distro, err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return "", ctx.Err()
	case err := <-done:
		if err != nil {
			msg := strings.Join(strings.Fields(decodeWSLText(errOut.Bytes())), " ")
			if msg == "" {
				return decodeWSLText(out.Bytes()), fmt.Errorf("wsl.exe -d %s: %w", distro, err)
			}
			return decodeWSLText(out.Bytes()), fmt.Errorf("wsl.exe -d %s: %s", distro, msg)
		}
	}
	return decodeWSLText(out.Bytes()), nil
}

// Run is run() exported for the setup flow, which needs to execute provisioning
// steps inside the distro and show their output.
func Run(ctx context.Context, distro, script string) (string, error) {
	return run(ctx, distro, script)
}

// homePreamble guarantees $HOME is set for every in-distro script.
//
// `wsl.exe -d <distro> -- sh -c …` does NOT pass HOME, and /bin/sh on Ubuntu is
// dash, which — unlike bash — does not synthesise it from the passwd entry. So
// $HOME was the EMPTY STRING in every script here, and the failure surfaced as
//
//	mkdir: cannot create directory '': No such file or directory
//
// which names neither HOME nor the shell. It went unnoticed because the scripts
// that use $HOME had never successfully run: the one path that did work went
// through `curl … | bash`, and bash fills HOME in, so the same distro looked
// fine from one command and broken from another.
//
// This matters beyond a work directory: dejimad derives its socket and config
// from HOME, so a daemon started with it empty would write to /.dejima and the
// client would look somewhere else entirely — a much quieter failure than this
// one.
//
// getent first (correct for any user), /root last (correct for the WSL default,
// and better than nothing). `:` with := assigns only when unset or empty.
const homePreamble = `: "${HOME:=$(getent passwd "$(id -u)" 2>/dev/null | cut -d: -f6)}"; : "${HOME:=/root}"; export HOME
`

// RunExe invokes wsl.exe with *management* arguments (--install, --set-version,
// …) rather than a command inside a distro. It returns the combined output even
// on failure so callers can classify the error (e.g. an old wsl.exe that lacks
// a flag), and streams nothing — these operations are slow but quiet.
func RunExe(ctx context.Context, args ...string) (string, error) {
	if !Supported() {
		return "", ErrUnsupported
	}
	cmd := execCommand("wsl.exe", args...)
	isolateConsole(cmd)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("wsl.exe: %w", err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return "", ctx.Err()
	case err := <-done:
		combined := decodeWSLText(out.Bytes()) + decodeWSLText(errOut.Bytes())
		if err != nil {
			msg := strings.Join(strings.Fields(combined), " ")
			if msg == "" {
				return combined, fmt.Errorf("wsl.exe %s: %w", strings.Join(args, " "), err)
			}
			return combined, fmt.Errorf("wsl.exe %s: %s", strings.Join(args, " "), msg)
		}
		return combined, nil
	}
}

// dialTimeout bounds spawning wsl.exe. Starting a *stopped* distro is the slow
// case (the VM boots), so this is generous.
const dialTimeout = 90 * time.Second

// DialTimeout is the handshake budget callers should use when building a
// transport for this path.
func DialTimeout() time.Duration { return dialTimeout }
