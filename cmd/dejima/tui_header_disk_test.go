package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/aoos/dejima/internal/api"
)

// Storage on the header's server line. It earns the row on the rule the header
// already states — a property of the server that changes what you do next —
// and it was previously reachable only by knowing to press `%`.
//
// Each test renders a real header from a model in a stated state rather than
// asserting on diskHeaderNotes alone, because the way this regresses is the
// helper staying correct while nothing calls it.

const gib = 1 << 30

func withDisk(t *testing.T, used, free int64) tuiModel {
	t.Helper()
	m := seededModel(t, island("alpha"))
	m.overview = &api.OverviewResponse{DiskUsedBytes: used, DiskFreeBytes: free}
	return m
}

func TestHeaderShowsUsedAndFreeStorage(t *testing.T) {
	out := plain(withDisk(t, 42*gib, 310*gib).renderHeader())
	if !strings.Contains(out, "disk 42.0 GiB") {
		t.Errorf("header should name what the islands occupy:\n%s", out)
	}
	if !strings.Contains(out, "310.0 GiB free") {
		t.Errorf("header should name the room left, which is the half that "+
			"changes a decision:\n%s", out)
	}
}

// Zero free means UNDETERMINABLE — no island was running to df through — and
// must never render as though the disk were full.
func TestHeaderNeverPresentsUnknownFreeSpaceAsNoSpace(t *testing.T) {
	out := plain(withDisk(t, 42*gib, 0).renderHeader())
	if !strings.Contains(out, "disk 42.0 GiB") {
		t.Errorf("usage is known and should still show:\n%s", out)
	}
	if strings.Contains(out, "free") {
		t.Errorf("header claimed something about free space it does not know:\n%s", out)
	}
}

// Nothing known says nothing. A standing "disk 0 B" is noise on every redraw.
func TestHeaderIsSilentWhenStorageIsUnknown(t *testing.T) {
	if out := plain(seededModel(t, island("alpha")).renderHeader()); strings.Contains(out, "disk") {
		t.Errorf("no overview yet — header should say nothing about storage:\n%s", out)
	}
	if out := plain(withDisk(t, 0, 0).renderHeader()); strings.Contains(out, "disk") {
		t.Errorf("all zeroes should be silent, not 'disk 0 B':\n%s", out)
	}
}

// The short form keeps FREE and drops the usage breakdown, not the other way
// round: what Dejima occupies is trivia next to whether the next island fits.
func TestHeaderStorageShortFormKeepsTheFreeFigure(t *testing.T) {
	m := withDisk(t, 42*gib, 310*gib)
	m.width = 99 // the narrow end of full-header mode: infoW is 69 cols
	m.activeSource = "env"

	out := plain(m.renderHeader())
	if !strings.Contains(out, "310.0 GiB free") {
		t.Errorf("the free figure is the one worth the columns:\n%s", out)
	}
	if strings.Contains(out, "disk 42.0 GiB") {
		t.Errorf("at 99 cols the full note does not fit; expected the short "+
			"form, got the long one:\n%s", out)
	}
}

// The guard that matters for a line with a column budget: the info block is
// rendered with MaxWidth, which TRUNCATES rather than wraps, so an over-long
// server line silently loses its tail — and the tail is exactly where the new
// note sits. A truncated header is also a wrapped header one column later,
// which desyncs the body's row math.
//
// Every width in full-header range, with every optional piece present at once.
func TestHeaderNeverOutgrowsItsWidth(t *testing.T) {
	for w := 99; w <= 200; w++ {
		m := withDisk(t, 42*gib, 310*gib)
		m.width = w
		m.activeHost = "mac-mini.taila1b2c3.ts.net:7273"
		m.activeSource = "env"
		m.localModels = []string{"qwen-coder-7b", "llama3.1-8b"}
		for _, ln := range strings.Split(m.renderHeader(), "\n") {
			if got := lipgloss.Width(ln); got > w {
				t.Fatalf("at width %d a header line is %d cols and would wrap "+
					"or be clipped: %q", w, got, plain(ln))
			}
		}
	}
}

// Free space with no usage figure still answers the useful question, so it is
// reported rather than withheld for being half an answer.
func TestHeaderShowsFreeSpaceEvenWithoutAUsageFigure(t *testing.T) {
	out := plain(withDisk(t, 0, 310*gib).renderHeader())
	if !strings.Contains(out, "310.0 GiB free") {
		t.Errorf("free space alone is still worth saying:\n%s", out)
	}
}
