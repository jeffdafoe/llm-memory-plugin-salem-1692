// Package promptlog provides a non-blocking, bounded, in-memory store of the
// RENDERED DELIBERATION PROMPTS the engine sends to NPC agents, keyed per
// actor — the debug counterpart to the redacted tick-telemetry ring
// (engine/sim/telemetry). ZBBS-HOME-360.
//
// Why it exists: the telemetry ring deliberately carries NO raw prompts ("no
// raw prompts / LLM responses / private text ever"), so when an NPC behaves
// oddly there was no way to see what it actually perceived. RingSink captures
// the full rendered prompt per tick so an operator can pull "what did this
// agent see on its last few ticks" through the operator-gated umbilical.
//
// Why PER-ACTOR (not one global ring like telemetry): the debug question is
// always "show me THIS agent's recent prompts," and prompts are large (a few KB
// each). A small per-actor ring guarantees the last N prompts for every agent
// regardless of how chatty its neighbours are. Two bounds keep memory finite:
// perActorCap prompts per actor, AND a cap on the number of distinct actors
// tracked (DefaultMaxActors) — when a new actor would exceed it, the
// least-recently-active actor's ring is evicted. The actor cap matters because
// Salem churns transient actors (visitors that spawn and despawn): without it
// the map would retain one entry per distinct id ever seen for the life of the
// process. Total memory is bounded by maxActors * perActorCap * promptSize.
//
// Non-blocking contract (same as telemetry's RingSink): WritePrompt runs on the
// tick-worker goroutines and must never wait on a reader. The mutex is held
// only for a map lookup + slice append/trim, never across I/O. In-memory only;
// lost on restart by design — a debug surface, not durable state.
package promptlog

import (
	"sync"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// DefaultPerActorCapacity is the per-actor retention used when New is given a
// non-positive capacity. A handful of recent prompts is enough to reconstruct
// "what led to this decision" without holding many KB per agent.
const DefaultPerActorCapacity = 8

// DefaultMaxActors bounds the number of distinct actors whose prompts are
// retained at once — covers the fixed agent-NPC population plus a healthy
// window of recently-active transient visitors. When exceeded, the
// least-recently-active actor's ring is evicted (see WritePrompt).
const DefaultMaxActors = 64

// Stats is a point-in-time summary of a RingSink, surfaced so an operator can
// see how much prompt history is retained.
type Stats struct {
	// PerActorCapacity is the fixed max prompts retained per actor.
	PerActorCapacity int
	// Actors is how many distinct actors currently have buffered prompts.
	Actors int
	// Buffered is the total prompt records currently held across all actors.
	Buffered int
	// Written is the total prompts ever accepted (monotonic).
	Written uint64
	// Dropped is the total prompts evicted because an actor's ring was full
	// when a newer prompt arrived (monotonic).
	Dropped uint64
}

// RingSink is a per-actor bounded store of PromptRecords, safe for concurrent
// writers (tick workers) and readers (umbilical HTTP goroutines). Implements
// sim.PromptSink.
type RingSink struct {
	mu        sync.Mutex
	cap       int
	maxActors int
	byActor   map[sim.ActorID][]sim.PromptRecord
	written   uint64
	dropped   uint64
}

// New builds a RingSink retaining the last perActorCap prompts PER ACTOR,
// across at most DefaultMaxActors distinct actors. A non-positive capacity
// falls back to DefaultPerActorCapacity.
func New(perActorCap int) *RingSink {
	if perActorCap <= 0 {
		perActorCap = DefaultPerActorCapacity
	}
	return &RingSink{
		cap:       perActorCap,
		maxActors: DefaultMaxActors,
		byActor:   make(map[sim.ActorID][]sim.PromptRecord),
	}
}

// WritePrompt records one rendered prompt for rec.ActorID, evicting that
// actor's oldest prompt when the per-actor ring is full. Non-blocking: holds
// the mutex only for the append/trim. A record with an empty ActorID is
// dropped (nothing to key on). Implements sim.PromptSink.
func (r *RingSink) WritePrompt(rec sim.PromptRecord) {
	if r == nil || rec.ActorID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	buf, exists := r.byActor[rec.ActorID]
	// A NEW actor arriving at the actor cap evicts the least-recently-active
	// actor's whole ring, so the distinct-actor count stays bounded under
	// transient-actor churn (visitors). Existing actors never trigger eviction.
	if !exists && len(r.byActor) >= r.maxActors {
		r.evictStalestActorLocked()
	}
	buf = append(buf, rec)
	if len(buf) > r.cap {
		// Drop the oldest. The slice stays bounded at cap; the small copy is
		// fine for a debug surface at this cardinality (cap is a handful).
		over := len(buf) - r.cap
		buf = append(buf[:0:0], buf[over:]...)
		r.dropped += uint64(over)
	}
	r.byActor[rec.ActorID] = buf
	r.written++
}

// evictStalestActorLocked deletes the actor whose most-recent prompt is the
// oldest (least recently active), counting its dropped prompts. Caller holds
// r.mu. O(actors), run only on overflow (a new actor arriving at the cap).
func (r *RingSink) evictStalestActorLocked() {
	var victim sim.ActorID
	var stalest time.Time
	have := false
	for id, buf := range r.byActor {
		var last time.Time
		if len(buf) > 0 {
			last = buf[len(buf)-1].At
		}
		if !have || last.Before(stalest) {
			victim, stalest, have = id, last, true
		}
	}
	if have {
		r.dropped += uint64(len(r.byActor[victim]))
		delete(r.byActor, victim)
	}
}

// Recent returns up to limit of an actor's most-recent prompts, OLDEST FIRST
// (chronological, matching the telemetry read shape). limit <= 0 returns all
// retained. The returned slice is a fresh copy — the caller may keep it past
// the lock. Empty (non-nil) when the actor has no buffered prompts.
func (r *RingSink) Recent(actorID sim.ActorID, limit int) []sim.PromptRecord {
	if r == nil {
		return []sim.PromptRecord{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	buf := r.byActor[actorID]
	if len(buf) == 0 {
		return []sim.PromptRecord{}
	}
	start := 0
	if limit > 0 && limit < len(buf) {
		start = len(buf) - limit // keep the most-recent `limit`, still oldest-first
	}
	out := make([]sim.PromptRecord, len(buf)-start)
	copy(out, buf[start:])
	return out
}

// Stats returns a point-in-time summary.
func (r *RingSink) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	buffered := 0
	for _, buf := range r.byActor {
		buffered += len(buf)
	}
	return Stats{
		PerActorCapacity: r.cap,
		Actors:           len(r.byActor),
		Buffered:         buffered,
		Written:          r.written,
		Dropped:          r.dropped,
	}
}
