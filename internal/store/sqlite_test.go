package store

import (
	"testing"
	"time"
)

func openMem(t *testing.T) *SQLiteStore {
	t.Helper()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSubjectUpsertAndGet(t *testing.T) {
	st := openMem(t)
	now := time.Now().UTC().Truncate(time.Second)

	sub := &Subject{ID: "u1", Timezone: "America/New_York", UpdatedAt: now}
	if err := st.SubjectUpsert(sub); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := st.SubjectGet("u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected subject, got nil")
	}
	if got.Timezone != "America/New_York" {
		t.Errorf("timezone: got %q", got.Timezone)
	}

	// Update timezone.
	sub.Timezone = "Europe/London"
	if err := st.SubjectUpsert(sub); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	got, _ = st.SubjectGet("u1")
	if got.Timezone != "Europe/London" {
		t.Errorf("after update timezone: got %q", got.Timezone)
	}
}

func TestSubjectGet_NotFound(t *testing.T) {
	st := openMem(t)
	got, err := st.SubjectGet("missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestEventAppendAndCount(t *testing.T) {
	st := openMem(t)
	now := time.Now().UTC()

	for i := 0; i < 3; i++ {
		err := st.EventAppend("u1", &EventRecord{
			ID:         "e" + string(rune('0'+i)),
			SubjectID:  "u1",
			Priority:   "bulk",
			Decision:   "ACT_NOW",
			OccurredAt: now,
		})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	count, err := st.CountEvents("u1", "bulk", "1d")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Errorf("count: got %d, want 3", count)
	}

	// Different priority should return 0.
	count, _ = st.CountEvents("u1", "critical", "1d")
	if count != 0 {
		t.Errorf("critical count: got %d, want 0", count)
	}
}

func TestCountEvents_RollingWindow(t *testing.T) {
	st := openMem(t)
	now := time.Now().UTC()

	// Two recent events (within 1d window).
	for i := 0; i < 2; i++ {
		st.EventAppend("u1", &EventRecord{
			ID: "recent" + string(rune('0'+i)), SubjectID: "u1",
			Priority: "bulk", Decision: "ACT_NOW", OccurredAt: now.Add(-30 * time.Minute),
		})
	}
	// One old event (25 hours ago — outside 1d window).
	st.EventAppend("u1", &EventRecord{
		ID: "old", SubjectID: "u1",
		Priority: "bulk", Decision: "ACT_NOW", OccurredAt: now.Add(-25 * time.Hour),
	})

	count, err := st.CountEvents("u1", "bulk", "1d")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("rolling window count: got %d, want 2", count)
	}
}

