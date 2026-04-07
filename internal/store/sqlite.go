package store

import (
	"database/sql"
	"encoding/json"
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
    id             TEXT PRIMARY KEY,
    timezone       TEXT NOT NULL DEFAULT '',
    updated_at     INTEGER NOT NULL,  -- unix seconds
    channel_health TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS event_log (
    id             TEXT PRIMARY KEY,
    subject_id     TEXT NOT NULL,
    priority       TEXT NOT NULL,
    decision       TEXT NOT NULL,
    reason         TEXT NOT NULL DEFAULT '',
    occurred_at    INTEGER NOT NULL,  -- unix seconds
    deliver_at     INTEGER NOT NULL DEFAULT 0,  -- unix seconds; non-zero for DELAY
    outcome        TEXT NOT NULL DEFAULT 'pending',
    outcome_reason TEXT NOT NULL DEFAULT '',
    resolved_at    INTEGER NOT NULL DEFAULT 0   -- unix seconds; non-zero when terminal
);

CREATE INDEX IF NOT EXISTS idx_event_log_subject_priority_time
    ON event_log (subject_id, priority, occurred_at);

`

func (s *SQLiteStore) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	// ALTER TABLE migrations for existing databases that pre-date these columns.
	// SQLite returns an error for duplicate columns; silently ignore it.
	s.db.Exec(`ALTER TABLE event_log ADD COLUMN deliver_at INTEGER NOT NULL DEFAULT 0`)     //nolint:errcheck
	s.db.Exec(`ALTER TABLE event_log ADD COLUMN outcome TEXT NOT NULL DEFAULT 'pending'`)   //nolint:errcheck
	s.db.Exec(`ALTER TABLE event_log ADD COLUMN outcome_reason TEXT NOT NULL DEFAULT ''`)   //nolint:errcheck
	s.db.Exec(`ALTER TABLE event_log ADD COLUMN resolved_at INTEGER NOT NULL DEFAULT 0`)    //nolint:errcheck
	s.db.Exec(`ALTER TABLE subjects ADD COLUMN channel_health TEXT NOT NULL DEFAULT '{}'`)  //nolint:errcheck
	return nil
}

// SubjectGet fetches a subject by ID. Returns nil, nil if not found.
func (s *SQLiteStore) SubjectGet(id string) (*Subject, error) {
	row := s.db.QueryRow(
		`SELECT id, timezone, updated_at, channel_health FROM subjects WHERE id = ?`, id,
	)
	var sub Subject
	var updatedUnix int64
	var healthJSON string
	err := row.Scan(&sub.ID, &sub.Timezone, &updatedUnix, &healthJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: SubjectGet %q: %w", id, err)
	}
	sub.UpdatedAt = time.Unix(updatedUnix, 0).UTC()
	if healthJSON != "" && healthJSON != "{}" {
		sub.ChannelHealth = make(map[string]string)
		_ = json.Unmarshal([]byte(healthJSON), &sub.ChannelHealth)
	}
	return &sub, nil
}

// SubjectUpsert creates or updates a subject record.
// channel_health is never overwritten by this method; use SubjectUpdateChannelHealth instead.
func (s *SQLiteStore) SubjectUpsert(sub *Subject) error {
	_, err := s.db.Exec(
		`INSERT INTO subjects (id, timezone, updated_at, channel_health)
         VALUES (?, ?, ?, '{}')
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

// SubjectReset deletes all event history for a subject, resetting all caps.
// The subject row itself is preserved.
func (s *SQLiteStore) SubjectReset(id string) error {
	_, err := s.db.Exec(`DELETE FROM event_log WHERE subject_id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: SubjectReset %q: %w", id, err)
	}
	return nil
}

// EventList returns up to limit recent events for subjectID, newest first.
func (s *SQLiteStore) EventList(subjectID string, limit int) ([]*EventRecord, error) {
	rows, err := s.db.Query(
		`SELECT id, subject_id, priority, decision, reason, occurred_at, deliver_at,
                outcome, outcome_reason, resolved_at
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
		`SELECT id, subject_id, priority, decision, reason, occurred_at, deliver_at,
                outcome, outcome_reason, resolved_at
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
		case "ACT_NOW":
			stats.ActNow = count
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

	// All-time outcome counts for delivery success rate.
	outcomeRows, err := s.db.Query(`SELECT outcome, COUNT(*) FROM event_log GROUP BY outcome`)
	if err != nil {
		return nil, fmt.Errorf("store: StatsToday outcomes: %w", err)
	}
	defer outcomeRows.Close()

	stats.OutcomeCounts = map[string]int{
		"success":     0,
		"failed_temp": 0,
		"failed_perm": 0,
		"pending":     0,
	}
	var successCount, resolvedCount int
	for outcomeRows.Next() {
		var outcome string
		var count int
		if err := outcomeRows.Scan(&outcome, &count); err != nil {
			return nil, fmt.Errorf("store: StatsToday outcomes scan: %w", err)
		}
		if outcome != "" {
			stats.OutcomeCounts[outcome] = count
			if outcome != "pending" {
				resolvedCount += count
			}
			if outcome == "success" {
				successCount = count
			}
		}
	}
	if err := outcomeRows.Err(); err != nil {
		return nil, err
	}
	if resolvedCount > 0 {
		stats.DeliverySuccessRate = float64(successCount) / float64(resolvedCount) * 100
	}

	return stats, nil
}

// scanEventRows is a shared helper for EventList, EventListRecent, and EventGetByID.
func scanEventRows(rows *sql.Rows, caller string) ([]*EventRecord, error) {
	var records []*EventRecord
	for rows.Next() {
		var e EventRecord
		var occurredUnix, deliverUnix, resolvedUnix int64
		if err := rows.Scan(
			&e.ID, &e.SubjectID, &e.Priority, &e.Decision, &e.Reason,
			&occurredUnix, &deliverUnix,
			&e.Outcome, &e.OutcomeReason, &resolvedUnix,
		); err != nil {
			return nil, fmt.Errorf("store: %s scan: %w", caller, err)
		}
		e.OccurredAt = time.Unix(occurredUnix, 0).UTC()
		if deliverUnix > 0 {
			e.DeliverAt = time.Unix(deliverUnix, 0).UTC()
		}
		if resolvedUnix > 0 {
			e.ResolvedAt = time.Unix(resolvedUnix, 0).UTC()
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

// EventGetByID fetches a single event by its ID. Returns nil, nil if not found.
func (s *SQLiteStore) EventGetByID(eventID string) (*EventRecord, error) {
	rows, err := s.db.Query(
		`SELECT id, subject_id, priority, decision, reason, occurred_at, deliver_at,
                outcome, outcome_reason, resolved_at
         FROM event_log WHERE id = ? LIMIT 1`,
		eventID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: EventGetByID %q: %w", eventID, err)
	}
	defer rows.Close()
	records, err := scanEventRows(rows, "EventGetByID")
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	return records[0], nil
}

// OutcomeUpdate records a delivery outcome on an existing event.
func (s *SQLiteStore) OutcomeUpdate(eventID, outcome, reason string) error {
	resolvedAt := int64(0)
	// Mark resolved_at for any outcome being set (callers set terminal outcomes).
	resolvedAt = time.Now().UTC().Unix()
	_, err := s.db.Exec(
		`UPDATE event_log SET outcome = ?, outcome_reason = ?, resolved_at = ? WHERE id = ?`,
		outcome, reason, resolvedAt, eventID,
	)
	if err != nil {
		return fmt.Errorf("store: OutcomeUpdate %q: %w", eventID, err)
	}
	return nil
}

// CapRefund deletes the event_log row for subject+priority at occurredAt,
// decrementing CountEvents by 1 and refunding a cap slot.
func (s *SQLiteStore) CapRefund(subjectID, priority string, occurredAt time.Time) error {
	_, err := s.db.Exec(
		`DELETE FROM event_log WHERE id = (
            SELECT id FROM event_log
            WHERE subject_id = ? AND priority = ? AND occurred_at = ?
            LIMIT 1
        )`,
		subjectID, priority, occurredAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("store: CapRefund subject=%q priority=%q: %w", subjectID, priority, err)
	}
	return nil
}

// SubjectUpdateChannelHealth upserts a channel health entry on the subjects row.
func (s *SQLiteStore) SubjectUpdateChannelHealth(subjectID, channel, outcome string) error {
	var healthJSON string
	err := s.db.QueryRow(`SELECT channel_health FROM subjects WHERE id = ?`, subjectID).Scan(&healthJSON)
	if err == sql.ErrNoRows {
		return fmt.Errorf("store: SubjectUpdateChannelHealth: subject %q not found", subjectID)
	}
	if err != nil {
		return fmt.Errorf("store: SubjectUpdateChannelHealth: %w", err)
	}

	health := make(map[string]string)
	if healthJSON != "" && healthJSON != "{}" {
		_ = json.Unmarshal([]byte(healthJSON), &health)
	}
	health[channel] = outcome

	newJSON, err := json.Marshal(health)
	if err != nil {
		return fmt.Errorf("store: SubjectUpdateChannelHealth marshal: %w", err)
	}
	_, err = s.db.Exec(`UPDATE subjects SET channel_health = ? WHERE id = ?`, string(newJSON), subjectID)
	if err != nil {
		return fmt.Errorf("store: SubjectUpdateChannelHealth update: %w", err)
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
