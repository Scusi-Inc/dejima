package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/localmodel"
	"github.com/aoos/dejima/internal/runtime"
)

// A stopped backend is the state these tests are about, and it is not exotic:
// `brew services` cannot always bootstrap into the daemon's launchd domain, and
// any host reboot leaves the binary installed with nothing listening. The
// backend CLI is a CLIENT of that server, so every write path fails against it —
// pull downloads nothing and prints "run 'ollama serve'", which the daemon
// relayed to an operator on a different machine entirely.
//
// The real ollama is absent under test, so the only state a real backend can be
// in here is "not installed" — which is a different branch. Hence the fake and
// the Server.localBE seam.

// fakeBackend is a LocalBackend whose install/run state is set by the test and
// which records what was actually asked of it.
type fakeBackend struct {
	installed bool
	running   bool
	startErr  error

	startCalls int
	pulled     []string
	removed    []string
}

func (f *fakeBackend) Name() localmodel.Backend { return localmodel.BackendOllama }
func (f *fakeBackend) Endpoint() string         { return localmodel.OllamaEndpoint }
func (f *fakeBackend) AllowHostPort() string    { return localmodel.OllamaAllowHostPort }
func (f *fakeBackend) Detect(context.Context) (bool, bool) {
	return f.installed, f.running
}

func (f *fakeBackend) List(context.Context) ([]localmodel.InstalledModel, error) {
	return nil, nil
}

func (f *fakeBackend) Pull(_ context.Context, ref string) (io.ReadCloser, error) {
	f.pulled = append(f.pulled, ref)
	return io.NopCloser(strings.NewReader("pulling manifest\n")), nil
}

func (f *fakeBackend) Remove(_ context.Context, ref string) error {
	f.removed = append(f.removed, ref)
	return nil
}

func (f *fakeBackend) Install(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("installed\n")), nil
}

// Start mirrors the real one: a no-op when already answering, otherwise it
// brings the server up (or fails), and success means the backend now ANSWERS.
func (f *fakeBackend) Start(context.Context) error {
	f.startCalls++
	if f.running {
		return nil
	}
	if f.startErr != nil {
		return f.startErr
	}
	f.running = true
	return nil
}

// newTestServerWithBackend is newTestServer with the host inference backend
// swapped, since the handler under test is chosen by that state.
func newTestServerWithBackend(t *testing.T, be localmodel.LocalBackend) http.Handler {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ledger.ResetDefault()
	srv := joinBackground(t, NewServer(&fakeRuntime{status: runtime.StatusRunning},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	srv.localBE = be
	return srv.Handler()
}

// A pull against an installed-but-stopped backend must START it and then pull,
// not hand the operator the backend's own error.
func TestLocalPullStartsAStoppedBackend(t *testing.T) {
	be := &fakeBackend{installed: true, running: false}
	h := newTestServerWithBackend(t, be)

	rr := do(t, h, http.MethodPost, "/v1/local/models/qwen-coder-7b/pull", "")
	if !ok2xx(rr.Code) {
		t.Fatalf("POST pull: %d, body %s", rr.Code, rr.Body.String())
	}
	if be.startCalls == 0 {
		t.Error("the pull never tried to start the backend — it went straight to a CLI " +
			"that needs a server, which is the reported failure")
	}
	// The resolved ref, not the alias: ResolveRef still has to run.
	if len(be.pulled) != 1 || !strings.Contains(be.pulled[0], "qwen2.5-coder") {
		t.Errorf("pulled %v, want the resolved 7b ref once", be.pulled)
	}
}

// When the backend cannot be started, the pull must FAIL BEFORE shelling out.
//
// Reaching the CLI anyway is what produced the reported output: a 200 with a
// progress stream that carries the backend's "run 'ollama serve'" and then
// `ERROR: exit status 1`. Nothing was downloaded, so a success status and a
// stream are both lies, and the advice was for a machine the operator was not
// sitting at.
func TestLocalPullRefusesWhenTheBackendWillNotStart(t *testing.T) {
	be := &fakeBackend{installed: true, running: false, startErr: errors.New("did not answer within 30s")}
	h := newTestServerWithBackend(t, be)

	rr := do(t, h, http.MethodPost, "/v1/local/models/qwen-coder-7b/pull", "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503 — a stopped backend is this host being unable to "+
			"serve the request, and 2xx would put a progress stream over a no-op", rr.Code)
	}
	if len(be.pulled) != 0 {
		t.Errorf("the pull ran anyway (%v), so the operator still gets the backend's own "+
			"unfollowable error relayed from another machine", be.pulled)
	}
	if !strings.Contains(rr.Body.String(), "DAEMON HOST") {
		t.Errorf("the failure does not say which machine is stuck: %s", rr.Body.String())
	}
}

// `rm` is a client of the same server, so it has the same trap.
func TestLocalRemoveStartsAStoppedBackend(t *testing.T) {
	be := &fakeBackend{installed: true, running: false}
	h := newTestServerWithBackend(t, be)

	rr := do(t, h, http.MethodDelete, "/v1/local/models/qwen-coder-7b", "")
	if !ok2xx(rr.Code) {
		t.Fatalf("DELETE model: %d, body %s", rr.Code, rr.Body.String())
	}
	if be.startCalls == 0 || len(be.removed) != 1 {
		t.Errorf("start calls=%d removed=%v — rm must bring the server up too", be.startCalls, be.removed)
	}
}

// With nothing installed, neither path should try to start a thing that is not
// there; it should name the install command.
func TestLocalPullWithNoBackendInstalledSaysInstall(t *testing.T) {
	be := &fakeBackend{installed: false}
	h := newTestServerWithBackend(t, be)

	rr := do(t, h, http.MethodPost, "/v1/local/models/qwen-coder-7b/pull", "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", rr.Code)
	}
	if be.startCalls != 0 {
		t.Error("tried to start a backend that is not installed")
	}
	if !strings.Contains(rr.Body.String(), "dejima local install") {
		t.Errorf("no way forward in the error: %s", rr.Body.String())
	}
}
