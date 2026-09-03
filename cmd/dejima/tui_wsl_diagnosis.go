package main

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/wsl"
)

// wslDiagnosisMsg carries a probe-backed diagnosis of a wsl:// target back into
// the Update loop.
//
// host is carried so a stale probe cannot land on a different target: the
// operator can switch profiles with [C] while a probe is in flight, and a
// diagnosis of the distro they just left would read as a diagnosis of the one
// they just chose.
type wslDiagnosisMsg struct {
	host      string
	diagnosis daemonDiagnosis
}

// probeWSLDiagnosisCmd inspects the distro off the Update loop.
//
// wsl.Probe shells out to wsl.exe twice and the first call BOOTS a stopped
// distro, which is seconds — the dashboard must not sit still for that. The
// bound is generous for the same reason: a cold distro that takes its time is
// the normal case here, not a hang.
func probeWSLDiagnosisCmd(host string) tea.Cmd {
	distro := wsl.Distro(host)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var repp *wsl.Report
		rep, err := wsl.Probe(ctx, distro)
		if err == nil {
			repp = &rep
		}
		return wslDiagnosisMsg{host: host, diagnosis: diagnoseWSLDaemon(distro, repp, err)}
	}
}
