// Package telemetry provides a non-blocking, bounded in-memory sink for the
// engine's per-tick lifecycle telemetry (sim.TickTelemetryRecord). It is the
// concrete sim.TickTelemetrySink the umbilical read surface (httpapi
// /umbilical/telemetry) snapshots — the engine writes records on the world
// goroutine, the umbilical reads them on HTTP goroutines, and a RingSink
// bridges the two without ever blocking the writer.
//
// Why a ring buffer: the sink contract (engine/sim/repo.go) requires
// WriteTickTelemetry to be non-blocking — it runs on the world goroutine (the
// reactor evaluator's "deferred" records) and on the worker-pool goroutines,
// and must never wait on a consumer. A fixed-capacity ring drops the OLDEST
// record on overflow rather than blocking or growing unbounded, so a slow or
// absent reader can never stall the engine or leak memory. The umbilical is a
// debug/introspection surface, so losing the oldest records under churn is the
// right trade (recent history is what a debugger wants).
package telemetry

import (
	"sync"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// DefaultCapacity is the record retention used when New is given a
// non-positive capacity. ~2k records covers a meaningful window of recent tick
// lifecycle activity for debugging without holding much memory (each record is
// a few small fields + a tiny redacted detail map).
const DefaultCapacity = 2048

// Stats is a point-in-time summary of a RingSink, surfaced by the umbilical so
// an operator can tell whether the buffer is saturating (Dropped climbing) and
// how much history is currently retained.
type Stats struct {
	// Capacity is the fixed ring size (max records retained).
	Capacity int
	// Size is how many records are currently buffered (0..Capacity).
	Size int
	// Written is the total records ever accepted (monotonic).
	Written uint64
	// Dropped is the total records evicted because the ring was full when a
	// newer record arrived (monotonic). Dropped > 0 means the reader is not
	// keeping up with the retention window — not an error, just back-pressure
	// the ring absorbed on the engine's behalf.
	Dropped uint64
}

// RingSink is a fixed-capacity ring buffer of TickTelemetryRecords, safe for
// concurrent writers and readers. The mutex is held only for the duration of a
// slice index write / copy — never across I/O or a channel op — so a writer is
// never blocked waiting on a consumer (the non-blocking contract).
type RingSink struct {
	mu      sync.Mutex
	buf     []sim.TickTelemetryRecord
	head    int    // index of the oldest record when full; insertion point chases it
	size    int    // current number of buffered records (0..cap)
	written uint64 // total accepted (monotonic)
	dropped uint64 // total evicted on overflow (monotonic)
}

// New builds a RingSink retaining the last capacity records. A non-positive
// capacity falls back to DefaultCapacity.
func New(capacity int) *RingSink {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &RingSink{buf: make([]sim.TickTelemetryRecord, capacity)}
}

// WriteTickTelemetry records rec, evicting the oldest entry if the ring is
// full. Implements sim.TickTelemetrySink. Non-blocking by construction: it only
// takes the mutex for a single index write. The caller (the world goroutine /
// worker pool) is never made to wait on a reader.
func (r *RingSink) WriteTickTelemetry(rec sim.TickTelemetryRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(r.buf)
	// Insertion point is head + size, wrapping. While not full, size grows;
	// once full, every write overwrites the slot at head (the oldest) and
	// advances head, so the buffer always holds the most recent n records.
	idx := (r.head + r.size) % n
	r.buf[idx] = rec
	r.written++
	if r.size < n {
		r.size++
		return
	}
	// Full: we just overwrote the oldest. Advance head to the new oldest.
	r.head = (r.head + 1) % n
	r.dropped++
}

// Snapshot returns the buffered records in chronological order (oldest first).
// The returned slice is a fresh copy the caller owns — it never aliases the
// ring's backing array, so a concurrent write can't mutate it mid-read.
func (r *RingSink) Snapshot() []sim.TickTelemetryRecord {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]sim.TickTelemetryRecord, r.size)
	n := len(r.buf)
	for i := 0; i < r.size; i++ {
		out[i] = r.buf[(r.head+i)%n]
	}
	return out
}

// Stats returns a point-in-time summary (capacity, current size, lifetime
// written/dropped counts).
func (r *RingSink) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Stats{
		Capacity: len(r.buf),
		Size:     r.size,
		Written:  r.written,
		Dropped:  r.dropped,
	}
}
