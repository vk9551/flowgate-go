package engine

import (
	"errors"
	"fmt"

	"github.com/vk9551/flowgate-io/internal/config"
	"github.com/vk9551/flowgate-io/internal/store"
)

// ErrEventNotFound is returned when ProcessOutcome cannot find the event.
var ErrEventNotFound = errors.New("event not found")

// ErrOutcomeConflict is returned when the event already has a different terminal outcome.
var ErrOutcomeConflict = errors.New("event already has a different terminal outcome")

// ProcessOutcome records a delivery outcome for a previously-decided event.
// It applies cap refunds and channel health updates as configured.
// channel is the delivery channel to tag on the subject health record (e.g. "email", "push").
// If channel is empty, the event's priority name is used as a fallback.
func ProcessOutcome(eventID, outcome, reason, channel string, st store.Store, cfg *config.Config) error {
	// Look up the event.
	ev, err := st.EventGetByID(eventID)
	if err != nil {
		return fmt.Errorf("feedback: get event: %w", err)
	}
	if ev == nil {
		return ErrEventNotFound
	}

	// Find outcome config.
	outcomeCfg := findOutcomeCfg(cfg, outcome)
	if outcomeCfg == nil {
		return fmt.Errorf("feedback: unknown outcome %q", outcome)
	}

	// Check whether the existing outcome is already terminal.
	if ev.Outcome != "" && ev.Outcome != cfg.DefaultOutcome {
		existing := findOutcomeCfg(cfg, ev.Outcome)
		if existing != nil && existing.Terminal {
			if ev.Outcome == outcome {
				// Same terminal outcome — idempotent.
				return nil
			}
			return ErrOutcomeConflict
		}
	}

	// Refund the cap slot for this event.
	if outcomeCfg.RefundCap {
		if err := st.CapRefund(ev.SubjectID, ev.Priority, ev.OccurredAt); err != nil {
			return fmt.Errorf("feedback: cap refund: %w", err)
		}
	}

	// Mark channel unhealthy for terminal non-success outcomes.
	if outcomeCfg.Terminal && outcome != config.OutcomeNameSuccess {
		ch := channel
		if ch == "" {
			ch = ev.Priority
		}
		if err := st.SubjectUpdateChannelHealth(ev.SubjectID, ch, outcome); err != nil {
			return fmt.Errorf("feedback: channel health: %w", err)
		}
	}

	// Persist the outcome.
	if err := st.OutcomeUpdate(eventID, outcome, reason); err != nil {
		return fmt.Errorf("feedback: outcome update: %w", err)
	}

	return nil
}

// findOutcomeCfg returns the OutcomeCfg for the named outcome, or nil if not found.
func findOutcomeCfg(cfg *config.Config, name string) *config.OutcomeCfg {
	for i := range cfg.Outcomes {
		if cfg.Outcomes[i].Name == name {
			return &cfg.Outcomes[i]
		}
	}
	return nil
}
