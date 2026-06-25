package main

import (
	"time"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/link"
	"github.com/aoos/dejima/internal/policy"
)

// Demo mode (`dejima tui --demo`) drives the dashboard from a synthetic fleet
// instead of a live daemon, so the site recordings (#12 / d6's Track B) are
// reproducible, controllable, and leak no real repos/paths/secrets. The fetch
// commands short-circuit to these builders when m.demo is set; the fleet's agent
// states churn on the tick so the hero clip looks alive. Nothing here touches a
// network or a real island. See strategy/tui-capture-runbook.md for the scenes.

// demoLatest cycles an agent through working → needs-you → idle so the fleet
// animates. Offset by index so the agents aren't all in lock-step; /2 slows it
// to a readable cadence for a recording.
func demoLatest(i, tick int) string {
	switch (tick/2 + i) % 3 {
	case 0:
		return "" // running, no terminal signal → "working" (green)
	case 1:
		return "waiting-for-input" // → "needs you" (amber)
	default:
		return "task-complete" // running + done → "idle" (grey)
	}
}

func demoAgent(id, typ string, i, tick int, ageH time.Duration) api.AgentInfo {
	return api.AgentInfo{
		ID:         id,
		Type:       typ,
		State:      "running",
		Attachable: typ != "headless",
		CreatedAt:  time.Now().Add(-ageH),
		AgentState: &api.AgentStateInfo{Latest: demoLatest(i, tick), UpdatedAt: time.Now()},
	}
}

// demoIslands is the synthetic fleet: a multi-agent flagship, a couple of
// smaller islands, and one hibernated — across two repos, so group-by-repo also
// reads well.
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

	web := api.IslandInfo{
		Name: "storefront", Repo: "github.com/acme/storefront", Agent: "claude-code",
		State: "running", Container: "running", Stats: stat(3.1, jit),
		Agents: []api.AgentInfo{
			demoAgent("a1", "claude-code", 0, tick, 2*time.Hour),
			demoAgent("a2", "codex", 1, tick, 90*time.Minute),
			demoAgent("a3", "headless", 2, tick, 40*time.Minute),
		},
	}
	api2 := api.IslandInfo{
		Name: "api-gateway", Repo: "github.com/acme/storefront", Agent: "claude-code",
		State: "running", Container: "running", Stats: stat(2.2, jit*0.7+5),
		Agents: []api.AgentInfo{
			demoAgent("a1", "claude-code", 2, tick, 3*time.Hour),
			demoAgent("a2", "claude-code", 0, tick, 25*time.Minute),
		},
	}
	infra := api.IslandInfo{
		Name: "infra", Repo: "github.com/acme/infra", Agent: "codex",
		State: "running", Container: "running", Stats: stat(1.4, jit*0.4+3),
		Agents: []api.AgentInfo{
			demoAgent("c1", "codex", 1, tick, 5*time.Hour),
		},
	}
	docs := api.IslandInfo{
		Name: "docs-site", Repo: "github.com/acme/infra", Agent: "claude-code",
		State: "hibernated", Container: "exited",
		Agents: []api.AgentInfo{{ID: "a1", Type: "claude-code", State: "stopped"}},
	}
	// Surface the island-level "needs you" flag when its first agent is waiting,
	// so the row glyph matches the agent state (mirrors the real daemon).
	for i := range []*api.IslandInfo{&web, &api2, &infra} {
		isl := []*api.IslandInfo{&web, &api2, &infra}[i]
		if len(isl.Agents) > 0 && isl.Agents[0].AgentState != nil {
			isl.AgentState = isl.Agents[0].AgentState
		}
	}
	return []api.IslandInfo{web, api2, infra, docs}
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
