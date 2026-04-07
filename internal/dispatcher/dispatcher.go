// Package dispatcher sends webhook callbacks with exponential-backoff retry.
package dispatcher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/vk9551/flowgate-io/internal/config"
	"github.com/vk9551/flowgate-io/internal/store"
)

const (
	defaultRetries     = 3
	defaultBackoffBase = time.Second
	maxBackoff         = 30 * time.Second
	attemptTimeout     = 10 * time.Second
)

// CallbackPayload is the JSON body posted to a callback URL.
type CallbackPayload struct {
	EventID         string    `json:"event_id"`
	SubjectID       string    `json:"subject_id"`
	Decision        string    `json:"decision"`
	Reason          string    `json:"reason"`
	Priority        string    `json:"priority"`
	OriginalPayload string    `json:"original_payload"`
	FiredAt         time.Time `json:"fired_at"`
}

// DigestPayload is posted to the digest_ready callback URL.
type DigestPayload struct {
	SubjectID string        `json:"subject_id"`
	Priority  string        `json:"priority"`
	Items     []DigestItem  `json:"items"`
	FiredAt   time.Time     `json:"fired_at"`
}

// DigestItem mirrors scheduler.DigestItem to avoid circular imports.
type DigestItem struct {
	EventID string `json:"event_id"`
	Payload string `json:"payload"`
}

// Dispatcher sends webhook callbacks. It is safe for concurrent use.
type Dispatcher struct {
	getCfg func() *config.Config
	client *http.Client
}

// New creates a Dispatcher. getCfg is called on each Dispatch so the dispatcher
// always uses the live config (supports hot-reload).
func New(getCfg func() *config.Config) *Dispatcher {
	return &Dispatcher{
		getCfg: getCfg,
		client: &http.Client{Timeout: attemptTimeout},
	}
}

// Dispatch fires a callback for a delayed event that has become due.
// callbackType selects the URL: "send_now", "suppressed", or "digest_ready".
// An empty or unconfigured URL is silently skipped.
func (d *Dispatcher) Dispatch(e *store.ScheduledEvent, callbackType string) error {
	cfg := d.getCfg()
	target := resolveTarget(cfg, callbackType)
	if target == nil || target.URL == "" {
		log.Printf("dispatcher: no callback URL for %q — skipping event %s", callbackType, e.ID)
		return nil
	}

	payload := CallbackPayload{
		EventID:         e.ID,
		SubjectID:       e.SubjectID,
		Decision:        callbackType,
		Priority:        e.Priority,
		OriginalPayload: e.Payload,
		FiredAt:         time.Now().UTC(),
	}

	return d.sendWithRetry(target, payload)
}

// DispatchDigest fires the digest_ready callback for a batch of suppressed events.
func (d *Dispatcher) DispatchDigest(subjectID, priority string, items []DigestItem) error {
	cfg := d.getCfg()
	target := resolveTarget(cfg, "digest_ready")
	if target == nil || target.URL == "" {
		log.Printf("dispatcher: no digest_ready URL — skipping digest for %s/%s", subjectID, priority)
		return nil
	}

	payload := DigestPayload{
		SubjectID: subjectID,
		Priority:  priority,
		Items:     items,
		FiredAt:   time.Now().UTC(),
	}

	return d.sendWithRetry(target, payload)
}

// sendWithRetry POSTs payload as JSON with exponential backoff.
func (d *Dispatcher) sendWithRetry(target *config.CallbackTarget, payload any) error {
	retries := target.Retries
	if retries <= 0 {
		retries = defaultRetries
	}

	backoffBase := defaultBackoffBase
	if target.BackoffBase != "" {
		if parsed, err := time.ParseDuration(target.BackoffBase); err == nil {
			backoffBase = parsed
		}
	}

	method := target.Method
	if method == "" {
		method = http.MethodPost
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("dispatcher: marshal payload: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < retries; attempt++ {
		if attempt > 0 {
			delay := backoff(backoffBase, attempt)
			time.Sleep(delay)
		}

		req, err := http.NewRequest(method, target.URL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("dispatcher: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := d.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("attempt %d: %w", attempt+1, err)
			log.Printf("dispatcher: %v", lastErr)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("attempt %d: HTTP %d from %s", attempt+1, resp.StatusCode, target.URL)
		log.Printf("dispatcher: %v", lastErr)
	}

	return fmt.Errorf("dispatcher: all %d attempt(s) failed for %s: %w", retries, target.URL, lastErr)
}

// resolveTarget returns the CallbackTarget for a given callback type.
func resolveTarget(cfg *config.Config, callbackType string) *config.CallbackTarget {
	switch callbackType {
	case "send_now":
		return cfg.Callbacks.SendNow
	case "suppressed":
		return cfg.Callbacks.Suppressed
	case "digest_ready":
		return cfg.Callbacks.DigestReady
	case "delayed":
		return cfg.Callbacks.Delayed
	default:
		return nil
	}
}

// backoff computes min(base * 2^(attempt-1), maxBackoff).
func backoff(base time.Duration, attempt int) time.Duration {
	exp := math.Pow(2, float64(attempt-1))
	d := time.Duration(float64(base) * exp)
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}
