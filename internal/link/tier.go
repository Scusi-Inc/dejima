package link

import "strings"

// Tier is the risk class of a cross-island action, the basis of the action
// gate's safety posture (docs/action-gate-spec.md §"Risk tiers"):
//
//   - TierBenign     — read/peek/status; safe to auto-approve by policy.
//   - TierMutating   — write/dispatch/build; gated (policy or human).
//   - TierDestructive— delete/purge/irreversible; ALWAYS explicit human approval,
//     never silently auto-approvable. This is the wipe-the-drive backstop.
type Tier string

const (
	TierBenign      Tier = "benign"
	TierMutating    Tier = "mutating"
	TierDestructive Tier = "destructive"
)

// AutoApprovable reports whether a policy rule may ever auto-approve this tier.
// Destructive never can (until a deliberately-deferred future opt-in escalation),
// so this is the single chokepoint enforcing that non-negotiable.
func (t Tier) AutoApprovable() bool { return t != TierDestructive }

// destructiveHints / benignHints classify an action by its NAME. Destructive
// wins ties: an action whose name suggests both (e.g. "read-and-delete") is
// treated as destructive — fail toward the safe (gated) side. Anything that
// matches neither defaults to mutating (gated), so an unrecognized action is
// never mistaken for safe.
var destructiveHints = []string{
	"delete", "destroy", "purge", "wipe", "remove", "rm", "drop",
	"erase", "format", "reset", "teardown", "uninstall", "revoke", "kill",
}

var benignHints = []string{
	"read", "get", "list", "ls", "status", "stat", "peek", "show",
	"view", "fetch", "describe", "inspect", "ping", "health", "info", "query",
}

// ClassifyTier maps an action name to a risk tier, conservatively. It is a
// heuristic floor (a destination may later declare tiers explicitly); the safety
// model does not rest on it alone — the default posture is prompt-everything, so
// nothing auto-approves without an operator rule, and destructive is excluded
// from rules regardless.
func ClassifyTier(action string) Tier {
	a := strings.ToLower(strings.TrimSpace(action))
	if containsHint(a, destructiveHints) {
		return TierDestructive
	}
	if containsHint(a, benignHints) {
		return TierBenign
	}
	return TierMutating
}

// containsHint reports whether any hint appears as a token in a (split on
// non-alphanumerics), so "delete-file" matches "delete" but "undeletable" does
// not match "delete" as a stray substring.
func containsHint(a string, hints []string) bool {
	tokens := strings.FieldsFunc(a, func(r rune) bool {
		isAlnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		return !isAlnum // split on every non-alphanumeric rune
	})
	for _, tok := range tokens {
		for _, h := range hints {
			if tok == h {
				return true
			}
		}
	}
	return false
}
