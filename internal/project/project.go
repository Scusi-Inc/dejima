package project

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/aoos/dejima/internal/paths"
)

// State is the desired state of an island.
type State string

const (
	StateRunning    State = "running"
	StateHibernated State = "hibernated"
)

// Resources captures the docker resource caps applied to the container.
// All fields optional; zero/empty means unlimited.
type Resources struct {
	Memory string `toml:"memory,omitempty"` // e.g. "4G"
	CPUs   string `toml:"cpus,omitempty"`   // e.g. "2.0"
	Disk   string `toml:"disk,omitempty"`   // e.g. "20G" — maps to --storage-opt size=
}

// Project is the persisted record for a single island.
type Project struct {
	Name         string    `toml:"name"`
	RepoURL      string    `toml:"repo"`
	Agent        string    `toml:"agent"`
	Image        string    `toml:"image"`
	Resources    Resources `toml:"resources,omitempty"`
	CreatedAt    time.Time `toml:"created_at"`
	LastUsedAt   time.Time `toml:"last_used_at"`
	DesiredState State     `toml:"state"`
}

// ContainerName returns the deterministic container name for this project.
func (p *Project) ContainerName() string {
	return "dejima-" + p.Name
}

// WorkspaceVolume returns the workspace volume name.
func (p *Project) WorkspaceVolume() string {
	return "dejima-" + p.Name + "-workspace"
}

// AgentVolume returns the agent on-disk state volume name (e.g., for ~/.claude).
func (p *Project) AgentVolume() string {
	return "dejima-" + p.Name + "-agent"
}

// NetworkName returns the per-island Docker network name.
func (p *Project) NetworkName() string {
	return "dejima-net-" + p.Name
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidateName ensures a name is safe to use in container/volume names.
func ValidateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: must match %s", name, nameRe.String())
	}
	return nil
}

// DeriveNameFromRepo extracts a reasonable default name from a repo URL or path.
func DeriveNameFromRepo(repo string) string {
	s := strings.TrimSuffix(repo, "/")
	s = strings.TrimSuffix(s, ".git")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.ToLower(s)
	// Replace anything not in the allowed set with '-'.
	b := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b = append(b, byte(r))
		default:
			b = append(b, '-')
		}
	}
	out := strings.Trim(string(b), "-.")
	if out == "" {
		out = "island"
	}
	return out
}

// Save writes the project config to disk.
func (p *Project) Save() error {
	path, err := paths.ProjectConfigPath(p.Name)
	if err != nil {
		return err
	}
	data, err := toml.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal project %q: %w", p.Name, err)
	}
	return os.WriteFile(path, data, 0o600)
}

// Load reads an existing project by name.
func Load(name string) (*Project, error) {
	path, err := paths.ProjectConfigPath(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("island %q not found", name)
		}
		return nil, err
	}
	var p Project
	if err := toml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("unmarshal project %q: %w", name, err)
	}
	return &p, nil
}

// Delete removes the project's on-host config directory.
func Delete(name string) error {
	dir, err := paths.ProjectDir(name)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// List returns every project the daemon knows about.
func List() ([]*Project, error) {
	dir, err := paths.ProjectsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []*Project
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := Load(e.Name())
		if err != nil {
			// Skip projects whose configs are missing or unreadable; surface elsewhere.
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// Exists reports whether a project with this name has a config on disk.
func Exists(name string) bool {
	path, err := paths.ProjectConfigPath(name)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// EnsureProjectSubdirs creates intake/, exports/, logs/ for a project.
func EnsureProjectSubdirs(name string) error {
	dir, err := paths.ProjectDir(name)
	if err != nil {
		return err
	}
	for _, sub := range []string{"intake", "exports", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return err
		}
	}
	return nil
}
