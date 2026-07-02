package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/invite"
)

// teamView is the owner-only "Team" overlay (opened with `I`, for Invite). It is
// the no-CLI path to onboarding a teammate: pick a role + island scope, mint a
// bearer token via the existing CreateToken API, and show a copyable invite the
// teammate pastes once. It also lists every issued token and revokes them. The
// daemon is the source of truth for authority — minting is owner-only, so a
// non-owner caller gets a 403 on open, which we render as a clear gate rather
// than an empty form.
//
// The teammate "Join" side (paste invite -> persist -> connect) is a separate
// first-run flow that depends on a1's invite-blob encode/decode + token
// persistence. Until that lands, the minted panel shows the connection details
// (host + secret) as the manual hand-off; the one-paste blob slots in here once
// the format exists.
type teamView struct {
	loading   bool
	loadErr   string // generic load failure (daemon down, decode, …)
	ownerOnly bool   // the list call 403'd — caller isn't the owner
	tokens    []api.TokenView

	// invite form
	focus    int  // index into focusItems()
	roleSel  int  // 0 = operator, 1 = viewer (owner is never minted from here)
	scopeAll bool // true = token spans all islands; false = the checked subset
	scopeSel map[string]bool
	owner    string // tenant the teammate acts as (multi-tenant scoping); "" = full access (host owner)
	host     string // daemon host:port the teammate dials (operator-supplied; the daemon can't self-detect it)
	label    string

	minting    bool
	minted     *api.CreateTokenResponse // last mint; shown once until dismissed
	mintedBlob string                   // the paste-safe invite for the last mint ("" if no host given)
	actionErr  string                   // mint / revoke error
}

// teamRoles are the roles the invite flow offers. Owner is deliberately absent:
// handing out full authority is a deliberate CLI act (`dejima token create
// --role owner`), never a casual TUI invite.
var teamRoles = []string{"operator", "viewer"}

const (
	tfRole   = iota // role toggle
	tfOwner         // tenant text field (multi-tenant scoping; blank = full access)
	tfScope         // all-islands vs custom toggle
	tfIsland        // one per island (only when !scopeAll); idx = island index
	tfHost          // daemon host:port text field
	tfLabel         // optional label text field
	tfCreate        // the "create invite" button
	tfToken         // one per issued token; idx = token index
)

// teamFocusItem is one navigable thing in the overlay: a form field, an island
// checkbox, or a token row. idx disambiguates the repeated kinds (islands,
// tokens).
type teamFocusItem struct {
	kind int
	idx  int
}

// openTeamView opens the Team overlay and kicks off the owner-only token list.
// The host field is prefilled with the current connection target when that's a
// real host:port — that's the address this very client dialed, so it's the one a
// teammate would dial too. When we're on the local socket (activeHost == "")
// there's nothing to prefill: the operator must supply the daemon's reachable
// address, which the daemon can't self-detect.
func (m tuiModel) openTeamView() (tea.Model, tea.Cmd) {
	m.team = &teamView{loading: true, scopeAll: true, scopeSel: map[string]bool{}, host: m.activeHost}
	return m, m.loadTokensCmd()
}

type tokensLoadedMsg struct {
	tokens []api.TokenView
	err    error
}

func (m tuiModel) loadTokensCmd() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		toks, err := c.ListTokens(ctx)
		return tokensLoadedMsg{tokens: toks, err: err}
	}
}

func (v *teamView) applyLoaded(msg tokensLoadedMsg) {
	v.loading = false
	if msg.err != nil {
		// The token routes are owner-only; a non-owner caller 403s. Detect that so
		// we can explain it rather than show a raw error.
		if isOwnerOnlyErr(msg.err) {
			v.ownerOnly = true
			return
		}
		v.loadErr = msg.err.Error()
		return
	}
	v.ownerOnly, v.loadErr = false, ""
	v.tokens = msg.tokens
}

