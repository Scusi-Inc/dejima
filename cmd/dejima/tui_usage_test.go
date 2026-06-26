package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

func TestMemUsagePct(t *testing.T) {
	if _, ok := memUsagePct(nil); ok {
		t.Error("nil stats should not yield a percent")
	}
	if _, ok := memUsagePct(&api.IslandStats{MemoryUsageBytes: 100}); ok {
		t.Error("zero limit should not yield a percent")
	}
	if p, ok := memUsagePct(&api.IslandStats{MemoryUsageBytes: 50, MemoryLimitBytes: 100}); !ok || p != 50 {
		t.Errorf("memUsagePct = %v,%v want 50,true", p, ok)
	}
}

func TestParseCapBytes(t *testing.T) {
	cases := map[string]uint64{"20G": 20 << 30, "512m": 512 << 20, "2g": 2 << 30, "1024K": 1 << 20}
	for in, want := range cases {
		if got, ok := parseCapBytes(in); !ok || got != want {
			t.Errorf("parseCapBytes(%q) = %d,%v want %d,true", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "unlimited", "G", "abc"} {
		if _, ok := parseCapBytes(bad); ok {
			t.Errorf("parseCapBytes(%q) should be unparseable", bad)
		}
	}
}

func TestNearCapStyle(t *testing.T) {
	if _, flag := nearCapStyle(74); flag {
		t.Error("74%% should not flag")
	}
	if _, flag := nearCapStyle(75); !flag {
		t.Error("75%% should flag (amber)")
	}
	if _, flag := nearCapStyle(95); !flag {
		t.Error("95%% should flag (red)")
	}
}

// TestShortStatusNearCapFlag: the island row flags memory pressure only when
// near the cap, so a healthy fleet stays quiet (a3 usage #1).
func TestShortStatusNearCapFlag(t *testing.T) {
	hot := api.IslandInfo{Container: "running", Stats: &api.IslandStats{MemoryUsageBytes: 39, MemoryLimitBytes: 40, CPUPercent: 50}}
	if !strings.Contains(plain(shortStatus(hot, "")), "mem 9") || !strings.Contains(plain(shortStatus(hot, "")), "⚠") {
		t.Errorf("near-cap island should flag memory: %q", plain(shortStatus(hot, "")))
	}
	cool := api.IslandInfo{Container: "running", Stats: &api.IslandStats{MemoryUsageBytes: 1, MemoryLimitBytes: 40, CPUPercent: 5}}
	if strings.Contains(plain(shortStatus(cool, "")), "⚠") {
		t.Errorf("healthy island should NOT flag: %q", plain(shortStatus(cool, "")))
	}
	// No stats (e.g. list payload without usage yet) → graceful, just the state.
	if got := plain(shortStatus(api.IslandInfo{Container: "running"}, "")); got != "running" {
		t.Errorf("no-stats island = %q, want just 'running'", got)
	}
}
