package engine

import (
	"fmt"
	"testing"
	"time"

	"github.com/vk9551/flowgate-io/internal/config"
	"github.com/vk9551/flowgate-io/internal/store"
)

// newTestStore opens an in-memory SQLite instance. Each call gets a fresh DB.
func newTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// subjectCfg is the waking-hours config used across tests.
var defaultSubjectCfg = config.SubjectCfg{
	IDField:       "user_id",
	TimezoneField: "user_tz",
	WakingHours: config.WakingHours{
		Start: "07:00",
		End:   "22:00",
	},
}

// wakingTime returns today's 10:00 AM New York time in UTC.
// Using today's date keeps seeded events within the rolling 24-hour cap window.
func wakingTime() time.Time {
	loc, _ := time.LoadLocation("America/New_York")
	y, m, d := time.Now().In(loc).Date()
	return time.Date(y, m, d, 10, 0, 0, 0, loc).UTC()
}

// quietTime returns today's 02:00 AM New York time in UTC.
func quietTime() time.Time {
	loc, _ := time.LoadLocation("America/New_York")
	y, m, d := time.Now().In(loc).Date()
	return time.Date(y, m, d, 2, 0, 0, 0, loc).UTC()
}

func nySubject() *store.Subject {
	return &store.Subject{ID: "u1", Timezone: "America/New_York", UpdatedAt: time.Now()}
}

func bulkPolicy(limit int) *config.Policy {
	return &config.Policy{
		Priority: "bulk",
		Caps: []config.CapRule{
			{Scope: "subject", PeriodRaw: "1d", Period: 24 * time.Hour, Limit: limit},
		},
		Window:              config.WindowCfg{RespectWakingHours: true, MaxDelayRaw: "48h", MaxDelay: 48 * time.Hour},
		DecisionOnCapBreach: "suppress",
	}
}

func bulkPriority() *config.Priority {
	return &config.Priority{Name: "bulk", Default: true}
}

func criticalPriority() *config.Priority {
	return &config.Priority{Name: "critical", BypassAll: true}
}

func criticalPolicy() *config.Policy {
	return &config.Policy{Priority: "critical", Decision: "send_now"}
}

// appendEvents writes n past events for subject/priority into the store.
func appendEvents(t *testing.T, st store.Store, subjectID, priority string, n int, at time.Time) {
	t.Helper()
	for i := 0; i < n; i++ {
		err := st.EventAppend(subjectID, &store.EventRecord{
			ID:         fmt.Sprintf("seed-%d", i),
			SubjectID:  subjectID,
			Priority:   priority,
			Decision:   OutcomeSendNow,
			Reason:     "seed",
			OccurredAt: at,
		})
		if err != nil {
			t.Fatalf("appendEvents: %v", err)
		}
	}
}

