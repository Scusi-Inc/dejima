package runtime

import "testing"

// The volume-copy worker must be NAMEABLE and LABELLED.
//
// It ran as a bare `docker run --rm`, so Docker named it at random and Dejima —
// which finds containers as `dejima-<island>` — could never see it again. `--rm`
// looked like enough, but it fires on EXIT: a copy that hangs, or whose client
// dies with the daemon's context, leaves the container running and the flag
// unfired.
//
// An operator found exactly that: `peaceful_mclaren`, the island image, RUNNING,
// three weeks old, holding two volumes, invisible to every dejima surface and
// untouched by purge — which only removes containers it can name. They reported
// it as purge being incomplete; purge could not have known it existed.
func TestVolumeCopyWorkerIsNamedAfterItsDestination(t *testing.T) {
	got := volumeCopyContainer("dejima-alpha-workspace")
	if got != "dejima-copy-dejima-alpha-workspace" {
		t.Errorf("worker name = %q, want it derived from the destination volume", got)
	}
	// dejima-prefixed so it is legible in `docker ps` as ours, and so a
	// name-based sweep finds it alongside the island containers.
	if len(got) < len("dejima-") || got[:len("dejima-")] != "dejima-" {
		t.Errorf("worker name %q is not identifiable as dejima's", got)
	}
	// Stable across retries of the same copy — otherwise each attempt leaks a
	// differently-named container, which is the original bug with extra steps.
	if again := volumeCopyContainer("dejima-alpha-workspace"); again != got {
		t.Errorf("worker name is not stable: %q then %q", got, again)
	}
	// Distinct per destination, so two concurrent clones cannot collide.
	if other := volumeCopyContainer("dejima-beta-workspace"); other == got {
		t.Error("two different destinations produce the same worker name")
	}
}

// The label is what lets an orphan sweep identify dejima's workers by something
// better than a name convention someone can quietly change.
func TestVolumeCopyLabelIsAKeyValuePair(t *testing.T) {
	if VolumeCopyLabel == "" {
		t.Fatal("no label on the volume-copy worker")
	}
	// `docker run --label` takes key=value; a bare key would silently set an
	// empty value and match nothing a sweep filters on.
	found := false
	for i := 0; i < len(VolumeCopyLabel); i++ {
		if VolumeCopyLabel[i] == '=' {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("label %q is not key=value, so a --filter label=… sweep will not match it",
			VolumeCopyLabel)
	}
}