// isOwnerOnlyErr reports whether err is roleAuth's owner-gate 403. The client
// surfaces the server's message verbatim ("role \"operator\" may not access this
// route (requires owner)") or a bare "http 403" if it couldn't decode the body.
func isOwnerOnlyErr(err error) bool {
	s := err.Error()
	return strings.Contains(s, "requires owner") ||
		strings.Contains(s, "may not access this route") ||
		strings.Contains(s, "http 403")
}

type tokenMintedMsg struct {
	resp *api.CreateTokenResponse
	blob string // the paste-safe invite (empty when no host was given)
	err  error
}

// mintTokenCmd mints the token and, when a host was supplied, encodes the
// paste-safe invite blob in the same step — the bearer secret is only returned
// once, so the blob must be built here, not later. host empty → token only (the
// minted panel falls back to the manual DEJIMA_HOST/TOKEN hand-off).
func (m tuiModel) mintTokenCmd(req api.CreateTokenRequest, host string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resp, err := c.CreateToken(ctx, req)
		if err != nil {
			return tokenMintedMsg{err: err}
		}
		var blob string
		if strings.TrimSpace(host) != "" {
			blob, err = invite.Encode(invite.Payload{
				Host:    strings.TrimSpace(host),
				Token:   resp.Secret,
				Role:    resp.Token.Role,
				Islands: resp.Token.Islands,
				Label:   resp.Token.Label,
				// Name omitted — SaveInvite derives a profile name from the host.
			})
			if err != nil {
				// Token minted but the invite couldn't be built — surface it so the
				// operator can revoke the now-orphaned token (id is in the list).
				return tokenMintedMsg{resp: resp, err: fmt.Errorf("token %s minted, but encoding the invite failed: %w (revoke it below)", resp.Token.ID, err)}
			}
		}
		return tokenMintedMsg{resp: resp, blob: blob}
	}
}

type tokenRevokedMsg struct{ err error }

func (m tuiModel) revokeTokenCmd(id string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return tokenRevokedMsg{err: c.RevokeToken(ctx, id)}
	}
}

// islandNames returns the island names in stable (sorted) order — the order the
// scope checklist and focus model walk.
func (m tuiModel) islandNames() []string {
	names := make([]string, 0, len(m.islands))
	for _, isl := range m.islands {
		names = append(names, isl.Name)
	}
	sort.Strings(names)
	return names
}

// focusItems builds the ordered list of navigable items, given the current form
// state (islands appear only when scoped; tokens trail the form). Focus is an
// index into this slice, so the form stays a single top-to-bottom flow.
func (m tuiModel) teamFocusItems() []teamFocusItem {
	v := m.team
	items := []teamFocusItem{{kind: tfRole}, {kind: tfOwner}, {kind: tfScope}}
	if !v.scopeAll {
		for i := range m.islandNames() {
			items = append(items, teamFocusItem{kind: tfIsland, idx: i})
		}
	}
	items = append(items, teamFocusItem{kind: tfHost}, teamFocusItem{kind: tfLabel}, teamFocusItem{kind: tfCreate})
	for i := range v.tokens {
		items = append(items, teamFocusItem{kind: tfToken, idx: i})
	}
	return items
}

// isTextField reports whether a focus kind is a free-text input (owner / host /
// label) — printable runes type into it, and j/k don't navigate away.
func isTextField(kind int) bool {
	return kind == tfOwner || kind == tfHost || kind == tfLabel
}

func (m tuiModel) teamCurrent() teamFocusItem {
	items := m.teamFocusItems()
	if len(items) == 0 {
		return teamFocusItem{kind: tfRole}
	}
	i := m.team.focus
	if i < 0 {
		i = 0
	}
	if i >= len(items) {
		i = len(items) - 1
	}
	return items[i]
}

