package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

// The GitHub device flow, run INSIDE the pane.
//
// It used to spawn `dejima github connect` in a new terminal window. That is the
// wrong mechanism even where it works — it hands the operator to a second
// surface and cannot report what happened there — and on Windows the window died
// instantly while the pane reported "opened … approve it on GitHub, then [r] to
// reload". A window that vanished was indistinguishable from one being used, so
// the pane's most confident sentence was its least reliable one.
//
// The daemon already exposed the two halves this needs — GitHubDeviceStart and
// GitHubDevicePoll — so nothing new was required underneath. The client only
// ever sees the user code and the URL; the device_code that exchanges for a
// token stays in the daemon.
//
// THE RULE THIS FILE IS WRITTEN TO: never report something we cannot observe. We
// can observe that the daemon issued a code, and that a poll came back
// authorized. We cannot observe that a browser opened, that a human saw a page,
// or that anyone approved anything — so the pane says what it asked for and what
// it is waiting on, and only the poll result is allowed to say "connected".

// deviceFlowState is where a running flow has got to.
type deviceFlowState int

const (
	deviceFlowStarting deviceFlowState = iota // asked the daemon for a code
	deviceFlowWaiting                         // code in hand, polling GitHub
	deviceFlowDone                            // authorized and stored
	deviceFlowFailed                          // expired, denied, or errored
)

// deviceFlow is a device-flow sign-in in progress, owned by the GitHub pane.
type deviceFlow struct {
	name        string // identity name to store under ("" lets the daemon name it)
	makeDefault bool
	state       deviceFlowState

	sessionID string
	userCode  string
	verifyURI string
	interval  time.Duration
	expiresAt time.Time

	// browserAsked records that we RAN an opener, not that a browser appeared.
	// The distinction is the whole point: we can see the command's error, and
	// nothing beyond it.
	browserAsked bool
	browserErr   string

	identity string // set on success
	login    string
	err      string
}

type deviceStartedMsg struct {
	resp api.GitHubDeviceStartResponse
	err  error
}

type devicePolledMsg struct {
	sessionID string
	resp      api.GitHubDevicePollResponse
	err       error
}

// devicePollTickMsg asks for the next poll. Carries the session it was scheduled
// for so a tick left over from a cancelled flow cannot drive a new one.
type devicePollTickMsg struct{ sessionID string }

func (m tuiModel) startDeviceFlowCmd() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		resp, err := c.GitHubDeviceStart(ctx)
		return deviceStartedMsg{resp: resp, err: err}
	}
}

func (m tuiModel) pollDeviceFlowCmd(f *deviceFlow) tea.Cmd {
	c := m.client
	req := api.GitHubDevicePollRequest{SessionID: f.sessionID, Name: f.name, Default: f.makeDefault}
	sess := f.sessionID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		resp, err := c.GitHubDevicePoll(ctx, req)
		return devicePolledMsg{sessionID: sess, resp: resp, err: err}
	}
}

func devicePollTick(sessionID string, d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return devicePollTickMsg{sessionID: sessionID} })
}

// applyDeviceStarted lands the daemon's response: a code to show and a URL to
// visit, or the reason there is neither.
func (m tuiModel) applyDeviceStarted(msg deviceStartedMsg) (tuiModel, tea.Cmd) {
	v := m.github
	if v == nil || v.connect == nil {
		return m, nil // the pane closed while we were asking
	}
	f := v.connect
	if msg.err != nil {
		f.state, f.err = deviceFlowFailed, msg.err.Error()
		return m, nil
	}
	f.state = deviceFlowWaiting
	f.sessionID = msg.resp.SessionID
	f.userCode = msg.resp.UserCode
	f.verifyURI = msg.resp.VerificationURI
	// Honour the daemon's interval; fall back rather than hammering GitHub, which
	// answers a too-fast poll with slow_down and can invalidate the flow.
	f.interval = time.Duration(max(msg.resp.Interval, 5)) * time.Second
	if msg.resp.ExpiresIn > 0 {
		f.expiresAt = m.now().Add(time.Duration(msg.resp.ExpiresIn) * time.Second)
	}
	return m, devicePollTick(f.sessionID, f.interval)
}

