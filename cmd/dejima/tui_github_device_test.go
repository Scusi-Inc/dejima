package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/api"
)

func startedFlow(t *testing.T) tuiModel {
	t.Helper()
	m := seededModel(t, island("alpha", "a1"))
	m.github = &githubView{connect: &deviceFlow{name: "aoos", makeDefault: true, state: deviceFlowStarting}}
	mm, _ := m.applyDeviceStarted(deviceStartedMsg{resp: api.GitHubDeviceStartResponse{
		SessionID: "sess-1", UserCode: "ABCD-1234",
		VerificationURI: "https://github.com/login/device", ExpiresIn: 900, Interval: 5,
	}})
	return mm
}

// THE RULE THIS WHOLE FEATURE IS WRITTEN TO. The pane used to spawn a terminal
// window and then say "opened `dejima github connect` — approve it on GitHub".
// On Windows the window died instantly and that sentence stayed on screen: a
// window that vanished was indistinguishable from one being used, so the pane's
// most confident claim was its least reliable one.
//
// It may now say what it ASKED FOR and what it is WAITING ON. It may not say a
// browser opened, a page was seen, or anything was approved.
func TestTheFlowClaimsNothingItCannotObserve(t *testing.T) {
	m := startedFlow(t)
	body := plain(m.renderGithubView())

	// What it must show: the two things the operator has to act on.
	for _, want := range []string{"ABCD-1234", "https://github.com/login/device", "waiting"} {
		if !strings.Contains(body, want) {
			t.Errorf("the pane must show %q while waiting:\n%s", want, body)
		}
	}
	// What it must not claim.
	for _, forbidden := range []string{"opened your browser", "approved", "connected", "signed in"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Errorf("the pane claims %q, which it cannot observe:\n%s", forbidden, body)
		}
	}
	// And it must not have gone back to pushing a CLI command at a TUI user.
	if strings.Contains(body, "dejima github connect") {
		t.Errorf("the pane prints a shell command at someone already in the TUI:\n%s", body)
	}
}

// Pressing [o] runs an opener. We can see that command's error and nothing
// beyond it — so a successful exec says we ASKED, not that a browser appeared.
func TestOpeningTheBrowserReportsTheAskNotTheOutcome(t *testing.T) {
	m := startedFlow(t)
	mm, _, _ := m.deviceFlowKey(key("o"))
	body := plain(mm.renderGithubView())

	if !mm.github.connect.browserAsked {
		t.Fatal("[o] did not attempt to open the page")
	}
	if strings.Contains(body, "opened your browser") || strings.Contains(body, "opened the page") {
		t.Errorf("the pane asserts a browser appeared, which no exit code tells it:\n%s", body)
	}
	// BOTH BRANCHES, DRIVEN DIRECTLY. In this container openURL fails, so the
	// rendering above only ever exercises the couldn't-launch path — and a
	// mutation that rewrote the SUCCESS line to "opened your browser" survived
	// this test until the success branch was driven on purpose. A branch the test
	// cannot reach is a branch the test does not cover, however much of the
	// function it appears to touch.
	f := mm.github.connect
	f.browserErr = ""
	okBody := plain(mm.renderGithubView())
	if strings.Contains(strings.ToLower(okBody), "opened your browser") ||
		strings.Contains(strings.ToLower(okBody), "opened the page") {
		t.Errorf("the success branch asserts a browser appeared, which no exit code tells it:\n%s", okBody)
	}
	if !strings.Contains(okBody, "asked your system") {
		t.Errorf("the success branch should say what it asked for:\n%s", okBody)
	}

	f.browserErr = "exec: \"xdg-open\": executable file not found"
	failBody := plain(mm.renderGithubView())
	if !strings.Contains(failBody, "couldn't launch") {
		t.Errorf("the failure branch should say the opener did not run:\n%s", failBody)
	}
	// The URL stays on screen either way — it is the path that works when the
	// opener silently does nothing, which is the case we cannot detect.
	if !strings.Contains(body, "https://github.com/login/device") {
		t.Errorf("the URL vanished after [o], leaving nothing to fall back to:\n%s", body)
	}
}

