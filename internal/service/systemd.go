package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
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
	if err := tmpl.Execute(&buf, map[string]string{"ExecStart": execStart}); err != nil {
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
	if err := exec.Command("systemctl", "--user", "restart", systemdUnitName).Run(); err != nil {
		return fmt.Errorf("systemctl restart %s: %w — is the service installed? (`dejima service install`)",
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

[Install]
WantedBy=default.target
`