// teamKey drives the overlay. While a freshly-minted invite is showing, any of
// enter/esc/q dismisses it (and refreshes the list); otherwise the form +
// token-list navigation is live.
func (m tuiModel) teamKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.team

	// The one-time minted invite owns the pane until dismissed.
	if v.minted != nil {
		switch msg.String() {
		case "c":
			// Copy the ready-to-run onboarding block (install + join) — or the raw
			// secret when there's no one-paste invite — to the operator's clipboard.
			payload, notice := m.mintedCopyPayload()
			return m, copyToClipboardCmd(payload, notice)
		case "enter", "esc", "q":
			v.minted = nil
			v.loading = true
			return m, m.loadTokensCmd()
		}
		return m, nil
	}

	items := m.teamFocusItems()
	if v.focus >= len(items) {
		v.focus = len(items) - 1
	}
	if v.focus < 0 {
		v.focus = 0
	}
	cur := items[v.focus]

	// While a text field has focus, printable runes type into it; navigation is
	// via tab / arrows so it doesn't eat the keystrokes. Host and label are the
	// two text fields.
	if isTextField(cur.kind) {
		field := &v.label
		switch cur.kind {
		case tfHost:
			field = &v.host
		case tfOwner:
			field = &v.owner
		}
		switch msg.Type {
		case tea.KeyRunes, tea.KeySpace:
			*field += string(msg.Runes)
			return m, nil
		case tea.KeyBackspace:
			if *field != "" {
				*field = (*field)[:len(*field)-1]
			}
			return m, nil
		}
	}

	switch msg.String() {
	case "esc", "q", "I":
		m.team = nil
		return m, nil
	case "r":
		v.loading = true
		v.loadErr = ""
		return m, m.loadTokensCmd()
	case "down", "tab":
		if v.focus < len(items)-1 {
			v.focus++
		}
		return m, nil
	case "up", "shift+tab":
		if v.focus > 0 {
			v.focus--
		}
		return m, nil
	case "j":
		if !isTextField(cur.kind) && v.focus < len(items)-1 {
			v.focus++
		}
		return m, nil
	case "k":
		if !isTextField(cur.kind) && v.focus > 0 {
			v.focus--
		}
		return m, nil
	case "left", "right", "h", "l":
		switch cur.kind {
		case tfRole:
			v.roleSel = 1 - v.roleSel
		case tfScope:
			v.scopeAll = !v.scopeAll
		}
		return m, nil
	case " ", "x":
		switch cur.kind {
		case tfIsland:
			name := m.islandNames()[cur.idx]
			if v.scopeSel[name] {
				delete(v.scopeSel, name)
			} else {
				v.scopeSel[name] = true
			}
		case tfScope:
			v.scopeAll = !v.scopeAll
		case tfToken:
			if msg.String() == "x" {
				return m.teamRevokeFocused()
			}
		}
		return m, nil
	case "d":
		if cur.kind == tfToken {
			return m.teamRevokeFocused()
		}
		return m, nil
	case "enter":
		switch cur.kind {
		case tfRole:
			v.roleSel = 1 - v.roleSel
		case tfScope:
			v.scopeAll = !v.scopeAll
		case tfIsland:
			name := m.islandNames()[cur.idx]
			if v.scopeSel[name] {
				delete(v.scopeSel, name)
			} else {
				v.scopeSel[name] = true
			}
		case tfCreate:
			return m.teamMint()
		case tfToken:
			// no-op: tokens are revoked with d/x, not Enter (avoid accidental revoke)
		}
		return m, nil
	}
	return m, nil
}

// teamMintRequest builds the CreateToken request from the current form, or
// returns a non-empty validation error (a custom scope with no island chosen).
func (m tuiModel) teamMintRequest() (api.CreateTokenRequest, string) {
	v := m.team
	req := api.CreateTokenRequest{
		Role:  teamRoles[v.roleSel],
		Owner: strings.TrimSpace(v.owner),
		Label: strings.TrimSpace(v.label),
	}
	if !v.scopeAll {
		var islands []string
		for _, name := range m.islandNames() {
			if v.scopeSel[name] {
				islands = append(islands, name)
			}
		}
		if len(islands) == 0 {
			return req, "pick at least one island, or switch scope to all islands (←/→ on Scope)"
		}
		req.Islands = islands
	}
	return req, ""
}