// applyDevicePolled advances or ends the flow.
func (m tuiModel) applyDevicePolled(msg devicePolledMsg) (tuiModel, tea.Cmd) {
	v := m.github
	if v == nil || v.connect == nil || v.connect.sessionID != msg.sessionID {
		return m, nil // cancelled, or a reply from a flow that has been replaced
	}
	f := v.connect
	if msg.err != nil {
		// A poll that could not be made is not a denial. Keep waiting — a dropped
		// request during a sign-in the operator is still completing must not be
		// reported as a refusal they did not make.
		return m, devicePollTick(f.sessionID, f.interval)
	}
	switch msg.resp.State {
	case "authorized":
		f.state = deviceFlowDone
		f.identity, f.login = msg.resp.Identity, msg.resp.Login
		v.notice = fmt.Sprintf("connected %s%s — identity %q is ready",
			msg.resp.Login, map[bool]string{true: " (default)", false: ""}[f.makeDefault], msg.resp.Identity)
		v.connect = nil
		v.loading = true
		// Reload in place: the list the operator is looking at is the thing that
		// just changed, and asking them to press [r] is the handoff this file
		// exists to remove.
		return m, m.loadGithubIdentitiesCmd()
	case "slow_down":
		// GitHub asking for more room. Take its interval when it gives one.
		f.interval = time.Duration(max(msg.resp.Interval, int(f.interval/time.Second)+5)) * time.Second
		return m, devicePollTick(f.sessionID, f.interval)
	case "expired":
		f.state, f.err = deviceFlowFailed, "the code expired before it was approved"
		return m, nil
	case "access_denied":
		f.state, f.err = deviceFlowFailed, "the request was denied on GitHub"
		return m, nil
	default: // authorization_pending, or a state a newer daemon knows and we do not
		return m, devicePollTick(f.sessionID, f.interval)
	}
}

// deviceFlowKey handles keys while a flow is running. It owns them: the pane's
// list navigation would be meaningless here, and [c] restarting the flow the
// operator is halfway through is the kind of thing that loses a code they have
// already typed into a browser.
func (m tuiModel) deviceFlowKey(msg tea.KeyMsg) (tuiModel, tea.Cmd, bool) {
	v := m.github
	f := v.connect
	switch msg.String() {
	case "esc", "ctrl+[", "q":
		// Cancels the FLOW, not the pane. Nothing is revoked: if the operator
		// approved on GitHub in the meantime, the daemon's session is simply never
		// polled again, so say what stopped rather than implying an undo.
		v.connect = nil
		v.notice = "stopped waiting — nothing was changed"
		return m, nil, true
	case "o", "O":
		if f.verifyURI == "" {
			return m, nil, true
		}
		f.browserAsked = true
		if err := openURL(f.verifyURI); err != nil {
			f.browserErr = err.Error()
		} else {
			f.browserErr = ""
		}
		return m, nil, true
	}
	return m, nil, true // a flow in progress swallows everything else
}

// renderDeviceFlow draws the running flow. The code is the thing the operator
// must read and type, so it gets the room.
func (v *githubView) renderDeviceFlow(now time.Time) string {
	f := v.connect
	var b strings.Builder
	b.WriteString(styleHeader.Render("  Connect GitHub"))
	b.WriteString("\n\n")

	switch f.state {
	case deviceFlowStarting:
		b.WriteString(styleMuted.Render("  asking the daemon for a sign-in code…"))
		return b.String()
	case deviceFlowFailed:
		b.WriteString(styleErrored.Render("  ⚠ " + f.err))
		b.WriteString("\n\n")
		b.WriteString(styleMuted.Render("  [c] try again   [esc] back"))
		return b.String()
	}

	b.WriteString("  " + styleMuted.Render("1. open") + "  " + styleAccent.Render(f.verifyURI) + "\n")
	b.WriteString("  " + styleMuted.Render("2. enter") + "  " + styleSelected.Render(" "+f.userCode+" ") + "\n\n")

	// What we are DOING, not what we hope happened. "waiting for GitHub" is
	// observable — a poll is in flight on a timer. "opened your browser" would
	// not be, which is the claim the old window-spawn made and could not keep.
	b.WriteString(styleWaiting.Render("  ⏳ waiting for you to approve it on GitHub…"))
	if !f.expiresAt.IsZero() {
		if left := f.expiresAt.Sub(now); left > 0 {
			b.WriteString(styleMuted.Render(fmt.Sprintf("   (the code expires in %s)", humanDuration(left))))
		}
	}
	b.WriteString("\n")

	switch {
	case f.browserErr != "":
		b.WriteString(styleMuted.Render("  couldn't launch a browser here — open the URL above yourself"))
		b.WriteString("\n")
	case f.browserAsked:
		// Deliberately not "opened your browser": we ran an opener and it returned
		// without an error. Whether anything appeared on screen is not something
		// this process can see.
		b.WriteString(styleMuted.Render("  asked your system to open that page — if nothing appeared, use the URL above"))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("  [o] open the page   [esc] stop waiting"))
	return b.String()
}

// now is the model's clock, injected for tests. See escseq.go for why it exists.
func (m tuiModel) now() time.Time {
	if m.nowFn == nil {
		return time.Now()
	}
	return m.nowFn()
}

// openGithubViewConnecting opens the GitHub pane with a device-flow sign-in
// already running, for callers that know the operator needs an identity right
// now — the island creator's gate, and its identity screen.
//
// It exists so those callers have somewhere in-TUI to hand off to. They used to
// spawn a terminal window and report that they had; this replaces the claim with
// a screen the operator can watch, on the same surface they started from.
func (m tuiModel) openGithubViewConnecting(name string) (tea.Model, tea.Cmd) {
	m.github = &githubView{
		connect: &deviceFlow{name: name, makeDefault: true, state: deviceFlowStarting},
	}
	return m, m.startDeviceFlowCmd()
}
