package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore is a Store backed by a SQLite database.
type SQLiteStore struct {
	db *sql.DB
}

// OpenSQLite opens (or creates) a SQLite database at dsn and runs migrations.
// Use dsn = ":memory:" for tests.
func OpenSQLite(dsn string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite %q: %w", dsn, err)
	}

	// SQLite performs best with a single writer connection.
	db.SetMaxOpenConns(1)

	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

const schema = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS subjects (
    id          TEXT PRIMARY KEY,
    timezone    TEXT NOT NULL DEFAULT '',
    updated_at  INTEGER NOT NULL  -- unix seconds
);

CREATE TABLE IF NOT EXISTS event_log (
    id          TEXT PRIMARY KEY,
    subject_id  TEXT NOT NULL,
    priority    TEXT NOT NULL,
    decision    TEXT NOT NULL,
    reason      TEXT NOT NULL DEFAULT '',
    occurred_at INTEGER NOT NULL,  -- unix seconds
    deliver_at  INTEGER NOT NULL DEFAULT 0  -- unix seconds; non-zero for DELAY
);

CREATE INDEX IF NOT EXISTS idx_event_log_subject_priority_time
    ON event_log (subject_id, priority, occurred_at);

CREATE TABLE IF NOT EXISTS scheduled_events (
    id          TEXT PRIMARY KEY,
    subject_id  TEXT NOT NULL,
    priority    TEXT NOT NULL,
    payload     TEXT NOT NULL DEFAULT '',
    deliver_at  INTEGER NOT NULL,  -- unix seconds
    created_at  INTEGER NOT NULL   -- unix seconds
);

CREATE INDEX IF NOT EXISTS idx_scheduled_deliver
    ON scheduled_events (deliver_at);
`

func (s *SQLiteStore) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	// Add deliver_at to event_log for existing databases that pre-date this column.
	// SQLite returns an error for duplicate columns; ignore it.
	s.db.Exec(`ALTER TABLE event_log ADD COLUMN deliver_at INTEGER NOT NULL DEFAULT 0`) //nolint:errcheck
	return nil
}

// SubjectGet fetches a subject by ID. Returns nil, nil if not found.
func (s *SQLiteStore) SubjectGet(id string) (*Subject, error) {
	row := s.db.QueryRow(
		`SELECT id, timezone, updated_at FROM subjects WHERE id = ?`, id,
	)
	var sub Subject
	var updatedUnix int64
	err := row.Scan(&sub.ID, &sub.Timezone, &updatedUnix)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: SubjectGet %q: %w", id, err)
	}
	sub.UpdatedAt = time.Unix(updatedUnix, 0).UTC()
	return &sub, nil
}

// SubjectUpsert creates or updates a subject record.
func (s *SQLiteStore) SubjectUpsert(sub *Subject) error {
	_, err := s.db.Exec(
		`INSERT INTO subjects (id, timezone, updated_at)
         VALUES (?, ?, ?)
         ON CONFLICT(id) DO UPDATE SET
             timezone   = excluded.timezone,
             updated_at = excluded.updated_at`,
		sub.ID, sub.Timezone, sub.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("store: SubjectUpsert %q: %w", sub.ID, err)
	}
	return nil
}

// EventAppend writes a decision record to the event log.
func (s *SQLiteStore) EventAppend(subjectID string, e *EventRecord) error {
	deliverUnix := int64(0)
	if !e.DeliverAt.IsZero() {
		deliverUnix = e.DeliverAt.Unix()
	}
	_, err := s.db.Exec(
		`INSERT INTO event_log (id, subject_id, priority, decision, reason, occurred_at, deliver_at)
         VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.ID, subjectID, e.Priority, e.Decision, e.Reason, e.OccurredAt.Unix(), deliverUnix,
	)
	if err != nil {
		return fmt.Errorf("store: EventAppend subject=%q: %w", subjectID, err)
	}
	return nil
}

// SubjectReset deletes all event history and scheduled events for a subject,
// resetting all caps. The subject row itself is preserved.
func (s *SQLiteStore) SubjectReset(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: SubjectReset begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`DELETE FROM event_log WHERE subject_id = ?`, id); err != nil {
		return fmt.Errorf("store: SubjectReset event_log %q: %w", id, err)
	}
	if _, err := tx.Exec(`DELETE FROM scheduled_events WHERE subject_id = ?`, id); err != nil {
		return fmt.Errorf("store: SubjectReset scheduled_events %q: %w", id, err)
	}
	return tx.Commit()
}

