package sim

import (
	mathrand "math/rand/v2"
	"time"
)

// Reactor primitive — warrant-driven evaluator (Phase 2 PR 2).
//
// Replaces v1's heap-based reactor_scheduler.go (269 LOC) with a state-as-
// queue design: Actor.WarrantedSince is the source of truth, the evaluator
// scans warranted actors on a coalesced cadence and emits ReactorTickDue
// events for those whose jitter window has elapsed.
//
// Why warrant-driven over heap-driven: v1's heap is a parallel queue that
// can desync from the actor's actual state — every pop must re-check the
// warrant, at which point the heap is just an optimization over scanning.
// At village scale (50-100 actors) the scan is microseconds inside the
// world goroutine. No merge logic, no index map, no heap.Fix; the warrant
// IS the queue.
//
// Critical invariants:
//
//   - Warrants are consumed at EMIT time, NOT at LLM completion. The LLM
//     call takes seconds; events arriving during that window stamp fresh
//     warrants that fire after the current tick completes. Clearing on
//     completion would lose any signal that arrived mid-call (stampWarrant
//     no-ops on already-warranted actors). See WarrantMeta.
//
//   - TickAttemptID is a generation, not just a bool. A timed-out attempt
//     completing late must not clear a newer attempt's in-flight flag.
//
//   - Warrants are ephemeral. LoadWorld wipes Warrants / WarrantedSince /
//     WarrantDueAt / TickInFlight / TickAttemptID. Cascade origins re-
//     engage actors via fresh events post-restart. No interface-typed
//     fields cross the checkpoint serialization boundary.

// WarrantKind discriminates the reason an actor's tick was warranted.
// Typed string so log output and tests stay readable. Open set — adding a
// new kind is a one-line append; consumers SHOULD include a default branch
// so unknown kinds don't break them.
type WarrantKind string

const (
	WarrantKindUnknown          WarrantKind = ""
	WarrantKindPCSpoke          WarrantKind = "pc_spoke"
	WarrantKindNPCSpoke         WarrantKind = "npc_spoke"
	WarrantKindHuddleJoined     WarrantKind = "huddle_joined"      // the joiner
	WarrantKindHuddlePeerJoined WarrantKind = "huddle_peer_joined" // prior members
	WarrantKindHuddleLeft       WarrantKind = "huddle_left"        // the leaver
	WarrantKindHuddlePeerLeft   WarrantKind = "huddle_peer_left"   // remaining members
	WarrantKindHuddleConcluded  WarrantKind = "huddle_concluded"   // evicted members
	WarrantKindArrived          WarrantKind = "arrived"
	WarrantKindNeedThreshold    WarrantKind = "need_threshold"
	WarrantKindIdleBackstop     WarrantKind = "idle_backstop"
	WarrantKindPaid             WarrantKind = "paid"
	WarrantKindAdmin            WarrantKind = "admin" // operator forced a tick
)

// WarrantReason is the marker interface for kind-specific warrant payloads.
// Each concrete reason carries its own data and reports its Kind so the
// kind discriminator and payload can't drift apart (no separate Kind field
// on WarrantMeta — single source of truth).
//
// The marker is unexported on purpose — external packages cannot satisfy
// it, so the set of warrant reasons is closed at the sim package boundary.
//
// PR 2 ships two concrete reasons:
//   - BasicWarrantReason for kinds without extra payload (most current callers).
//   - SpeechWarrantReason as a bootstrap example of a reason that carries
//     data the prompt builder will need (speech excerpt, speaker, ID).
//
// Future reasons (ArrivalWarrantReason, ProductionWarrantReason, etc.) land
// in the PRs that introduce their producer subsystems.
type WarrantReason interface {
	isWarrantReason()
	Kind() WarrantKind
}

// BasicWarrantReason is the catch-all reason for warrant kinds that don't
// carry kind-specific data beyond what WarrantMeta already has
// (TriggerActorID, Force). Most current huddle-event warrants use this.
type BasicWarrantReason struct {
	K WarrantKind
}

func (BasicWarrantReason) isWarrantReason()    {}
func (r BasicWarrantReason) Kind() WarrantKind { return r.K }

// SpeechWarrantReason captures the speech that warranted the tick.
//
// PR 2 ships this as a bootstrap example — no production callsite uses it
// yet (speech subsystem hasn't ported). When speech ports, callsites build
// this reason directly and the prompt builder type-switches on it to lead
// with the unaddressed line.
//
// Excerpt is the literal speech text. Sanitization (max length, newline
// handling, prompt-injection guards) is PR 3's responsibility at render
// time — payload storage stays faithful to what was said.
type SpeechWarrantReason struct {
	SpeechID SpeechID
	Speaker  ActorID
	Excerpt  string
}

func (SpeechWarrantReason) isWarrantReason()  {}
func (SpeechWarrantReason) Kind() WarrantKind { return WarrantKindPCSpoke }

