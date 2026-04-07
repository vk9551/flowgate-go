package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/vk9551/flowgate-io/internal/config"
	"github.com/vk9551/flowgate-io/internal/engine"
	"github.com/vk9551/flowgate-io/internal/store"
	"github.com/google/uuid"
)

// ── POST /v1/events ──────────────────────────────────────────────────────────

// eventRequest is the JSON body accepted by POST /v1/events.
// All top-level string fields (and any additional fields) are passed as-is
// into the engine matcher. Non-string values are coerced to strings.
type eventRequest map[string]any

// eventResponse is the JSON response for POST /v1/events.
type eventResponse struct {
	EventID         string     `json:"event_id"`
	Decision        string     `json:"decision"`
	DeliverAt       *time.Time `json:"deliver_at,omitempty"`
	Reason          string     `json:"reason"`
	Priority        string     `json:"priority"`
	SuppressedToday int        `json:"suppressed_today"`
}

func (s *Server) handleEventsPost(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()

	var raw eventRequest
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Convert all fields to strings for the engine matcher.
	evt := make(engine.Event, len(raw))
	for k, v := range raw {
		switch sv := v.(type) {
		case string:
			evt[k] = sv
		default:
			evt[k] = fmt.Sprintf("%v", v)
		}
	}

	// Extract subject ID.
	subjectID := evt[cfg.Subject.IDField]
	if subjectID == "" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("missing required field %q", cfg.Subject.IDField))
		return
	}

	// Upsert subject (capture timezone if provided in the event).
	subject := &store.Subject{
		ID:        subjectID,
		UpdatedAt: time.Now().UTC(),
	}
	if tz, ok := evt[cfg.Subject.TimezoneField]; ok && tz != "" {
		subject.Timezone = tz
	} else {
		// Preserve existing timezone if already stored.
		existing, err := s.store.SubjectGet(subjectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store error")
			return
		}
		if existing != nil {
			subject.Timezone = existing.Timezone
		}
	}
	if err := s.store.SubjectUpsert(subject); err != nil {
		writeError(w, http.StatusInternalServerError, "store error")
		return
	}

	// Match priority.
	priority := engine.MatchPriority(cfg.Priorities, evt)
	if priority == nil {
		writeError(w, http.StatusUnprocessableEntity, "no matching priority and no default configured")
		return
	}

	// Find policy for this priority.
	policy := findPolicy(cfg, priority.Name)
	if policy == nil {
		// No policy means send_now (no constraints defined).
		policy = &config.Policy{Priority: priority.Name, Decision: "send_now"}
	}

	eventID := uuid.New().String()
	now := time.Now().UTC()

	decision, err := engine.CheckAndRecord(subject, priority, policy, cfg.Subject, s.store, eventID, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "engine error: "+err.Error())
		return
	}

	// If the decision is DELAY, enqueue it in the scheduler.
	if decision.Outcome == engine.OutcomeDelay && s.sched != nil {
		rawPayload, _ := json.Marshal(raw)
		se := &store.ScheduledEvent{
			ID:        eventID,
			SubjectID: subjectID,
			Priority:  decision.Priority,
			Payload:   string(rawPayload),
			DeliverAt: decision.DeliverAt,
			CreatedAt: now,
		}
		if err := s.sched.Schedule(se); err != nil {
			// Non-fatal: log and continue. The decision has already been recorded.
			// TODO: consider returning 500 here if strict delivery is required.
			_ = err
		}
	}

	// Count suppressed events today for this subject (all priorities).
	suppressedToday, err := s.store.CountDecisions(subjectID, engine.OutcomeSuppress, "1d")
	if err != nil {
		suppressedToday = 0 // non-fatal
	}

	resp := eventResponse{
		EventID:         eventID,
		Decision:        decision.Outcome,
		Reason:          decision.Reason,
		Priority:        decision.Priority,
		SuppressedToday: suppressedToday,
	}
	if !decision.DeliverAt.IsZero() {
		t := decision.DeliverAt
		resp.DeliverAt = &t
	}

	writeJSON(w, http.StatusOK, resp)
}

// findPolicy locates the Policy for the named priority. Returns nil if absent.
func findPolicy(cfg *config.Config, priorityName string) *config.Policy {
	for i := range cfg.Policies {
		if cfg.Policies[i].Priority == priorityName {
			return &cfg.Policies[i]
		}
	}
	return nil
}

// ── GET /v1/subjects/:id ─────────────────────────────────────────────────────

