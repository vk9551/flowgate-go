package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vk9551/flowgate-io/internal/config"
	"github.com/vk9551/flowgate-io/internal/engine"
	"github.com/vk9551/flowgate-io/internal/store"
	"github.com/golang-jwt/jwt/v5"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// postOutcome sends POST /v1/events/{id}/outcome.
func postOutcome(t *testing.T, handler http.Handler, eventID string, body map[string]any, token string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/events/"+eventID+"/outcome", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// testCfg builds a minimal config for tests.
func testCfg(secret string) *config.Config {
	return &config.Config{
		Version: "1.0",
		Subject: config.SubjectCfg{
			IDField:       "user_id",
			TimezoneField: "user_tz",
			WakingHours:   config.WakingHours{Start: "07:00", End: "22:00"},
		},
		Priorities: []config.Priority{
			{
				Name:      "critical",
				Match:     []config.MatchRule{{Field: "type", In: []string{"otp", "order_confirmed"}}},
				BypassAll: true,
			},
			{
				Name:    "bulk",
				Match:   []config.MatchRule{{Field: "type", Prefix: "marketing_"}},
				Default: true,
			},
		},
		Policies: []config.Policy{
			{Priority: "critical", Decision: "act_now"},
			{
				Priority: "bulk",
				Window:   config.WindowCfg{RespectWakingHours: false}, // disable quiet hours for test simplicity
				Caps: []config.CapRule{
					{Scope: "subject", PeriodRaw: "1d", Period: 24 * time.Hour, Limit: 1},
				},
				DecisionOnCapBreach: "suppress",
			},
		},
		Server: config.ServerCfg{
			Port: 7700,
			Auth: config.AuthCfg{Type: "jwt", Secret: secret},
		},
		Outcomes: []config.OutcomeCfg{
			{Name: "success", RefundCap: false, Terminal: true},
			{Name: "failed_temp", RefundCap: true, Terminal: false},
			{Name: "failed_perm", RefundCap: true, Terminal: true},
			{Name: "pending", RefundCap: false, Terminal: false},
		},
		DefaultOutcome: "pending",
	}
}

// makeToken creates a signed HS256 JWT for tests.
func makeToken(secret string) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "test",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	s, _ := tok.SignedString([]byte(secret))
	return s
}

// newTestServer spins up a Server with an in-memory SQLite store.
func newTestServer(t *testing.T, secret string) (*Server, store.Store) {
	t.Helper()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	srv := NewServer("testdata/flowgate.yaml", testCfg(secret), st)
	return srv, st
}

// postEvent sends a POST /v1/events with the given body and auth token.
func postEvent(t *testing.T, handler http.Handler, body map[string]any, token string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestHealth_Public(t *testing.T) {
	srv, _ := newTestServer(t, "secret")
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("health: got %d, want 200", rr.Code)
	}
	var resp healthResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status: got %q, want ok", resp.Status)
	}
}

