package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// Substrate storage on the overview/aggregate endpoints. Two things are being
// held down here, and they fail in opposite directions:
//
//   - the USED figure must count Dejima's volumes and nothing else, because
//     `docker system df -v` reports every volume on the engine;
//   - the FREE figure must come from inside a running island, and must stay
//     ABSENT rather than read 0 when there is no island to measure through.
//
// Both are rendered on the header's server line, where a wrong number is read
// on every redraw and acted on.

// df output in the shape `df -kP /workspace` actually produces: a header row
// and one POSIX-formatted row. 61202244 1K-blocks ≈ 58.4 GiB, 45678901
// available ≈ 43.6 GiB.
const dfPosixSample = `Filesystem     1024-blocks     Used Available Capacity Mounted on
/dev/vda1         61202244 12345678  45678901      22% /workspace
`

func dfHook(out string) func([]string) (string, string, int, bool) {
	return func(cmd []string) (string, string, int, bool) {
		if len(cmd) > 0 && cmd[0] == "df" {
			return out, "", 0, true
		}
		return "", "", 0, false
	}
}

func getOverview(t *testing.T, h http.Handler) OverviewResponse {
	t.Helper()
	rr := do(t, h, http.MethodGet, "/v1/overview", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("overview: %d %s", rr.Code, rr.Body.String())
	}
	var out OverviewResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The attribution fix. An operator running anything else in Docker had its
// volumes counted as Dejima's — and the figure's own name says "across all
// islands", so there was nothing in the number to suggest otherwise.
func TestOverviewStorageCountsOnlyDejimaVolumes(t *testing.T) {
	h, f := newTestServer(t)
	p := &project.Project{Name: "isl", DesiredState: project.StateRunning}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	f.volumeSizes = map[string]int64{
		p.WorkspaceVolume(): 5 << 30,
		p.HomeVolume():      3 << 30,
		"pg_data":           40 << 30, // somebody else's Postgres
		"my-compose-stack":  12 << 30,
	}
	f.execHook = dfHook(dfPosixSample)

	got := getOverview(t, h).DiskUsedBytes
	if want := int64(8 << 30); got != want {
		t.Errorf("DiskUsedBytes = %d (%.1f GiB), want %d (8 GiB) — non-dejima "+
			"volumes are being billed to us", got, float64(got)/(1<<30), want)
	}
}

// Leaked volumes stay counted. A purge that half-finished leaves
// dejima-<name>-workspace behind with no project to look it up from, and those
// bytes are still occupied — so attribution is by prefix, not by walking the
// live projects.
func TestOverviewStorageKeepsVolumesLeftBehindByAPurge(t *testing.T) {
	h, f := newTestServer(t)
	p := &project.Project{Name: "isl", DesiredState: project.StateRunning}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	f.volumeSizes = map[string]int64{
		p.WorkspaceVolume():          2 << 30,
		"dejima-gone-away-workspace": 6 << 30, // island deleted, volume wasn't
		"dejima-gone-away-home":      1 << 30,
	}
	f.execHook = dfHook(dfPosixSample)

	if got, want := getOverview(t, h).DiskUsedBytes, int64(9<<30); got != want {
		t.Errorf("DiskUsedBytes = %d, want %d — orphaned island volumes still "+
			"occupy the disk and must show up somewhere", got, want)
	}
}

// Free space is measured from inside an island, because the daemon's own
// filesystem is not the engine's on macOS.
func TestOverviewFreeSpaceComesFromInsideARunningIsland(t *testing.T) {
	h, f := newTestServer(t)
	p := &project.Project{Name: "isl", DesiredState: project.StateRunning}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	f.volumeSizes = map[string]int64{p.WorkspaceVolume(): 1 << 30}

	var sawCmd []string
	f.execHook = func(cmd []string) (string, string, int, bool) {
		if len(cmd) > 0 && cmd[0] == "df" {
			sawCmd = cmd
			return dfPosixSample, "", 0, true
		}
		return "", "", 0, false
	}

	out := getOverview(t, h)
	if want := int64(61202244) * 1024; out.DiskCapacityBytes != want {
		t.Errorf("DiskCapacityBytes = %d, want %d", out.DiskCapacityBytes, want)
	}
	if want := int64(45678901) * 1024; out.DiskFreeBytes != want {
		t.Errorf("DiskFreeBytes = %d, want %d", out.DiskFreeBytes, want)
	}
	// -P is what keeps a long device name from wrapping the row and shifting
	// every field left by one; see parseDFPosix.
	if !strings.Contains(strings.Join(sawCmd, " "), "-kP") {
		t.Errorf("df was called as %v, want -kP — without -P a wrapped row "+
			"parses the use percentage as available space", sawCmd)
	}
	// And it must have been measured in /workspace, which IS the volume. df on
	// the container's / would report the image layer, not the island's disk.
	if len(sawCmd) == 0 || sawCmd[len(sawCmd)-1] != "/workspace" {
		t.Errorf("df target = %v, want /workspace", sawCmd)
	}
}

// Nothing running: used-only, and free stays ABSENT. A 0 rendered as "0 B free"
// on a half-empty disk is the kind of wrong an operator acts on.
func TestOverviewReportsUsedOnlyWhenNothingIsRunning(t *testing.T) {
	h, f := newTestServer(t)
	f.status = runtime.StatusExited
	p := &project.Project{Name: "isl", DesiredState: project.StateHibernated}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	f.volumeSizes = map[string]int64{p.WorkspaceVolume(): 4 << 30}
	f.execHook = dfHook(dfPosixSample) // would answer, if anything asked

	out := getOverview(t, h)
	if out.DiskUsedBytes != 4<<30 {
		t.Errorf("DiskUsedBytes = %d, want 4 GiB — usage needs no running island", out.DiskUsedBytes)
	}
	if out.DiskFreeBytes != 0 || out.DiskCapacityBytes != 0 {
		t.Errorf("free/capacity = %d/%d, want 0/0 — there was no island to df through",
			out.DiskFreeBytes, out.DiskCapacityBytes)
	}
}

// The aggregate panel and the header must not disagree: same attribution.
func TestAggregateStorageUsesTheSameAttributionAsOverview(t *testing.T) {
	h, f := newTestServer(t)
	p := &project.Project{Name: "isl", DesiredState: project.StateRunning}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	f.volumeSizes = map[string]int64{
		p.WorkspaceVolume(): 7 << 30,
		"pg_data":           40 << 30,
	}
	f.execHook = dfHook(dfPosixSample)

	rr := do(t, h, http.MethodGet, "/v1/aggregate", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("aggregate: %d %s", rr.Code, rr.Body.String())
	}
	var agg AggregateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &agg); err != nil {
		t.Fatal(err)
	}
	if agg.DiskTotalBytes != 7<<30 {
		t.Errorf("aggregate DiskTotalBytes = %d, want 7 GiB (not 47)", agg.DiskTotalBytes)
	}
	if agg.DiskFreeBytes != int64(45678901)*1024 {
		t.Errorf("aggregate DiskFreeBytes = %d, want the df figure", agg.DiskFreeBytes)
	}
}

