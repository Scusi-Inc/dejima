package link

import "testing"

func TestClassifyTier(t *testing.T) {
	cases := map[string]Tier{
		"read-file":     TierBenign,
		"status":        TierBenign,
		"get_metrics":   TierBenign,
		"list-islands":  TierBenign,
		"dispatch-task": TierMutating, // unrecognized verb → gated, not benign
		"build":         TierMutating,
		"write-config":  TierMutating,
		"delete-island": TierDestructive,
		"purge":         TierDestructive,
		"wipe_volume":   TierDestructive,
		"reset-agent":   TierDestructive,
		"":              TierMutating, // empty → safe default (gated)
	}
	for action, want := range cases {
		if got := ClassifyTier(action); got != want {
			t.Errorf("ClassifyTier(%q) = %q, want %q", action, got, want)
		}
	}
}

// Destructive wins a tie, and a destructive substring that isn't a token must not
// trip the classifier ("undeletable" ≠ "delete").
func TestClassifyTier_TieAndTokenBoundary(t *testing.T) {
	if got := ClassifyTier("read-and-delete"); got != TierDestructive {
		t.Errorf("mixed read+delete should be destructive, got %q", got)
	}
	if got := ClassifyTier("undeletable-status"); got == TierDestructive {
		t.Errorf("'undeletable' must not match the 'delete' token; got destructive")
	}
}

// The destructive backstop: only destructive is non-auto-approvable.
func TestTierAutoApprovable(t *testing.T) {
	if !TierBenign.AutoApprovable() || !TierMutating.AutoApprovable() {
		t.Error("benign and mutating must be auto-approvable by policy")
	}
	if TierDestructive.AutoApprovable() {
		t.Error("destructive must NEVER be auto-approvable")
	}
}