// SpeechID is a stable identifier for a single speech utterance. Stub
// today; speech subsystem port lands the producer + persistence.
type SpeechID string

// WarrantMeta is one entry in an actor's Warrants list — a signal that
// fired during the actor's warranted window. The evaluator carries the
// full list into ReactorTickDue; the prompt builder (PR 3) renders each
// entry to surface what the actor should address.
//
// Force=true bypasses the per-minute gross gate at emit time (used for
// admin overrides and emergency reasons). Idempotency: multiple stamps in
// the same warrant cycle accumulate the list; the earliest WarrantedSince
// / WarrantDueAt are preserved.
type WarrantMeta struct {
	TriggerActorID ActorID
	Force          bool
	Reason         WarrantReason
}

// Kind returns the WarrantKind of the meta's reason, or WarrantKindUnknown
// if Reason is nil. Convenience for filtering and metrics.
func (m WarrantMeta) Kind() WarrantKind {
	if m.Reason == nil {
		return WarrantKindUnknown
	}
	return m.Reason.Kind()
}

// tryStampWarrant is the single funnel for stamping a warrant on an actor.
// All callsites that observe an event the actor should think about route
// through here.
//
//   - Already-warranted: appends meta to Warrants (capped at
//     Settings.MaxWarrantsPerActor; oldest dropped). Preserves earliest
//     WarrantedSince and WarrantDueAt — merge by accumulation, not
//     replacement.
//   - Not warranted: stamps WarrantedSince=now, picks a jitter from
//     Settings.ReactorJitterMin..Max, stamps WarrantDueAt=now+jitter,
//     initializes Warrants with [meta].
//   - Idempotency on duplicate signal source: callers that want to dedup
//     by (kind, triggerActor) must check Warrants themselves; the funnel
//     does not de-duplicate by default.
//
// Tick-in-flight does NOT block stamping — fresh signals must accumulate
// so they're available for the NEXT tick. The TickInFlight gate only
// prevents the evaluator from re-emitting the same actor while their LLM
// call is pending.
//
// Unexported by design — warrant stamping is the privilege of mutation
// commands inside Command.Fn. External callers reach it through Commands.
func tryStampWarrant(w *World, actor *Actor, meta WarrantMeta, now time.Time) {
	if actor == nil || meta.Reason == nil {
		return
	}
	if actor.WarrantedSince != nil {
		actor.Warrants = appendCappedWarrant(actor.Warrants, meta, w.Settings.MaxWarrantsPerActor)
		return
	}
	t := now
	actor.WarrantedSince = &t
	due := now.Add(pickWarrantJitter(w.Settings, now))
	actor.WarrantDueAt = &due
	actor.Warrants = []WarrantMeta{meta}
}

// pickWarrantJitter returns a duration in [ReactorJitterMin,
// ReactorJitterMax). Falls back to a small safe default if settings
// haven't been loaded yet (e.g. tests that don't seed the environment).
func pickWarrantJitter(s WorldSettings, _ time.Time) time.Duration {
	min := s.ReactorJitterMin
	max := s.ReactorJitterMax
	if min <= 0 {
		min = defaultReactorJitterMin
	}
	if max <= 0 {
		max = defaultReactorJitterMax
	}
	if max <= min {
		return min
	}
	span := int64(max - min)
	return min + time.Duration(mathrand.Int64N(span))
}

// appendCappedWarrant appends meta to the slice. If len(list) >= cap (cap
// > 0), drops the oldest entry — the freshest signals are the ones most
// likely to be relevant. cap <= 0 means uncapped.
func appendCappedWarrant(list []WarrantMeta, meta WarrantMeta, cap int) []WarrantMeta {
	list = append(list, meta)
	if cap > 0 && len(list) > cap {
		drop := len(list) - cap
		list = append([]WarrantMeta(nil), list[drop:]...)
	}
	return list
}

// clearWarrant resets the warrant state on the actor. Called by the
// evaluator at emit time and by LoadWorld during restart.
func clearWarrant(a *Actor) {
	a.WarrantedSince = nil
	a.WarrantDueAt = nil
	a.Warrants = nil
}

// resetReactorStateOnLoad wipes ephemeral reactor state on LoadWorld so a
// checkpoint with TickInFlight=true doesn't wedge the actor after restart
// and stale rate-gate history doesn't delay fresh post-restart warrants.
// Warrants are also cleared — interface-typed payloads aren't designed to
// survive serialization, and post-restart cascade origins re-engage actors
// via fresh events anyway.
func resetReactorStateOnLoad(a *Actor) {
	clearWarrant(a)
	a.TickInFlight = false
	a.TickAttemptID = ""
	a.RecentReactorTicks = nil
}