// parseDFPosix fails CLOSED. Each of these shapes would otherwise produce a
// confident wrong number rather than an error.
func TestParseDFPosixRejectsWhatItCannotTrust(t *testing.T) {
	// The wrapped form, which is what df emits WITHOUT -P when the device name
	// is long: every field shifts left by one, so "Available" would be read
	// from the use percentage. Five fields, so the six-field floor rejects it.
	wrapped := "Filesystem 1024-blocks Used Available Capacity Mounted on\n" +
		"/dev/mapper/a-very-long-logical-volume-name\n" +
		"           61202244 12345678 45678901 22% /workspace\n"
	for name, in := range map[string]string{
		"wrapped row":   wrapped,
		"header only":   "Filesystem 1024-blocks Used Available Capacity Mounted on\n",
		"empty":         "",
		"non-numeric":   "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/vda1 - - - - /workspace\n",
		"zero capacity": "Filesystem 1024-blocks Used Available Capacity Mounted on\ntmpfs 0 0 0 0% /workspace\n",
		"error from df": "df: /workspace: No such file or directory\n",
	} {
		if got := parseDFPosix(in); got.CapacityBytes != 0 || got.FreeBytes != 0 {
			t.Errorf("%s: parsed as %+v, want zero — an unparseable row must read "+
				"as unknown, not as a number", name, got)
		}
	}
}

func TestParseDFPosixReadsTheRow(t *testing.T) {
	got := parseDFPosix(dfPosixSample)
	if got.CapacityBytes != int64(61202244)*1024 || got.FreeBytes != int64(45678901)*1024 {
		t.Errorf("parsed %+v, want capacity=%d free=%d",
			got, int64(61202244)*1024, int64(45678901)*1024)
	}
}
