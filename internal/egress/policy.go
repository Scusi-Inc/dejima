package egress

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Mode is an island's egress posture.
type Mode string

const (
	// ModeObserve (the default, incl. the zero value) allows all egress and just
	// records it — the Phase 1 behavior. A Deny list still blocks specific hosts.
	ModeObserve Mode = "observe"
	// ModeEnforce is deny-all: only hosts on the Allow list are permitted. This is
	// the containment posture (mirrors Port/MCP deny-all + operator grants), but
	// opt-in per island because an island legitimately needs some egress.
	ModeEnforce Mode = "enforce"
)

// IslandPolicy is one island's egress rules. Host matching is by exact hostname
// OR domain suffix — a rule "github.com" also covers "api.github.com" — so an
// operator allows a service with one entry, not one per subdomain/CDN node.
type IslandPolicy struct {
	Mode  Mode     `json:"mode"`            // "" == observe
	Allow []string `json:"allow,omitempty"` // permitted hosts (used in enforce mode)
	Deny  []string `json:"deny,omitempty"`  // always-blocked hosts (even in observe mode)
}

// PolicyPatch is an incremental change applied to an island's policy (the body
// of PATCH .../egress/policy). Empty Mode leaves the mode unchanged; the add/
// remove lists are applied as sets (idempotent — re-adding is a no-op).
type PolicyPatch struct {
	Mode        Mode     `json:"mode,omitempty"`
	AddAllow    []string `json:"add_allow,omitempty"`
	RemoveAllow []string `json:"remove_allow,omitempty"`
	AddDeny     []string `json:"add_deny,omitempty"`
	RemoveDeny  []string `json:"remove_deny,omitempty"`
}

// PolicyStore is a persistent, per-island egress policy. It implements Policy,
// so the proxy consults it per connection; the daemon's API mutates it. With no
// rules for an island it reports observe/allow-all, so enabling Phase 2 changes
// nothing until an operator sets a policy.
type PolicyStore struct {
	mu       sync.RWMutex
	path     string
	byIsland map[string]IslandPolicy
}

// OpenPolicy loads the persisted egress policy from path (missing/corrupt →
// empty, i.e. observe-all for every island). Mutations persist back to path so
// policy survives a daemon restart.
func OpenPolicy(path string) *PolicyStore {
	s := &PolicyStore{path: path, byIsland: map[string]IslandPolicy{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.byIsland) // corrupt → start empty (best-effort)
		if s.byIsland == nil {
			s.byIsland = map[string]IslandPolicy{}
		}
	}
	return s
}

// Allow implements Policy: deny-list wins always; in enforce mode only allow-
// listed hosts pass; observe (and the zero value) permits everything.
func (s *PolicyStore) Allow(island, host string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := s.byIsland[island]
	if hostMatchesAny(host, p.Deny) {
		return false
	}
	if p.Mode == ModeEnforce {
		return hostMatchesAny(host, p.Allow)
	}
	return true
}

// Get returns a copy of an island's policy (zero value = observe, no rules).
func (s *PolicyStore) Get(island string) IslandPolicy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := s.byIsland[island]
	return IslandPolicy{Mode: p.Mode, Allow: append([]string(nil), p.Allow...), Deny: append([]string(nil), p.Deny...)}
}

// Apply mutates an island's policy with the patch and persists, returning the
// resulting policy. An unknown Mode (not observe/enforce/"") is rejected.
func (s *PolicyStore) Apply(island string, patch PolicyPatch) (IslandPolicy, error) {
	if patch.Mode != "" && patch.Mode != ModeObserve && patch.Mode != ModeEnforce {
		return IslandPolicy{}, &PolicyError{"unknown mode " + string(patch.Mode) + " (want observe|enforce)"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.byIsland[island]
	if patch.Mode != "" {
		p.Mode = patch.Mode
	}
	p.Allow = applySet(p.Allow, patch.AddAllow, patch.RemoveAllow)
	p.Deny = applySet(p.Deny, patch.AddDeny, patch.RemoveDeny)
	s.byIsland[island] = p
	s.saveLocked()
	return IslandPolicy{Mode: p.Mode, Allow: append([]string(nil), p.Allow...), Deny: append([]string(nil), p.Deny...)}, nil
}

// PolicyError is a validation failure from Apply.
type PolicyError struct{ msg string }

func (e *PolicyError) Error() string { return e.msg }

func (s *PolicyStore) saveLocked() {
	if s.path == "" {
		return
	}
	b, err := json.MarshalIndent(s.byIsland, "", "  ")
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".egress-policy-*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	_ = tmp.Chmod(0o600)
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmpName, s.path)
}

// applySet adds/removes hosts from a list as a normalized, deduped, sorted set.
func applySet(cur, add, remove []string) []string {
	set := map[string]struct{}{}
	for _, h := range cur {
		if n := normalizeHost(h); n != "" {
			set[n] = struct{}{}
		}
	}
	for _, h := range add {
		if n := normalizeHost(h); n != "" {
			set[n] = struct{}{}
		}
	}
	for _, h := range remove {
		delete(set, normalizeHost(h))
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

func normalizeHost(h string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
}

// hostMatchesAny reports whether host equals or is a subdomain of any rule.
func hostMatchesAny(host string, rules []string) bool {
	h := normalizeHost(host)
	if h == "" {
		return false
	}
	for _, r := range rules {
		r = normalizeHost(r)
		if r == "" {
			continue
		}
		if h == r || strings.HasSuffix(h, "."+r) {
			return true
		}
	}
	return false
}

// PolicyStore satisfies Policy.
var _ Policy = (*PolicyStore)(nil)
