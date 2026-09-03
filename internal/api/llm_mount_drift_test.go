package api

import (
	"net/http"
	"testing"

	"github.com/aoos/dejima/internal/runtime"
)

// An island created before the LLM mount became unconditional must be
// IDENTIFIABLE, not merely unlucky.
//
// 8b850d1 made islandLLMConfigDir return its directory even when no provider
// resolves, so credentialBindMounts always includes /opt/host/llm. That fixes
// every island created since. It cannot fix one that already exists: credential
// mounts are decided once, inside createContainerForProject, and there is no
// way to add a bind to a running container. Those islands still need
// `dejima upgrade` before a registered provider can reach them.
//
// Which left the local-models page saying, accurately, "this page cannot tell
// which islands those are." It could. It was asking the island's CONFIG — a
// question about intent — when the question was about the running container.
// credentialMountReport already asks the runtime and diffs the two; the LLM
// mount simply was not in the list it diffs.
//
// The signal is now unambiguous in one direction, which is what makes it
// usable: configured is ALWAYS true for this path, so mounted=false means
// exactly "created before the fix, recreate to deliver the key" and nothing
// else.
func TestAnIslandMissingTheLLMMountIsNamed(t *testing.T) {
	h, f := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}

	// A fresh island has the mount — the post-fix case, and the control that
	// makes the drift below mean something rather than being the default state.
	if drift := getGrants(t, h, "proj").Credentials.Drift(); len(drift) != 0 {
		t.Fatalf("a freshly created island already drifts, so this guard cannot "+
			"tell the two populations apart: %+v", drift)
	}

	// Now stand in for a container the OLD daemon created: everything the same,
	// minus the LLM bind it would never have added.
	f.mu.Lock()
	var kept []runtime.BindMount
	dropped := false
	for _, b := range f.lastCreate.BindMounts {
		if b.ContainerPath == LLMCredentialMountPath {
			dropped = true
			continue
		}
		kept = append(kept, b)
	}
	f.lastCreate.BindMounts = kept
	f.mu.Unlock()
	if !dropped {
		t.Fatal("the island was created without an LLM mount to remove, so the " +
			"simulated pre-fix container is identical to the real one and this " +
			"guard is checking nothing")
	}

	rep := getGrants(t, h, "proj").Credentials
	if !rep.Known {
		t.Fatalf("the container exists, so its mounts are knowable: %+v", rep)
	}
	drift := rep.Drift()
	if len(drift) != 1 {
		t.Fatalf("expected exactly the LLM mount to drift, got %+v", drift)
	}
	if drift[0].Path != LLMCredentialMountPath {
		t.Fatalf("wrong credential flagged: %+v", drift[0])
	}
	if !drift[0].Configured || drift[0].Mounted {
		t.Errorf("expected configured=true mounted=false — the island should have "+
			"the mount and does not, which is the recreate signal. got %+v", drift[0])
	}
}

// The report must carry the LLM mount at all.
//
// Drift is computed by walking credentialMounts(), so a path missing from that
// list is not reported as agreeing — it is not reported at all, and an absent
// row reads as "nothing to say about this credential" on every surface. That is
// how a mount can be missing from a container while the operator's own
// containment report stays silent about it.
func TestTheLLMMountIsInTheReportAtAll(t *testing.T) {
	h, _ := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	rep := getGrants(t, h, "proj").Credentials
	for _, st := range rep.States {
		if st.Path == LLMCredentialMountPath {
			if !st.Configured {
				t.Errorf("the LLM mount is unconditional since 8b850d1, so a fresh "+
					"island must report it configured: %+v", st)
			}
			return
		}
	}
	t.Errorf("no LLM row in the credential mount report, so an island missing the "+
		"mount is silent rather than flagged:\n%+v", rep.States)
}
