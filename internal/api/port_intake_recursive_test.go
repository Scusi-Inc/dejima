package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime/runtimetest"
)

// treeFixture builds a scope directory with the awkward entries a real import
// hits: nested files, a symlink pointing OUT of the scope, a symlink pointing
// IN, a directory symlink, and an empty directory.
func treeFixture(t *testing.T) (scope string, secret string) {
	t.Helper()
	base := t.TempDir()
	scope = filepath.Join(base, "scope")
	secret = filepath.Join(base, "secrets")
	mustMkdir(t, filepath.Join(scope, "sub"))
	mustMkdir(t, filepath.Join(scope, "empty"))
	mustMkdir(t, secret)

	mustWrite(t, filepath.Join(scope, "a.txt"), "aaa")
	mustWrite(t, filepath.Join(scope, "sub", "b.txt"), "bbbb")
	mustWrite(t, filepath.Join(secret, "id_rsa"), "PRIVATE KEY")

	mustSymlink(t, filepath.Join(secret, "id_rsa"), filepath.Join(scope, "escape"))
	mustSymlink(t, secret, filepath.Join(scope, "dirlink"))
	mustSymlink(t, filepath.Join(scope, "a.txt"), filepath.Join(scope, "alias"))
	return scope, secret
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
}

// The walk must never follow a symlink, and must SAY which ones it skipped.
// Silently omitting files is its own bug: the caller cannot tell a skipped
// symlink from a file that was never there.
func TestWalkIntakeTree_NeverFollowsSymlinks(t *testing.T) {
	scope, _ := treeFixture(t)

	files, skipped, err := walkIntakeTree(scope)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	var got []string
	for _, f := range files {
		got = append(got, f.rel)
	}
	want := []string{"a.txt", "sub/b.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("files = %v, want %v", got, want)
	}

	// The escaping link must not appear as a file under ANY name — following it
	// would copy a private key out of the scope under an innocuous path.
	for _, f := range files {
		if f.rel == "escape" || strings.HasPrefix(f.rel, "dirlink/") || f.rel == "alias" {
			t.Errorf("a symlink was followed and would be imported: %q", f.rel)
		}
	}

	skips := map[string]string{}
	for _, s := range skipped {
		skips[s.Rel] = s.Reason
	}
	for _, rel := range []string{"escape", "dirlink", "alias"} {
		if skips[rel] == "" {
			t.Errorf("symlink %q was neither imported nor reported — a silent omission", rel)
		} else if !strings.Contains(skips[rel], "symlink") {
			t.Errorf("skip reason for %q = %q, want it to name the symlink", rel, skips[rel])
		}
	}
}

// The walk is only half the protection, and the weaker half. resolveWithinScope
// is what actually refuses an escape, and the walk deliberately still routes
// every file through it. This asserts that directly, so a future change that
// "optimises" the walk to copy discovered paths cannot quietly remove the line
// that matters.
func TestResolveWithinScope_RefusesEscapingSymlinks(t *testing.T) {
	scope, _ := treeFixture(t)

	if _, _, err := resolveWithinScope(scope, "a.txt"); err != nil {
		t.Fatalf("a real file in the scope should resolve, got: %v", err)
	}
	for _, rel := range []string{"escape", "dirlink/id_rsa"} {
		if _, _, err := resolveWithinScope(scope, rel); err == nil {
			t.Errorf("%q resolved — a symlink out of the scope must be refused", rel)
		} else if !strings.Contains(err.Error(), "escapes the scope") {
			t.Errorf("%q refused for the wrong reason: %v", rel, err)
		}
	}
}

// A directory of nothing but symlinks looks exactly like an empty directory once
// they are skipped. "I copied nothing" and "there was nothing to copy" are
// different sentences, and the refusal has to say which one happened.
func TestWalkIntakeTree_EmptyAfterSkipsIsDistinguishable(t *testing.T) {
	base := t.TempDir()
	scope := filepath.Join(base, "scope")
	mustMkdir(t, scope)
	mustWrite(t, filepath.Join(base, "target.txt"), "x")
	mustSymlink(t, filepath.Join(base, "target.txt"), filepath.Join(scope, "only-a-link"))

	files, skipped, err := walkIntakeTree(scope)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("files = %v, want none", files)
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v, want the one symlink", skipped)
	}
	if got := summarizeSkips(skipped); !strings.Contains(got, "symlink") {
		t.Errorf("summary = %q; an empty-import refusal must explain WHY it found "+
			"nothing, or a directory of symlinks reads as an empty directory", got)
	}
}

// Empty directories are not files and must not be counted as importable — a
// tree of empty directories would otherwise pass the empty-result check while
// importing nothing.
func TestWalkIntakeTree_DirectoriesAreNotFiles(t *testing.T) {
	base := t.TempDir()
	mustMkdir(t, filepath.Join(base, "a", "b", "c"))
	files, _, err := walkIntakeTree(base)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("files = %v, want none — directories are not importable content", files)
	}
}

