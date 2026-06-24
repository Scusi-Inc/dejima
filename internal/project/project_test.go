package project

import (
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestDeriveNameFromRepo(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://github.com/foo/bar.git", "bar"},
		{"git@github.com:foo/bar.git", "bar"},
		{"git@github.com:foo/bar", "bar"},
		{"https://github.com/foo/Bar-Baz.git", "bar-baz"},
		{"/Users/aoos/code/dejima", "dejima"},
		{"./foo", "foo"},
		{"foo!!", "foo"},
		{"", "island"},
	}
	for _, c := range cases {
		if got := DeriveNameFromRepo(c.in); got != c.want {
			t.Errorf("DeriveNameFromRepo(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidateName(t *testing.T) {
	valid := []string{"foo", "foo-bar", "foo.bar", "foo_bar", "a", "abc123"}
	for _, n := range valid {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) errored unexpectedly: %v", n, err)
		}
	}
	invalid := []string{"", "FOO", "-foo", ".foo", "foo/bar", "foo bar"}
	for _, n := range invalid {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) should have errored", n)
		}
	}
}

func TestContainerAndVolumeNames(t *testing.T) {
	p := &Project{Name: "myrepo"}
	if got, want := p.ContainerName(), "dejima-myrepo"; got != want {
		t.Errorf("ContainerName() = %q, want %q", got, want)
	}
	if got, want := p.WorkspaceVolume(), "dejima-myrepo-workspace"; got != want {
		t.Errorf("WorkspaceVolume() = %q, want %q", got, want)
	}
	if got, want := p.HomeVolume(), "dejima-myrepo-home"; got != want {
		t.Errorf("HomeVolume() = %q, want %q", got, want)
	}
}

func TestEnsureAgentsMigratesLegacyScalar(t *testing.T) {
	p := &Project{Name: "myrepo", Agent: "claude-code"}
	p.EnsureAgents()
	if len(p.Agents) != 1 {
		t.Fatalf("EnsureAgents() produced %d agents, want 1", len(p.Agents))
	}
	a := p.Agents[0]
	if a.ID != "a1" || a.Type != "claude-code" || a.Worktree != "/workspace" || a.Tmux != "agent-a1" {
		t.Errorf("synthesized agent = %+v, want {a1 claude-code /workspace agent-a1}", a)
	}
}

func TestEnsureAgentsHeadlessHasNoTmux(t *testing.T) {
	p := &Project{Name: "h", Agent: "headless", Cmd: "python loop.py"}
	p.EnsureAgents()
	a := p.Agents[0]
	if a.Tmux != "" {
		t.Errorf("headless agent Tmux = %q, want empty", a.Tmux)
	}
	if a.Cmd != "python loop.py" {
		t.Errorf("headless agent Cmd = %q, want it carried over", a.Cmd)
	}
}

func TestEnsureAgentsIdempotentAndNonClobbering(t *testing.T) {
	p := &Project{Name: "m", Agent: "claude-code", Agents: []AgentSpec{{ID: "a7", Type: "codex"}}}
	p.EnsureAgents()
	if len(p.Agents) != 1 || p.Agents[0].ID != "a7" {
		t.Fatalf("EnsureAgents clobbered existing Agents: %+v", p.Agents)
	}
	// No legacy scalar and no agents → nothing to synthesize.
	empty := &Project{Name: "e"}
	empty.EnsureAgents()
	if len(empty.Agents) != 0 {
		t.Errorf("EnsureAgents on empty project produced %d agents, want 0", len(empty.Agents))
	}
}

func TestNextAgentID(t *testing.T) {
	cases := []struct {
		name string
		ids  []string
		want string
	}{
		{"", nil, "a1"}, // no name → "a" fallback prefix
		{"", []string{"a1"}, "a2"},
		{"", []string{"a1", "a3"}, "a4"},         // monotonic: max+1, never reuse the gap
		{"", []string{"a2", "bogus", ""}, "a3"},  // ignore unparseable ids
		{"Port", nil, "p1"},                      // island letter leads the id
		{"port", []string{"p1"}, "p2"},           // case-insensitive prefix
		{"Gizmo", []string{"g2"}, "g3"},          // continue from existing
		{"Payments", []string{"a1", "a2"}, "p3"}, // legacy a-ids → continue monotonic at new prefix
		{"123box", nil, "b1"},                    // skip leading non-letters
		{"42", nil, "a1"},                        // no letter at all → "a"
	}
	for _, c := range cases {
		p := &Project{Name: c.name}
		for _, id := range c.ids {
			p.Agents = append(p.Agents, AgentSpec{ID: id})
		}
		if got := p.NextAgentID(); got != c.want {
			t.Errorf("NextAgentID(name=%q, %v) = %q, want %q", c.name, c.ids, got, c.want)
		}
	}
}

