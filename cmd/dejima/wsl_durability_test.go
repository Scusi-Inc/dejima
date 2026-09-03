package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeDistro answers scripted in-distro commands, so every assertion can be
// made wrong ON PURPOSE. That is the point: an assertion nobody has watched fail
// is not evidence of anything, and this file exists because setup's old
// self-report could not fail.
type fakeDistro struct {
	terminated  int
	termErr     error
	answers     map[string]string // substring of the script -> stdout
	failing     map[string]bool   // substring of the script -> return an error
	ran         []string
	socketAfter int // succeed the `test -S` probe only from this call onward
	socketCalls int
}

func (f *fakeDistro) Name() string { return "TestDistro" }

func (f *fakeDistro) Terminate(context.Context) error {
	f.terminated++
	return f.termErr
}

func (f *fakeDistro) Run(_ context.Context, script string) (string, error) {
	f.ran = append(f.ran, script)
	if strings.Contains(script, "test -S") {
		f.socketCalls++
		if f.socketCalls < f.socketAfter {
			return "", errors.New("no such file")
		}
		return "", nil
	}
	for frag, fail := range f.failing {
		if fail && strings.Contains(script, frag) {
			return "", errors.New("command failed: " + frag)
		}
	}
	for frag, out := range f.answers {
		if strings.Contains(script, frag) {
			return out, nil
		}
	}
	return "", nil
}

// Port 7274 is 0x1C6A, and 31.1.0.1 is 0100011F in /proc/net/tcp's
// little-endian hex. Both were wrong in the first draft of this fixture — the
// port as 0x1C72 (7282) — which made the healthy case assert against a listener
// that was not there. A fixture that does not represent a working host cannot
// show that a check passes on one.
const (
	fixtureListenPortHex = "1C6A"     // 7274
	fixtureBridgeHex     = "0100011F" // 31.1.0.1
	fixtureLoopbackHex   = "0100007F" // 127.0.0.1
	fixtureBridgeAddr    = "31.1.0.1"
)

func fixtureUnit() string {
	return "[Service]\nEnvironment=HOME=/root\n" +
		"Environment=DEJIMAD_TOKEN_TCP=" + fixtureBridgeAddr + ":7274\n"
}

func fixtureProcNetTCP(addrHex string) string {
	return "  sl  local_address rem_address   st\n" +
		"   0: " + fixtureLoopbackHex + ":0016 00000000:0000 0A\n" +
		"   1: " + addrHex + ":" + fixtureListenPortHex + " 00000000:0000 0A\n"
}

// A healthy distro: HOME, a socket, a live pid, a unit declaring the bridge
// bind, a listener actually on it, and the image present.
func healthyDistro() *fakeDistro {
	return &fakeDistro{
		socketAfter: 1,
		answers: map[string]string{
			"printenv HOME":                           "/root\n",
			"pgrep -x dejimad":                        "412\n",
			"cat /etc/systemd/system/dejimad.service": fixtureUnit(),
			"cat /proc/net/tcp":                       fixtureProcNetTCP(fixtureBridgeHex),
			"docker images":                           "dejima/island:latest\nubuntu:24.04\n",
			"docker ps":                               "",
		},
	}
}

func TestProveDurability_PassesOnAHealthyHost(t *testing.T) {
	f := healthyDistro()
	var out bytes.Buffer
	if err := proveDurability(context.Background(), f, &out); err != nil {
		t.Fatalf("a healthy host must pass: %v\n%s", err, out.String())
	}
	if f.terminated != 1 {
		t.Errorf("the distro was terminated %d times; the restart IS the experiment", f.terminated)
	}
	if !strings.Contains(out.String(), "verified: the daemon came back after a distro restart") {
		t.Errorf("the run did not say what it verified:\n%s", out.String())
	}
}

