package main

import (
	"strings"
	"testing"
)

// The checklist is the last thing a new operator reads, and it is the surface a
// field report was filed against: a dozen equal-weight bullets, each with a
// command buried mid-sentence, and nothing saying which mattered. The response
// was "That's a lot of steps" — while most of them were optional.
func TestProvManualSeparatesRequiredFromOptional(t *testing.T) {
	pc := &provCtx{}
	pc.addManualFor(whyReboot, "Enable auto-login", "System Settings → Users & Groups")
	pc.addManualFor(whyRemote, "Bring Tailscale up", "sudo tailscale up --ssh --accept-dns=true")
	pc.addManualFor(whyPerf, "Right-size the Docker VM", "Docker Desktop → Settings → Resources")
	pc.addManual("Retry the local-models install", "dejima local install")
	out := renderProvManual(pc)

	// The question the list raises is "is my install finished?". Answer it.
	if !strings.Contains(out, "Dejima works now") {
		t.Errorf("with no blocking steps the checklist must say so up front:\n%s", out)
	}

	// Title on its own line, command indented beneath — not run together.
	if !strings.Contains(out, "• Bring Tailscale up\n      sudo tailscale up") {
		t.Errorf("title and command are not on separate lines:\n%s", out)
	}

	// Optional work must not outrank remote access.
	if strings.Index(out, whyRemote) > strings.Index(out, "Optional") {
		t.Errorf("Optional is ordered above %q:\n%s", whyRemote, out)
	}
}

// The inverse claim is the one that must never be wrong: if something genuinely
// blocks, the checklist must NOT open by saying everything is fine.
func TestProvManualDoesNotClaimReadyWhenBlocked(t *testing.T) {
	pc := &provCtx{}
	pc.addManualFor(whyBlocking, "Install Docker Desktop", "brew install --cask docker-desktop")
	pc.addManual("Set up local models", "dejima local install")
	out := renderProvManual(pc)

	if strings.Contains(out, "Dejima works now") {
		t.Errorf("a blocking step is outstanding but the checklist says it works:\n%s", out)
	}
	if !strings.Contains(out, "Some of these are required") {
		t.Errorf("blocking steps present but never announced:\n%s", out)
	}
	if strings.Index(out, whyBlocking) > strings.Index(out, "Optional") {
		t.Errorf("blocking steps are not listed first:\n%s", out)
	}
}
