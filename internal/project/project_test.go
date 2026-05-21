package project

import "testing"

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
	if got, want := p.AgentVolume(), "dejima-myrepo-agent"; got != want {
		t.Errorf("AgentVolume() = %q, want %q", got, want)
	}
}