func TestProveDurability_FailsOnEachBrokenEndState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(*fakeDistro)
		expect string
		fast   bool // shorten the socket-wait budget for cases that must time out
	}{
		{
			// The budget and poll are shortened for this one case; the decision is
			// what is under test, not how long it is willing to wait.
			name:   "the daemon never comes back",
			break_: func(f *fakeDistro) { f.socketAfter = 1000 },
			expect: "did not come back",
			fast:   true,
		},
		{
			name:   "pgrep exits 0 but names no pid",
			break_: func(f *fakeDistro) { f.answers["pgrep -x dejimad"] = "\n" },
			expect: "named no pid",
		},
		{
			name:   "no process at all",
			break_: func(f *fakeDistro) { f.failing = map[string]bool{"pgrep -x dejimad": true} },
			expect: "durability checks failed",
		},
		{
			name: "the unit was corrected but the process kept its old bind",
			break_: func(f *fakeDistro) {
				// unit says 31.1.0.1, the process is on loopback
				f.answers["cat /proc/net/tcp"] = fixtureProcNetTCP(fixtureLoopbackHex)
			},
			expect: "the unit was changed and the process was not restarted onto it",
		},
		{
			name: "nothing listening on the declared port",
			break_: func(f *fakeDistro) {
				f.answers["cat /proc/net/tcp"] = "  sl  local_address rem_address   st\n"
			},
			expect: "nothing is listening",
		},
		{
			name: "docker answers but the image is absent",
			break_: func(f *fakeDistro) {
				f.answers["docker images"] = "ubuntu:24.04\n"
			},
			expect: "does not have dejima/island:latest",
		},
		{
			name:   "docker does not answer",
			break_: func(f *fakeDistro) { f.failing = map[string]bool{"docker images": true} },
			expect: "docker did not answer",
		},
		{
			name:   "the distro cannot be terminated",
			break_: func(f *fakeDistro) { f.termErr = errors.New("wsl.exe: access denied") },
			expect: "durability is unproven",
		},
		{
			name:   "the distro never boots again",
			break_: func(f *fakeDistro) { f.failing = map[string]bool{"printenv HOME": true} },
			expect: "did not come back after being terminated",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.fast {
				budget, poll := durabilitySocketBudget, durabilitySocketPoll
				durabilitySocketBudget, durabilitySocketPoll = 30*time.Millisecond, time.Millisecond
				t.Cleanup(func() { durabilitySocketBudget, durabilitySocketPoll = budget, poll })
			}
			f := healthyDistro()
			tc.break_(f)

			var out bytes.Buffer
			err := proveDurability(context.Background(), f, &out)
			if err == nil {
				t.Fatalf("a broken end state passed. Output was:\n%s", out.String())
			}
			combined := err.Error() + out.String()
			if !strings.Contains(combined, tc.expect) {
				t.Errorf("the failure does not say what was wrong.\nwant substring: %q\ngot error: %v\noutput:\n%s",
					tc.expect, err, out.String())
			}
		})
	}
}

// It must never skip. "Cannot reach the distro" is a failure, not an absence of
// information — this project has shipped six guards that reported green with no
// subject, and three printed ok on a skip.
func TestProveDurability_NeverPassesWhenItCannotLook(t *testing.T) {
	f := &fakeDistro{socketAfter: 1, failing: map[string]bool{"printenv HOME": true}}
	var out bytes.Buffer
	if err := proveDurability(context.Background(), f, &out); err == nil {
		t.Fatalf("an unreachable distro produced a pass:\n%s", out.String())
	}
	if strings.Contains(out.String(), "verified:") {
		t.Errorf("it printed a verification it did not perform:\n%s", out.String())
	}
}

// /proc/net/tcp is little-endian hex. Getting this backwards would call every
// loopback bind a bridge bind, which is the exact defect the listener check
// exists to catch — so it would pass on the broken host and fail on the good one.
func TestHexToDottedQuadIsLittleEndian(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"0100007F", "127.0.0.1"},
		{"00000000", "0.0.0.0"},
		{"0111AC1F", "31.172.17.1"},
	} {
		got, ok := hexToDottedQuad(tc.in)
		if !ok {
			t.Fatalf("%s did not parse", tc.in)
		}
		if got != tc.want {
			t.Errorf("%s -> %s, want %s", tc.in, got, tc.want)
		}
	}
	if _, ok := hexToDottedQuad("0100"); ok {
		t.Error("a short address parsed; a truncated /proc line must not become an address")
	}
}

// A unit with no token listener is not a failure — a VM-backed engine writes no
// override and loopback is correct there. Reading "must be on the bridge" would
// fail those hosts for being correctly configured.
func TestDeclaredTokenBind(t *testing.T) {
	addr, port := declaredTokenBind("[Service]\nEnvironment=DEJIMAD_TOKEN_TCP=172.17.0.1:7274\n")
	if addr != "172.17.0.1" || port != 7274 {
		t.Errorf("got %s:%d, want 172.17.0.1:7274", addr, port)
	}
	if _, port := declaredTokenBind("[Service]\nEnvironment=HOME=/root\n"); port != 0 {
		t.Error("a unit with no token listener must report no declared port")
	}
}
