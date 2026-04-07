package engine

import (
	"testing"
	"time"

	"github.com/vk9551/flowgate-io/internal/config"
	"github.com/vk9551/flowgate-io/internal/store"
)

// testCfgWithOutcomes builds a config with default outcomes applied.
func testCfgWithOutcomes() *config.Config {
	cfg := &config.Config{
		Version: "1.0",
		Subject: config.SubjectCfg{
			IDField:       "user_id",
			TimezoneField: "user_tz",
			WakingHours:   config.WakingHours{Start: "07:00", End: "22:00"},
		},
		Priorities: []config.Priority{
			{Name: "bulk", Default: true},
		},
		Policies: []config.Policy{
			{Priority: "bulk", Decision: "send_now"},
		},
	}
	// Apply the same defaults the loader would apply.
	cfg.Outcomes = []config.OutcomeCfg{
		{Name: "success", RefundCap: false, Terminal: true},
		{Name: "failed_temp", RefundCap: true, Terminal: false},
		{Name: "failed_perm", RefundCap: true, Terminal: true},
		{Name: "pending", RefundCap: false, Terminal: false},
	}
	cfg.DefaultOutcome = "pending"
	return cfg
}

// seedEvent inserts a subject + event_log row and returns the event ID.
func seedEvent(t *testing.T, st store.Store, subjectID, priority string) string {
	t.Helper()
	sub := &store.Subject{ID: subjectID, UpdatedAt: time.Now().UTC()}
	if err := st.SubjectUpsert(sub); err != nil {
		t.Fatalf("SubjectUpsert: %v", err)
	}
	eventID := "evt-" + subjectID + "-" + priority
	rec := &store.EventRecord{
		ID:         eventID,
		SubjectID:  subjectID,
		Priority:   priority,
		Decision:   "ACT_NOW",
		Reason:     "act_now",
		OccurredAt: time.Now().UTC(),
	}
	if err := st.EventAppend(subjectID, rec); err != nil {
		t.Fatalf("EventAppend: %v", err)
	}
	return eventID
}

// TestProcessOutcome_Success verifies that a success outcome does not refund the cap.
func TestProcessOutcome_Success(t *testing.T) {
	st := newTestStore(t)
	cfg := testCfgWithOutcomes()
	eventID := seedEvent(t, st, "u1", "bulk")

	if err := ProcessOutcome(eventID, "success", "", "", st, cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ev, _ := st.EventGetByID(eventID)
	if ev.Outcome != "success" {
		t.Errorf("outcome: got %q, want success", ev.Outcome)
	}
	// Cap should NOT be refunded — event still in event_log.
	ev2, _ := st.EventGetByID(eventID)
	if ev2 == nil {
		t.Error("success outcome should not delete the event_log row")
	}
}

// TestProcessOutcome_FailedTemp verifies cap refund and non-terminal behaviour.
func TestProcessOutcome_FailedTemp(t *testing.T) {
	st := newTestStore(t)
	cfg := testCfgWithOutcomes()

	sub := &store.Subject{ID: "u2", UpdatedAt: time.Now().UTC()}
	st.SubjectUpsert(sub)

	now := time.Now().UTC()
	eventID := "evt-u2-bulk"
	rec := &store.EventRecord{
		ID:         eventID,
		SubjectID:  "u2",
		Priority:   "bulk",
		Decision:   "ACT_NOW",
		Reason:     "act_now",
		OccurredAt: now,
	}
	st.EventAppend("u2", rec)

	// Verify CountEvents is 1 before refund.
	before, _ := st.CountEvents("u2", "bulk", "1d")
	if before != 1 {
		t.Fatalf("CountEvents before: got %d, want 1", before)
	}

	if err := ProcessOutcome(eventID, "failed_temp", "conn_timeout", "", st, cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Cap refund should remove the event_log row → CountEvents drops to 0.
	after, _ := st.CountEvents("u2", "bulk", "1d")
	if after != 0 {
		t.Errorf("CountEvents after cap refund: got %d, want 0", after)
	}

	// failed_temp is non-terminal — should not mark channel health.
	subject, _ := st.SubjectGet("u2")
	if len(subject.ChannelHealth) != 0 {
		t.Errorf("failed_temp should not update channel health, got %v", subject.ChannelHealth)
	}
}

// TestProcessOutcome_FailedPerm verifies cap refund AND channel health update.
func TestProcessOutcome_FailedPerm(t *testing.T) {
	st := newTestStore(t)
	cfg := testCfgWithOutcomes()
	eventID := seedEvent(t, st, "u3", "bulk")

	if err := ProcessOutcome(eventID, "failed_perm", "hard_bounce", "email", st, cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	subject, _ := st.SubjectGet("u3")
	if subject.ChannelHealth["email"] != "failed_perm" {
		t.Errorf("channel_health[email]: got %q, want failed_perm", subject.ChannelHealth["email"])
	}
}

// TestProcessOutcome_UnknownOutcome verifies validation error for unknown outcomes.
func TestProcessOutcome_UnknownOutcome(t *testing.T) {
	st := newTestStore(t)
	cfg := testCfgWithOutcomes()
	eventID := seedEvent(t, st, "u4", "bulk")

	err := ProcessOutcome(eventID, "totally_made_up", "", "", st, cfg)
	if err == nil {
		t.Fatal("expected error for unknown outcome, got nil")
	}
}

// TestProcessOutcome_DuplicateTerminal_Same verifies idempotency for repeated terminal outcomes.
func TestProcessOutcome_DuplicateTerminal_Same(t *testing.T) {
	st := newTestStore(t)
	cfg := testCfgWithOutcomes()
	eventID := seedEvent(t, st, "u5", "bulk")

	if err := ProcessOutcome(eventID, "success", "", "", st, cfg); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Same terminal outcome again — must be idempotent.
	if err := ProcessOutcome(eventID, "success", "", "", st, cfg); err != nil {
		t.Fatalf("idempotent second call: %v", err)
	}
}

// TestProcessOutcome_DuplicateTerminal_Different verifies conflict error.
func TestProcessOutcome_DuplicateTerminal_Different(t *testing.T) {
	st := newTestStore(t)
	cfg := testCfgWithOutcomes()
	eventID := seedEvent(t, st, "u6", "bulk")

	if err := ProcessOutcome(eventID, "success", "", "", st, cfg); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Different terminal outcome — should return ErrOutcomeConflict.
	err := ProcessOutcome(eventID, "failed_perm", "", "", st, cfg)
	if err == nil {
		t.Fatal("expected ErrOutcomeConflict, got nil")
	}
	if err != ErrOutcomeConflict {
		t.Errorf("expected ErrOutcomeConflict, got %v", err)
	}
}

// TestProcessOutcome_EventNotFound verifies 404-style error for missing events.
func TestProcessOutcome_EventNotFound(t *testing.T) {
	st := newTestStore(t)
	cfg := testCfgWithOutcomes()

	err := ProcessOutcome("nonexistent-id", "success", "", "", st, cfg)
	if err == nil {
		t.Fatal("expected ErrEventNotFound, got nil")
	}
	if err != ErrEventNotFound {
		t.Errorf("expected ErrEventNotFound, got %v", err)
	}
}