// teamMint validates the form and fires the CreateToken command.
func (m tuiModel) teamMint() (tea.Model, tea.Cmd) {
	req, verr := m.teamMintRequest()
	if verr != "" {
		m.team.actionErr = verr
		return m, nil
	}
	m.team.minting = true
	m.team.actionErr = ""
	return m, m.mintTokenCmd(req, m.team.host)
}

func (m tuiModel) teamRevokeFocused() (tea.Model, tea.Cmd) {
	v := m.team
	cur := m.teamCurrent()
	if cur.kind != tfToken || cur.idx >= len(v.tokens) {
		return m, nil
	}
	v.actionErr = ""
	return m, m.revokeTokenCmd(v.tokens[cur.idx].ID)
}

// renderTeamView renders the overlay; the caller (View) wraps it in stylePane.
func (m tuiModel) renderTeamView() string {
	v := m.team
	var b strings.Builder
	b.WriteString(styleTitle.Render("Team · invite a teammate"))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("mint a bearer token, hand over the invite — no CLI, no env vars"))
	b.WriteString("\n\n")

	switch {
	case v.loading:
		return b.String() + styleMuted.Render("loading…")
	case v.ownerOnly:
		b.WriteString(styleWaiting.Render("Team management is owner-only."))
		b.WriteString("\n\n" + styleMuted.Render(
			"You're connected with an operator or viewer credential. Reconnect as the\n"+
				"owner (the trusted local daemon, or an owner token) to invite teammates."))
		b.WriteString("\n\n" + styleMuted.Render("[esc] close"))
		return b.String()
	case v.loadErr != "":
		return b.String() + styleErrored.Render("error: "+v.loadErr) +
			"\n\n" + styleMuted.Render("(is the daemon reachable? [r] retry, [esc] close)")
	case v.minted != nil:
		return b.String() + m.renderMintedInvite()
	}

	return b.String() + m.renderTeamForm()
}

