package provision

import (
	"encoding/json"
	"os"
	"time"

	"github.com/aoos/dejima/internal/paths"
)

// State is the resumable progress record for the host-provisioning wizard,
// persisted at ~/.dejima/provisioning-state.json. Re-running the wizard reads it
// and resumes from CurrentPhase, so an operator who exits mid-flow (e.g. while
// Docker Desktop installs) picks up where they left off.
type State struct {
	StartedAt       time.Time      `json:"started_at"`
	CompletedPhases []string       `json:"completed_phases"`
	CurrentPhase    string         `json:"current_phase"`
	Skipped         []string       `json:"skipped"`
	Answers         map[string]any `json:"answers"`
}

// Load reads the provisioning state, or returns a fresh one if none exists.
func Load() (*State, error) {
	p, err := paths.ProvisioningStatePath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return &State{StartedAt: time.Now().UTC(), Answers: map[string]any{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.Answers == nil {
		s.Answers = map[string]any{}
	}
	return &s, nil
}

// Save writes the state atomically (temp + rename), 0600.
func (s *State) Save() error {
	p, err := paths.ProvisioningStatePath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Reset removes the state file (the wizard's `--reset`).
func Reset() error {
	p, err := paths.ProvisioningStatePath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// IsComplete reports whether the phase id already finished in a prior run.
func (s *State) IsComplete(id string) bool {
	for _, c := range s.CompletedPhases {
		if c == id {
			return true
		}
	}
	return false
}

// markComplete records a phase as finished and clears CurrentPhase if it pointed
// here. Idempotent.
func (s *State) markComplete(id string) {
	if !s.IsComplete(id) {
		s.CompletedPhases = append(s.CompletedPhases, id)
	}
	if s.CurrentPhase == id {
		s.CurrentPhase = ""
	}
}

// markSkipped records a phase (or sub-step) the operator skipped, for the
// end-of-run manual checklist.
func (s *State) markSkipped(id string) {
	for _, k := range s.Skipped {
		if k == id {
			return
		}
	}
	s.Skipped = append(s.Skipped, id)
}