// Only an authorized poll may say "connected", and it names what was stored.
func TestOnlyAnAuthorizedPollReportsSuccess(t *testing.T) {
	m := startedFlow(t)
	mm, cmd := m.applyDevicePolled(devicePolledMsg{sessionID: "sess-1", resp: api.GitHubDevicePollResponse{
		State: "authorized", Identity: "aoos", Login: "aoos-user",
	}})
	if mm.github.connect != nil {
		t.Error("an authorized flow should be finished, not still waiting")
	}
	if !strings.Contains(mm.github.notice, "aoos") {
		t.Errorf("success must name the identity that was stored, got %q", mm.github.notice)
	}
	// ...and the list the operator is looking at is reloaded in place, rather
	// than being told to press [r] — the handoff this feature exists to remove.
	if cmd == nil || !mm.github.loading {
		t.Error("an authorized flow must reload the identity list in place")
	}
}

// A poll that could not be MADE is not a refusal. Reporting a dropped request as
// a denial tells the operator they were rejected when they were not, during a
// sign-in they may be halfway through completing.
func TestATransportFailureIsNotADenial(t *testing.T) {
	m := startedFlow(t)
	mm, cmd := m.applyDevicePolled(devicePolledMsg{sessionID: "sess-1", err: errors.New("connection reset")})
	if mm.github.connect == nil {
		t.Fatal("a failed poll ended the flow")
	}
	if mm.github.connect.state == deviceFlowFailed {
		t.Errorf("a dropped request was reported as a failed sign-in: %q", mm.github.connect.err)
	}
	if cmd == nil {
		t.Error("a failed poll must schedule another — otherwise the flow silently stops")
	}
}

// GitHub's real refusals do end it, and each says which one it was: an expired
// code and a denied request need different things from the operator.
func TestExpiryAndDenialAreDistinguished(t *testing.T) {
	for _, tc := range []struct{ state, want string }{
		{"expired", "expired"},
		{"access_denied", "denied"},
	} {
		m := startedFlow(t)
		mm, _ := m.applyDevicePolled(devicePolledMsg{sessionID: "sess-1", resp: api.GitHubDevicePollResponse{State: tc.state}})
		f := mm.github.connect
		if f == nil || f.state != deviceFlowFailed {
			t.Fatalf("%s did not end the flow", tc.state)
		}
		if !strings.Contains(f.err, tc.want) {
			t.Errorf("%s reported %q, which does not say which refusal it was", tc.state, f.err)
		}
	}
}

// A tick left over from a cancelled sign-in must not drive the one that replaced
// it: the reply would be applied to a session it was never issued for.
func TestAStaleSessionCannotDriveTheCurrentFlow(t *testing.T) {
	m := startedFlow(t)
	mm, cmd := m.applyDevicePolled(devicePolledMsg{sessionID: "an-older-session", resp: api.GitHubDevicePollResponse{
		State: "authorized", Identity: "wrong", Login: "wrong",
	}})
	if mm.github.connect == nil {
		t.Error("a reply for a stale session ended the current flow")
	}
	if strings.Contains(mm.github.notice, "wrong") {
		t.Errorf("a stale reply was reported as this flow's success: %q", mm.github.notice)
	}
	if cmd != nil {
		t.Error("a stale reply scheduled work")
	}
}

// Cancelling stops the waiting and says so. It must not imply an undo: if the
// operator approved on GitHub a moment ago, the daemon's session simply stops
// being polled — nothing is revoked, and claiming otherwise would be the same
// unobservable assertion in the other direction.
func TestCancellingSaysWhatItDidAndNoMore(t *testing.T) {
	m := startedFlow(t)
	mm, _, _ := m.deviceFlowKey(key("esc"))
	if mm.github == nil {
		t.Fatal("esc closed the whole pane; it should only stop the sign-in")
	}
	if mm.github.connect != nil {
		t.Error("esc did not stop the sign-in")
	}
	notice := strings.ToLower(mm.github.notice)
	if !strings.Contains(notice, "stopped waiting") {
		t.Errorf("cancelling should say what stopped, got %q", mm.github.notice)
	}
	for _, forbidden := range []string{"cancelled on github", "revoked", "undone"} {
		if strings.Contains(notice, forbidden) {
			t.Errorf("cancelling claims %q, which it did not do: %q", forbidden, mm.github.notice)
		}
	}
}

// slow_down is GitHub asking for room. Ignoring it risks the flow being
// invalidated, so the interval must grow rather than stay put.
func TestSlowDownWidensThePollingInterval(t *testing.T) {
	m := startedFlow(t)
	before := m.github.connect.interval
	mm, cmd := m.applyDevicePolled(devicePolledMsg{sessionID: "sess-1", resp: api.GitHubDevicePollResponse{
		State: "slow_down", Interval: 10,
	}})
	if got := mm.github.connect.interval; got <= before {
		t.Errorf("interval stayed at %s after slow_down (was %s)", got, before)
	}
	if cmd == nil {
		t.Error("slow_down must still schedule the next poll")
	}
}