// renderTeamForm draws the invite form followed by the issued-token list.
func (m tuiModel) renderTeamForm() string {
	v := m.team
	items := m.teamFocusItems()
	cur := items[clamp(v.focus, 0, len(items)-1)]
	var b strings.Builder

	// Helper: a focusable row, "▸ " when this kind/idx is the cursor.
	row := func(focused bool, s string) string {
		if focused {
			return styleSelected.Render("▸ "+s) + "\n"
		}
		return "  " + s + "\n"
	}
	focusedOn := func(kind, idx int) bool { return cur.kind == kind && cur.idx == idx }

	// Role.
	roleVal := fmt.Sprintf("‹ %s ›", teamRoles[v.roleSel])
	b.WriteString(row(cur.kind == tfRole, styleHeader.Render("Role")+"   "+styleAccent.Render(roleVal)))
	roleHelp := "operator: full island lifecycle · viewer: read-only"
	b.WriteString("    " + styleMuted.Render(roleHelp) + "\n\n")

	// Owner (tenant). Blank = full access to the host owner's fleet (back-compat);
	// a teammate id scopes them to only the islands that owner creates/owns.
	ownerVal := v.owner
	if cur.kind == tfOwner {
		ownerVal += "▎" // cursor
	}
	if ownerVal == "" {
		ownerVal = styleMuted.Render("(blank = full access; a teammate id scopes them, e.g. amanda)")
	} else {
		ownerVal = styleAccent.Render(ownerVal)
	}
	b.WriteString(row(cur.kind == tfOwner, styleHeader.Render("Owner")+"  "+ownerVal))
	b.WriteString("\n")

	// Scope.
	scopeVal := "‹ all islands ›"
	if !v.scopeAll {
		scopeVal = "‹ pick islands ›"
	}
	b.WriteString(row(cur.kind == tfScope, styleHeader.Render("Scope")+"  "+styleAccent.Render(scopeVal)))
	if !v.scopeAll {
		names := m.islandNames()
		if len(names) == 0 {
			b.WriteString("    " + styleMuted.Render("no islands yet — switch to all islands") + "\n")
		}
		for i, name := range names {
			box := "[ ]"
			if v.scopeSel[name] {
				box = styleRunning.Render("[x]")
			}
			b.WriteString("  " + row(focusedOn(tfIsland, i), "  "+box+" "+name))
		}
	}
	b.WriteString("\n")

	// Host — the daemon address the teammate dials. Required for the one-paste
	// invite blob (the daemon can't self-detect its reachable address); leave it
	// blank and the mint still works, falling back to the manual hand-off.
	hostVal := v.host
	if cur.kind == tfHost {
		hostVal += "▎" // cursor
	}
	if hostVal == "" {
		hostVal = styleMuted.Render("(e.g. minion.ts.net:7274 — for the one-paste invite)")
	} else {
		hostVal = styleAccent.Render(hostVal)
	}
	b.WriteString(row(cur.kind == tfHost, styleHeader.Render("Host")+"   "+hostVal))
	b.WriteString("\n")

	// Label.
	labelVal := v.label
	if cur.kind == tfLabel {
		labelVal += "▎" // cursor
	}
	if labelVal == "" {
		labelVal = styleMuted.Render("(optional — e.g. amanda)")
	} else {
		labelVal = styleAccent.Render(labelVal)
	}
	b.WriteString(row(cur.kind == tfLabel, styleHeader.Render("Label")+"  "+labelVal))
	b.WriteString("\n")

	// Create button.
	btn := "Create invite"
	if v.minting {
		btn = "Creating…"
	}
	b.WriteString(row(cur.kind == tfCreate, styleRunning.Render("[ "+btn+" ]")))
	if v.actionErr != "" {
		b.WriteString("  " + styleErrored.Render(v.actionErr) + "\n")
	}
	b.WriteString("\n")

	// Issued tokens.
	b.WriteString(styleHeader.Render(fmt.Sprintf("Issued tokens (%d)", len(v.tokens))))
	b.WriteString("\n")
	if len(v.tokens) == 0 {
		b.WriteString("  " + styleMuted.Render("none yet") + "\n")
	}
	for i, t := range v.tokens {
		label := t.Label
		if label == "" {
			label = styleMuted.Render("(no label)")
		}
		scope := "all islands"
		if len(t.Islands) > 0 {
			scope = strings.Join(t.Islands, ",")
		}
		line := fmt.Sprintf("%-10s %-8s %-16s %s",
			styleAccent.Render(t.Role),
			label,
			styleMuted.Render(truncate(scope, 16)),
			styleMuted.Render(t.ID))
		b.WriteString(row(focusedOn(tfToken, i), line))
	}
	b.WriteString("\n")
	b.WriteString(styleMuted.Render(
		"[↑/↓ or tab] move   [←/→] change   [space] toggle island   " +
			"[enter] create   [d] revoke token   [esc] close"))
	return b.String()
}

// Canonical client-install commands (source of truth: docs/distribution.md).
// The curl line covers macOS + Linux; brew is the macOS alternative; the PS line
// is the Windows client. Shown on the minted-invite panel and used by the copy.
const (
	installClientCmd  = "curl -fsSL https://dejima.tech/install-client.sh | bash"
	installClientBrew = "brew install aoos/dejima/dejima"
	installClientWin  = "irm https://dejima.tech/install-client.ps1 | iex"
)

// mintedCopyPayload is what the [c] hotkey puts on the clipboard. For a one-paste
// invite it's the two-line "install then join" block a teammate pastes on their
// OWN machine to go from a bare laptop to connected; otherwise it's the raw
// secret (the no-host fallback). No trailing newline on the join line, so a paste
// runs the install and leaves `dejima join …` at the prompt to confirm.
func (m tuiModel) mintedCopyPayload() (payload, notice string) {
	v := m.team
	if v.mintedBlob != "" {
		return installClientCmd + "\ndejima join " + v.mintedBlob,
			"copied install + join command to clipboard"
	}
	return v.minted.Secret, "copied secret to clipboard"
}

