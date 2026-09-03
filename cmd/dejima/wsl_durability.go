package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/wsl"
)

// Proving that `dejima wsl setup` produced a DURABLE host, by restarting the
// distro and looking at what came back.
//
// Setup used to end by narrating its own steps: socat installed, Docker present,
// dejimad running, image built, connection verified. Every line was true and the
// host still could not survive a restart — the operator needed eleven attempts
// to get there by hand, and three of those runs reported clean while silently
// skipping the unit install, because `enable --now` leaves an already-running
// unit alone.
//
// THE ASSERTIONS MUST NOT SHARE SETUP'S ASSUMPTIONS. A self-report that re-runs
// the reporter's own logic is wrong in the same way the report was, and lies with
// more confidence. So each one reads observable state:
//
//	a socket file exists          not "we ran the install step"
//	a pid is alive                not "systemctl enable returned 0"
//	the listener matches the unit  read the address; do not trust that the
//	                               Environment= line reached the process
//	the image is present          ask docker, not the build's exit code
//
// Every one of those distinctions is a defect this project shipped. `open -a
// Docker` returns 0 without launching. npm succeeds when an optional binary
// fails. wsl.exe answers while WSL is uninstalled. The exit code never meant the
// thing happened.

// distroRuntime is the seam that makes this testable off Windows. Production
// talks to a real distro; tests drive a fake one and can therefore make each
// answer wrong on purpose, which is the only way to know an assertion can fail.
type distroRuntime interface {
	// Run executes a script inside the distro and returns its combined output.
	Run(ctx context.Context, script string) (string, error)
	// Terminate shuts the distro down. The next Run boots it again — that boot is
	// the whole experiment.
	Terminate(ctx context.Context) error
	// Name identifies the distro in operator-facing output.
	Name() string
}

// durabilityFinding is one assertion's outcome. A finding always carries a
// detail line, whether it passed or failed: "verified: the daemon came back
// after a distro restart" is the sentence that was missing, and so is the
// specific reason when it did not.
type durabilityFinding struct {
	Name   string
	OK     bool
	Detail string
}

// proveDurability restarts the distro and asserts the end state came back.
//
// It returns an error when any assertion fails. A setup that cannot demonstrate
// the daemon survives has not finished, so the caller must not print a clean
// bill of health over this.
//
// IT NEVER SKIPS. If the distro cannot be reached, that is a failure, not an
// absence of information: this project has produced six variants of a guard
// reporting green with no subject, and three of them printed ok on a skip.
func proveDurability(ctx context.Context, rt distroRuntime, out io.Writer) error {
	fmt.Fprintf(out, "\nProving the daemon survives a restart of %s …\n", rt.Name())

	if err := rt.Terminate(ctx); err != nil {
		return fmt.Errorf("could not terminate %s, so durability is unproven: %w", rt.Name(), err)
	}
	fmt.Fprintln(out, "  • distro terminated")

	// The boot. Any command starts a terminated distro; systemd then brings the
	// daemon up, which is the thing being tested.
	home, err := bootAndReadHome(ctx, rt)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "  • distro booted")

	sock := home + "/.dejima/dejimad.sock"

	// Wait in Go, not in the shell. A counter in an in-distro script is what
	// produced `sh: 18: [: Illegal number:` on a real machine — this channel eats
	// `$`, so no script here contains one.
	waited, err := waitForSocketAfterBoot(ctx, rt, sock)
	if err != nil {
		return fmt.Errorf("the daemon did not come back after %s restarted: %w\n"+
			"  its supervision unit is not doing its job; check:  dejima wsl status", rt.Name(), err)
	}

	findings := []durabilityFinding{
		{Name: "socket", OK: true, Detail: fmt.Sprintf("%s exists (%s after boot)", sock, waited.Round(100*time.Millisecond))},
	}
	findings = append(findings,
		durabilityDaemonProcess(ctx, rt),
		durabilityListenerMatchesUnit(ctx, rt),
		durabilityIslandImage(ctx, rt),
	)

	failed := 0
	for _, f := range findings {
		mark := "✓"
		if !f.OK {
			mark = "✗"
			failed++
		}
		fmt.Fprintf(out, "  %s %s — %s\n", mark, f.Name, f.Detail)
	}

	if failed > 0 {
		return fmt.Errorf("%d of %d durability checks failed after restarting %s; "+
			"the host is installed but not proven durable", failed, len(findings), rt.Name())
	}
	fmt.Fprintln(out, "  verified: the daemon came back after a distro restart")
	return nil
}