// The expiry is shown while it is still in the future, because a code that has
// silently died looks exactly like one nobody has typed yet.
func TestTheCodeExpiryIsVisible(t *testing.T) {
	m := startedFlow(t)
	if !strings.Contains(plain(m.renderGithubView()), "expires in") {
		t.Errorf("the pane does not say the code expires:\n%s", plain(m.renderGithubView()))
	}
	// Once past, the countdown is simply absent rather than counting backwards.
	m.github.connect.expiresAt = m.now().Add(-time.Minute)
	if strings.Contains(plain(m.renderGithubView()), "expires in") {
		t.Error("an expired code still advertises a countdown")
	}
}

// THE INVARIANT THE WHOLE FEATURE EXISTS FOR, asserted against the state it is
// most likely to be broken in: authorization_pending is what GitHub returns for
// most of a sign-in's life, so a mistake there is the one an operator meets.
//
// Added because a mutation that reported "connected" on the pending branch
// survived the first pass. Every other test drove a terminal state; none of them
// fed the ordinary one, so the code path the operator spends the flow in was the
// path with no assertion on it.
func TestPendingIsNeverReportedAsSuccess(t *testing.T) {
	m := startedFlow(t)
	mm, cmd := m.applyDevicePolled(devicePolledMsg{sessionID: "sess-1", resp: api.GitHubDevicePollResponse{
		State: "authorization_pending",
	}})

	if mm.github.connect == nil {
		t.Fatal("a pending poll ended the sign-in — the operator is still mid-approval")
	}
	if mm.github.connect.state != deviceFlowWaiting {
		t.Errorf("a pending poll moved the flow out of waiting, to state %v", mm.github.connect.state)
	}
	if cmd == nil {
		t.Error("a pending poll scheduled no follow-up, so the flow silently stops")
	}
	for _, forbidden := range []string{"connected", "ready", "signed in"} {
		if strings.Contains(strings.ToLower(mm.github.notice), forbidden) {
			t.Errorf("a pending poll claimed %q: %q", forbidden, mm.github.notice)
		}
	}
	if body := strings.ToLower(plain(mm.renderGithubView())); strings.Contains(body, "connected") {
		t.Errorf("the pane reports a connection while still waiting for one:\n%s", body)
	}
}

// An unrecognised state — one a newer daemon knows and this build does not —
// must fall to waiting rather than to success. The reassuring reading of an
// unknown answer is the one that must never be the default.
func TestAnUnknownPollStateKeepsWaitingRatherThanClaimingSuccess(t *testing.T) {
	m := startedFlow(t)
	mm, cmd := m.applyDevicePolled(devicePolledMsg{sessionID: "sess-1", resp: api.GitHubDevicePollResponse{
		State: "some_state_from_a_newer_daemon",
	}})
	if mm.github.connect == nil || cmd == nil {
		t.Fatal("an unknown state ended the flow instead of continuing to wait")
	}
	if strings.Contains(strings.ToLower(mm.github.notice), "connected") {
		t.Errorf("an unknown state was read as success: %q", mm.github.notice)
	}
}

// A SELF-HOSTED DAEMON HAS NO OAUTH APP, so guided sign-in is dark by default —
// the norm here, not an incident. The CLI has caught this since `dejima github
// connect` was written and completes over the token path instead. The TUI
// printed the daemon's 501 verbatim and stopped, pointing at a THIRD command
// (`dejima auth push --github`) rather than the one that would have worked.
//
// Same daemon, same operator, one surface dead-ending while the other just
// works, is the defect. The tests below are about what the pane OFFERS, not
// about the wording of the message.
func unconfiguredFlow(t *testing.T, ghPresent bool) tuiModel {
	t.Helper()
	m := seededModel(t, island("alpha", "a1"))
	m.github = &githubView{connect: &deviceFlow{name: "aoos", state: deviceFlowStarting}}
	mm, _ := m.applyDeviceStarted(deviceStartedMsg{
		err: errors.New("guided GitHub sign-in isn't configured on this daemon (no OAuth app); use `dejima auth push --github` with a token instead"),
	})
	// Probed for real in production; pinned here, since whether a signed-in gh
	// exists on the machine running the tests is not this test's subject.
	if f := mm.github.connect; f != nil {
		f.ghAvailable = ghPresent
	}
	return mm
}