// EventList returns up to limit recent events for subjectID, newest first.
func (s *SQLiteStore) EventList(subjectID string, limit int) ([]*EventRecord, error) {
	rows, err := s.db.Query(
		`SELECT id, subject_id, priority, decision, reason, occurred_at, deliver_at
         FROM event_log WHERE subject_id = ?
         ORDER BY occurred_at DESC LIMIT ?`,
		subjectID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: EventList %q: %w", subjectID, err)
	}
	defer rows.Close()
	return scanEventRows(rows, "EventList")
}

// EventListRecent returns the most recent decisions across all subjects.
func (s *SQLiteStore) EventListRecent(limit int) ([]*EventRecord, error) {
	rows, err := s.db.Query(
		`SELECT id, subject_id, priority, decision, reason, occurred_at, deliver_at
         FROM event_log ORDER BY occurred_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: EventListRecent: %w", err)
	}
	defer rows.Close()
	return scanEventRows(rows, "EventListRecent")
}

// StatsToday returns aggregated decision counts since the start of the current UTC day.
func (s *SQLiteStore) StatsToday() (*Stats, error) {
	todayStart := time.Now().UTC().Truncate(24 * time.Hour).Unix()

	// Count by decision type.
	rows, err := s.db.Query(
		`SELECT decision, COUNT(*) FROM event_log WHERE occurred_at >= ? GROUP BY decision`,
		todayStart,
	)
	if err != nil {
		return nil, fmt.Errorf("store: StatsToday counts: %w", err)
	}
	defer rows.Close()

	stats := &Stats{}
	for rows.Next() {
		var decision string
		var count int
		if err := rows.Scan(&decision, &count); err != nil {
			return nil, fmt.Errorf("store: StatsToday scan: %w", err)
		}
		stats.TotalToday += count
		switch decision {
		case "SEND_NOW":
			stats.SendNow = count
		case "DELAY":
			stats.Delayed = count
		case "SUPPRESS":
			stats.Suppressed = count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if stats.TotalToday > 0 {
		stats.SuppressionRate = float64(stats.Suppressed) / float64(stats.TotalToday) * 100
	}

	// Average delay for DELAY decisions (deliver_at - occurred_at in seconds).
	var avgDelay sql.NullFloat64
	err = s.db.QueryRow(
		`SELECT AVG(CAST(deliver_at - occurred_at AS REAL))
         FROM event_log
         WHERE decision = 'DELAY' AND deliver_at > 0 AND occurred_at >= ?`,
		todayStart,
	).Scan(&avgDelay)
	if err != nil {
		return nil, fmt.Errorf("store: StatsToday avg delay: %w", err)
	}
	if avgDelay.Valid {
		stats.AvgDelaySeconds = avgDelay.Float64
	}

	// Active scheduled events count.
	err = s.db.QueryRow(`SELECT COUNT(*) FROM scheduled_events`).Scan(&stats.ActiveScheduled)
	if err != nil {
		return nil, fmt.Errorf("store: StatsToday scheduled: %w", err)
	}

	return stats, nil
}

// scanEventRows is a shared helper for EventList and EventListRecent.
func scanEventRows(rows *sql.Rows, caller string) ([]*EventRecord, error) {
	var records []*EventRecord
	for rows.Next() {
		var e EventRecord
		var occurredUnix, deliverUnix int64
		if err := rows.Scan(&e.ID, &e.SubjectID, &e.Priority, &e.Decision, &e.Reason, &occurredUnix, &deliverUnix); err != nil {
			return nil, fmt.Errorf("store: %s scan: %w", caller, err)
		}
		e.OccurredAt = time.Unix(occurredUnix, 0).UTC()
		if deliverUnix > 0 {
			e.DeliverAt = time.Unix(deliverUnix, 0).UTC()
		}
		records = append(records, &e)
	}
	return records, rows.Err()
}

// CountDecisions counts events for subjectID with a specific decision outcome
// within the rolling window defined by period.
func (s *SQLiteStore) CountDecisions(subjectID, decision, period string) (int, error) {
	d, err := parsePeriod(period)
	if err != nil {
		return 0, fmt.Errorf("store: CountDecisions bad period %q: %w", period, err)
	}
	since := time.Now().UTC().Add(-d).Unix()

	var count int
	err = s.db.QueryRow(
		`SELECT COUNT(*) FROM event_log
         WHERE subject_id = ? AND decision = ? AND occurred_at >= ?`,
		subjectID, decision, since,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: CountDecisions: %w", err)
	}
	return count, nil
}

// CountEvents counts events for subject+priority within a rolling window.
// period is parsed the same way as config durations ("1d", "1h", "30m", etc.).
func (s *SQLiteStore) CountEvents(subjectID, priority, period string) (int, error) {
	d, err := parsePeriod(period)
	if err != nil {
		return 0, fmt.Errorf("store: CountEvents bad period %q: %w", period, err)
	}
	since := time.Now().UTC().Add(-d).Unix()

	var count int
	err = s.db.QueryRow(
		`SELECT COUNT(*) FROM event_log
         WHERE subject_id = ? AND priority = ? AND occurred_at >= ?`,
		subjectID, priority, since,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: CountEvents: %w", err)
	}
	return count, nil
}

// ScheduledList returns all scheduled events with deliver_at <= before.
func (s *SQLiteStore) ScheduledList(before time.Time) ([]*ScheduledEvent, error) {
	rows, err := s.db.Query(
		`SELECT id, subject_id, priority, payload, deliver_at, created_at
         FROM scheduled_events WHERE deliver_at <= ? ORDER BY deliver_at`,
		before.Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("store: ScheduledList: %w", err)
	}
	defer rows.Close()

	var events []*ScheduledEvent
	for rows.Next() {
		var e ScheduledEvent
		var deliverUnix, createdUnix int64
		if err := rows.Scan(&e.ID, &e.SubjectID, &e.Priority, &e.Payload, &deliverUnix, &createdUnix); err != nil {
			return nil, fmt.Errorf("store: ScheduledList scan: %w", err)
		}
		e.DeliverAt = time.Unix(deliverUnix, 0).UTC()
		e.CreatedAt = time.Unix(createdUnix, 0).UTC()
		events = append(events, &e)
	}
	return events, rows.Err()
}

// ScheduledListAll returns every scheduled event, ordered by deliver_at.
func (s *SQLiteStore) ScheduledListAll() ([]*ScheduledEvent, error) {
	rows, err := s.db.Query(
		`SELECT id, subject_id, priority, payload, deliver_at, created_at
         FROM scheduled_events ORDER BY deliver_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: ScheduledListAll: %w", err)
	}
	defer rows.Close()

	var events []*ScheduledEvent
	for rows.Next() {
		var e ScheduledEvent
		var deliverUnix, createdUnix int64
		if err := rows.Scan(&e.ID, &e.SubjectID, &e.Priority, &e.Payload, &deliverUnix, &createdUnix); err != nil {
			return nil, fmt.Errorf("store: ScheduledListAll scan: %w", err)
		}
		e.DeliverAt = time.Unix(deliverUnix, 0).UTC()
		e.CreatedAt = time.Unix(createdUnix, 0).UTC()
		events = append(events, &e)
	}
	return events, rows.Err()
}

// ScheduledInsert persists a new scheduled event.
func (s *SQLiteStore) ScheduledInsert(e *ScheduledEvent) error {
	_, err := s.db.Exec(
		`INSERT INTO scheduled_events (id, subject_id, priority, payload, deliver_at, created_at)
         VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.SubjectID, e.Priority, e.Payload, e.DeliverAt.Unix(), e.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("store: ScheduledInsert %q: %w", e.ID, err)
	}
	return nil
}

// ScheduledDelete removes a scheduled event by ID.
func (s *SQLiteStore) ScheduledDelete(id string) error {
	_, err := s.db.Exec(`DELETE FROM scheduled_events WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: ScheduledDelete %q: %w", id, err)
	}
	return nil
}

// Close releases the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// parsePeriod converts "1d", "2h", "30m" etc. to time.Duration.
// Duplicates the logic in config/loader.go to keep store self-contained.
func parsePeriod(s string) (time.Duration, error) {
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var n int
		if _, err := fmt.Sscanf(s[:len(s)-1], "%d", &n); err != nil {
			return 0, fmt.Errorf("invalid period %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