// Ordering is deterministic so the Ledger reads as a stable sequence and two
// imports of the same tree are comparable.
func TestWalkIntakeTree_DeterministicOrder(t *testing.T) {
	base := t.TempDir()
	for _, n := range []string{"z.txt", "a.txt", "m.txt"} {
		mustWrite(t, filepath.Join(base, n), n)
	}
	files, _, err := walkIntakeTree(base)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.rel)
	}
	if strings.Join(got, ",") != "a.txt,m.txt,z.txt" {
		t.Errorf("order = %v, want sorted", got)
	}
}

// Sizes come off the walk so the byte cap can be answered before anything moves.
func TestWalkIntakeTree_ReportsSizesForTheCap(t *testing.T) {
	base := t.TempDir()
	mustWrite(t, filepath.Join(base, "a.txt"), "12345")
	files, _, err := walkIntakeTree(base)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(files) != 1 || files[0].size != 5 {
		t.Fatalf("files = %+v, want one file of 5 bytes — without a size the byte "+
			"cap could only be enforced after copying", files)
	}
}

// --- handler-level: the refusals that must happen BEFORE anything moves -----

// intakeServer builds a server with a running fake island and one granted scope
// rooted at hostPath.
func intakeServer(t *testing.T, hostPath string) (*Server, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	p := &project.Project{
		Name:         "isl",
		DesiredState: project.StateRunning,
		Ports: []project.PortScope{{
			Name: "vault", HostPath: hostPath, Mode: "ro", GrantedAt: time.Now(),
		}},
	}
	if err := p.Save(); err != nil {
		t.Fatalf("save project: %v", err)
	}
	// The default ledger is a process-wide singleton that pins to the first HOME
	// it sees, so without a reset these tests inherit an earlier test's deleted
	// temp dir, ledgerAppend fails, and every file "fails to cross" for a reason
	// that has nothing to do with the code under test. cliEnv resets it for the
	// same reason. A logger is required too: ledgerAppend logs before returning
	// its error, so a nil log turns that failure into a panic.
	ledger.ResetDefault()
	return &Server{
		rt:  runtimetest.New(), // the fake reports running by default
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, "isl"
}

func postIntake(t *testing.T, s *Server, island string, body PortIntakeRequest) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/v1/islands/"+island+"/port/intake", bytes.NewReader(raw))
	r.SetPathValue("name", island)
	w := httptest.NewRecorder()
	s.handlePortIntake(w, r)
	return w.Code, w.Body.String()
}

// A directory without the flag must be refused — and the refusal has to NAME the
// flag. The old message said "intake is single-file in V1", which reads as "not
// supported" and sends people to tar + cp + untar over exec: a path that works
// and produces no Ledger entries at all. The wording is the feature.
func TestIntake_DirectoryWithoutRecursiveNamesTheFlag(t *testing.T) {
	scope, _ := treeFixture(t)
	s, isl := intakeServer(t, scope)

	code, body := postIntake(t, s, isl, PortIntakeRequest{Scope: "vault", SrcRel: "sub"})
	if code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400: %s", code, body)
	}
	if !strings.Contains(body, "recursive") {
		t.Errorf("the refusal must name the flag, or the caller reaches for tar; got: %s", body)
	}
	if strings.Contains(body, "single-file in V1") {
		t.Errorf("the dead-end wording survived: %s", body)
	}
}

// Caps are refused UP FRONT, with the real numbers. "Import my home directory"
// must fail in a second, not half-copy for ten minutes — and a refusal that does
// not say how far over you are just prompts a guess.
func TestIntake_FileCapRefusedBeforeAnythingMoves(t *testing.T) {
	base := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		mustWrite(t, filepath.Join(base, n+".txt"), n)
	}
	s, isl := intakeServer(t, base)

	code, body := postIntake(t, s, isl, PortIntakeRequest{
		Scope: "vault", SrcRel: ".", Recursive: true, MaxFiles: 2,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400: %s", code, body)
	}
	if !strings.Contains(body, "3") || !strings.Contains(body, "2") {
		t.Errorf("the refusal should carry both the actual count and the limit; got: %s", body)
	}
	// Nothing may have been copied: the fake runtime records every copy, and the
	// whole point of an up-front cap is that the refusal precedes the transfer.
	if n := s.rt.(*runtimetest.Fake).CopyCount(); n != 0 {
		t.Errorf("%d file(s) were copied despite the cap refusing the import", n)
	}
}

