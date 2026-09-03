package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/aoos/dejima/internal/fdlimit"
)

const systemdUnitName = "dejimad.service"

type systemdManager struct{}

func (m *systemdManager) unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", systemdUnitName), nil
}

// UnitInstalled reports whether the dejimad user unit exists on this host.
//
// Distinct from "does this host run systemd", and the distinction is the whole
// point: a WSL distro can run systemd perfectly well and still have no dejimad
// unit, because the daemon there is launched from the Windows side. An operator
// in exactly that state was told to run `sudo systemctl restart dejimad` after a
// self-update, and the restart the daemon had just attempted had already failed
// with "is the service installed?".
func UnitInstalled() bool {
	m := &systemdManager{}
	path, err := m.unitPath()
	if err != nil {
		return false
	}
	_, statErr := os.Stat(path)
	return statErr == nil
}

func (m *systemdManager) Install(binaryPath string, args []string) error {
	path, err := m.unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	execStart := binaryPath
	if len(args) > 0 {
		execStart += " " + strings.Join(args, " ")
	}
	tmpl := template.Must(template.New("unit").Parse(systemdTemplate))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]any{
		"ExecStart":   execStart,
		"LimitNOFILE": fdlimit.Target,
	}); err != nil {
		return err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return err
	}
	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if err := exec.Command("systemctl", "--user", "enable", "--now", systemdUnitName).Run(); err != nil {
		return fmt.Errorf("systemctl enable: %w", err)
	}
	return nil
}

func (m *systemdManager) Uninstall() error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", systemdUnitName).Run()
	path, err := m.unitPath()
	if err != nil {
		return err
	}
	_ = os.Remove(path)
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return nil
}

func (m *systemdManager) Restart() error {
	// The message names `systemctl --user`, which is what actually ran. It used
	// to say plain `systemctl restart dejimad.service` — a command that does not
	// exist here (the unit is a USER unit, in ~/.config/systemd/user), so an
	// operator copying the error out of the log ran the wrong thing and got a
	// different failure than the one being reported to them.
	if err := exec.Command("systemctl", "--user", "restart", systemdUnitName).Run(); err != nil {
		return fmt.Errorf("systemctl --user restart %s: %w — is the service installed? (`dejima service install`)",
			systemdUnitName, err)
	}
	return nil
}

func (m *systemdManager) Status() (string, error) {
	out, _ := exec.Command("systemctl", "--user", "is-active", systemdUnitName).Output()
	return strings.TrimSpace(string(out)), nil
}

const systemdTemplate = `[Unit]
Description=Dejima host daemon
After=network.target docker.service

[Service]
ExecStart={{.ExecStart}}
Restart=on-failure
RestartSec=5
# Each island egress tunnel costs two descriptors and lives as long as the
# agent's session; the distro default is not sized for that. The daemon also
# raises this itself at startup.
LimitNOFILE={{.LimitNOFILE}}

[Install]
WantedBy=default.target
`
