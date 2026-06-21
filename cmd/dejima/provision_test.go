package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProvStateProgress(t *testing.T) {
	st := &provState{Answers: map[string]string{}}
	if st.done("x") {
		t.Fatal("fresh state should have nothing done")
	}
	st.markDone("system-config")
	st.markDone("system-config") // idempotent
	st.markDone("tooling")
	if !st.done("system-config") || !st.done("tooling") {
		t.Fatal("markDone didn't record")
	}
	if len(st.CompletedPhases) != 2 {
		t.Fatalf("duplicate markDone should not double-add: %v", st.CompletedPhases)
	}
	st.markSkipped("vm-rightsize")
	st.markSkipped("vm-rightsize") // dedup
	if len(st.Skipped) != 1 {
		t.Fatalf("markSkipped should dedup: %v", st.Skipped)
	}
}

func TestProvStateSaveLoadRoundtrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := loadProvState()
	if st.StartedAt.IsZero() {
		t.Fatal("loadProvState should stamp StartedAt")
	}
	st.markDone("system-config")
	st.Answers["tailnet_fqdn"] = "mini.tail.ts.net"
	saveProvState(st)

	got := loadProvState()
	if !got.done("system-config") {
		t.Fatal("completed phase didn't persist")
	}
	if got.Answers["tailnet_fqdn"] != "mini.tail.ts.net" {
		t.Fatalf("answers didn't persist: %v", got.Answers)
	}

	// resetProvState removes the file → a fresh, empty state.
	resetProvState()
	if p, _ := provStatePath(); p != "" {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("resetProvState should remove the state file")
		}
	}
	if loadProvState().done("system-config") {
		t.Fatal("after reset, nothing should be done")
	}
}

func TestProvConfirmNonInteractive(t *testing.T) {
	pc := &provCtx{yes: true}
	if !pc.confirm("auto-yes default", true) {
		t.Fatal("--yes should take the default-yes path")
	}
	if pc.confirm("auto-no default", false) {
		t.Fatal("--yes must NOT auto-take a default-no (never auto-do a guarded step)")
	}
}

func TestProvAddManual(t *testing.T) {
	pc := &provCtx{}
	pc.addManual("do a thing")
	pc.addManual("do another")
	if len(pc.manual) != 2 {
		t.Fatalf("addManual: %v", pc.manual)
	}
}

func TestIsConnectionError(t *testing.T) {
	conn := []error{
		errors.New("daemon unreachable: dial tcp ..."),
		errors.New("dial tcp 1.2.3.4:7273: connect: connection refused"),
		errors.New("lookup bogus.host: no such host"),
		errors.New("read tcp: i/o timeout"),
	}
	for _, e := range conn {
		if !isConnectionError(e) {
			t.Errorf("should be a connection error: %v", e)
		}
	}
	notConn := []error{
		errors.New("island \"x\" already exists"),
		errors.New("invalid JSON"),
		nil,
	}
	for _, e := range notConn {
		if e == nil {
			continue
		}
		if isConnectionError(e) {
			t.Errorf("should NOT be a connection error: %v", e)
		}
	}
}

// TestProvStatePathUnderRoot guards that the state file lives in ~/.dejima, so
// it persists across runs alongside the rest of Dejima's per-user state.
func TestProvStatePathUnderRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, err := provStatePath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".dejima", "provisioning-state.json")
	if p != want {
		t.Fatalf("provStatePath = %q want %q", p, want)
	}
}
