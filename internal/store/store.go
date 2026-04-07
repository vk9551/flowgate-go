// Package store defines the storage interface and data types used across FlowGate.
// Implementations live in separate files (sqlite.go, etc.).
// Interfaces are defined here — in the package that uses them — per project convention.
package store

import "time"

// Subject represents a tracked entity (user, device, endpoint).
type Subject struct {
	ID        string    `json:"id"`
	Timezone  string    `json:"timezone"` // IANA tz, e.g. "America/New_York"
	UpdatedAt time.Time `json:"updated_at"`
}

// EventRecord is an immutable log entry written each time FlowGate makes a decision.
type EventRecord struct {
	ID         string    `json:"id"`
	SubjectID  string    `json:"subject_id"`
	Priority   string    `json:"priority"`
	Decision   string    `json:"decision"` // SEND_NOW | DELAY | SUPPRESS
	Reason     string    `json:"reason"`
	OccurredAt time.Time `json:"occurred_at"`
	DeliverAt  time.Time `json:"deliver_at,omitempty"` // non-zero only for DELAY decisions
}

// ScheduledEvent is a delayed event waiting to be delivered.
type ScheduledEvent struct {
	ID        string    `json:"id"`
	SubjectID string    `json:"subject_id"`
	Priority  string    `json:"priority"`
	Payload   string    `json:"payload"` // raw JSON from caller
	DeliverAt time.Time `json:"deliver_at"`
	CreatedAt time.Time `json:"created_at"`
}

// Stats holds aggregated decision counts for a time window (typically today).
type Stats struct {
	TotalToday      int     `json:"total_today"`
	SendNow         int     `json:"send_now"`
	Delayed         int     `json:"delayed"`
	Suppressed      int     `json:"suppressed"`
	SuppressionRate float64 `json:"suppression_rate"`
	AvgDelaySeconds float64 `json:"avg_delay_seconds"`
	ActiveScheduled int     `json:"active_scheduled"`
}

// Store is the persistence interface for FlowGate.
// All methods must be safe for concurrent use.
type Store interface {
	// SubjectGet fetches a subject by ID. Returns nil, nil when not found.
	SubjectGet(id string) (*Subject, error)

	// SubjectUpsert creates or updates a subject record.
	SubjectUpsert(s *Subject) error

	// SubjectReset deletes a subject's event history and scheduled events,
	// effectively resetting all caps. The subject row itself is preserved.
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

	// StatsToday returns aggregated decision counts since the start of the
	// current UTC day, plus active scheduled event count.
	StatsToday() (*Stats, error)

	// ScheduledList returns all scheduled events with DeliverAt <= before.
	ScheduledList(before time.Time) ([]*ScheduledEvent, error)

	// ScheduledListAll returns every scheduled event regardless of DeliverAt.
	// Used on scheduler startup to restore in-memory heap state.
	ScheduledListAll() ([]*ScheduledEvent, error)

	// ScheduledInsert persists a new scheduled event.
	ScheduledInsert(e *ScheduledEvent) error

	// ScheduledDelete removes a scheduled event by ID.
	ScheduledDelete(id string) error

	// Close releases any held resources (DB connections, file handles).
	Close() error
}
