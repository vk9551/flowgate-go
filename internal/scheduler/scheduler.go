// Package scheduler implements an in-process, heap-based delay queue.
// Events are persisted to the store first so they survive restarts.
package scheduler

import (
	"container/heap"
	"context"
	"log"
	"sync"
	"time"

	"github.com/vk9551/flowgate-io/internal/store"
)

// FireFunc is called when a scheduled event becomes due.
// It is called from the scheduler's internal goroutine; implementations
// must not block for long.
type FireFunc func(e *store.ScheduledEvent)

// Scheduler is a min-heap delay queue backed by a persistent store.
// All exported methods are safe for concurrent use.
type Scheduler struct {
	mu       sync.Mutex
	h        eventHeap
	st       store.Store
	onFire   FireFunc
	interval time.Duration // tick interval; default 5s

	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a Scheduler. interval is how often the heap is checked;
// pass 0 to use the default (5 s). onFire is called for each due event.
func New(st store.Store, onFire FireFunc, interval time.Duration) *Scheduler {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Scheduler{
		st:       st,
		onFire:   onFire,
		interval: interval,
		done:     make(chan struct{}),
	}
}

// Start loads all pending events from the store into the heap, then begins
// the tick loop. It returns an error only if the initial store load fails.
// The loop exits when ctx is cancelled or Stop is called.
func (s *Scheduler) Start(ctx context.Context) error {
	events, err := s.st.ScheduledListAll()
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.h = make(eventHeap, 0, len(events))
	for _, e := range events {
		s.h = append(s.h, e)
	}
	heap.Init(&s.h)
	s.mu.Unlock()

	log.Printf("scheduler: loaded %d pending event(s) from store", len(events))

	loopCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	go s.loop(loopCtx)
	return nil
}

// Schedule persists e to the store and pushes it onto the heap.
func (s *Scheduler) Schedule(e *store.ScheduledEvent) error {
	if err := s.st.ScheduledInsert(e); err != nil {
		return err
	}
	s.mu.Lock()
	heap.Push(&s.h, e)
	s.mu.Unlock()
	return nil
}

// Stop shuts down the tick loop and waits for it to exit.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	<-s.done
}

// loop is the internal tick goroutine.
func (s *Scheduler) loop(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.fire(now)
		}
	}
}

// fire pops and dispatches all events whose DeliverAt <= now.
func (s *Scheduler) fire(now time.Time) {
	for {
		s.mu.Lock()
		if s.h.Len() == 0 || s.h[0].DeliverAt.After(now) {
			s.mu.Unlock()
			return
		}
		e := heap.Pop(&s.h).(*store.ScheduledEvent)
		s.mu.Unlock()

		// Delete from store before firing so a crash-restart won't re-fire it.
		if err := s.st.ScheduledDelete(e.ID); err != nil {
			log.Printf("scheduler: delete event %q from store: %v", e.ID, err)
		}

		go s.onFire(e)
	}
}

// ── heap implementation ───────────────────────────────────────────────────────

type eventHeap []*store.ScheduledEvent

func (h eventHeap) Len() int            { return len(h) }
func (h eventHeap) Less(i, j int) bool  { return h[i].DeliverAt.Before(h[j].DeliverAt) }
func (h eventHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *eventHeap) Push(x any)         { *h = append(*h, x.(*store.ScheduledEvent)) }
func (h *eventHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	old[n-1] = nil // avoid memory leak
	*h = old[:n-1]
	return x
}
