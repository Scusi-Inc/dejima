package link

import (
	"testing"
	"time"
)

func TestQueueAddTakeList(t *testing.T) {
	q := NewQueue(time.Hour)
	r := q.Add(ActionRequest{From: "a", To: "b", ToAgent: "b1", Topic: "t", Action: "deploy"})
	if r.ID == "" || r.CreatedAt.IsZero() {
		t.Fatalf("Add did not stamp ID/CreatedAt: %+v", r)
	}
	if got := q.List(); len(got) != 1 {
		t.Fatalf("List = %d, want 1", len(got))
	}
	taken, ok := q.Take(r.ID)
	if !ok || taken.Action != "deploy" {
		t.Fatalf("Take = %+v ok=%v", taken, ok)
	}
	// Taken requests are gone (one-shot) and a second Take fails.
	if _, ok := q.Take(r.ID); ok {
		t.Error("second Take should fail")
	}
	if got := q.List(); len(got) != 0 {
		t.Errorf("List after Take = %d, want 0", len(got))
	}
}

// TestQueueExpiryFailClosed: a request past its TTL is never actionable (Take and
// List both drop it) — the fail-closed guarantee that also covers a daemon
// restart (in-memory queue starts empty).
func TestQueueExpiryFailClosed(t *testing.T) {
	q := NewQueue(time.Minute)
	now := time.Now()
	q.now = func() time.Time { return now }
	r := q.Add(ActionRequest{From: "a", To: "b", ToAgent: "b1", Topic: "t", Action: "deploy"})

	now = now.Add(2 * time.Minute) // advance past the TTL
	if _, ok := q.Take(r.ID); ok {
		t.Error("Take of an expired request must fail (fail-closed)")
	}
	if got := q.List(); len(got) != 0 {
		t.Errorf("List must drop expired requests; got %d", len(got))
	}
}
