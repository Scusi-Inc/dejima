package wsl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestHostRoundTrip(t *testing.T) {
	cases := []struct {
		in     string
		isHost bool
		distro string
	}{
		{"wsl://dejima", true, "dejima"},
		{"wsl://Ubuntu-22.04", true, "Ubuntu-22.04"},
		{"wsl://", true, DefaultDistro},  // shorthand
		{"wsl:///", true, DefaultDistro}, // stray slash
		{"  wsl://dejima  ", true, "dejima"},
		{"100.64.0.1:7273", false, ""},
		{"", false, ""},
		{"wsl.example.com:7273", false, ""}, // not the scheme, just a similar name
	}
	for _, c := range cases {
		if got := IsHost(c.in); got != c.isHost {
			t.Errorf("IsHost(%q) = %v, want %v", c.in, got, c.isHost)
		}
		if got := Distro(c.in); got != c.distro {
			t.Errorf("Distro(%q) = %q, want %q", c.in, got, c.distro)
		}
	}
	if got := Host("dejima"); got != "wsl://dejima" {
		t.Errorf("Host(dejima) = %q", got)
	}
	if got := Host(""); got != "wsl://"+DefaultDistro {
		t.Errorf("Host(\"\") = %q, want the default distro", got)
	}
}

// Off Windows a wsl:// target must fail with the reason, not by shelling out to
// a wsl.exe that doesn't exist.
func TestUnsupportedOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this asserts the non-Windows guard")
	}
	if Supported() {
		t.Fatal("Supported() should be false off Windows")
	}
	if Available() {
		t.Fatal("Available() should be false off Windows")
	}
	if _, err := Dial(context.Background(), "dejima"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Dial error = %v, want ErrUnsupported", err)
	}
	if _, err := Probe(context.Background(), "dejima"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Probe error = %v, want ErrUnsupported", err)
	}
	if _, err := RunExe(context.Background(), "--status"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("RunExe error = %v, want ErrUnsupported", err)
	}
}

func TestParseDistroList(t *testing.T) {
	// Real `wsl -l -v` output shape, including the leading-star default marker.
	const out = `  NAME            STATE           VERSION
* dejima          Running         2
  Ubuntu-20.04    Stopped         1
`
	got := parseDistroList(out)
	if len(got) != 2 {
		t.Fatalf("parsed %d distros, want 2: %+v", len(got), got)
	}
	if got[0] != (Distribution{Name: "dejima", State: "Running", Version: 2, Default: true}) {
		t.Errorf("first = %+v", got[0])
	}
	if got[1] != (Distribution{Name: "Ubuntu-20.04", State: "Stopped", Version: 1}) {
		t.Errorf("second = %+v", got[1])
	}
}

func TestParseDistroListEmpty(t *testing.T) {
	if got := parseDistroList(""); len(got) != 0 {
		t.Errorf("empty output should parse to nothing, got %+v", got)
	}
	if got := parseDistroList("  NAME  STATE  VERSION\n"); len(got) != 0 {
		t.Errorf("header-only output should parse to nothing, got %+v", got)
	}
}

