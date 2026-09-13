package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// The default must actually carry the isolation setting.
//
// Stated as a test because the value is a contract with a THIRD-PARTY BINARY:
// nothing in this repo consumes `isolatePeerMachines`, so a typo in the JSON
// tag, or a future refactor that drops the field, produces a perfectly valid
// managed-settings.json that the harness reads, ignores, and reports no error
// for. The failure mode of this whole feature is silence.
func TestHarnessPolicyDefaultIsolatesPeerMachines(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := &project.Project{Name: "wildfire"}

	dir, err := islandHarnessPolicyDir(p)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(filepath.Join(dir, harnessPolicyFileName))
	if err != nil {
		t.Fatalf("the policy file the island mounts must exist: %v", err)
	}
	// Decoded as a bare map, deliberately: round-tripping through harnessPolicy
	// would assert that our struct matches itself and would pass with any key
	// name at all. The harness reads a key spelled exactly this way.
	var got map[string]any
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatalf("the harness treats unparseable managed settings as absent, so this must be valid JSON: %v", err)
	}
	if got["isolatePeerMachines"] != true {
		t.Fatalf("policy does not set isolatePeerMachines=true, so the mount gates nothing: %s", blob)
	}
}

// An operator's edit must survive container recreate.
//
// This is the one behaviour that separates the policy file from every
// credential mount around it. Those are re-derived on every create because the
// daemon owns them. If this one were, an operator who opted an island out would
// find it silently back on after the next `dejima upgrade` — and a containment
// setting that reverts under you is worse than one that is merely strict,
// because you will not look at it twice.
func TestHarnessPolicyKeepsAnOperatorEdit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := &project.Project{Name: "wildfire"}

	dir, err := islandHarnessPolicyDir(p)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, harnessPolicyFileName)

	// The operator opts this island out, by hand, on the host.
	edited := []byte(`{"isolatePeerMachines": false, "operatorNote": "hex4x needs the mesh"}` + "\n")
	if err := os.WriteFile(file, edited, 0o644); err != nil {
		t.Fatal(err)
	}

	// Recreate the container: same call, second time.
	if _, err := islandHarnessPolicyDir(p); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(edited) {
		t.Fatalf("the operator's edit was overwritten on recreate:\n want: %s\n got:  %s", edited, after)
	}
}

// The DIRECTORY is what gets mounted, not the file inside it.
//
// Same trap island_secrets_mount_test.go exists for, and it bites harder here.
// The supported way to opt an island out is for the operator to edit this file
// on the host — with an editor, which means write-and-rename, which means a new
// inode. A file bind pins the inode it resolved at create time, so the island
// would keep reading the ORIGINAL policy forever while the operator reads their
// own edit on the host and believes it took. Both parties see something
// consistent and they disagree.
func TestHarnessPolicyMountsTheDirectorySoEditsLand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := &project.Project{Name: "wildfire"}

	dir, err := islandHarnessPolicyDir(p)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, harnessPolicyFileName)

	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("the mounted path must be a directory: %v", err)
	}
	if got := filepath.Dir(file); got != dir {
		t.Fatalf("policy file is not inside the mounted directory:\n file:  %s\n mount: %s", got, dir)
	}

	// Control: prove a rename actually changes the inode here, so the assertion
	// below is measuring the trap rather than restating that nothing moved.
	before := inodeOf(t, file)
	tmp := file + ".operator-edit"
	if err := os.WriteFile(tmp, []byte(`{"isolatePeerMachines": false}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, file); err != nil {
		t.Fatal(err)
	}
	if after := inodeOf(t, file); after == before {
		t.Skip("this filesystem reused the inode across a rename; the trap is unobservable here")
	}
	// The directory the container binds is unchanged by that rename, which is
	// precisely why the container resolves the new file on its next read.
	if got := inodeOf(t, dir); got != inodeOf(t, filepath.Dir(file)) {
		t.Fatal("the mounted directory moved, which a bind mount would not follow")
	}
}

// The mount must be READ-ONLY, or the gate is decorative.
//
// The agent in an island has passwordless sudo. Every other defence in this
// design — managed settings outranking user settings, the harness marking
// isolatePeerMachines bypass-immune — is downstream of the container being
// unable to rewrite this file. A read-write bind would leave all of that intact
// and all of it pointless, and nothing else in the system would notice.
func TestHarnessPolicyIsMountedReadOnly(t *testing.T) {
	h, f := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}

	f.mu.Lock()
	var found *runtime.BindMount
	for i := range f.lastCreate.BindMounts {
		if f.lastCreate.BindMounts[i].ContainerPath == HarnessPolicyMountPath {
			found = &f.lastCreate.BindMounts[i]
			break
		}
	}
	f.mu.Unlock()

	if found == nil {
		t.Fatalf("no bind at %s — the island is not subject to the policy at all", HarnessPolicyMountPath)
	}
	if !found.ReadOnly {
		t.Fatalf("the policy is mounted read-write, so an agent with root can "+
			"delete it and every downstream protection is decorative: %+v", *found)
	}
}

// An island created before this shipped must be IDENTIFIABLE.
//
// Bind mounts are decided once, at container create. A daemon carrying this
// feature does not retrofit the containers already on disk, so there is a
// population of islands where the policy exists on the host and the running
// container is not subject to it. That is the exact shape credential_mounts.go
// opens by warning about: a containment surface that under-reports reassures
// instead of failing. Configured is always true for this path, so mounted=false
// means precisely "recreate this island" and nothing else.
func TestAnIslandMissingTheHarnessPolicyMountIsNamed(t *testing.T) {
	h, f := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}

	// Control: a fresh island must NOT drift, or the assertion below is just
	// describing the default state.
	if drift := getGrants(t, h, "proj").Credentials.Drift(); len(drift) != 0 {
		t.Fatalf("a freshly created island already drifts, so this guard cannot "+
			"tell the two populations apart: %+v", drift)
	}

	// Stand in for a container the old daemon created: identical, minus the bind.
	f.mu.Lock()
	var kept []runtime.BindMount
	dropped := false
	for _, b := range f.lastCreate.BindMounts {
		if b.ContainerPath == HarnessPolicyMountPath {
			dropped = true
			continue
		}
		kept = append(kept, b)
	}
	f.lastCreate.BindMounts = kept
	f.mu.Unlock()
	if !dropped {
		t.Fatal("the island was created without a harness policy mount to remove, " +
			"so the simulated pre-fix container is identical to the real one and " +
			"this guard is checking nothing")
	}

	rep := getGrants(t, h, "proj").Credentials
	if !rep.Known {
		t.Fatalf("the container exists, so its mounts are knowable: %+v", rep)
	}
	drift := rep.Drift()
	if len(drift) != 1 || drift[0].Path != HarnessPolicyMountPath {
		t.Fatalf("expected exactly the harness policy mount to drift, got %+v", drift)
	}
	if !drift[0].Configured || drift[0].Mounted {
		t.Errorf("expected configured=true mounted=false — the recreate signal. got %+v", drift[0])
	}
}
