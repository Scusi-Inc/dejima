package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// The bug this file exists for: revoke the credential, don't recreate the
// container, and the island still HAS it — while every surface reporting from
// config says it doesn't. A containment surface that under-reports access is
// the reassuring direction to be wrong in.
func TestRevokedCredentialStillMountedIsReported(t *testing.T) {
	h, _ := newTestServer(t)
	seedHostGH(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	// Grant, then recreate so the mount is genuinely present in the container.
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/github/host-credential", ""); rr.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/upgrade", ""); rr.Code >= 300 {
		t.Fatalf("upgrade: %d %s", rr.Code, rr.Body.String())
	}
	// Now revoke WITHOUT recreating — the credential is still in the container.
	if rr := do(t, h, http.MethodDelete, "/v1/islands/proj/github/host-credential", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
	}

	g := getGrants(t, h, "proj")
	if !g.Credentials.Known {
		t.Fatalf("the container exists, so its mounts are knowable: %+v", g.Credentials)
	}
	if g.HostGitHub.Granted {
		t.Fatal("the record should show the revoke")
	}
	drift := g.Credentials.Drift()
	if len(drift) != 1 {
		t.Fatalf("expected exactly one drifted credential, got %+v", drift)
	}
	if drift[0].Configured || !drift[0].Mounted {
		t.Errorf("expected configured=false mounted=true (revoked but live), got %+v", drift[0])
	}
	if drift[0].Path != "/opt/host/gh-config" {
		t.Errorf("wrong credential flagged: %+v", drift[0])
	}
}

// The mirror: granted but not yet mounted. Less dangerous (it over-reports
// access, failing toward caution) but still a wrong statement on a containment
// surface, and the same comparison catches it.
func TestGrantedCredentialNotYetMountedIsReported(t *testing.T) {
	h, _ := newTestServer(t)
	seedHostGH(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	// Grant but do NOT recreate.
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/github/host-credential", ""); rr.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rr.Code, rr.Body.String())
	}

	g := getGrants(t, h, "proj")
	drift := g.Credentials.Drift()
	if len(drift) != 1 {
		t.Fatalf("expected the pending grant to show as drift, got %+v (states %+v)", drift, g.Credentials.States)
	}
	if !drift[0].Configured || drift[0].Mounted {
		t.Errorf("expected configured=true mounted=false (granted, pending recreate), got %+v", drift[0])
	}
}

// The steady state: record and container agree, so nothing is flagged. Without
// this, a report that flagged everything would also "pass" the two tests above.
func TestCredentialsAgreeAfterRecreate(t *testing.T) {
	h, _ := newTestServer(t)
	seedHostGH(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/github/host-credential", ""); rr.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands/proj/upgrade", ""); rr.Code >= 300 {
		t.Fatalf("upgrade: %d %s", rr.Code, rr.Body.String())
	}

	g := getGrants(t, h, "proj")
	if !g.Credentials.Known {
		t.Fatal("mounts should be knowable")
	}
	if d := g.Credentials.Drift(); len(d) != 0 {
		t.Errorf("record and container agree after recreate, got drift %+v", d)
	}
}

// "I couldn't look" must not render as "nothing is mounted". This is the same
// defect as the doctor check that vanished when netstat was missing: an
// unasked question presented as a clean answer.
func TestUninspectableContainerIsNotReportedAsClean(t *testing.T) {
	h, f := newTestServer(t)
	seedHostGH(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	f.mu.Lock()
	f.mountsErr = errors.New("engine unreachable")
	f.mu.Unlock()

	g := getGrants(t, h, "proj")
	if g.Credentials.Known {
		t.Fatal("the engine failed, so the mounts are NOT known")
	}
	if g.Credentials.Reason == "" {
		t.Error("an unknown report must carry a reason the surface can show")
	}
	// And it must not masquerade as agreement.
	if d := g.Credentials.Drift(); d != nil {
		t.Errorf("an unknown report has no drift to report, got %+v", d)
	}
}

// A container that doesn't exist yet is a DETERMINED answer — nothing is
// mounted, because there is nothing to mount into. Reporting that as "couldn't
// determine" would be the mirror error: manufacturing uncertainty, and it would
// make every uncreated island look uninspectable.
func TestMissingContainerIsDeterminedNotUnknown(t *testing.T) {
	h, f := newTestServer(t)
	seedHostGH(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	f.mu.Lock()
	f.status = "missing"
	f.mountsErr = errors.New("No such container") // what the engine says for a missing one
	f.mu.Unlock()

	g := getGrants(t, h, "proj")
	if !g.Credentials.Known {
		t.Fatalf("a missing container is a determined answer, not ignorance: %+v", g.Credentials)
	}
	for _, s := range g.Credentials.States {
		if s.Mounted {
			t.Errorf("nothing can be mounted into a container that doesn't exist: %+v", s)
		}
	}
}

// getGrants fetches the unified grants view for one island.
func getGrants(t *testing.T, h http.Handler, island string) IslandGrantsResponse {
	t.Helper()
	rr := do(t, h, http.MethodGet, "/v1/islands/"+island+"/grants", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("grants: %d %s", rr.Code, rr.Body.String())
	}
	var out IslandGrantsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