func TestGuidedSignInUnavailableIsNotAFailure(t *testing.T) {
	mm := unconfiguredFlow(t, true)
	f := mm.github.connect
	if f == nil {
		t.Fatal("the pane closed; the operator needs the alternatives")
	}
	// The distinction that IS the bug: deviceFlowFailed offers [c] to retry, and
	// retrying this one fails identically forever. It is a permanent property of
	// the daemon, not an incident.
	if f.state == deviceFlowFailed {
		t.Error("a daemon with no OAuth app was reported as a failed sign-in, " +
			"which offers a retry that can never succeed")
	}
	if f.state != deviceFlowUnavailable {
		t.Errorf("state = %v, want deviceFlowUnavailable", f.state)
	}
}

// The screen must offer the route that WORKS. Asserting on the rendered pane
// rather than on internal state, because "the operator can see a way forward"
// is the actual requirement.
func TestUnavailableOffersTheLocalGhRoute(t *testing.T) {
	mm := unconfiguredFlow(t, true)
	out := plain(mm.github.renderDeviceFlow(mm.now()))
	if !strings.Contains(out, "[g]") {
		t.Errorf("no [g] offer on a machine with a signed-in gh:\n%s", out)
	}
	if strings.Contains(out, "[c] try again") {
		t.Errorf("offered a retry that cannot succeed:\n%s", out)
	}
	// The daemon's own message names `dejima auth push --github`. Repeating that
	// verbatim is what sent the operator to a third command; the pane should
	// surface what it can DO instead.
	if strings.Contains(out, "auth push") {
		t.Errorf("relayed the daemon's third-command pointer instead of acting:\n%s", out)
	}
}

// With no gh to read, [g] must NOT be offered — an offer that fails on press is
// worse than one that was never made. The manual routes take its place.
func TestUnavailableWithoutGhOffersTheManualRoutes(t *testing.T) {
	mm := unconfiguredFlow(t, false)
	out := plain(mm.github.renderDeviceFlow(mm.now()))
	if strings.Contains(out, "[g]") {
		t.Errorf("offered [g] with no signed-in gh to read:\n%s", out)
	}
	if !strings.Contains(out, "gh auth login") || !strings.Contains(out, "--token-stdin") {
		t.Errorf("neither manual route is named:\n%s", out)
	}
}

// A failed [g] returns to the screen that lists the OTHER routes, with the
// reason on it. Dropping the operator somewhere with fewer options — or closing
// the pane — would take away the PAT path, which the failure did not invalidate.
func TestAFailedLocalGhKeepsTheAlternativesOnScreen(t *testing.T) {
	mm := unconfiguredFlow(t, true)
	mm, _ = mm.applyGhConnected(ghConnectedMsg{err: errors.New("gh is signed out")})
	f := mm.github.connect
	if f == nil {
		t.Fatal("the pane closed on a failed attempt")
	}
	if f.state != deviceFlowUnavailable {
		t.Errorf("state = %v, want to be back on the routes screen", f.state)
	}
	out := plain(mm.github.renderDeviceFlow(mm.now()))
	if !strings.Contains(out, "gh is signed out") {
		t.Errorf("the reason it failed is not on screen:\n%s", out)
	}
	if !strings.Contains(out, "[g]") {
		t.Errorf("the route was withdrawn after one failure:\n%s", out)
	}
}

// Success stores the identity and reloads the list in place — the same handoff
// removal the authorized device-flow path already makes.
func TestLocalGhSuccessStoresAndReloads(t *testing.T) {
	mm := unconfiguredFlow(t, true)
	mm, cmd := mm.applyGhConnected(ghConnectedMsg{identity: "aoos", login: "aoos", scopes: "repo, read:org"})
	if mm.github.connect != nil {
		t.Error("a stored identity should end the flow")
	}
	if !strings.Contains(mm.github.notice, "aoos") {
		t.Errorf("success must name what was stored, got %q", mm.github.notice)
	}
	if cmd == nil || !mm.github.loading {
		t.Error("the identity list must reload in place")
	}
}

// A token that authenticates but cannot push looks identical to a working one
// until an agent fails hours later with "Resource not accessible by personal
// access token" — a message naming nothing anyone can act on. Said HERE, at the
// moment it is stored, exactly as the CLI path says it.
func TestAReadOnlyTokenIsCalledOutWhenStored(t *testing.T) {
	mm := unconfiguredFlow(t, true)
	mm, _ = mm.applyGhConnected(ghConnectedMsg{identity: "aoos", login: "aoos", scopes: "read:org"})
	if !strings.Contains(mm.github.notice, "CANNOT") {
		t.Errorf("a token that cannot push was stored without saying so: %q", mm.github.notice)
	}
}
