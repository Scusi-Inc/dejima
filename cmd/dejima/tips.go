package main

// dashboardTips are one-line hints rotated in the dashboard header (under the
// tagline) and listed in `?` help. Keep each to ~one terminal line — roughly the
// tagline's width (~68 display columns) — so it fits beside the logo without
// wrapping. Each maps to a real, shipped feature.
var dashboardTips = []string{
	"Reattach to a running agent by pressing Enter — it never stopped.",
	"Press n to make an island: pick a repo, choose an agent, launch.",
	"Lost? Press ? for the full keymap.",
	"Agents in one island can message each other — try an Orchestrator.",
	"Press + to add another agent; they share the island's git + creds.",
	"Enter on an island opens all its agents, each in its own tab.",
	"Press h to hibernate an island (data kept); w wakes it later.",
	"From any tailnet device: dejima connect <name> (set DEJIMA_HOST).",
	"Press I to invite a teammate — a scoped token in one copyable blob.",
	"Ctrl-b d detaches and leaves the agent running in its session.",
	"Ctrl-\\ summons the dashboard from inside a session — it stays alive.",
	"Run dejima auth push so new islands inherit your Claude login.",
	"Schedule ambient work: dejima wake <name> --every 720h --task \"…\".",
	"Let an island spawn sub-agents on a budget: dejima spawn grant.",
	"Press / for the host-terminal band; t adds one (owner only).",
	"macOS: iTerm2/WezTerm/Ghostty give tabs + copy; Terminal.app can't.",
}

// currentTip returns the tip to show for a given rotation tick, advancing slowly
// (every few polls) so a reader isn't chasing a line that changes each refresh.
// tick typically comes from a per-poll counter; the divisor sets the dwell.
func currentTip(tick int) string {
	if len(dashboardTips) == 0 {
		return ""
	}
	if tick < 0 {
		tick = -tick
	}
	return dashboardTips[(tick/6)%len(dashboardTips)]
}