func TestAuth_UnauthenticatedRejected(t *testing.T) {
	srv, _ := newTestServer(t, "topsecret")
	rr := postEvent(t, srv.Handler(), map[string]any{"user_id": "u1", "type": "otp"}, "")
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestAuth_WrongTokenRejected(t *testing.T) {
	srv, _ := newTestServer(t, "topsecret")
	rr := postEvent(t, srv.Handler(), map[string]any{"user_id": "u1", "type": "otp"}, "not.a.token")
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestAuth_WrongSecretRejected(t *testing.T) {
	srv, _ := newTestServer(t, "topsecret")
	wrongToken := makeToken("different-secret")
	rr := postEvent(t, srv.Handler(), map[string]any{"user_id": "u1", "type": "otp"}, wrongToken)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestEvents_BypassAll_SendNow(t *testing.T) {
	const secret = "s3cr3t"
	srv, _ := newTestServer(t, secret)
	token := makeToken(secret)

	rr := postEvent(t, srv.Handler(), map[string]any{
		"user_id": "u1",
		"type":    "otp",
	}, token)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d body: %s", rr.Code, rr.Body.String())
	}

	var resp eventResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Decision != engine.OutcomeSendNow {
		t.Errorf("decision: got %q, want ACT_NOW", resp.Decision)
	}
	if resp.Reason != engine.ReasonBypassAll {
		t.Errorf("reason: got %q, want bypass_all", resp.Reason)
	}
	if resp.Priority != "critical" {
		t.Errorf("priority: got %q, want critical", resp.Priority)
	}
	if resp.EventID == "" {
		t.Error("event_id should be non-empty")
	}
}

func TestEvents_CapBreach_Suppress(t *testing.T) {
	const secret = "s3cr3t"
	srv, _ := newTestServer(t, secret)
	token := makeToken(secret)
	h := srv.Handler()

	// First event — under cap (limit=1).
	rr1 := postEvent(t, h, map[string]any{"user_id": "u1", "type": "marketing_weekly"}, token)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first event status: %d body: %s", rr1.Code, rr1.Body.String())
	}
	var r1 eventResponse
	json.NewDecoder(rr1.Body).Decode(&r1)
	if r1.Decision != engine.OutcomeSendNow {
		t.Errorf("first: got %q, want ACT_NOW", r1.Decision)
	}

	// Second event — cap breached.
	rr2 := postEvent(t, h, map[string]any{"user_id": "u1", "type": "marketing_weekly"}, token)
	if rr2.Code != http.StatusOK {
		t.Fatalf("second event status: %d body: %s", rr2.Code, rr2.Body.String())
	}
	var r2 eventResponse
	json.NewDecoder(rr2.Body).Decode(&r2)
	if r2.Decision != engine.OutcomeSuppress {
		t.Errorf("second: got %q, want SUPPRESS", r2.Decision)
	}
	if r2.Reason != engine.ReasonCapBreached {
		t.Errorf("reason: got %q, want cap_breached", r2.Reason)
	}
	if r2.SuppressedToday < 1 {
		t.Errorf("suppressed_today: got %d, want >= 1", r2.SuppressedToday)
	}
}

func TestEvents_MissingSubjectID(t *testing.T) {
	const secret = "s3cr3t"
	srv, _ := newTestServer(t, secret)
	token := makeToken(secret)

	rr := postEvent(t, srv.Handler(), map[string]any{"type": "otp"}, token) // no user_id
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestSubjectGet_NotFound(t *testing.T) {
	const secret = "s3cr3t"
	srv, _ := newTestServer(t, secret)
	token := makeToken(secret)

	req := httptest.NewRequest(http.MethodGet, "/v1/subjects/nobody", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestSubjectGet_WithHistory(t *testing.T) {
	const secret = "s3cr3t"
	srv, st := newTestServer(t, secret)
	token := makeToken(secret)
	h := srv.Handler()

	// Create the subject via POST /v1/events.
	postEvent(t, h, map[string]any{"user_id": "u42", "type": "otp"}, token)

	req := httptest.NewRequest(http.MethodGet, "/v1/subjects/u42", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body: %s", rr.Code, rr.Body.String())
	}
	var resp subjectResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Subject.ID != "u42" {
		t.Errorf("subject id: got %q", resp.Subject.ID)
	}
	if len(resp.History) != 1 {
		t.Errorf("history len: got %d, want 1", len(resp.History))
	}
	_ = st
}

func TestSubjectDelete_ResetsHistory(t *testing.T) {
	const secret = "s3cr3t"
	srv, _ := newTestServer(t, secret)
	token := makeToken(secret)
	h := srv.Handler()

	// Emit two events to u99 to build up history.
	postEvent(t, h, map[string]any{"user_id": "u99", "type": "otp"}, token)
	postEvent(t, h, map[string]any{"user_id": "u99", "type": "otp"}, token)

	// DELETE should reset.
	req := httptest.NewRequest(http.MethodDelete, "/v1/subjects/u99", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete: got %d body: %s", rr.Code, rr.Body.String())
	}

	// History should now be empty.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/subjects/u99", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)

	var resp subjectResponse
	json.NewDecoder(rr2.Body).Decode(&resp)
	if len(resp.History) != 0 {
		t.Errorf("after reset history len: got %d, want 0", len(resp.History))
	}
}

func TestPoliciesGet(t *testing.T) {
	const secret = "s3cr3t"
	srv, _ := newTestServer(t, secret)
	token := makeToken(secret)

	req := httptest.NewRequest(http.MethodGet, "/v1/policies", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var cfg config.Config
	if err := json.NewDecoder(rr.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cfg.Priorities) != 2 {
		t.Errorf("priorities: got %d", len(cfg.Priorities))
	}
}

func TestAuthNone_SkipsVerification(t *testing.T) {
	st, _ := store.OpenSQLite(":memory:")
	t.Cleanup(func() { st.Close() })

	cfg := testCfg("unused")
	cfg.Server.Auth.Type = "none"
	srv := NewServer("", cfg, st)

	rr := postEvent(t, srv.Handler(), map[string]any{"user_id": "u1", "type": "otp"}, "")
	// No token provided, auth=none — should reach the handler (200 or 422, not 401).
	if rr.Code == http.StatusUnauthorized {
		t.Errorf("auth=none should not return 401, got %d", rr.Code)
	}
}

// ── Outcome endpoint tests ───────────────────────────────────────────────────

func TestOutcome_ValidEvent_CapRefunded(t *testing.T) {
	const secret = "s3cr3t"
	srv, _ := newTestServer(t, secret)
	token := makeToken(secret)
	h := srv.Handler()

	// Create an event.
	rr := postEvent(t, h, map[string]any{"user_id": "u10", "type": "marketing_weekly"}, token)
	if rr.Code != http.StatusOK {
		t.Fatalf("post event: %d %s", rr.Code, rr.Body.String())
	}
	var evResp eventResponse
	json.NewDecoder(rr.Body).Decode(&evResp)

	// POST outcome: failed_temp (cap_refunded=true).
	rr2 := postOutcome(t, h, evResp.EventID, map[string]any{
		"outcome": "failed_temp",
		"reason":  "connection_timeout",
	}, token)
	if rr2.Code != http.StatusOK {
		t.Fatalf("post outcome: %d %s", rr2.Code, rr2.Body.String())
	}

	var or outcomeResponse
	if err := json.NewDecoder(rr2.Body).Decode(&or); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if or.EventID != evResp.EventID {
		t.Errorf("event_id: got %q, want %q", or.EventID, evResp.EventID)
	}
	if or.Outcome != "failed_temp" {
		t.Errorf("outcome: got %q, want failed_temp", or.Outcome)
	}
	if !or.CapRefunded {
		t.Error("cap_refunded: want true for failed_temp")
	}
	if or.PreviousOutcome != "pending" {
		t.Errorf("previous_outcome: got %q, want pending", or.PreviousOutcome)
	}
}

func TestOutcome_UnknownEventID_404(t *testing.T) {
	const secret = "s3cr3t"
	srv, _ := newTestServer(t, secret)
	token := makeToken(secret)

	rr := postOutcome(t, srv.Handler(), "no-such-event", map[string]any{"outcome": "success"}, token)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestOutcome_InvalidOutcomeName_400(t *testing.T) {
	const secret = "s3cr3t"
	srv, _ := newTestServer(t, secret)
	token := makeToken(secret)
	h := srv.Handler()

	rr := postEvent(t, h, map[string]any{"user_id": "u11", "type": "otp"}, token)
	var evResp eventResponse
	json.NewDecoder(rr.Body).Decode(&evResp)

	rr2 := postOutcome(t, h, evResp.EventID, map[string]any{"outcome": "not_a_real_outcome"}, token)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr2.Code)
	}
}

func TestSubjectGet_ChannelHealth_AfterFailedPerm(t *testing.T) {
	const secret = "s3cr3t"
	srv, _ := newTestServer(t, secret)
	token := makeToken(secret)
	h := srv.Handler()

	// Create event.
	rr := postEvent(t, h, map[string]any{"user_id": "u12", "type": "otp"}, token)
	var evResp eventResponse
	json.NewDecoder(rr.Body).Decode(&evResp)

	// Report failed_perm with channel=email.
	postOutcome(t, h, evResp.EventID, map[string]any{
		"outcome":  "failed_perm",
		"reason":   "hard_bounce",
		"metadata": map[string]any{"channel": "email"},
	}, token)

	// GET subject — channel_health should show email:failed_perm.
	req := httptest.NewRequest(http.MethodGet, "/v1/subjects/u12", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req)

	if rr2.Code != http.StatusOK {
		t.Fatalf("get subject: %d %s", rr2.Code, rr2.Body.String())
	}
	var resp subjectResponse
	if err := json.NewDecoder(rr2.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Subject.ChannelHealth["email"] != "failed_perm" {
		t.Errorf("channel_health[email]: got %q, want failed_perm", resp.Subject.ChannelHealth["email"])
	}
}
