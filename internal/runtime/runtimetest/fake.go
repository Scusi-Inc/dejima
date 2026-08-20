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
	"io"
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
	// lastCreate records the most recent CreateContainer so ContainerMounts can
	// answer consistently with what the server actually asked for.
	lastCreate runtime.CreateRequest
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
func (f *Fake) CopyToContainer(context.Context, string, string, string) error   { return nil }
func (f *Fake) CopyFromContainer(context.Context, string, string, string) error { return nil }
func (f *Fake) Logs(context.Context, string, bool) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("container logs\n")), nil
}