func TestIntake_ByteCapRefusedBeforeAnythingMoves(t *testing.T) {
	base := t.TempDir()
	mustWrite(t, filepath.Join(base, "big.txt"), strings.Repeat("x", 4096))
	s, isl := intakeServer(t, base)

	code, body := postIntake(t, s, isl, PortIntakeRequest{
		Scope: "vault", SrcRel: ".", Recursive: true, MaxBytes: 100,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400: %s", code, body)
	}
	if n := s.rt.(*runtimetest.Fake).CopyCount(); n != 0 {
		t.Errorf("%d file(s) crossed despite the byte cap refusing the import", n)
	}
}

// "I copied nothing" and "there was nothing to copy" are different sentences.
// A directory of only symlinks must not report success over an empty set, and
// must explain why it found nothing — otherwise it reads as an empty directory.
func TestIntake_EmptyResultRefusedLoudly(t *testing.T) {
	base := t.TempDir()
	scope := filepath.Join(base, "scope")
	mustMkdir(t, scope)
	mustWrite(t, filepath.Join(base, "target.txt"), "x")
	mustSymlink(t, filepath.Join(base, "target.txt"), filepath.Join(scope, "link"))

	s, isl := intakeServer(t, scope)
	code, body := postIntake(t, s, isl, PortIntakeRequest{Scope: "vault", SrcRel: ".", Recursive: true})
	if code == http.StatusOK {
		t.Fatalf("an import that matched nothing reported success: %s", body)
	}
	if !strings.Contains(body, "symlink") {
		t.Errorf("the refusal must say WHY it found nothing, or a directory of "+
			"symlinks is indistinguishable from an empty one; got: %s", body)
	}
}

// The positive control for the two cap tests above. They assert CopyCount()==0,
// which proves nothing unless the counter can reach a non-zero value on this
// path — a broken counter would satisfy them permanently. It is also the
// happy-path test: a real tree crosses, one Ledger-backed file at a time.
func TestIntake_RecursiveActuallyCopiesEveryFile(t *testing.T) {
	scope, _ := treeFixture(t) // a.txt, sub/b.txt, plus three symlinks
	s, isl := intakeServer(t, scope)

	code, body := postIntake(t, s, isl, PortIntakeRequest{Scope: "vault", SrcRel: ".", Recursive: true})
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", code, body)
	}
	var resp PortIntakeResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Files) != 2 {
		t.Fatalf("files = %+v, want the two regular files", resp.Files)
	}
	if n := s.rt.(*runtimetest.Fake).CopyCount(); n != 2 {
		t.Errorf("CopyCount = %d, want 2 — if this is 0 the counter is broken and "+
			"the cap tests' \"nothing moved\" assertions are vacuous", n)
	}
	if resp.BatchID == "" {
		t.Error("no batch id — the per-file Ledger entries have nothing grouping them")
	}
	if len(resp.Skipped) != 3 {
		t.Errorf("skipped = %+v, want the three symlinks reported", resp.Skipped)
	}
	if len(resp.Failed) != 0 {
		t.Errorf("unexpected failures: %+v", resp.Failed)
	}
	if resp.Bytes != 7 { // "aaa" + "bbbb"
		t.Errorf("bytes = %d, want 7", resp.Bytes)
	}
}

// Decision 4 of the spec: a partial import has a defined state and the caller is
// told. What crossed stays crossed and stays ledgered — there is NO rollback,
// because un-copying files is a destructive operation invented to tidy up a
// failure. But the response must not report success.
//
// 207 rather than 200 is the whole point: a 200 carrying a failure list is the
// "reports success while something didn't happen" shape, which is precisely what
// this surface exists to avoid.
func TestIntake_PartialFailureDoesNotReportSuccess(t *testing.T) {
	scope, _ := treeFixture(t)
	s, isl := intakeServer(t, scope)
	s.rt.(*runtimetest.Fake).CopyErrOn = "b.txt" // sub/b.txt fails, a.txt succeeds

	code, body := postIntake(t, s, isl, PortIntakeRequest{Scope: "vault", SrcRel: ".", Recursive: true})
	if code == http.StatusOK {
		t.Fatalf("a partial import reported 200; a success code over an incomplete "+
			"result is how it gets mistaken for a whole one: %s", body)
	}
	if code != http.StatusMultiStatus {
		t.Fatalf("code = %d, want 207: %s", code, body)
	}
	var resp PortIntakeResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Failed) != 1 || resp.Failed[0].Rel != "sub/b.txt" {
		t.Errorf("failed = %+v, want sub/b.txt named", resp.Failed)
	}
	if resp.Failed[0].Error == "" {
		t.Error("the failure carries no reason — the caller is told what failed but not why")
	}
	// No rollback: the file that DID cross is still reported as crossed.
	if len(resp.Files) != 1 || resp.Files[0].Rel != "a.txt" {
		t.Errorf("files = %+v, want a.txt still reported — nothing is rolled back", resp.Files)
	}
	if resp.Bytes != 3 {
		t.Errorf("bytes = %d, want 3 (only what actually crossed)", resp.Bytes)
	}
}
