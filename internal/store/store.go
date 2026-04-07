// Package store defines the storage interface and data types used across FlowGate.
// Implementations live in separate files (sqlite.go, etc.).
// Interfaces are defined here — in the package that uses them — per project convention.
package store

import "time"

// Subject represents a tracked entity (user, device, endpoint).
type Subject struct {
	ID            string            `json:"id"`
	Timezone      string            `json:"timezone"` // IANA tz, e.g. "America/New_York"
	UpdatedAt     time.Time         `json:"updated_at"`
	ChannelHealth map[string]string `json:"channel_health,omitempty"` // last outcome per delivery channel
}

// EventRecord is a log entry written each time FlowGate makes a decision.
// Outcome fields are populated later via the feedback API.
type EventRecord struct {
	ID            string    `json:"id"`
	SubjectID     string    `json:"subject_id"`
	Priority      string    `json:"priority"`
	Decision      string    `json:"decision"` // ACT_NOW | DELAY | SUPPRESS
	Reason        string    `json:"reason"`
	OccurredAt    time.Time `json:"occurred_at"`
	DeliverAt     time.Time `json:"deliver_at,omitempty"`     // non-zero only for DELAY decisions
	Outcome       string    `json:"outcome,omitempty"`        // delivery outcome reported by caller
	OutcomeReason string    `json:"outcome_reason,omitempty"` // caller-provided reason for the outcome
	ResolvedAt    time.Time `json:"resolved_at,omitempty"`    // when the terminal outcome was recorded
}

// Stats holds aggregated decision counts for a time window (typically today)
// plus all-time delivery outcome counts.
type Stats struct {
	TotalToday          int            `json:"total_today"`
	ActNow              int            `json:"act_now"`
	Delayed             int            `json:"delayed"`
	Suppressed          int            `json:"suppressed"`
	SuppressionRate     float64        `json:"suppression_rate"`
	AvgDelaySeconds     float64        `json:"avg_delay_seconds"`
	OutcomeCounts       map[string]int `json:"outcome_counts"`       // all-time outcome tallies
	DeliverySuccessRate float64        `json:"delivery_success_rate"` // success / (success+failed_*) × 100
}

// Store is the persistence interface for FlowGate.
// All methods must be safe for concurrent use.
type Store interface {
	// SubjectGet fetches a subject by ID. Returns nil, nil when not found.
	SubjectGet(id string) (*Subject, error)

	// SubjectUpsert creates or updates a subject record.
	SubjectUpsert(s *Subject) error

	// SubjectReset deletes a subject's event history, effectively resetting all caps.
	// The subject row itself is preserved.
	SubjectReset(id string) error

	// EventAppend writes a decision record to the event log.
	EventAppend(subjectID string, e *EventRecord) error

	// EventList returns up to limit recent events for subjectID, newest first.
	EventList(subjectID string, limit int) ([]*EventRecord, error)

	// EventListRecent returns the most recent decisions across all subjects.
	EventListRecent(limit int) ([]*EventRecord, error)

	// CountEvents returns how many events for the given subject+priority
	// occurred within the rolling window defined by period
	// (e.g. "1d" = last 24 h, "1h" = last 60 min).
	CountEvents(subjectID, priority, period string) (int, error)

	// CountDecisions returns how many events for subjectID with the given
	// decision outcome (e.g. "SUPPRESS") occurred within the rolling period.
	CountDecisions(subjectID, decision, period string) (int, error)

	// StatsToday returns aggregated decision counts since the start of the current UTC day.
	StatsToday() (*Stats, error)

	// EventGetByID fetches a single event by ID. Returns nil, nil if not found.
	EventGetByID(eventID string) (*EventRecord, error)

	// OutcomeUpdate records a delivery outcome on an existing event.
	OutcomeUpdate(eventID, outcome, reason string) error

	// CapRefund removes one cap-counted event entry for subject+priority
	// at the given occurredAt timestamp, effectively decrementing CountEvents by 1.
	CapRefund(subjectID, priority string, occurredAt time.Time) error

	// SubjectUpdateChannelHealth upserts the health status for a named channel
	// on a subject (e.g. channel="email", outcome="failed_perm").
	SubjectUpdateChannelHealth(subjectID, channel, outcome string) error

	// Close releases any held resources (DB connections, file handles).
	Close() error
}