// wsl.exe emits its own diagnostics as UTF-16LE while text forwarded from inside
// the distro is UTF-8. Getting this wrong turns "there is no distribution with
// the supplied name" into mojibake in the error the user reads.
func TestDecodeWSLText(t *testing.T) {
	utf16le := func(s string) []byte {
		var b []byte
		for _, u := range utf16.Encode([]rune(s)) {
			b = append(b, byte(u), byte(u>>8))
		}
		return b
	}
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"plain utf-8 passes through", []byte("socat: not found"), "socat: not found"},
		{"utf-16 with BOM", append([]byte{0xFF, 0xFE}, utf16le("no distribution")...), "no distribution"},
		{"utf-16 without BOM", utf16le("Wsl/Service/E_FAIL"), "Wsl/Service/E_FAIL"},
		{"empty", nil, ""},
		{"non-ascii utf-8 survives", []byte("café ✓"), "café ✓"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decodeWSLText(c.in); got != c.want {
				t.Errorf("decodeWSLText(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestReadyRequiresEveryPart(t *testing.T) {
	full := Report{Distro: "d", Exists: true, Version: 2, HasSocat: true, HasDocker: true, HasDejima: true, SocketUp: true}
	if !full.Ready() {
		t.Fatal("a fully-provisioned report should be Ready")
	}
	// Each field is load-bearing: flipping any one off must un-ready it, so a
	// half-provisioned distro can never be reported as a working host.
	for name, mutate := range map[string]func(*Report){
		"exists":  func(r *Report) { r.Exists = false },
		"version": func(r *Report) { r.Version = 1 },
		"socat":   func(r *Report) { r.HasSocat = false },
		"docker":  func(r *Report) { r.HasDocker = false },
		"dejimad": func(r *Report) { r.HasDejima = false },
		"socket":  func(r *Report) { r.SocketUp = false },
	} {
		t.Run(name, func(t *testing.T) {
			r := full
			mutate(&r)
			if r.Ready() {
				t.Errorf("Ready() should be false without %s", name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Transport
//
// Dial's contract is "a net.Conn carrying whatever the subprocess writes." We
// verify it by substituting a fake wsl.exe (this test binary re-executed) that
// speaks a real HTTP response over stdio, then driving a real http.Client
// through it — the same path api.NewWSLClient uses.
// ---------------------------------------------------------------------------

// TestHelperProcess is the fake `wsl.exe`. It is not a real test; it runs only
// when the environment marker is set.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("DEJIMA_WSL_HELPER") == "" {
		return
	}
	switch os.Getenv("DEJIMA_WSL_HELPER") {
	case "http":
		// Consume the request so the client's write doesn't block, then reply.
		go io.Copy(io.Discard, os.Stdin)
		body := `{"ok":true}`
		fmt.Fprintf(os.Stdout, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
		// Hold the pipe open briefly so the client reads before EOF.
		time.Sleep(200 * time.Millisecond)
	case "socat-missing":
		fmt.Fprintln(os.Stderr, "sh: 1: socat: not found")
		os.Exit(127)
	case "echo":
		io.Copy(os.Stdout, os.Stdin)
	}
	os.Exit(0)
}

// fakeWSL points execCommand at this test binary's helper process and forces
// Supported() to report true, so the transport can be exercised on Linux CI.
func fakeWSL(t *testing.T, mode string) {
	t.Helper()
	prev := execCommand
	t.Cleanup(func() { execCommand = prev })
	execCommand = func(_ string, _ ...string) *exec.Cmd {
		c := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
		c.Env = append(os.Environ(), "DEJIMA_WSL_HELPER="+mode)
		return c
	}
}

// dialForTest bypasses the Supported() guard (which is false on the Linux CI
// box) while exercising the real pipe/pump machinery.
func dialForTest(t *testing.T, mode string) *procConn {
	t.Helper()
	fakeWSL(t, mode)
	cmd := execCommand("wsl.exe")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	errBuf := &syncBuffer{}
	cmd.Stderr = errBuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pc := newProcConn(cmd, stdin, stdout, errBuf, "test")
	t.Cleanup(func() { _ = pc.Close() })
	return pc
}

// A full HTTP round-trip over the subprocess transport — the thing the client
// actually does.
func TestTransportCarriesHTTP(t *testing.T) {
	hc := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialForTest(t, "http"), nil
			},
		},
	}
	resp, err := hc.Get("http://dejimad/v1/health")
	if err != nil {
		t.Fatalf("GET over the wsl transport: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != `{"ok":true}` {
		t.Errorf("body = %q", b)
	}
}

// A missing socat presents to net/http as a bare EOF. The conn must translate
// that into the actionable message instead — this is the failure a Windows user
// is most likely to hit after a partial setup.
func TestReadErrorNamesMissingSocat(t *testing.T) {
	pc := dialForTest(t, "socat-missing")
	// Give the helper a moment to write stderr and exit.
	deadline := time.Now().Add(5 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		buf := make([]byte, 64)
		_ = pc.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		_, err = pc.Read(buf)
		if err != nil && !strings.Contains(err.Error(), "deadline") {
			break
		}
	}
	if err == nil {
		t.Fatal("expected a read error when socat is missing")
	}
	if !strings.Contains(err.Error(), "socat isn't installed") {
		t.Errorf("error should name the missing socat and the fix, got: %v", err)
	}
	if !strings.Contains(err.Error(), "dejima wsl setup") {
		t.Errorf("error should point at the remedy, got: %v", err)
	}
}

// Close must reap the subprocess: a TUI session opens and drops many
// connections, and a leaked wsl.exe per dial would pile up.
func TestCloseKillsSubprocess(t *testing.T) {
	pc := dialForTest(t, "echo")
	if pc.cmd.Process == nil {
		t.Fatal("no process started")
	}
	if err := pc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-pc.Reaped():
	case <-time.After(10 * time.Second):
		t.Fatal("subprocess was not reaped after Close")
	}
	// Closing twice must be safe — http.Transport does it on some paths.
	if err := pc.Close(); err != nil {
		t.Errorf("second Close should be a no-op, got %v", err)
	}
}

func TestConnAddrs(t *testing.T) {
	pc := dialForTest(t, "echo")
	if got := pc.RemoteAddr().String(); got != "wsl://test" {
		t.Errorf("RemoteAddr = %q, want wsl://test", got)
	}
	if got := pc.RemoteAddr().Network(); got != "wsl" {
		t.Errorf("Network = %q, want wsl", got)
	}
}
