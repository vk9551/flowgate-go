package dispatcher

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vk9551/flowgate-io/internal/config"
	"github.com/vk9551/flowgate-io/internal/store"
)

func testEvent(id string) *store.ScheduledEvent {
	return &store.ScheduledEvent{
		ID:        id,
		SubjectID: "u1",
		Priority:  "bulk",
		Payload:   `{"type":"newsletter"}`,
		DeliverAt: time.Now().UTC(),
		CreatedAt: time.Now().UTC(),
	}
}

// cfgWithURL builds a minimal config pointing send_now callbacks at url.
func cfgWithURL(url string) *config.Config {
	return &config.Config{
		Callbacks: config.CallbacksCfg{
			SendNow: &config.CallbackTarget{
				URL:         url,
				Method:      "POST",
				Retries:     3,
				BackoffBase: "1ms", // fast retries for tests
			},
		},
	}
}

func TestDispatch_Success(t *testing.T) {
	var gotBody CallbackPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %q, want POST", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := cfgWithURL(srv.URL)
	d := New(func() *config.Config { return cfg })

	evt := testEvent("evt-success")
	if err := d.Dispatch(evt, "send_now"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if gotBody.EventID != "evt-success" {
		t.Errorf("event_id: got %q", gotBody.EventID)
	}
	if gotBody.SubjectID != "u1" {
		t.Errorf("subject_id: got %q", gotBody.SubjectID)
	}
	if gotBody.Priority != "bulk" {
		t.Errorf("priority: got %q", gotBody.Priority)
	}
	if gotBody.OriginalPayload != `{"type":"newsletter"}` {
		t.Errorf("original_payload: got %q", gotBody.OriginalPayload)
	}
	if gotBody.FiredAt.IsZero() {
		t.Error("fired_at should not be zero")
	}
}

// TestDispatch_RetryOn500 verifies that a 500 response triggers a retry and
// the dispatcher succeeds once the server returns 200.
func TestDispatch_RetryOn500(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := cfgWithURL(srv.URL)
	d := New(func() *config.Config { return cfg })

	if err := d.Dispatch(testEvent("retry-evt"), "send_now"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if callCount.Load() != 2 {
		t.Errorf("expected 2 attempts, got %d", callCount.Load())
	}
}

// TestDispatch_GivesUpAfterMaxAttempts verifies an error is returned when all
// retries are exhausted.
func TestDispatch_GivesUpAfterMaxAttempts(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := cfgWithURL(srv.URL)
	d := New(func() *config.Config { return cfg })

	err := d.Dispatch(testEvent("fail-evt"), "send_now")
	if err == nil {
		t.Fatal("expected error after max attempts, got nil")
	}
	if callCount.Load() != 3 {
		t.Errorf("expected 3 attempts (Retries=3), got %d", callCount.Load())
	}
}

// TestDispatch_EmptyURLSkipsSilently verifies that a missing callback URL
// is not treated as an error.
func TestDispatch_EmptyURLSkipsSilently(t *testing.T) {
	cfg := &config.Config{
		Callbacks: config.CallbacksCfg{
			// send_now is nil → no URL configured
		},
	}
	d := New(func() *config.Config { return cfg })
	if err := d.Dispatch(testEvent("skip-evt"), "send_now"); err != nil {
		t.Errorf("expected nil error for empty URL, got: %v", err)
	}
}

// TestDispatch_NetworkError retries on connection-refused and ultimately fails.
func TestDispatch_NetworkError(t *testing.T) {
	cfg := cfgWithURL("http://127.0.0.1:1") // port 1 is always refused
	d := New(func() *config.Config { return cfg })
	err := d.Dispatch(testEvent("net-err"), "send_now")
	if err == nil {
		t.Fatal("expected error for unreachable host, got nil")
	}
}

// TestDispatchDigest_Success verifies the digest payload shape.
func TestDispatchDigest_Success(t *testing.T) {
	var gotBody DigestPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.Config{
		Callbacks: config.CallbacksCfg{
			DigestReady: &config.CallbackTarget{
				URL:         srv.URL,
				Method:      "POST",
				Retries:     1,
				BackoffBase: "1ms",
			},
		},
	}
	d := New(func() *config.Config { return cfg })

	items := []DigestItem{
		{EventID: "e1", Payload: `{"type":"newsletter"}`},
		{EventID: "e2", Payload: `{"type":"newsletter"}`},
	}
	if err := d.DispatchDigest("u1", "bulk", items); err != nil {
		t.Fatalf("DispatchDigest: %v", err)
	}

	if gotBody.SubjectID != "u1" {
		t.Errorf("subject_id: got %q", gotBody.SubjectID)
	}
	if gotBody.Priority != "bulk" {
		t.Errorf("priority: got %q", gotBody.Priority)
	}
	if len(gotBody.Items) != 2 {
		t.Errorf("items len: got %d, want 2", len(gotBody.Items))
	}
}
