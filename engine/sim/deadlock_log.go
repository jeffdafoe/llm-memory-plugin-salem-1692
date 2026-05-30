package sim

import (
	"sync"
	"time"
)

// deadlock_log.go — bounded in-memory ring of recent stable soft-block
// events (ZBBS-WORK-340). The locomotion ticker calls World.RecordDeadlock
// each time advanceActorLocomotion hits the per-MoveIntent stuck-tick cap;
// the umbilical /api/village/umbilical/deadlocks read route dumps the ring
// so operators can see how often live play is contending and which actors
// keep wedging each other — the signal the engine couldn't get from a
// count-only counter.
//
// As of ZBBS-HOME-327 the recorded mover is NOT frozen: it records the entry
// (the contention canary) and then walks THROUGH the blocking actor and
// continues. The ring is therefore a "stable block resolved by walk-through"
// log, not a "mover is stuck forever" log — a non-empty ring means contention
// happened and was forced past, not that anyone is wedged. (Member-own-door
// blocks resolve earlier via ZBBS-HOME-348's immediate walk-through and never
// reach this record, so the ring skews toward corridor / mutual-block /
// non-member-door contention.)
//
// In-memory + lossy-on-restart: transient diagnostics, no durability need,
// so no Postgres (see shared GUIDELINES). Same shape as the server-observed
// errorRing in httpapi/errorlog.go, but lives on World because the data
// originates inside the world goroutine's locomotion tick rather than at
// the HTTP transport boundary.

// defaultDeadlockRingSize bounds the recent-deadlock ring. Same as
// httpapi.defaultErrorRingSize — generous enough that a busy stretch
// doesn't flush the ring before an operator can look, small enough that
// a runaway never bloats memory.
const defaultDeadlockRingSize = 256

// DeadlockEntry is one stable-soft-block event recorded for operator
// visibility (the mover walks through and continues — ZBBS-HOME-327).
// Flattened from MoveDestination's tagged union so the wire
// payload is plain values — Position-kind moves carry a non-zero
// DestPosition and empty ids; structure-kind moves carry a non-empty
// DestStructureID; object_visit moves carry a non-empty DestObjectID. The
// other fields are zero-valued (the convention used by the rest of the v2
// wire surface).
type DeadlockEntry struct {
	Time time.Time `json:"time"`

	MoverID   ActorID  `json:"mover_id"`
	MoverName string   `json:"mover_name"`
	MoverPos  Position `json:"mover_pos"`

	DestinationKind MoveDestinationKind `json:"destination_kind"`
	DestStructureID StructureID         `json:"destination_structure_id,omitempty"`
	DestObjectID    VillageObjectID     `json:"destination_object_id,omitempty"`
	DestPosition    Position            `json:"destination_position,omitempty"`

	// OccupantID/OccupantName identify the actor whose tile was the
	// immediate next-tile blocker at the moment the stuck counter tripped —
	// the actor the mover then walks through. May be empty if the occupant
	// left the tile between the soft-block classification and the record
	// (race-safe — empty fields just mean "we couldn't identify the occupant
	// at record time").
	OccupantID   ActorID  `json:"occupant_id,omitempty"`
	OccupantName string   `json:"occupant_name,omitempty"`
	OccupantTile Position `json:"occupant_tile"`

	// ReplanFailed distinguishes the two flavors of stable block the operator
	// cares about (the mover walks through either way — ZBBS-HOME-327 — but
	// the cause differs):
	//   - true  → re-plan with the occupant tile blocked returned no path.
	//             No alternative route exists (sleeping-Abraham-in-the-doorway
	//             pattern); the mover had to force straight through.
	//   - false → re-plan found an alt path but its first tile was ALSO
	//             occupied, repeatedly, for the full stuck-tick window.
	//             Usually a mutual block or a clogged corridor.
	ReplanFailed bool `json:"replan_failed"`
}

// DeadlockLog is a fixed-size circular buffer of recent DeadlockEntry,
// overwriting oldest-first once full. Mutex-guarded — record is called on
// the world goroutine from advanceActorLocomotion; Snapshot is called from
// HTTP request goroutines (the umbilical handler).
type DeadlockLog struct {
	mu      sync.Mutex
	entries []DeadlockEntry
	next    int
	full    bool
}

// newDeadlockLog returns an initialized ring sized to size. A non-positive
// size falls back to defaultDeadlockRingSize. Called from NewWorld so every
// World (production and test) has a non-nil log.
func newDeadlockLog(size int) *DeadlockLog {
	if size <= 0 {
		size = defaultDeadlockRingSize
	}
	return &DeadlockLog{entries: make([]DeadlockEntry, size)}
}

// record appends one DeadlockEntry to the ring. Nil-safe: a registry-less
// World (a hand-built test fixture that bypassed NewWorld) silently skips,
// so the locomotion ticker never panics a caller.
func (r *DeadlockLog) record(e DeadlockEntry) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.entries[r.next] = e
	r.next = (r.next + 1) % len(r.entries)
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

// Snapshot returns the recorded entries oldest→newest as a fresh slice
// safe to read off the world goroutine. Nil-safe (a nil receiver returns
// nil — mirrors TickerHealth.Snapshot).
func (r *DeadlockLog) Snapshot() []DeadlockEntry {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]DeadlockEntry, r.next)
		copy(out, r.entries[:r.next])
		return out
	}
	out := make([]DeadlockEntry, 0, len(r.entries))
	out = append(out, r.entries[r.next:]...)
	out = append(out, r.entries[:r.next]...)
	return out
}

// RecordDeadlock appends one entry to the world's deadlock log. Called from
// advanceActorLocomotion when the per-MoveIntent stuck counter trips and
// the mover hard-stops with MoveStoppedDeadlocked. Nil-safe via the field
// accessor.
func (w *World) RecordDeadlock(e DeadlockEntry) {
	w.deadlockLog.record(e)
}

// DeadlockSnapshot returns the current recent-deadlock view. Read by the
// umbilical /deadlocks route; safe to call from any goroutine.
func (w *World) DeadlockSnapshot() []DeadlockEntry {
	return w.deadlockLog.Snapshot()
}

// destToView flattens a MoveDestination's tagged-union pointers into the
// plain-value subset DeadlockEntry carries (kind, optional structure id,
// optional object id, optional position). The kind disambiguates which
// sibling field is the "real" destination: structure_enter/structure_visit
// means the structure id is set; object_visit means the object id is set;
// position means the position is set. Sibling fields are zero-valued —
// the empty-id convention used everywhere else on the v2 wire surface.
func destToView(d MoveDestination) (MoveDestinationKind, StructureID, VillageObjectID, Position) {
	var sid StructureID
	var oid VillageObjectID
	var pos Position
	if d.StructureID != nil {
		sid = *d.StructureID
	}
	if d.ObjectID != nil {
		oid = *d.ObjectID
	}
	if d.Position != nil {
		pos = *d.Position
	}
	return d.Kind, sid, oid, pos
}