// renderMintedInvite shows the one-time secret and the hand-off details. The
// secret is shown ONCE — the daemon never returns it again.
func (m tuiModel) renderMintedInvite() string {
	v := m.team
	t := v.minted.Token
	var b strings.Builder
	b.WriteString(styleRunning.Render("✓ invite created"))
	b.WriteString("\n\n")

	scope := "all islands"
	if len(t.Islands) > 0 {
		scope = strings.Join(t.Islands, ", ")
	}
	who := t.Label
	if who == "" {
		who = "(unlabeled)"
	}
	b.WriteString(fmt.Sprintf("  %s   role %s · scope %s\n\n",
		styleAccent.Render(who), styleAccent.Render(t.Role), styleAccent.Render(scope)))

	if v.mintedBlob != "" {
		// The one-paste invite — carries the host + secret in a single line. The
		// teammate accepts it in the TUI (Connection → join via invite) or with
		// `dejima join <blob>`. It contains the secret, so it's a credential.
		b.WriteString(styleWaiting.Render("  Send this invite — shown once, and it carries the secret (treat like a password):"))
		b.WriteString("\n\n")
		b.WriteString("  " + styleTitle.Render(v.mintedBlob) + "\n\n")
		// Where + how to run it. The #1 onboarding confusion was teammates running
		// `dejima join` over SSH ON THIS HOST (old shared binary → "unknown command
		// join") instead of on their own laptop — and not realizing they must
		// install first (join can't self-install; chicken/egg). So spell out the
		// full 2-step quickstart, and say plainly it's the teammate's OWN machine.
		b.WriteString(styleHeader.Render("  Your teammate runs this on THEIR OWN computer (their laptop) — not on this host:"))
		b.WriteString("\n\n")
		b.WriteString("    1. " + styleAccent.Render(installClientCmd) + "\n")
		b.WriteString(styleMuted.Render("       macOS also: "+installClientBrew+"   ·   Windows: "+installClientWin) + "\n")
		b.WriteString("    2. " + styleAccent.Render("dejima join "+truncate(v.mintedBlob, 24)+"…") + "\n\n")
		b.WriteString(styleMuted.Render("    …or accept it in the TUI: Connection (s) → [j] join via invite, then paste."))
		b.WriteString("\n\n")
		b.WriteString(styleMuted.Render("  Revoke anytime from the list below ([d] on the token)."))
		b.WriteString("\n\n")
		b.WriteString(styleMuted.Render("  [c] copy install+join   [enter/esc] done"))
		return b.String()
	}

	// No host was given, so there's no blob — fall back to the manual hand-off:
	// the secret + the two env values the teammate sets to connect.
	b.WriteString(styleWaiting.Render("  Copy this secret now — it is shown only once:"))
	b.WriteString("\n\n")
	b.WriteString("  " + styleTitle.Render(v.minted.Secret) + "\n\n")
	b.WriteString(styleMuted.Render("  No host was set, so there's no one-paste invite. The teammate connects with:"))
	b.WriteString("\n")
	b.WriteString("    export DEJIMA_HOST=" + styleMuted.Render("<this daemon's reachable host:port>") + "\n")
	b.WriteString("    export DEJIMA_TOKEN=" + styleAccent.Render("<the secret above>") + "\n\n")
	b.WriteString(styleMuted.Render("  Tip: set Host on the form to get a single paste-safe invite instead."))
	b.WriteString("\n\n")
	b.WriteString(styleMuted.Render("  [c] copy secret   [enter/esc] done"))
	return b.String()
}

// clamp bounds i to [lo, hi]; hi<lo yields lo.
func clamp(i, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if i < lo {
		return lo
	}
	if i > hi {
		return hi
	}
	return i
}