// bootAndReadHome boots the distro and reads HOME from it in one step, so a
// distro that cannot answer at all fails here rather than in an assertion.
func bootAndReadHome(ctx context.Context, rt distroRuntime) (string, error) {
	out, err := rt.Run(ctx, "printenv HOME")
	if err != nil {
		return "", fmt.Errorf("%s did not come back after being terminated: %w", rt.Name(), err)
	}
	home := strings.TrimSpace(out)
	if home == "" || !strings.HasPrefix(home, "/") {
		return "", fmt.Errorf("%s booted but reported an unusable HOME (%q)", rt.Name(), out)
	}
	return home, nil
}

// durabilitySocketBudget is how long systemd gets to bring the daemon back.
// A variable so tests can shorten it: a real 45-second wait in a unit test is
// dead CI time, and the thing under test is the decision, not the duration.
var durabilitySocketBudget = 45 * time.Second

// durabilitySocketPoll is the gap between probes; a variable for the same reason.
var durabilitySocketPoll = time.Second

// waitForSocketAfterBoot gives systemd a bounded moment to bring the daemon up.
func waitForSocketAfterBoot(ctx context.Context, rt distroRuntime, sock string) (time.Duration, error) {
	budget := durabilitySocketBudget
	start := time.Now()
	for {
		if _, err := rt.Run(ctx, "test -S "+sock); err == nil {
			return time.Since(start), nil
		}
		if time.Since(start) > budget {
			return time.Since(start), fmt.Errorf("no socket at %s within %s", sock, budget)
		}
		select {
		case <-ctx.Done():
			return time.Since(start), ctx.Err()
		case <-time.After(durabilitySocketPoll):
		}
	}
}

// durabilityDaemonProcess asserts a dejimad PID is alive.
//
// The pid is PARSED, not merely requested. `pgrep` exiting 0 with empty output
// would otherwise read as success — the same shape as every exit-code bug above.
func durabilityDaemonProcess(ctx context.Context, rt distroRuntime) durabilityFinding {
	out, err := rt.Run(ctx, "pgrep -x dejimad")
	if err != nil {
		return durabilityFinding{Name: "daemon process", Detail: "no dejimad process is running: " + err.Error()}
	}
	for _, line := range strings.Fields(out) {
		if pid, convErr := strconv.Atoi(line); convErr == nil && pid > 0 {
			return durabilityFinding{Name: "daemon process", OK: true, Detail: fmt.Sprintf("dejimad alive (pid %d)", pid)}
		}
	}
	return durabilityFinding{
		Name:   "daemon process",
		Detail: fmt.Sprintf("pgrep succeeded but named no pid (%q) — the exit code did not mean the process exists", strings.TrimSpace(out)),
	}
}

// durabilityListenerMatchesUnit compares the bind the UNIT declares against the bind
// the RUNNING PROCESS actually has.
//
// This is the assertion worth the most. `systemctl enable --now` leaves an
// already-running unit alone, so a corrected unit can change a file and nothing
// else: the Environment= line says the token listener is on the bridge, the
// process is still bound to loopback, and every other check passes. Containers
// then cannot reach the daemon, which is a defect this project shipped.
//
// Reading only "is it on the bridge" would be wrong for a VM-backed engine,
// where loopback is correct and no override is written. Comparing the two
// observable sides is right for both engines and catches the drift either way.
func durabilityListenerMatchesUnit(ctx context.Context, rt distroRuntime) durabilityFinding {
	const name = "listener"
	unit, err := rt.Run(ctx, "cat /etc/systemd/system/dejimad.service")
	if err != nil {
		return durabilityFinding{Name: name, Detail: "cannot read the supervision unit: " + err.Error()}
	}
	wantAddr, port := declaredTokenBind(unit)
	if port == 0 {
		return durabilityFinding{Name: name, OK: true,
			Detail: "the unit declares no token listener, so there is no bind to disagree with"}
	}

	tcp, err := rt.Run(ctx, "cat /proc/net/tcp")
	if err != nil {
		return durabilityFinding{Name: name, Detail: "cannot read /proc/net/tcp: " + err.Error()}
	}
	gotAddrs := listeningAddrsOnPort(tcp, port)
	if len(gotAddrs) == 0 {
		return durabilityFinding{Name: name,
			Detail: fmt.Sprintf("the unit declares %s:%d but nothing is listening on port %d", wantAddr, port, port)}
	}
	for _, a := range gotAddrs {
		if a == wantAddr || a == "0.0.0.0" {
			return durabilityFinding{Name: name, OK: true,
				Detail: fmt.Sprintf("listening on %s:%d, as the unit declares", a, port)}
		}
	}
	return durabilityFinding{Name: name,
		Detail: fmt.Sprintf("the unit declares %s:%d but the running daemon is bound to %s:%d — "+
			"the unit was changed and the process was not restarted onto it",
			wantAddr, port, strings.Join(gotAddrs, ","), port)}
}

