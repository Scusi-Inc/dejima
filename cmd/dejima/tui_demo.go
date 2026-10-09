package main

import (
	"os"
	"time"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/clientcfg"
	"github.com/aoos/dejima/internal/link"
	"github.com/aoos/dejima/internal/policy"
	"github.com/aoos/dejima/internal/reposrc"
	"github.com/aoos/dejima/internal/secrets"
)

// Demo mode (`dejima tui --demo`) drives the dashboard from a synthetic fleet
// instead of a live daemon, so the site recordings (#12 / d6's Track B) are
// reproducible, controllable, and leak no real repos/paths/secrets. The fetch
// commands short-circuit to these builders when m.demo is set; the fleet's agent
// states churn on the tick so the hero clip looks alive. Nothing here touches a
// network or a real island. See strategy/tui-capture-runbook.md for the scenes.

// demoFrozen reports whether the fleet should hold still.
//
// The animation below is right for a screen recording and fatal for a frame
// CAPTURE. scripts/capture-demo-frames.py identifies a screen by hashing it, so
// a fleet that rewrites "working" to "needs you" every few seconds mints a new
// state every few seconds: a walk that takes minutes recorded 178 frames of
// what are really ten screens, every edge pointed at a frame that had already
// expired, and pruning reduced a 232-edge graph to nine. Numbers were already
// handled (the hash flattens digit runs); these are words, and no hash can tell
// a word that changed because the UI changed from one that changed because a
// timer fired. So the generator holds still instead, and only when asked.
func demoFrozen() bool { return os.Getenv("DEJIMA_DEMO_FREEZE") != "" }

// demoLatest cycles an agent through working → needs-you → idle so the fleet
// animates. Offset by index so the agents aren't all in lock-step; /2 slows it
// to a readable cadence for a recording. Frozen, the spread across agents stays
// (the fleet still shows all three states at once) and only the motion stops.
func demoLatest(i, tick int) string {
	if demoFrozen() {
		tick = 0
	}
	switch (tick/2 + i) % 3 {
	case 0:
		return "" // running, no terminal signal → "working" (green)
	case 1:
		return "waiting-for-input" // → "needs you" (amber)
	default:
		return "task-complete" // running + done → "idle" (grey)
	}
}

func demoAgent(id, label, typ string, i, tick int, ageH time.Duration) api.AgentInfo {
	return api.AgentInfo{
		ID:         id,
		Label:      label,
		Type:       typ,
		State:      "running",
		Attachable: typ != "headless",
		CreatedAt:  time.Now().Add(-ageH),
		AgentState: &api.AgentStateInfo{Latest: demoLatest(i, tick), UpdatedAt: time.Now()},
	}
}

// demoIslands is the synthetic fleet.
//
// THREE UNRELATED PROJECTS, NOT ONE COMPANY'S MICROSERVICES, and none of them
// named after a repo the operator actually owns -- a demo fleet must be
// invented, not borrowed. The first version
// was storefront / api-gateway / infra / docs-site, all under github.com/acme,
// with two islands sharing a repo. That reads as one deployment split four
// ways, which undersells the thing being shown: people run Dejima across the
// unrelated projects they happen to own, a game beside a payments backend
// beside a phone app.
//
// AGENTS ARE NAMED BY ROLE, because that is how a fleet is actually driven —
// an orchestrator plus workers on their own worktrees, which is the pattern
// this product exists to make survivable. "manager / level-designer / campaign"
// says what the island is doing; "a1 / a2 / a3" says only that there are three
// of something.
func demoIslands(tick int) []api.IslandInfo {
	stat := func(memGB float64, cpu float64) *api.IslandStats {
		return &api.IslandStats{
			MemoryUsageBytes: uint64(memGB * 1024 * 1024 * 1024),
			MemoryLimitBytes: 8 * 1024 * 1024 * 1024,
			CPUPercent:       cpu,
		}
	}
	// CPU jitters with the tick so the stats line isn't frozen.
	jit := float64((tick*7)%23) + 12

	forge := api.IslandInfo{
		Name: "pixelforge", Repo: "github.com/you/pixelforge", Agent: "claude-code",
		State: "running", Container: "running", Stats: stat(3.1, jit),
		Agents: []api.AgentInfo{
			demoAgent("a1", "manager", "claude-code", 0, tick, 24*time.Hour),
			demoAgent("a2", "level-design", "codex", 1, tick, 18*time.Hour),
			demoAgent("a3", "encounters", "claude-code", 2, tick, 7*time.Hour),
			demoAgent("a4", "balance", "headless", 1, tick, 40*time.Minute),
		},
	}
	nimbus := api.IslandInfo{
		Name: "nimbus-api", Repo: "github.com/you/nimbus-api", Agent: "claude-code",
		State: "running", Container: "running", Stats: stat(2.2, jit*0.7+5),
		Agents: []api.AgentInfo{
			demoAgent("a1", "manager", "claude-code", 2, tick, 3*time.Hour),
			demoAgent("a2", "migrations", "codex", 0, tick, 25*time.Minute),
			demoAgent("a3", "load-test", "headless", 1, tick, 2*time.Hour),
			demoAgent("a4", "security-scan", "headless", 2, tick, 6*time.Hour),
		},
	}
	// Hibernated, and deliberately so: stop-and-keep is a real state of the
	// product and the only one that shows what an idle island costs (nothing).
	// Three islands was the brief, so this is the third rather than a fourth —
	// it carries two agents so the state reads as "parked", not "empty".
	harbor := api.IslandInfo{
		Name: "harbor-ios", Repo: "github.com/you/harbor-ios", Agent: "codex",
		State: "hibernated", Container: "exited",
		Agents: []api.AgentInfo{
			{ID: "c1", Label: "core", Type: "codex", State: "stopped"},
			{ID: "c2", Label: "ui", Type: "claude-code", State: "stopped"},
		},
	}
	// Surface the island-level "needs you" flag when its first agent is waiting,
	// so the row glyph matches the agent state (mirrors the real daemon).
	for _, isl := range []*api.IslandInfo{&forge, &nimbus} {
		if len(isl.Agents) > 0 && isl.Agents[0].AgentState != nil {
			isl.AgentState = isl.Agents[0].AgentState
		}
	}
	return []api.IslandInfo{forge, nimbus, harbor}
}