// actorReactorDue is the cheap pre-check the evaluator runs against every
// actor on each scan. Returns true when:
//
//   - the actor has a warrant (both WarrantedSince and WarrantDueAt non-nil),
//   - now is at or past WarrantDueAt,
//   - the actor is not already mid-tick (TickInFlight false).
//
// Requires BOTH WarrantedSince and WarrantDueAt — the evaluator
// dereferences both at emit time, so the precheck defends the invariant.
// An inconsistent state with one set and the other nil is treated as
// not-due (caller can clear and re-stamp via tryStampWarrant).
//
// Per-minute rate gating is applied separately (see checkRateGate) so a
// rate-capped actor can be delayed by pushing WarrantDueAt rather than
// silently skipped each scan.
//
// Unexported by design — eligibility primitives are part of the reactor
// boundary, not a public API.
func actorReactorDue(a *Actor, now time.Time) bool {
	if a == nil || a.WarrantedSince == nil || a.WarrantDueAt == nil {
		return false
	}
	if a.TickInFlight {
		return false
	}
	return !now.Before(*a.WarrantDueAt)
}

// actorCanReactNow is the context-aware eligibility check. Currently
// minimal — the v1 conditions (asleep, off-stage, deceased, no current
// huddle) hang off subsystems that haven't ported yet. PR 2 lands the
// hook; later PRs fill in the checks as their state lands.
//
// What's checked today:
//   - Actor still exists (caller already has the pointer, so this is a nil
//     guard).
//   - If the actor's CurrentHuddleID points at a concluded huddle, the
//     warrant is stale — caller should clear and skip.
//
// Returns (eligible, stale). When stale=true, caller clears the warrant
// (it was for a context that no longer exists). When eligible=false but
// stale=false, caller backs off (temporarily unavailable; warrant stays).
func actorCanReactNow(w *World, a *Actor) (eligible bool, stale bool) {
	if a == nil {
		return false, true
	}
	if a.CurrentHuddleID != "" {
		if h, ok := w.Huddles[a.CurrentHuddleID]; ok && h.ConcludedAt != nil {
			return false, true
		}
	}
	return true, false
}

// checkRateGate returns true when the actor is below the per-minute cap.
// The cap is a "gross gate" — settings-driven, no cost calculation. cap
// <= 0 disables the gate. RecentReactorTicks is the per-actor ring of
// recent tick timestamps; entries older than rateWindow don't count.
func checkRateGate(a *Actor, now time.Time, cap int, rateWindow time.Duration) bool {
	if cap <= 0 {
		return true
	}
	if a.RecentReactorTicks == nil {
		return true
	}
	cutoff := now.Add(-rateWindow)
	count := 0
	for _, t := range a.RecentReactorTicks.Snapshot() {
		if t.After(cutoff) {
			count++
		}
	}
	return count < cap
}

// recordReactorTick appends now to the actor's RecentReactorTicks ring,
// allocating the buffer lazily. Capacity is sized to comfortably exceed
// the per-minute cap so the rate-gate's window-count stays exact.
//
// Resize semantics: if cap is raised at runtime above the existing ring's
// capacity, the ring is rebuilt at the larger size with existing entries
// preserved in order. Without this, a ring allocated under a low cap
// couldn't enforce a later-raised cap (the new threshold could never be
// reached because the ring drops old ticks before count reaches cap).
func recordReactorTick(a *Actor, now time.Time, cap int) {
	capacity := cap * 2
	if capacity < defaultRecentReactorTicksCap {
		capacity = defaultRecentReactorTicksCap
	}
	if a.RecentReactorTicks == nil {
		a.RecentReactorTicks = NewRingBuffer[time.Time](capacity)
	} else if a.RecentReactorTicks.Cap() < capacity {
		old := a.RecentReactorTicks.Snapshot()
		rb := NewRingBuffer[time.Time](capacity)
		for _, t := range old {
			rb.Push(t)
		}
		a.RecentReactorTicks = rb
	}
	a.RecentReactorTicks.Push(now)
}

// newTickAttemptID mints an opaque generation string for a reactor tick
// attempt. Used to disambiguate stale completions: a completion command
// is only honored when its AttemptID matches the actor's current
// TickAttemptID. Implementation is random-hex (same idiom as huddle/scene
// IDs) — sortability isn't required since the comparison is exact.
func newTickAttemptID() string {
	return "tk-" + randomHex(12)
}

// Defaults applied when WorldSettings hasn't been initialized (e.g. test
// worlds that bypass repo loading and don't seed an Environment). Real
// production settings come from WorldSettings; these exist so the reactor
// is functional in test scaffolds without forcing every test to seed
// settings.
const (
	defaultReactorJitterMin        = 1 * time.Second
	defaultReactorJitterMax        = 4 * time.Second
	defaultReactorEvaluatorCadence = 250 * time.Millisecond
	defaultMaxWarrantAge           = 90 * time.Second
	defaultMaxWarrantsPerActor     = 16
	defaultRateWindow              = time.Minute
	defaultRecentReactorTicksCap   = 32
)
