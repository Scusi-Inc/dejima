// Package runtimetest provides a reusable in-memory runtime.Runtime fake for
// tests that need a real api.Server without a Docker engine — e.g. the CLI
// table tests, which drive the real cobra commands against an httptest daemon.
//
// It is deliberately a no-op stub: container ops succeed and record nothing,
// status is configurable, and Exec/Logs return empty output. Tests that need to
// assert on exec argv or drive probe results should use the richer per-package
// fakes; this one exists so a server can be constructed and exercised over HTTP.
package runtimetest

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"

	"github.com/aoos/dejima/internal/runtime"
)

// Fake is a minimal runtime.Runtime that satisfies the interface with safe
// defaults. The zero value reports a running container; set StatusVal to
// override what Status() returns.
type Fake struct {
	mu        sync.Mutex
	StatusVal runtime.ContainerStatus
	// MountsVal overrides what ContainerMounts reports; MountsErr forces it to
	// fail, which is how a test exercises the "couldn't determine" path.
	MountsVal []string
	MountsErr error
	// ReapsVal overrides what ContainerReapsOrphans reports; ReapsErr forces it
	// to fail, which is how a test exercises the "couldn't determine" path. The
	// default (nil/nil) reports true, matching a container the daemon created.
	ReapsVal *bool
	ReapsErr error
	// DialFn backs DialContainerPort. Nil makes every dial fail, which is the
	// safe default: a test that needs a gateway has to say so.
	DialFn func(ctx context.Context, name, host string, port int) (net.Conn, error)
	// lastCreate records the most recent CreateContainer so ContainerMounts can
	// answer consistently with what the server actually asked for.
	lastCreate runtime.CreateRequest
	// copies counts CopyToContainer calls. It exists so a test can assert that a
	// refusal happened BEFORE any bytes moved — without it, such a test proves
	// only that an error came back, which is also what a refusal issued halfway
	// through a transfer looks like.
	copies int
	// CopyErrOn makes CopyToContainer fail for any destination containing this
	// substring. Staging a MID-TRANSFER failure is otherwise impossible against a
	// fake that always succeeds, and "some files crossed and some did not" is a
	// state with its own required behaviour.
	CopyErrOn string
	// execCalls records every Exec, so a test can assert the SHAPE of what the
	// server ran rather than only its effect. Needed for injection: "the nudge
	// reached the agent" and "the nudge was typed but never submitted" produce
	// the same absence of error, and only the argument list distinguishes them.
	execCalls [][]string
}

// ExecCalls returns a copy of every Exec the server has made, in order.
func (f *Fake) ExecCalls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.execCalls))
	for i, c := range f.execCalls {
		out[i] = append([]string(nil), c...)
	}
	return out
}

// CopyCount reports how many times CopyToContainer has been called.
func (f *Fake) CopyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.copies
}

// New returns a Fake that reports its containers as running.
func New() *Fake { return &Fake{StatusVal: runtime.StatusRunning} }

var _ runtime.Runtime = (*Fake)(nil)

func (f *Fake) EnsureVolume(context.Context, string) error       { return nil }
func (f *Fake) RemoveVolume(context.Context, string, bool) error { return nil }
func (f *Fake) CopyVolumeData(context.Context, string, string, string) error {
	return nil
}
func (f *Fake) EnsureNetwork(context.Context, string) error { return nil }
func (f *Fake) RemoveNetwork(context.Context, string) error { return nil }

func (f *Fake) Stats(context.Context, string) (runtime.Stats, error) {
	return runtime.Stats{}, nil
}
func (f *Fake) StatsAll(context.Context) (map[string]runtime.Stats, error) {
	return map[string]runtime.Stats{}, nil
}
func (f *Fake) VolumeSizes(context.Context) (map[string]int64, error) {
	return map[string]int64{}, nil
}

func (f *Fake) CreateContainer(_ context.Context, req runtime.CreateRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCreate = req
	return "cid", nil
}
func (f *Fake) UpdateResources(context.Context, string, string) error { return nil }
func (f *Fake) StopContainer(context.Context, string) error           { return nil }
func (f *Fake) StartContainer(context.Context, string) error          { return nil }
func (f *Fake) RemoveContainer(context.Context, string, bool) error   { return nil }

func (f *Fake) Status(context.Context, string) (runtime.ContainerStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.StatusVal == "" {
		return runtime.StatusRunning, nil
	}
	return f.StatusVal, nil
}
func (f *Fake) Inspect(context.Context, string) (runtime.Health, error) {
	return runtime.Health{}, nil
}

// ContainerMounts reports MountsVal when set, otherwise the destinations of the
// last CreateContainer — so a fake container is self-consistent with what was
// asked for. MountsErr forces the "couldn't look" path.
func (f *Fake) ContainerMounts(context.Context, string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MountsErr != nil {
		return nil, f.MountsErr
	}
	if f.MountsVal != nil {
		return f.MountsVal, nil
	}
	var dests []string
	for _, b := range f.lastCreate.BindMounts {
		dests = append(dests, b.ContainerPath)
	}
	return dests, nil
}

// DialContainerPort returns DialFn's conn when set, else an error. A fake that
// silently returned a working pipe would let a proxy test pass without ever
// reaching a gateway.
func (f *Fake) DialContainerPort(ctx context.Context, name, host string, port int) (net.Conn, error) {
	f.mu.Lock()
	fn := f.DialFn
	f.mu.Unlock()
	if fn == nil {
		return nil, errors.New("runtimetest: no DialFn set")
	}
	return fn(ctx, name, host, port)
}

// ContainerReapsOrphans reports ReapsVal when set, else true — the state of a
// container this daemon created. ReapsErr forces the "couldn't look" path.
func (f *Fake) ContainerReapsOrphans(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ReapsErr != nil {
		return false, f.ReapsErr
	}
	if f.ReapsVal != nil {
		return *f.ReapsVal, nil
	}
	return true, nil
}

func (f *Fake) Exec(_ context.Context, _ string, cmd []string) (string, string, int, error) {
	f.mu.Lock()
	f.execCalls = append(f.execCalls, append([]string(nil), cmd...))
	f.mu.Unlock()
	// A couple of probes the server runs during create/list — answer them so the
	// happy path doesn't stall. Default: success with no output.
	switch {
	case len(cmd) >= 3 && cmd[0] == "test" && cmd[2] == "/workspace/.git":
		return "", "", 0, nil // primary repo present
	case len(cmd) >= 3 && cmd[0] == "test" && strings.HasSuffix(cmd[2], "/.git"):
		return "", "", 1, nil // agent worktree not yet created
	case len(cmd) >= 2 && cmd[0] == "tmux" && cmd[1] == "has-session":
		return "", "", 1, nil // no session yet
	default:
		return "", "", 0, nil
	}
}
func (f *Fake) ExecStream(context.Context, string, []string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *Fake) ImageExists(context.Context, string) (bool, error) { return true, nil }
func (f *Fake) BuildImage(context.Context, string, string, string, map[string]string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *Fake) CopyToContainer(_ context.Context, _, _, dst string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CopyErrOn != "" && strings.Contains(dst, f.CopyErrOn) {
		return errors.New("simulated copy failure")
	}
	f.copies++
	return nil
}
func (f *Fake) CopyFromContainer(context.Context, string, string, string) error { return nil }
func (f *Fake) Logs(context.Context, string, bool) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("container logs\n")), nil
}