func TestSetPrimaryID(t *testing.T) {
	t.Run("renames primary id and derives tmux", func(t *testing.T) {
		p := &Project{Name: "Port", Agents: []AgentSpec{
			{ID: "a1", Tmux: "agent-a1"},
			{ID: "a2", Tmux: "agent-a2"},
		}}
		p.SetPrimaryID("p1")
		if p.Agents[0].ID != "p1" {
			t.Errorf("primary id = %q, want p1", p.Agents[0].ID)
		}
		if p.Agents[0].Tmux != "agent-p1" {
			t.Errorf("primary tmux = %q, want agent-p1", p.Agents[0].Tmux)
		}
		if p.Agents[1].ID != "a2" || p.Agents[1].Tmux != "agent-a2" {
			t.Errorf("non-primary agent mutated: %+v", p.Agents[1])
		}
	})

	t.Run("headless primary with no tmux keeps tmux empty", func(t *testing.T) {
		p := &Project{Name: "Home", Agents: []AgentSpec{{ID: "a1", Tmux: ""}}}
		p.SetPrimaryID("h1")
		if p.Agents[0].ID != "h1" {
			t.Errorf("primary id = %q, want h1", p.Agents[0].ID)
		}
		if p.Agents[0].Tmux != "" {
			t.Errorf("tmux = %q, want empty (headless gets no session)", p.Agents[0].Tmux)
		}
	})

	t.Run("no agents is a no-op", func(t *testing.T) {
		p := &Project{Name: "Empty"}
		p.SetPrimaryID("e1") // must not panic
		if len(p.Agents) != 0 {
			t.Errorf("agents grew to %d", len(p.Agents))
		}
	})
}

func TestAddRemoveAgent(t *testing.T) {
	p := &Project{Agents: []AgentSpec{{ID: "a1"}}}
	p.AddAgent(AgentSpec{ID: "a2"})
	if _, ok := p.AgentByID("a2"); !ok {
		t.Fatal("AddAgent did not append a2")
	}
	if !p.RemoveAgent("a1") {
		t.Fatal("RemoveAgent(a1) returned false")
	}
	if _, ok := p.AgentByID("a1"); ok {
		t.Error("a1 still present after RemoveAgent")
	}
	if p.RemoveAgent("nope") {
		t.Error("RemoveAgent(nope) returned true for a missing id")
	}
	if pa := p.PrimaryAgent(); pa == nil || pa.ID != "a2" {
		t.Errorf("PrimaryAgent() = %+v, want a2", pa)
	}
}

// TestAgentsTOMLRoundTrip asserts the new Agents array and the legacy scalar
// coexist in TOML so an older daemon can still read `agent`.
func TestAgentsTOMLRoundTrip(t *testing.T) {
	in := &Project{
		Name:  "myrepo",
		Agent: "claude-code",
		Agents: []AgentSpec{
			{ID: "a1", Type: "claude-code", Tmux: "dejima", Worktree: "/workspace"},
			{ID: "a2", Type: "codex", Tmux: "agent-a2", Worktree: "/workspace/.agents/a2", Branch: "agent/a2"},
		},
	}
	data, err := toml.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Project
	if err := toml.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Agent != "claude-code" {
		t.Errorf("legacy scalar agent not preserved: %q", out.Agent)
	}
	if len(out.Agents) != 2 || out.Agents[1].ID != "a2" || out.Agents[1].Worktree != "/workspace/.agents/a2" {
		t.Errorf("Agents not round-tripped: %+v", out.Agents)
	}
}

// StampVersion prefers the upgrade stamp, falls back to the build stamp, and is
// empty for islands created before version stamping (provenance unknown). The
// stamp also survives a TOML round-trip.
func TestStampVersion(t *testing.T) {
	cases := []struct {
		built, upgraded, want string
	}{
		{"v0.1.4", "", "v0.1.4"},       // only ever built
		{"v0.1.4", "v0.5.3", "v0.5.3"}, // upgraded since → upgrade stamp wins
		{"", "", ""},                   // legacy island, unknown provenance
		{"", "v0.5.3", "v0.5.3"},       // back-filled at upgrade
	}
	for _, tc := range cases {
		p := &Project{BuiltVersion: tc.built, UpgradedVersion: tc.upgraded}
		if got := p.StampVersion(); got != tc.want {
			t.Errorf("StampVersion(built=%q,upgraded=%q) = %q, want %q", tc.built, tc.upgraded, got, tc.want)
		}
	}

	in := &Project{Name: "r", Agent: "claude-code", BuiltVersion: "v0.1.4", UpgradedVersion: "v0.5.3"}
	data, err := toml.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Project
	if err := toml.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.BuiltVersion != "v0.1.4" || out.UpgradedVersion != "v0.5.3" {
		t.Errorf("version stamp not round-tripped: built=%q upgraded=%q", out.BuiltVersion, out.UpgradedVersion)
	}
}
