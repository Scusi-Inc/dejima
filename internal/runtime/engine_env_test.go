package runtime

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// The doc comment on Docker has claimed since this file was written that podman
// is "easy to swap with by overriding Bin" — and NOTHING EVER OVERRODE IT.
// NewDocker hardcoded "docker", so the only way to run podman was to put a file
// named `docker` on PATH.
//
// An operator hit this on Linux with `alias docker=podman` and watched every
// check fail while `docker version` worked when they typed it: aliases live in
// interactive shells and exec.Command does not consult them. The claim in the
// comment was true of the struct and false of the constructor, which is the
// shape that survives review — nobody checks whether the seam is reachable.
func TestContainerBinIsOverridable(t *testing.T) {
	t.Setenv("DEJIMAD_CONTAINER_BIN", "podman")
	if got := NewDocker().bin(); got != "podman" {
		t.Errorf("bin() = %q, want podman — the seam the comment promised is unreachable", got)
	}
}

func TestContainerBinDefaultsToDocker(t *testing.T) {
	// Explicitly cleared: the test host's own environment must not decide this.
	t.Setenv("DEJIMAD_CONTAINER_BIN", "")
	if got := NewDocker().bin(); got != "docker" {
		t.Errorf("bin() = %q, want docker", got)
	}
}

// DEJIMAD_BUILD_OPTS exists for options Dejima has no opinion about but the
// operator does. The forcing case: podman failing the island build with
// "cannot apply additional memory protection after relocation", a seccomp denial
// the operator fixes with `--security-opt seccomp=unconfined`. They knew the
// flag; there was nowhere to put it.
func TestBuildOptsAreParsedFromTheEnvironment(t *testing.T) {
	t.Setenv("DEJIMAD_BUILD_OPTS", "--security-opt seccomp=unconfined --network=host")
	got := NewDocker().BuildOpts
	want := []string{"--security-opt", "seccomp=unconfined", "--network=host"}
	if !slices.Equal(got, want) {
		t.Errorf("BuildOpts = %q, want %q", got, want)
	}
}

func TestNoBuildOptsByDefault(t *testing.T) {
	t.Setenv("DEJIMAD_BUILD_OPTS", "")
	if got := NewDocker().BuildOpts; len(got) != 0 {
		t.Errorf("BuildOpts = %q, want none", got)
	}
}

// POSITION IS LOAD-BEARING, and this is the part a "just append them" fix gets
// wrong. The build context path is the LAST argument; an operator flag appended
// after it is parsed as a second context (or as a stray argument) and the build
// fails in a way that reads like their flag is unsupported.
func TestBuildOptsLandBeforeTheContextPath(t *testing.T) {
	d := &Docker{Bin: "docker", BuildOpts: []string{"--security-opt", "seccomp=unconfined"}}
	args := d.buildArgv("/ctx", "Dockerfile", "tag", nil)

	ctxAt := slices.Index(args, "/ctx")
	optAt := slices.Index(args, "--security-opt")
	if ctxAt < 0 || optAt < 0 {
		t.Fatalf("argv is missing the context or the opt: %q", args)
	}
	if optAt > ctxAt {
		t.Errorf("operator opt at %d lands AFTER the context path at %d — it would be "+
			"parsed as an argument, not a flag: %q", optAt, ctxAt, args)
	}
	if args[0] != "build" {
		t.Errorf("argv[0] = %q, want build — opts must not displace the subcommand", args[0])
	}
}

// An empty or whitespace-only value must not inject an empty argument, which
// some engines reject outright.
func TestWhitespaceBuildOptsInjectNothing(t *testing.T) {
	t.Setenv("DEJIMAD_BUILD_OPTS", "   \t  ")
	if got := NewDocker().BuildOpts; len(got) != 0 {
		t.Errorf("BuildOpts = %q, want none from a whitespace-only value", got)
	}
	d := &Docker{Bin: "docker"}
	for _, a := range d.buildArgv("/ctx", "Dockerfile", "tag", nil) {
		if strings.TrimSpace(a) == "" {
			t.Errorf("empty argument in argv: %q", d.buildArgv("/ctx", "Dockerfile", "tag", nil))
		}
	}
}

// Guard against the env being read at CALL time rather than at construction.
// A daemon that re-read this per build would change behaviour underneath a
// running fleet when someone edited a service file.
func TestEnvIsReadOnceAtConstruction(t *testing.T) {
	t.Setenv("DEJIMAD_CONTAINER_BIN", "podman")
	d := NewDocker()
	_ = os.Setenv("DEJIMAD_CONTAINER_BIN", "nerdctl")
	if got := d.bin(); got != "podman" {
		t.Errorf("bin() = %q — the value moved after construction", got)
	}
}