func TestCheckAndRecord(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T, st store.Store)
		subject     *store.Subject
		priority    *config.Priority
		policy      *config.Policy
		subjectCfg  config.SubjectCfg
		now         time.Time
		wantOutcome string
		wantReason  string
	}{
		{
			name:        "under cap during waking hours → SEND_NOW",
			setup:       func(t *testing.T, st store.Store) {},
			subject:     nySubject(),
			priority:    bulkPriority(),
			policy:      bulkPolicy(3),
			subjectCfg:  defaultSubjectCfg,
			now:         wakingTime(),
			wantOutcome: OutcomeSendNow,
			wantReason:  ReasonSendNow,
		},
		{
			name: "at cap limit (exactly limit events already recorded) → SUPPRESS",
			setup: func(t *testing.T, st store.Store) {
				appendEvents(t, st, "u1", "bulk", 3, time.Now().UTC().Add(-1*time.Hour))
			},
			subject:     nySubject(),
			priority:    bulkPriority(),
			policy:      bulkPolicy(3),
			subjectCfg:  defaultSubjectCfg,
			now:         wakingTime(),
			wantOutcome: OutcomeSuppress,
			wantReason:  ReasonCapBreached,
		},
		{
			name: "over cap → SUPPRESS",
			setup: func(t *testing.T, st store.Store) {
				appendEvents(t, st, "u1", "bulk", 5, time.Now().UTC().Add(-30*time.Minute))
			},
			subject:     nySubject(),
			priority:    bulkPriority(),
			policy:      bulkPolicy(3),
			subjectCfg:  defaultSubjectCfg,
			now:         wakingTime(),
			wantOutcome: OutcomeSuppress,
			wantReason:  ReasonCapBreached,
		},
		{
			name:        "bypass_all ignores caps and quiet hours → SEND_NOW",
			setup:       func(t *testing.T, st store.Store) {},
			subject:     nySubject(),
			priority:    criticalPriority(),
			policy:      criticalPolicy(),
			subjectCfg:  defaultSubjectCfg,
			now:         quietTime(), // middle of the night
			wantOutcome: OutcomeSendNow,
			wantReason:  ReasonBypassAll,
		},
		{
			name:        "quiet hours → DELAY",
			setup:       func(t *testing.T, st store.Store) {},
			subject:     nySubject(),
			priority:    bulkPriority(),
			policy:      bulkPolicy(10),
			subjectCfg:  defaultSubjectCfg,
			now:         quietTime(),
			wantOutcome: OutcomeDelay,
			wantReason:  ReasonQuietHours,
		},
		{
			name: "bypass_all overrides even when over cap",
			setup: func(t *testing.T, st store.Store) {
				appendEvents(t, st, "u1", "critical", 99, time.Now().UTC().Add(-1*time.Hour))
			},
			subject:     nySubject(),
			priority:    criticalPriority(),
			policy:      criticalPolicy(),
			subjectCfg:  defaultSubjectCfg,
			now:         wakingTime(),
			wantOutcome: OutcomeSendNow,
			wantReason:  ReasonBypassAll,
		},
		{
			name: "old events outside rolling window don't count toward cap",
			setup: func(t *testing.T, st store.Store) {
				// Insert events 26 hours ago — always outside the 1d window.
				old := time.Now().UTC().Add(-26 * time.Hour)
				appendEvents(t, st, "u1", "bulk", 5, old)
			},
			subject:     nySubject(),
			priority:    bulkPriority(),
			policy:      bulkPolicy(3),
			subjectCfg:  defaultSubjectCfg,
			now:         wakingTime(),
			wantOutcome: OutcomeSendNow,
			wantReason:  ReasonSendNow,
		},
		{
			name:        "no waking hours check when RespectWakingHours=false",
			setup:       func(t *testing.T, st store.Store) {},
			subject:     nySubject(),
			priority:    bulkPriority(),
			policy: &config.Policy{
				Priority:            "bulk",
				Window:              config.WindowCfg{RespectWakingHours: false},
				Caps:                []config.CapRule{{Scope: "subject", PeriodRaw: "1d", Limit: 10}},
				DecisionOnCapBreach: "suppress",
			},
			subjectCfg:  defaultSubjectCfg,
			now:         quietTime(),
			wantOutcome: OutcomeSendNow,
			wantReason:  ReasonSendNow,
		},
		{
			name:        "UTC subject (no timezone set) during quiet hours → DELAY",
			setup:       func(t *testing.T, st store.Store) {},
			subject:     &store.Subject{ID: "u1", Timezone: "UTC"},
			priority:    bulkPriority(),
			policy:      bulkPolicy(10),
			subjectCfg:  defaultSubjectCfg,
			// 03:00 UTC is outside 07:00–22:00 UTC window.
			now:         time.Date(2026, 4, 6, 3, 0, 0, 0, time.UTC),
			wantOutcome: OutcomeDelay,
			wantReason:  ReasonQuietHours,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newTestStore(t)

			// Upsert the subject so the store knows about it.
			if err := st.SubjectUpsert(tt.subject); err != nil {
				t.Fatalf("SubjectUpsert: %v", err)
			}

			tt.setup(t, st)

			got, err := CheckAndRecord(
				tt.subject,
				tt.priority,
				tt.policy,
				tt.subjectCfg,
				st,
				"evt-test",
				tt.now,
			)
			if err != nil {
				t.Fatalf("CheckAndRecord: %v", err)
			}
			if got.Outcome != tt.wantOutcome {
				t.Errorf("outcome: got %q, want %q", got.Outcome, tt.wantOutcome)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("reason: got %q, want %q", got.Reason, tt.wantReason)
			}
			if got.Priority != tt.priority.Name {
				t.Errorf("priority: got %q, want %q", got.Priority, tt.priority.Name)
			}
		})
	}
}

// TestCheckAndRecord_EventRecorded verifies that CheckAndRecord writes to the event log.
func TestCheckAndRecord_EventRecorded(t *testing.T) {
	st := newTestStore(t)
	sub := nySubject()
	if err := st.SubjectUpsert(sub); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	_, err := CheckAndRecord(sub, bulkPriority(), bulkPolicy(10), defaultSubjectCfg, st, "evt-abc", now)
	if err != nil {
		t.Fatalf("CheckAndRecord: %v", err)
	}

	// The event should now count in the cap window.
	count, err := st.CountEvents("u1", "bulk", "1d")
	if err != nil {
		t.Fatalf("CountEvents: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 event recorded, got %d", count)
	}
}

// TestCheckAndRecord_DeliverAtSet verifies DeliverAt is populated for DELAY decisions.
func TestCheckAndRecord_DeliverAtSet(t *testing.T) {
	st := newTestStore(t)
	sub := nySubject()
	if err := st.SubjectUpsert(sub); err != nil {
		t.Fatal(err)
	}

	d, err := CheckAndRecord(sub, bulkPriority(), bulkPolicy(10), defaultSubjectCfg, st, "evt-delay", quietTime())
	if err != nil {
		t.Fatalf("CheckAndRecord: %v", err)
	}
	if d.Outcome != OutcomeDelay {
		t.Fatalf("expected DELAY, got %s", d.Outcome)
	}
	if d.DeliverAt.IsZero() {
		t.Error("DeliverAt should be set for DELAY outcome")
	}
	// Next waking window for NY at 02:00 is 07:00 same day (UTC+4 = 11:00 UTC).
	if d.DeliverAt.Hour() != 11 {
		t.Errorf("DeliverAt hour: got %d, want 11 (07:00 NY in UTC)", d.DeliverAt.Hour())
	}
}