func demoIsland(name string, tick int) (*api.IslandInfo, bool) {
	for _, isl := range demoIslands(tick) {
		if isl.Name == name {
			c := isl
			return &c, true
		}
	}
	return nil, false
}

func demoOverview(tick int) *api.OverviewResponse {
	isls := demoIslands(tick)
	o := &api.OverviewResponse{TotalIslands: len(isls), DockerReachable: true, IslandImagePresent: true}
	for _, isl := range isls {
		switch isl.Container {
		case "running":
			o.Running++
			if isl.Stats != nil {
				o.MemoryUsageBytes += isl.Stats.MemoryUsageBytes
			}
		default:
			o.Hibernated++
		}
	}
	o.MemoryLimitBytes = 8 * 1024 * 1024 * 1024
	o.CPUPercent = float64((tick*7)%23) + 18
	return o
}

// demoPending stages the action-gate scene (B2): a benign-ish mutating request
// and a DESTRUCTIVE one, so the badge goes red and the destructive row is the
// money shot. Stable across ticks so the recording can dwell on it.
func demoPending() []link.ActionRequest {
	now := time.Now()
	return []link.ActionRequest{
		{ID: "act-7f3", From: "storefront", FromAgent: "a1", To: "api-gateway", ToAgent: "a1",
			Topic: "deploys", Action: "dispatch-task", Tier: link.TierMutating,
			Params: `{"task":"run integration suite"}`, CreatedAt: now.Add(-40 * time.Second)},
		{ID: "act-b91", From: "storefront", FromAgent: "a2", To: "infra", ToAgent: "c1",
			Topic: "ops", Action: "drop-database", Tier: link.TierDestructive,
			Params: `{"database":"orders_staging"}`, CreatedAt: now.Add(-12 * time.Second)},
	}
}

func demoPolicy() []policy.Rule {
	return []policy.Rule{
		{From: "storefront", To: "api-gateway", Action: "dispatch-task", MaxCount: 50, Used: 6,
			ExpiresAt: time.Now().Add(38 * time.Minute), CreatedAt: time.Now().Add(-20 * time.Minute), CreatedBy: "operator"},
	}
}

// demoSecrets is the synthetic per-island secret set for the site recording of
// the secrets pane — plausible names, no real values, fixed fingerprints so the
// clip is reproducible. See tui_secrets.go's demo branches.
func demoSecrets(island string) []secrets.Meta {
	base := time.Now().Add(-14 * 24 * time.Hour)
	return []secrets.Meta{
		{Name: "EXPO_TOKEN", CreatedAt: base, UpdatedAt: base.Add(9 * 24 * time.Hour), Fingerprint: "4f2a91c8"},
		{Name: "NPM_TOKEN", CreatedAt: base.Add(2 * 24 * time.Hour), UpdatedAt: base.Add(2 * 24 * time.Hour), Fingerprint: "b7e0d613"},
	}
}

// demoRepos is the synthetic repo list for the site recording of the guided
// first-island flow — no real filesystem scan, so no real repo names leak.
func demoRepos() []reposrc.Repo {
	return []reposrc.Repo{
		{Name: "pixelforge", Path: "/home/you/code/pixelforge"},
		{Name: "nimbus-api", Path: "/home/you/code/nimbus-api"},
		{Name: "harbor-ios", Path: "/home/you/code/harbor-ios"},
	}
}

// demoProfiles is the synthetic connection list for the switcher scene. Real
// profiles are the operator's host names and tailnet addresses; a site recording
// must never carry them, which is the same rule the rest of this file follows.
func demoProfiles() []clientcfg.Profile {
	return []clientcfg.Profile{
		{Name: "local", Host: ""},
		{Name: "minion", Host: "minion.tail-scale.ts.net:7273"},
		{Name: "studio", Host: "studio.tail-scale.ts.net:7273"},
	}
}
