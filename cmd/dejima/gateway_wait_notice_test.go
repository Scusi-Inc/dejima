package main

import (
	"strings"
	"testing"
)

// gatewayReadyBudget is five minutes, sized for the npm install a first launch
// runs inside the container. The CLI has announced that wait since #356; the
// dashboard passed nil for the same hook, so pressing ⏎ on a gateway agent
// showed nothing at all for up to five minutes and then an error — which reads
// as a broken island rather than a slow install.

func TestGatewayWaitNoticeNamesTheAgentAndTheReason(t *testing.T) {
	tm := newTunnelManager()
	if got := tm.waitingNotice(); got != "" {
		t.Fatalf("notice with nothing waiting: %q", got)
	}

	tm.markWaiting("oc-home", "a1")
	note := tm.waitingNotice()
	if note == "" {
		t.Fatal("no notice while an agent's gateway is still arriving")
	}
	if !strings.Contains(note, "oc-home") {
		t.Errorf("notice doesn't say which island is waited on:\n  %s", note)
	}
	// The operator's question is "is it broken", not "is it slow". A bare
	// "waiting…" answers the wrong one.
	if !strings.Contains(note, "normal") {
		t.Errorf("notice doesn't say the wait is expected:\n  %s", note)
	}

	tm.clearWaiting("oc-home", "a1")
	if got := tm.waitingNotice(); got != "" {
		t.Errorf("notice survived the wait it describes: %q", got)
	}
}

// The dashboard's 2s tick is what puts the notice on screen and takes it off
// again. A notice that outlived its wait would sit under a success or a failure
// contradicting it.
func TestTickSurfacesAndRetractsTheGatewayWaitNotice(t *testing.T) {
	tm := newTunnelManager()
	tm.markWaiting("oc-home", "a1")

	m := tuiModel{tunnels: tm}
	next, _ := m.Update(tickMsg{})
	shown := next.(tuiModel)
	if !strings.HasPrefix(shown.lastNotice, gatewayWaitNoticePrefix) {
		t.Fatalf("tick showed no wait notice; lastNotice = %q", shown.lastNotice)
	}

	tm.clearWaiting("oc-home", "a1")
	after, _ := shown.Update(tickMsg{})
	if n := after.(tuiModel).lastNotice; n != "" {
		t.Errorf("tick left a stale wait notice after the wait ended: %q", n)
	}
}

// Control: the retraction must key on OUR notice, not clear the footer on every
// tick. Without this, widening the retraction to `m.lastNotice = ""` would keep
// the test above green while wiping every other notice the dashboard shows.
func TestTickLeavesOtherNoticesAlone(t *testing.T) {
	const other = "already up to date"
	m := tuiModel{tunnels: newTunnelManager(), lastNotice: other}
	if strings.HasPrefix(other, gatewayWaitNoticePrefix) {
		t.Fatal("the sample notice looks like a gateway notice — this control can't tell them apart")
	}
	next, _ := m.Update(tickMsg{})
	if n := next.(tuiModel).lastNotice; n != other {
		t.Errorf("tick clobbered an unrelated notice: %q, want %q", n, other)
	}
}