type subjectResponse struct {
	Subject *store.Subject       `json:"subject"`
	History []*store.EventRecord `json:"history"`
}

func (s *Server) handleSubjectGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	sub, err := s.store.SubjectGet(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store error")
		return
	}
	if sub == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("subject %q not found", id))
		return
	}

	history, err := s.store.EventList(id, 20)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store error")
		return
	}
	if history == nil {
		history = []*store.EventRecord{}
	}

	writeJSON(w, http.StatusOK, subjectResponse{Subject: sub, History: history})
}

// ── DELETE /v1/subjects/:id ──────────────────────────────────────────────────

func (s *Server) handleSubjectDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := s.store.SubjectReset(id); err != nil {
		writeError(w, http.StatusInternalServerError, "store error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "reset", "subject_id": id})
}

// ── GET /v1/policies ─────────────────────────────────────────────────────────

func (s *Server) handlePoliciesGet(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	writeJSON(w, http.StatusOK, cfg)
}

// ── POST /v1/policies/reload ─────────────────────────────────────────────────

func (s *Server) handlePoliciesReload(w http.ResponseWriter, r *http.Request) {
	newCfg, err := config.Load(s.cfgPath)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "reload failed: "+err.Error())
		return
	}
	s.setConfig(newCfg)
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

// ── GET /v1/stats ─────────────────────────────────────────────────────────────

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.StatsToday()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store error")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// ── GET /v1/events/recent ─────────────────────────────────────────────────────

func (s *Server) handleEventsRecent(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		var n int
		if _, err := fmt.Sscanf(q, "%d", &n); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	events, err := s.store.EventListRecent(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store error")
		return
	}
	if events == nil {
		events = []*store.EventRecord{}
	}
	writeJSON(w, http.StatusOK, events)
}

// ── POST /v1/events/{event_id}/outcome ───────────────────────────────────────

type outcomeRequest struct {
	Outcome  string         `json:"outcome"`
	Reason   string         `json:"reason"`
	Metadata map[string]any `json:"metadata"`
}

type outcomeResponse struct {
	EventID         string `json:"event_id"`
	Outcome         string `json:"outcome"`
	CapRefunded     bool   `json:"cap_refunded"`
	PreviousOutcome string `json:"previous_outcome"`
}

func (s *Server) handleOutcomePost(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("event_id")
	cfg := s.getConfig()

	var req outcomeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Outcome == "" {
		writeError(w, http.StatusBadRequest, "outcome is required")
		return
	}

	// Validate outcome name and capture refund flag before mutating state.
	outcomeCfg := findOutcomeCfgInConfig(cfg, req.Outcome)
	if outcomeCfg == nil {
		writeError(w, http.StatusBadRequest, "unknown outcome: "+req.Outcome)
		return
	}

	// Fetch current event to capture previous_outcome.
	ev, err := s.store.EventGetByID(eventID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store error")
		return
	}
	if ev == nil {
		writeError(w, http.StatusNotFound, "event not found: "+eventID)
		return
	}
	previousOutcome := ev.Outcome

	// Extract optional channel from metadata.
	channel := ""
	if ch, ok := req.Metadata["channel"].(string); ok {
		channel = ch
	}

	if err := engine.ProcessOutcome(eventID, req.Outcome, req.Reason, channel, s.store, cfg); err != nil {
		switch {
		case errors.Is(err, engine.ErrEventNotFound):
			writeError(w, http.StatusNotFound, "event not found: "+eventID)
		case errors.Is(err, engine.ErrOutcomeConflict):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, outcomeResponse{
		EventID:         eventID,
		Outcome:         req.Outcome,
		CapRefunded:     outcomeCfg.RefundCap,
		PreviousOutcome: previousOutcome,
	})
}

// findOutcomeCfgInConfig is the handler-side helper (avoids importing engine internals).
func findOutcomeCfgInConfig(cfg *config.Config, name string) *config.OutcomeCfg {
	for i := range cfg.Outcomes {
		if cfg.Outcomes[i].Name == name {
			return &cfg.Outcomes[i]
		}
	}
	return nil
}

// ── GET /v1/health ────────────────────────────────────────────────────────────

type healthResponse struct {
	Status  string `json:"status"`
	Uptime  string `json:"uptime"`
	Version string `json:"version"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(s.startTime).Truncate(time.Second).String()
	writeJSON(w, http.StatusOK, healthResponse{
		Status:  "ok",
		Uptime:  uptime,
		Version: s.getConfig().Version,
	})
}
