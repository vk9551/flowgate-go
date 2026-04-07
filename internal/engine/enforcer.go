package engine

import (
	"fmt"
	"time"

	"github.com/vk9551/flowgate-io/internal/config"
	"github.com/vk9551/flowgate-io/internal/store"
)

// Decision is the outcome FlowGate returns for an event.
type Decision struct {
	Outcome    string    // SEND_NOW | DELAY | SUPPRESS
	Reason     string    // machine-readable reason code
	DeliverAt  time.Time // set when Outcome == DELAY
	Priority   string
}

// Reason codes.
const (
	ReasonSendNow       = "send_now"
	ReasonBypassAll     = "bypass_all"
	ReasonQuietHours    = "quiet_hours"
	ReasonCapBreached   = "cap_breached"
	ReasonNoPolicyFound = "no_policy"
)

// Outcome values.
const (
	OutcomeSendNow  = "SEND_NOW"
	OutcomeDelay    = "DELAY"
	OutcomeSuppress = "SUPPRESS"
)

// CheckAndRecord evaluates caps and waking-hour constraints for the given
// subject + priority + policy, records the decision in the store, and returns
// the Decision. It is the central decision point for Session 2.
func CheckAndRecord(
	subject *store.Subject,
	priority *config.Priority,
	policy *config.Policy,
	subjectCfg config.SubjectCfg,
	st store.Store,
	eventID string,
	now time.Time,
) (Decision, error) {
	// P0 / bypass_all: ignore all caps and waking hours.
	if priority.BypassAll {
		d := Decision{
			Outcome:  OutcomeSendNow,
			Reason:   ReasonBypassAll,
			Priority: priority.Name,
		}
		if err := recordEvent(st, subject.ID, eventID, priority.Name, d, now); err != nil {
			return d, err
		}
		return d, nil
	}

	// Check caps first (cheaper than tz lookup).
	breached, err := anyCap(subject.ID, priority.Name, policy.Caps, st)
	if err != nil {
		return Decision{}, err
	}
	if breached {
		outcome := OutcomeSuppress
		if policy.DecisionOnCapBreach != "" {
			outcome = normaliseOutcome(policy.DecisionOnCapBreach)
		}
		d := Decision{
			Outcome:  outcome,
			Reason:   ReasonCapBreached,
			Priority: priority.Name,
		}
		if err := recordEvent(st, subject.ID, eventID, priority.Name, d, now); err != nil {
			return d, err
		}
		return d, nil
	}

	// Waking-hours check.
	if policy.Window.RespectWakingHours {
		inWindow, nextOpen, whErr := inWakingWindow(subject, subjectCfg, now)
		if whErr != nil {
			// Non-fatal: fall through to SEND_NOW if tz is unknown.
			// TODO: consider making this stricter if tz is required.
			_ = whErr
		} else if !inWindow {
			d := Decision{
				Outcome:   OutcomeDelay,
				Reason:    ReasonQuietHours,
				DeliverAt: nextOpen,
				Priority:  priority.Name,
			}
			if err := recordEvent(st, subject.ID, eventID, priority.Name, d, now); err != nil {
				return d, err
			}
			return d, nil
		}
	}

	// All clear.
	d := Decision{
		Outcome:  OutcomeSendNow,
		Reason:   ReasonSendNow,
		Priority: priority.Name,
	}
	if err := recordEvent(st, subject.ID, eventID, priority.Name, d, now); err != nil {
		return d, err
	}
	return d, nil
}

// anyCap returns true if any cap rule is currently breached.
func anyCap(subjectID, priorityName string, caps []config.CapRule, st store.Store) (bool, error) {
	for _, cap := range caps {
		period := cap.PeriodRaw
		if period == "" {
			// Fall back to Duration representation if raw string was not set.
			period = fmt.Sprintf("%ds", int(cap.Period.Seconds()))
		}
		count, err := st.CountEvents(subjectID, priorityName, period)
		if err != nil {
			return false, err
		}
		if count >= cap.Limit {
			return true, nil
		}
	}
	return false, nil
}

// inWakingWindow reports whether now falls within the subject's waking hours.
// Returns (true, zero, nil) if inside, (false, nextOpen, nil) if outside.
func inWakingWindow(
	subject *store.Subject,
	subjectCfg config.SubjectCfg,
	now time.Time,
) (bool, time.Time, error) {
	tz := subject.Timezone
	if tz == "" {
		tz = "UTC"
	}

	loc, err := time.LoadLocation(tz)
	if err != nil {
		return false, time.Time{}, fmt.Errorf("enforcer: unknown timezone %q: %w", tz, err)
	}

	local := now.In(loc)

	startH, startM, err := parseHHMM(subjectCfg.WakingHours.Start)
	if err != nil {
		return false, time.Time{}, err
	}
	endH, endM, err := parseHHMM(subjectCfg.WakingHours.End)
	if err != nil {
		return false, time.Time{}, err
	}

	// Build today's window boundaries in local time.
	y, mo, d := local.Date()
	windowStart := time.Date(y, mo, d, startH, startM, 0, 0, loc)
	windowEnd := time.Date(y, mo, d, endH, endM, 0, 0, loc)

	if !local.Before(windowStart) && local.Before(windowEnd) {
		return true, time.Time{}, nil
	}

	// Outside the window — compute next open time.
	var nextOpen time.Time
	if local.Before(windowStart) {
		// Before today's window opens.
		nextOpen = windowStart
	} else {
		// After today's window; next open is tomorrow's start.
		nextOpen = windowStart.Add(24 * time.Hour)
	}
	return false, nextOpen.UTC(), nil
}

// parseHHMM parses "HH:MM" into hour and minute integers.
func parseHHMM(s string) (int, int, error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, 0, fmt.Errorf("enforcer: invalid time format %q, expected HH:MM", s)
	}
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, 0, fmt.Errorf("enforcer: parse HH:MM %q: %w", s, err)
	}
	return h, m, nil
}

// normaliseOutcome maps config decision strings to canonical outcome constants.
func normaliseOutcome(s string) string {
	switch s {
	case "send_now":
		return OutcomeSendNow
	case "suppress":
		return OutcomeSuppress
	case "delay":
		return OutcomeDelay
	default:
		return OutcomeSuppress
	}
}

// recordEvent writes the decision to the event log.
func recordEvent(
	st store.Store,
	subjectID, eventID, priorityName string,
	d Decision,
	now time.Time,
) error {
	return st.EventAppend(subjectID, &store.EventRecord{
		ID:         eventID,
		SubjectID:  subjectID,
		Priority:   priorityName,
		Decision:   d.Outcome,
		Reason:     d.Reason,
		OccurredAt: now,
		DeliverAt:  d.DeliverAt, // zero for non-DELAY decisions
	})
}
