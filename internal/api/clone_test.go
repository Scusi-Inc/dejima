package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aoos/dejima/internal/project"
)

// TestCloneIsland: cloning copies config (owner/tags/agents), copies both
// volumes (workspace + home) source→dest, brings up the new container, and the
// source is untouched.
func TestCloneIsland(t *testing.T) {
	h, f := newTestServer(t)
	src := &project.Project{
		Name:         "web",
		RepoURL:      "https://example.test/web.git",
		Image:        "dejima/island:latest",
		Owner:        "alice@laptop",
		Tags:         map[string]string{"team": "web"},
		DesiredState: project.StateRunning,
		Agents: []project.AgentSpec{
			{ID: "p1", Type: "claude-code", Tmux: "agent-p1", Worktree: "/workspace"},
		},
		Ports: []project.PortScope{{Name: "vault", HostPath: "/host/secret", Mode: "ro"}},
	}
	if err := src.Save(); err != nil {
		t.Fatal(err)
	}

	rr := do(t, h, http.MethodPost, "/v1/islands/web/clone", `{"new_name":"web-2"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("clone: %d %s", rr.Code, rr.Body.String())
	}
	var info IslandInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Name != "web-2" || info.Owner != "alice@laptop" || info.Tags["team"] != "web" {
		t.Errorf("clone info = %+v, want name web-2 + owner/tags carried", info)
	}

	// The new project persisted, carries the agents, and drops Ports (deny-all).
	clone, err := project.Load("web-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(clone.Agents) != 1 || clone.Agents[0].ID != "p1" {
		t.Errorf("clone agents = %+v, want the source's p1", clone.Agents)
	}
	if len(clone.Ports) != 0 {
		t.Errorf("clone carried Ports %v; should start deny-all", clone.Ports)
	}

	// Both volumes were copied source→dest.
	wantCopies := map[string]string{
		src.WorkspaceVolume(): clone.WorkspaceVolume(),
		src.HomeVolume():      clone.HomeVolume(),
	}
	got := map[string]string{}
	for _, c := range f.volumeCopies {
		got[c[0]] = c[1]
	}
	for from, to := range wantCopies {
		if got[from] != to {
			t.Errorf("missing volume copy %s → %s (copies=%v)", from, to, f.volumeCopies)
		}
	}

	// New container was created for the clone.
	if f.lastCreate.Name != clone.ContainerName() {
		t.Errorf("last container created = %q, want %q", f.lastCreate.Name, clone.ContainerName())
	}

	// Source still exists.
	if !project.Exists("web") {
		t.Error("source island web disappeared after clone")
	}
}

// TestCloneRejectsBadTarget: unknown source → 404, duplicate target → 409,
// same-name → 400.
func TestCloneRejectsBadTarget(t *testing.T) {
	h, _ := newTestServer(t)
	if err := (&project.Project{Name: "a", DesiredState: project.StateRunning}).Save(); err != nil {
		t.Fatal(err)
	}
	if err := (&project.Project{Name: "b", DesiredState: project.StateRunning}).Save(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		src, body string
		want      int
	}{
		{"missing", `{"new_name":"x"}`, http.StatusNotFound},
		{"a", `{"new_name":"b"}`, http.StatusConflict},
		{"a", `{"new_name":"a"}`, http.StatusBadRequest},
		{"a", `{"new_name":"bad name!"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		rr := do(t, h, http.MethodPost, "/v1/islands/"+tc.src+"/clone", tc.body)
		if rr.Code != tc.want {
			t.Errorf("clone %s %s: got %d, want %d", tc.src, tc.body, rr.Code, tc.want)
		}
	}
}