// declaredTokenBind extracts the token listener's address and port from the
// unit's Environment=DEJIMAD_TOKEN_TCP line. A zero port means none is declared.
func declaredTokenBind(unit string) (string, int) {
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		const key = "Environment=DEJIMAD_TOKEN_TCP="
		if !strings.HasPrefix(line, key) {
			continue
		}
		hostPort := strings.TrimSpace(strings.TrimPrefix(line, key))
		i := strings.LastIndex(hostPort, ":")
		if i < 0 {
			return "", 0
		}
		port, err := strconv.Atoi(hostPort[i+1:])
		if err != nil {
			return "", 0
		}
		return hostPort[:i], port
	}
	return "", 0
}

// listeningAddrsOnPort parses /proc/net/tcp and returns the dotted-quad local
// addresses in LISTEN state on the given port.
//
// /proc/net/tcp rather than `ss` or `netstat`: a minimal WSL image ships neither,
// and "it worked on mine" is how this project's last shell-tool assumption
// landed. Parsing happens in GO — the shell gets no decisions, only text.
func listeningAddrsOnPort(procNetTCP string, port int) []string {
	const stateListen = "0A"
	var addrs []string
	for i, line := range strings.Split(procNetTCP, "\n") {
		if i == 0 { // header
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 || f[3] != stateListen {
			continue
		}
		local := f[1]
		j := strings.LastIndex(local, ":")
		if j < 0 {
			continue
		}
		p, err := strconv.ParseInt(local[j+1:], 16, 32)
		if err != nil || int(p) != port {
			continue
		}
		if addr, ok := hexToDottedQuad(local[:j]); ok {
			addrs = append(addrs, addr)
		}
	}
	return addrs
}

// hexToDottedQuad converts /proc/net/tcp's little-endian hex address to dotted
// quad: 0100007F is 127.0.0.1, not 1.0.0.127.
func hexToDottedQuad(h string) (string, bool) {
	if len(h) != 8 {
		return "", false
	}
	var b [4]byte
	for i := 0; i < 4; i++ {
		v, err := strconv.ParseUint(h[i*2:i*2+2], 16, 8)
		if err != nil {
			return "", false
		}
		b[3-i] = byte(v)
	}
	return fmt.Sprintf("%d.%d.%d.%d", b[0], b[1], b[2], b[3]), true
}

// durabilityIslandImage asks docker whether the image is there.
//
// Not the build's exit code: a first `dejima init` failing after a setup that
// said it worked is the failure this replaces, and the build reporting success
// is exactly what it did.
func durabilityIslandImage(ctx context.Context, rt distroRuntime) durabilityFinding {
	const name = "island image"
	out, err := rt.Run(ctx, "docker images --format {{.Repository}}:{{.Tag}}")
	if err != nil {
		return durabilityFinding{Name: name, Detail: "docker did not answer after the restart: " + err.Error()}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == api.DefaultImage {
			return durabilityFinding{Name: name, OK: true, Detail: api.DefaultImage + " present"}
		}
	}
	return durabilityFinding{Name: name,
		Detail: fmt.Sprintf("docker answered but does not have %s — the first `dejima init` would fail", api.DefaultImage)}
}

// liveDistro is the production distroRuntime: a real WSL distro.
type liveDistro struct{ distro string }

func (d liveDistro) Name() string { return d.distro }

func (d liveDistro) Run(ctx context.Context, script string) (string, error) {
	return wsl.Run(ctx, d.distro, script)
}

func (d liveDistro) Terminate(ctx context.Context) error {
	_, err := wsl.RunExe(ctx, "--terminate", d.distro)
	return err
}

// runningIslandCount reports how many islands are up, so the restart does not
// bounce live work without saying so.
//
// The ruling that asked for this check did not cover the re-run case: at the end
// of a FIRST install nothing is running and the restart costs nothing, but setup
// is also re-run against working hosts, and terminating the distro there stops
// every island mid-task. Asking is not a skip — declining still fails, loudly,
// with durability reported as unproven. What must never happen is a clean bill
// of health over an unproven host.
func runningIslandCount(ctx context.Context, rt distroRuntime) int {
	out, err := rt.Run(ctx, "docker ps --format {{.Names}}")
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "dejima-") {
			n++
		}
	}
	return n
}
