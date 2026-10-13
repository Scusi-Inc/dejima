package main

import (
	"reflect"
	"testing"
)

// The classifier decides what the check offers to DELETE, so its table is
// tested directly rather than through a docker shell-out.
func TestClassifySnapshotVolumes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		volumes     []string
		wantOrphans []string
		wantLive    int
	}{{
		name: "snapshots of a purged island are orphans",
		// No dejima-gone-home: the island was purged, so these are unreachable.
		volumes:     []string{"dejima-gone-home-snap-1700000000", "dejima-gone-home-snap-1700000100"},
		wantOrphans: []string{"dejima-gone-home-snap-1700000000", "dejima-gone-home-snap-1700000100"},
	}, {
		name:     "snapshots of a live island are kept",
		volumes:  []string{"dejima-live-home", "dejima-live-workspace", "dejima-live-home-snap-1700000000"},
		wantLive: 1,
	}, {
		// The case that makes LastIndex load-bearing. Splitting on the first
		// marker yields island "oc", whose home volume is absent — so a live
		// island's only backups would be offered for deletion.
		name:     "an island whose name ends in -home is not misparsed",
		volumes:  []string{"dejima-oc-home-home", "dejima-oc-home-home-snap-1700000000"},
		wantLive: 1,
	}, {
		name: "live and purged islands are told apart in one pass",
		volumes: []string{
			"dejima-live-home", "dejima-live-home-snap-1700000000",
			"dejima-gone-home-snap-1700000200",
		},
		wantOrphans: []string{"dejima-gone-home-snap-1700000200"},
		wantLive:    1,
	}, {
		name:    "unrelated volumes are ignored",
		volumes: []string{"postgres-data", "dejima-live-home", "", "  "},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			orphans, live := classifySnapshotVolumes(tc.volumes)
			if !reflect.DeepEqual(orphans, tc.wantOrphans) {
				t.Errorf("orphans = %v, want %v", orphans, tc.wantOrphans)
			}
			if live != tc.wantLive {
				t.Errorf("live = %d, want %d", live, tc.wantLive)
			}
		})
	}
}

func TestIslandFromSnapshotVolume(t *testing.T) {
	for _, tc := range []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"dejima-isl-home-snap-1700000000", "isl", true},
		{"dejima-oc-home-home-snap-1700000000", "oc-home", true},
		{"dejima-isl-home", "", false},     // the home volume itself, not a snapshot
		{"postgres-data", "", false},       // not ours
		{"dejima--home-snap-1", "", false}, // empty island name
	} {
		got, ok := islandFromSnapshotVolume(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("islandFromSnapshotVolume(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}
