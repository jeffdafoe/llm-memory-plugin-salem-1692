package main

// Per-tick batch buffer for chronicler dispatches triggered by scheduled
// agent-NPC shift boundaries.
//
// Origin: with the chronicler-dispatch redesign, agent NPCs are no longer
// walked to/from work by the worker scheduler. Instead, the worker
// scheduler enqueues a shift_start / shift_end event for each agent NPC
// hitting a boundary; the chronicler then receives those events in its
// next perception and decides whether to attend each NPC. Decorative
// NPCs continue to use the walk path unchanged.
//
// Batching: events sharing (event_type, boundary_minute) fold into one
// batch. Two NPCs starting their shift at the same minute produce one
// chronicler dispatch listing both, not two separate fires.
//
// Drain semantics: the queue is drained exactly once per chronicler fire
// at perception build time. Phase fires, cascade fires, and the dedicated
// shift-boundary dispatcher all drain through the same path, so any fire
// happening at or after enqueue time picks up the pending events. The
// dedicated shift-boundary dispatcher is the safety net for the case
// where no other fire happens in the same tick.
//
// Concurrency: `dispatchScheduledBehaviors` (the enqueue site) runs
// single-goroutine via the server tick. Drain is called from chronicler
// fires which can run concurrently (cascade fires from arrivals or PC
// speech). The mutex protects against the concurrent-drain race.

import (
	"sync"
	"time"
)

// chroniclerDispatchEventType discriminates the kind of scheduled boundary
// the queue is reporting. Held as a string for direct rendering into the
// chronicler perception without a separate label table.
type chroniclerDispatchEventType string

const (
	dispatchShiftStart chroniclerDispatchEventType = "shift_start"
	dispatchShiftEnd   chroniclerDispatchEventType = "shift_end"
)

// chroniclerDispatchAgent is one agent-NPC entry within a batch. All
// fields are pre-resolved at enqueue time so the perception render is a
// pure formatting step (no DB roundtrips per agent at fire time).
type chroniclerDispatchAgent struct {
	ID           string
	DisplayName  string
	CurrentPlace string // "the Inn" / "the open village" / etc.
	WorkPlace    string // "the Blacksmith"
	ShiftStart   string // "07:00"
	ShiftEnd     string // "19:00"
}

// chroniclerDispatchBatch is one (event_type, boundary_minute) group with
// all the agents to surface to the chronicler.
type chroniclerDispatchBatch struct {
	EventType  chroniclerDispatchEventType
	BoundaryAt time.Time
	Agents     []chroniclerDispatchAgent
}

// chroniclerBatchKey is the dedup key for batching. Two enqueues with the
// same (event_type, boundary_minute) fold into the same batch entry.
type chroniclerBatchKey struct {
	EventType chroniclerDispatchEventType
	UnixMin   int64
}

// chroniclerDispatchQueue is the per-process buffer. Held on App so all
// callers share one queue.
type chroniclerDispatchQueue struct {
	mu      sync.Mutex
	batches map[chroniclerBatchKey]*chroniclerDispatchBatch
}

func newChroniclerDispatchQueue() *chroniclerDispatchQueue {
	return &chroniclerDispatchQueue{
		batches: make(map[chroniclerBatchKey]*chroniclerDispatchBatch),
	}
}

// enqueue adds an agent to the batch keyed on (eventType, boundaryAt).
// Same-key enqueues append to the existing batch's agent list.
//
// Nil-safe: returns silently when q is nil so partially-constructed App
// instances (tests, alternate initializers) don't panic. Production
// main() always initializes the queue.
func (q *chroniclerDispatchQueue) enqueue(eventType chroniclerDispatchEventType, boundaryAt time.Time, agent chroniclerDispatchAgent) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	key := chroniclerBatchKey{
		EventType: eventType,
		UnixMin:   boundaryAt.Unix() / 60,
	}
	b, ok := q.batches[key]
	if !ok {
		b = &chroniclerDispatchBatch{
			EventType:  eventType,
			BoundaryAt: boundaryAt,
		}
		q.batches[key] = b
	}
	b.Agents = append(b.Agents, agent)
}

// drain returns all queued batches and clears the queue. Called from
// fireChronicler before perception build so the destructive action is
// tied to an actual chronicler invocation, not perception formatting.
// Returns nil when the queue is empty so callers can cheaply check
// `len(drained) == 0`.
//
// Nil-safe: returns nil when q is nil.
func (q *chroniclerDispatchQueue) drain() []*chroniclerDispatchBatch {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.batches) == 0 {
		return nil
	}
	out := make([]*chroniclerDispatchBatch, 0, len(q.batches))
	for _, b := range q.batches {
		out = append(out, b)
	}
	q.batches = make(map[chroniclerBatchKey]*chroniclerDispatchBatch)
	return out
}

// pending reports the number of queued batches without draining. Used by
// the dedicated shift dispatcher to decide whether to fire the chronicler
// (a separate dispatch isn't worth it if a phase or cascade fire just
// drained the queue this tick).
//
// Nil-safe: returns 0 when q is nil.
func (q *chroniclerDispatchQueue) pending() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.batches)
}
