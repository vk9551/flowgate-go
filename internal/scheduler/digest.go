package scheduler

import (
	"sync"
	"time"

	"github.com/vk9551/flowgate-io/internal/config"
)

// DigestItem is a single suppressed event collected for a digest batch.
type DigestItem struct {
	EventID string `json:"event_id"`
	Payload string `json:"payload"`
}

// DigestReadyFunc is called when a digest batch is ready to deliver.
type DigestReadyFunc func(subjectID, priority string, items []DigestItem)

// DigestCollector groups suppressed events by subject+priority and fires a
// digest callback after the configured wait duration or when MaxItems is hit.
type DigestCollector struct {
	mu      sync.Mutex
	groups  map[string]*digestGroup
	onReady DigestReadyFunc
}

type digestGroup struct {
	items []DigestItem
	timer *time.Timer
}

// NewDigestCollector creates a DigestCollector. onReady is called (in its own
// goroutine) when a digest batch is ready.
func NewDigestCollector(onReady DigestReadyFunc) *DigestCollector {
	return &DigestCollector{
		groups:  make(map[string]*digestGroup),
		onReady: onReady,
	}
}

// Add records a suppressed event into the appropriate digest group.
// If digest is not enabled for this policy, Add is a no-op.
func (dc *DigestCollector) Add(subjectID, priority, eventID, payload string, d config.DigestCfg) {
	if !d.Enabled {
		return
	}

	wait := d.Wait
	if wait <= 0 {
		wait = 4 * time.Hour // sensible default
	}
	maxItems := d.MaxItems

	key := subjectID + ":" + priority

	dc.mu.Lock()
	defer dc.mu.Unlock()

	g, exists := dc.groups[key]
	if !exists {
		g = &digestGroup{}
		dc.groups[key] = g
		// Start the wait timer on first item in the group.
		g.timer = time.AfterFunc(wait, func() {
			dc.flush(subjectID, priority, key)
		})
	}

	g.items = append(g.items, DigestItem{EventID: eventID, Payload: payload})

	// Fire immediately if we've hit the item cap.
	if maxItems > 0 && len(g.items) >= maxItems {
		g.timer.Stop()
		go dc.flush(subjectID, priority, key)
	}
}

// flush fires the onReady callback for the group and removes it from the map.
func (dc *DigestCollector) flush(subjectID, priority, key string) {
	dc.mu.Lock()
	g, ok := dc.groups[key]
	if !ok {
		dc.mu.Unlock()
		return
	}
	items := make([]DigestItem, len(g.items))
	copy(items, g.items)
	delete(dc.groups, key)
	dc.mu.Unlock()

	if len(items) > 0 {
		dc.onReady(subjectID, priority, items)
	}
}
