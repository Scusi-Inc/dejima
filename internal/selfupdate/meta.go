package selfupdate

// meta.go records the daemon's install context so it can update *itself* later.
// A source-installed daemon lives in /usr/local/bin and otherwise can't find its
// git checkout; a system LaunchDaemon must be restarted in the system domain.
// `dejima service install` writes this once; the daemon reads it on self-update.

import (
	"encoding/json"
	"os"

	"github.com/aoos/dejima/internal/paths"
)

// InstallMeta is what `dejima service install` records for later self-update.
type InstallMeta struct {
	// SourceDir is the git checkout a source install was built from (""for a
	// release/binary install — it updates by download instead).
	SourceDir string `json:"source_dir,omitempty"`
	// System is true when installed as a system service (macOS system
	// LaunchDaemon), so a self-restart must target the system domain.
	System bool `json:"system"`
}

// SaveInstallMeta writes the install context (0600).
func SaveInstallMeta(m InstallMeta) error {
	p, err := paths.DaemonInstallMetaPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// LoadInstallMeta reads the install context. A missing file yields a zero
// InstallMeta (no error) — an older install that predates this metadata.
func LoadInstallMeta() (InstallMeta, error) {
	p, err := paths.DaemonInstallMetaPath()
	if err != nil {
		return InstallMeta{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return InstallMeta{}, nil
		}
		return InstallMeta{}, err
	}
	var m InstallMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return InstallMeta{}, err
	}
	return m, nil
}

// RestartArgs returns the `dejima` args that restart the service in the right
// domain (system installs need --system).
func (m InstallMeta) RestartArgs() []string {
	args := []string{"service", "restart"}
	if m.System {
		args = append(args, "--system")
	}
	return args
}
