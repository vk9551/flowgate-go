package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/vk9551/flowgate-io/internal/store"
)

func openMem(t *testing.T) store.Store {
	t.Helper()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func makeEvent(id string, deliverAt time.Time) *store.ScheduledEvent {
	return &store.ScheduledEvent{
		ID:        id,
		SubjectID: "u1",
		Priority:  "bulk",
		Payload:   `{}`,
		DeliverAt: deliverAt,
		CreatedAt: time.Now().UTC(),
	}
}

// TestScheduler_FiresAfterDelay verifies that an event scheduled 50ms in the
// future fires within a reasonable window when the tick interval is short.
func TestScheduler_FiresAfterDelay(t *testing.T) {
	st := openMem(t)

	fired := make(chan *store.ScheduledEvent, 1)
	sched := New(st, func(e *store.ScheduledEvent) { fired <- e }, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sched.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deliverAt := time.Now().Add(50 * time.Millisecond)
	evt := makeEvent("e1", deliverAt)
	if err := sched.Schedule(evt); err != nil {
		t.Fatalf("Schedule: %v", err)
	}

	select {
	case got := <-fired:
		if got.ID != "e1" {
			t.Errorf("fired event ID: got %q, want e1", got.ID)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("event did not fire within 500ms")
	}
}

// TestScheduler_HeapOrdering verifies that earlier events fire before later ones.
func TestScheduler_HeapOrdering(t *testing.T) {
	st := openMem(t)

	var mu sync.Mutex
	var order []string
	done := make(chan struct{})

	sched := New(st, func(e *store.ScheduledEvent) {
		mu.Lock()
		order = append(order, e.ID)
		if len(order) == 3 {
			close(done)
		}
		mu.Unlock()
	}, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sched.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	now := time.Now()
	// Schedule in reverse order so heap must reorder them.
	sched.Schedule(makeEvent("third", now.Add(120*time.Millisecond)))
	sched.Schedule(makeEvent("first", now.Add(40*time.Millisecond)))
	sched.Schedule(makeEvent("second", now.Add(80*time.Millisecond)))

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("not all events fired within 1s")
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"first", "second", "third"}
	for i, id := range want {
		if i >= len(order) || order[i] != id {
			t.Errorf("fire order[%d]: got %q, want %q (full order: %v)", i, order[i], id, order)
		}
	}
}

// TestScheduler_GracefulShutdown verifies Stop() returns without hanging.
func TestScheduler_GracefulShutdown(t *testing.T) {
	st := openMem(t)
	sched := New(st, func(e *store.ScheduledEvent) {}, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	if err := sched.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Schedule a far-future event that should never fire.
	sched.Schedule(makeEvent("far", time.Now().Add(time.Hour)))

	stopDone := make(chan struct{})
	go func() {
		cancel()
		sched.Stop()
		close(stopDone)
	}()

	select {
	case <-stopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return within 2s")
	}
}

// TestScheduler_PersistenceAcrossRestart verifies that a scheduled event
// survives a scheduler restart by being reloaded from the store.
func TestScheduler_PersistenceAcrossRestart(t *testing.T) {
	st := openMem(t)

	// --- First scheduler instance: schedule an event but don't let it fire. ---
	sched1 := New(st, func(e *store.ScheduledEvent) {}, time.Hour) // large interval → never ticks
	ctx1, cancel1 := context.WithCancel(context.Background())
	if err := sched1.Start(ctx1); err != nil {
		t.Fatalf("sched1 Start: %v", err)
	}

	deliverAt := time.Now().Add(50 * time.Millisecond)
	if err := sched1.Schedule(makeEvent("persist-me", deliverAt)); err != nil {
		t.Fatalf("Schedule: %v", err)
	}

	// Shut down first scheduler before the event fires.
	cancel1()
	sched1.Stop()

	// --- Second scheduler instance: reload from store and fire. ---
	fired := make(chan *store.ScheduledEvent, 1)
	sched2 := New(st, func(e *store.ScheduledEvent) { fired <- e }, 10*time.Millisecond)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	if err := sched2.Start(ctx2); err != nil {
		t.Fatalf("sched2 Start: %v", err)
	}

	select {
	case got := <-fired:
		if got.ID != "persist-me" {
			t.Errorf("persisted event ID: got %q", got.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("persisted event did not fire after restart")
	}
}

// TestScheduler_FireDeletesFromStore verifies the event is removed from
// the store after firing so it doesn't re-fire on next restart.
func TestScheduler_FireDeletesFromStore(t *testing.T) {
	st := openMem(t)

	fired := make(chan struct{}, 1)
	sched := New(st, func(e *store.ScheduledEvent) { fired <- struct{}{} }, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sched.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	sched.Schedule(makeEvent("deleteme", time.Now().Add(30*time.Millisecond)))

	select {
	case <-fired:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("event did not fire")
	}

	// Give a moment for the delete to complete.
	time.Sleep(20 * time.Millisecond)

	all, err := st.ScheduledListAll()
	if err != nil {
		t.Fatalf("ScheduledListAll: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected 0 events in store after fire, got %d", len(all))
	}
}
