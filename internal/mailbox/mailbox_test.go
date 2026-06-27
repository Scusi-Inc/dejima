package mailbox

import (
	"path/filepath"
	"testing"
	"time"
)

// TestPersistenceSurvivesRestart is the no-lost-work guarantee: a message sent
// before a daemon restart is still there after, and the seq cursor is preserved
// (it does NOT reset to 1), so an agent's saved `since` keeps working. This is
// the exact failure a3 reported — a restart wiped the ring and reset seq, losing
// undelivered cross-session coordination.
func TestPersistenceSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailbox.json")

	// First daemon lifetime: an intra-island message and a cross-island delivery
	// (the kind that was actually lost).
	s1 := Open(path, 8, nil)
	m1 := s1.Send("isl", "a1", "a2", "", "hello a2")
	mx := s1.DeliverExternal("isl", "other", "j2", "frontend", "a2", "ops", "cross hi")
	if m1.Seq != 1 || mx.Seq != 2 {
		t.Fatalf("seq not as expected: %d, %d", m1.Seq, mx.Seq)
	}

	// Restart: a brand-new store from the same path (simulates daemon bounce).
	s2 := Open(path, 8, nil)

	got := s2.Poll("isl", "a2", 0)
	if len(got) != 2 {
		t.Fatalf("messages lost across restart: got %d, want 2", len(got))
	}
	if got[0].Payload != "hello a2" || got[1].Payload != "cross hi" {
		t.Errorf("payloads not restored: %+v", got)
	}
	// Cross-island provenance must survive too (it's what lets the recipient show
	// a sender name, and proves the full Message round-tripped).
	if got[1].Origin == nil || got[1].Origin.SourceIsland != "other" || got[1].Origin.FromLabel != "frontend" {
		t.Errorf("Origin not restored: %+v", got[1].Origin)
	}

	// Cursor preserved: the next send continues from seq 2, NOT reset to 1.
	m3 := s2.Send("isl", "a1", "a2", "", "after restart")
	if m3.Seq != 3 {
		t.Fatalf("seq reset across restart: next seq = %d, want 3", m3.Seq)
	}
	// An agent that had read up to mx.Seq still only sees the new message.
	after := s2.Poll("isl", "a2", mx.Seq)
	if len(after) != 1 || after[0].Payload != "after restart" {
		t.Fatalf("saved cursor broken across restart: %+v", after)
	}
}

// TestInMemoryStoreDoesNotPersist confirms NewStore (no path) writes nothing —
// the test/ephemeral form is unchanged.
func TestInMemoryStoreDoesNotPersist(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(8) // no path
	s.Send("isl", "a1", "a2", "", "ephemeral")
	if entries, _ := filepath.Glob(filepath.Join(dir, "*")); len(entries) != 0 {
		t.Errorf("in-memory store should not write files, found %v", entries)
	}
}

func TestSendStampsSeqAndTime(t *testing.T) {
	s := NewStore(8)
	fixed := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return fixed }

	m1 := s.Send("isl", "p1", "p2", "", "hi")
	m2 := s.Send("isl", "p2", "", "build", "done")
	if m1.Seq != 1 || m2.Seq != 2 {
		t.Fatalf("seq not monotonic: %d, %d", m1.Seq, m2.Seq)
	}
	if !m1.Time.Equal(fixed) {
		t.Errorf("time not stamped: %v", m1.Time)
	}
	if m2.To != "" || m2.Topic != "build" {
		t.Errorf("broadcast/topic wrong: to=%q topic=%q", m2.To, m2.Topic)
	}
}

func TestPollVisibilityAndCursor(t *testing.T) {
	s := NewStore(16)
	s.Send("isl", "p1", "p2", "", "to-p2")   // seq 1: addressed to p2
	s.Send("isl", "p1", "", "", "broadcast") // seq 2: broadcast
	s.Send("isl", "p3", "p1", "", "to-p1")   // seq 3: addressed to p1

	// p2 sees its direct message + the broadcast, not p1's.
	got := s.Poll("isl", "p2", 0)
	if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("p2 poll = %+v", got)
	}
	// Cursor: since=2 returns only seq 3, and only for p1.
	if g := s.Poll("isl", "p1", 2); len(g) != 1 || g[0].Seq != 3 {
		t.Errorf("p1 since=2 = %+v", g)
	}
	// An empty-agent (observer) view sees only broadcasts.
	if g := s.Poll("isl", "", 0); len(g) != 1 || g[0].Payload != "broadcast" {
		t.Errorf("observer view = %+v", g)
	}
	// Island isolation: another island sees nothing.
	if g := s.Poll("other", "p2", 0); len(g) != 0 {
		t.Errorf("cross-island leak: %+v", g)
	}
}

func TestRingEviction(t *testing.T) {
	s := NewStore(3)
	for i := 0; i < 5; i++ {
		s.Send("isl", "p1", "", "", "m")
	}
	got := s.Poll("isl", "p1", 0)
	if len(got) != 3 {
		t.Fatalf("ring not bounded: kept %d, want 3", len(got))
	}
	if got[0].Seq != 3 || got[2].Seq != 5 {
		t.Errorf("evicted wrong end: %+v", got)
	}
	if s.Latest("isl") != 5 {
		t.Errorf("Latest = %d, want 5", s.Latest("isl"))
	}
}

func TestDeliverStampsFromLabelOnOrigin(t *testing.T) {
	s := NewStore(8)

	// DeliverExternal stamps the sender's display label into Origin.
	m := s.DeliverExternal("dst", "src", "j2", "frontend", "a1", "ops", "hi")
	if m.Origin == nil || !m.Origin.CrossIsland {
		t.Fatal("cross-island Origin not set")
	}
	if m.Origin.FromLabel != "frontend" {
		t.Errorf("from_label = %q, want frontend", m.Origin.FromLabel)
	}
	if m.From != "j2" {
		t.Errorf("from id must remain the addressing handle, got %q", m.From)
	}

	// An empty label leaves from_label empty (consumer falls back to the id).
	m2 := s.DeliverExternal("dst", "src", "j2", "", "a1", "ops", "hi")
	if m2.Origin.FromLabel != "" {
		t.Errorf("unset label should be empty, got %q", m2.Origin.FromLabel)
	}

	// DeliverAction stamps it too.
	ma := s.DeliverAction("dst", "src", "j2", "frontend", "a1", "ops", "deploy", "")
	if ma.Origin == nil || ma.Origin.FromLabel != "frontend" {
		t.Errorf("action delivery from_label = %q, want frontend", ma.Origin.FromLabel)
	}
	if ma.Action == nil || ma.Action.Type != "deploy" {
		t.Error("action delivery should still carry the typed Action")
	}
}
