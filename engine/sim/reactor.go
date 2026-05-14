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

	// PR 3a source metadata — makes a warrant causally identifiable so
	// tryStampWarrant can dedup on (Kind, SourceEventID) and PR 3's
	// perception can resolve the warrant's scene without reverse-scanning.
	// All value-typed (plain IDs with empty sentinels, no pointers) so
	// CloneActor's shallow Warrants copy stays correct.
	//
	// SourceEventID is the exact event that produced this warrant. It MUST
	// be nonzero for PR 3 perception warrants — the three dedup paths key
	// on it. A zero SourceEventID marks a warrant as "not event-sourced"
	// (legacy / internal callsites predating PR 3 perception); those bypass
	// dedup entirely, since (Kind, 0) would collapse unrelated warrants.
	SourceEventID EventID
	// RootEventID is a copy of the source event's causal root. Never a
	// dedup key — distinct SourceEventIDs under the same root are distinct
	// developments and must each stamp.
	RootEventID EventID
	// SourceActorID is the actor whose action produced the source event.
	// Empty = none / bulk (e.g. a force-conclude eviction with no single
	// trigger).
	SourceActorID ActorID
	// HuddleID / SceneID scope the warrant; empty = none. SceneID is load-
	// bearing — it is step 1 of PR 3's scene-resolution order.
	HuddleID HuddleID
	SceneID  SceneID
	// OccurredAt is the source event's wall-clock timestamp. Display /
	// debug metadata only — EventID is the authoritative causal order.
	OccurredAt time.Time
}
//
// Zero-lineage invariant (PR 3a): a warrant either carries FULL event
// lineage (SourceEventID != 0, with the rest of the source fields
// populated from that event) or NONE (all source fields left at their
// zero values). A nonzero RootEventID alongside a zero SourceEventID is
// not a valid state — there is no partial "looks sourced" metadata. The
// existing synchronous lifecycle stamp callsites (huddle join/leave/
// conclude, arrival) are stamp-before-emit, so in PR 3a they produce
// fully-zero, "not event-sourced" warrants; they are retrofitted with
// real lineage in PR 3 (see the PR 3 design note).

// WarrantSourceKey identifies the (warrant kind, source event) pair a
// warrant came from. It is the single dedup key shared by all three of
// tryStampWarrant's dedup paths — open-cycle, in-flight, and recently-
// consumed. A single source event can produce different kinds for the
// same actor, so Kind is part of the key.
//
// Dedup applies ONLY when SourceEventID != 0. A zero SourceEventID is the
// "not event-sourced" sentinel; (Kind, 0) as a key would collapse
// unrelated non-event-sourced warrants, so they bypass dedup. As a
// consequence, a zero-SourceEventID key is NEVER stored in the in-flight
// or recently-consumed sets either — sourceKeySet filters non-event-
// sourced warrants out at consume time, so the sets only ever hold real
// keys.
type WarrantSourceKey struct {
	Kind          WarrantKind
	SourceEventID EventID
}

// sourceKey returns the WarrantSourceKey for this meta. The key is only
// meaningful for dedup when SourceEventID != 0 — callers check that before
// using it (eventSourced).
func (m WarrantMeta) sourceKey() WarrantSourceKey {
	return WarrantSourceKey{Kind: m.Kind(), SourceEventID: m.SourceEventID}
}

// eventSourced reports whether this meta carries a real source event and
// therefore participates in tryStampWarrant's dedup paths.
func (m WarrantMeta) eventSourced() bool {
	return m.SourceEventID != 0
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
//
// Source-aware dedup (PR 3a): an event-sourced warrant (SourceEventID
// != 0) is dropped if its WarrantSourceKey is already (1) pending in the
// open warrant cycle, (2) consumed into the in-flight tick attempt, or
// (3) in the recently-consumed set within recentlyConsumedTTL. Together
// these coalesce near-simultaneous multi-path triggers and suppress a
// delayed duplicate of a stimulus a completed tick already addressed.
// Warrants with SourceEventID == 0 ("not event-sourced") bypass dedup —
// (Kind, 0) would collapse unrelated warrants.
//
// Tick-in-flight does NOT block stamping a NEW source — fresh signals must
// accumulate so they're available for the NEXT tick. The TickInFlight gate
// only prevents the evaluator from re-emitting the same actor while their
// LLM call is pending; the in-flight DEDUP path above suppresses only an
// exact-same-source duplicate, never a distinct development.
//
// Unexported by design — warrant stamping is the privilege of mutation
// commands inside Command.Fn. External callers reach it through Commands.
func tryStampWarrant(w *World, actor *Actor, meta WarrantMeta, now time.Time) {
	if actor == nil || meta.Reason == nil {
		return
	}

	// Source-aware dedup. Only event-sourced warrants participate; reads
	// from nil maps are safe (zero value, ok=false), so no nil-guards.
	if meta.eventSourced() {
		key := meta.sourceKey()
		// 1. Open-cycle: same source already pending this cycle.
		for _, pending := range actor.Warrants {
			if pending.eventSourced() && pending.sourceKey() == key {
				return
			}
		}
		// 2. In-flight: same source consumed into the attempt mid-LLM-call.
		if _, ok := actor.inFlightSourceKeys[key]; ok {
			return
		}
		// 3. Recently-consumed: a completed attempt addressed this exact
		//    source within the TTL window. Expired entries are ignored
		//    here and swept on the next insert (rememberConsumedSourceKey).
		if ts, ok := actor.recentlyConsumedSourceKeys[key]; ok &&
			now.Sub(ts) < recentlyConsumedTTL {
			return
		}
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
	a.inFlightSourceKeys = nil
	a.recentlyConsumedSourceKeys = nil
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

// TickAdmissionController decides whether the reactor evaluator may admit
// a tick right now — i.e. whether there is downstream capacity to actually
// run it. The evaluator consults CanAdmit BEFORE consuming an actor's
// warrants (Option A — admit before consume), so a "no" leaves the
// warrants open and nothing is lost.
//
// The substrate owns this interface; the default is alwaysAdmit, so the
// evaluator runs standalone in substrate tests with no handler wired. PR
// 3's worker pool implements it (CanAdmit reports len(jobChan) <
// cap(jobChan)) and MUST return false once the pool is stopping/stopped,
// otherwise an admit-then-send-to-closed-channel race is possible during
// shutdown.
type TickAdmissionController interface {
	CanAdmit() bool
}

// alwaysAdmit is the default TickAdmissionController — it admits every
// tick. With no PR 3 worker pool wired, the evaluator behaves exactly as
// it did before admission control existed.
type alwaysAdmit struct{}

func (alwaysAdmit) CanAdmit() bool { return true }

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

// lastReactorTickAt returns the timestamp of the actor's most recent
// reactor-tick emission — the newest entry of RecentReactorTicks. ok is
// false when the actor has never ticked (nil/empty ring); the
// MinReactorTickGap floor does not apply to a first tick.
func lastReactorTickAt(a *Actor) (time.Time, bool) {
	if a.RecentReactorTicks == nil || a.RecentReactorTicks.Len() == 0 {
		return time.Time{}, false
	}
	snap := a.RecentReactorTicks.Snapshot()
	return snap[len(snap)-1], true
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

// TickAttemptID is the generation identifier for a reactor tick attempt.
// It disambiguates stale completions: CompleteReactorTick is honored only
// when its AttemptID matches the actor's current TickAttemptID, so a late-
// returning timed-out attempt cannot clear a newer attempt's in-flight
// flag. Minted by newTickAttemptID; ephemeral — wiped on LoadWorld with
// the rest of the reactor state.
type TickAttemptID string

// newTickAttemptID mints an opaque generation identifier for a reactor
// tick attempt. Used to disambiguate stale completions: a completion
// command is only honored when its AttemptID matches the actor's current
// TickAttemptID. Implementation is random-hex (same idiom as huddle/scene
// IDs) — sortability isn't required since the comparison is exact.
func newTickAttemptID() TickAttemptID {
	return TickAttemptID("tk-" + randomHex(12))
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

	// defaultMinReactorTickGap is the per-actor minimum wall-clock gap
	// between reactor ticks when WorldSettings.MinReactorTickGap is unset.
	// A pacing floor independent of the optional per-minute rate cap.
	defaultMinReactorTickGap = 5 * time.Second

	// defaultAdmissionBackoff is how far the evaluator pushes an actor's
	// WarrantDueAt when tick admission control turns it away, when
	// WorldSettings.AdmissionBackoff is unset. ≈ the evaluator cadence, so
	// a deferred warrant is re-examined on roughly the next scan.
	defaultAdmissionBackoff = 250 * time.Millisecond

	// recentlyConsumedTTL / recentlyConsumedCap bound the per-actor
	// recently-consumed source-key set — tryStampWarrant's third dedup
	// path. A consumed key suppresses a delayed duplicate of the same
	// source event for up to the TTL; the cap is a hard ceiling with
	// expired-first-then-oldest eviction (see rememberConsumedSourceKey).
	recentlyConsumedTTL = 5 * time.Minute
	recentlyConsumedCap = 256
)

// sourceKeySet collects the WarrantSourceKeys of the event-sourced
// warrants in list into a set. Returns nil when none are event-sourced;
// a nil in-flight set is the valid "no source keys consumed" state.
// Called at ReactorTickDue emit to record what the attempt consumed.
func sourceKeySet(list []WarrantMeta) map[WarrantSourceKey]struct{} {
	var set map[WarrantSourceKey]struct{}
	for _, m := range list {
		if !m.eventSourced() {
			continue
		}
		if set == nil {
			set = make(map[WarrantSourceKey]struct{})
		}
		set[m.sourceKey()] = struct{}{}
	}
	return set
}

// rememberConsumedSourceKey records key in the actor's recently-consumed
// set with insertion time now, allocating the map lazily. When the set is
// already at recentlyConsumedCap it first sweeps entries older than
// recentlyConsumedTTL, then — if still at cap — evicts the single oldest
// entry by insertion time, before inserting. Called by CompleteReactorTick
// when a terminal status marks a source key as addressed.
func rememberConsumedSourceKey(a *Actor, key WarrantSourceKey, now time.Time) {
	if a.recentlyConsumedSourceKeys == nil {
		a.recentlyConsumedSourceKeys = make(map[WarrantSourceKey]time.Time)
	}
	m := a.recentlyConsumedSourceKeys
	if len(m) >= recentlyConsumedCap {
		cutoff := now.Add(-recentlyConsumedTTL)
		for k, ts := range m {
			if ts.Before(cutoff) {
				delete(m, k)
			}
		}
		for len(m) >= recentlyConsumedCap {
			var oldestKey WarrantSourceKey
			var oldestTS time.Time
			first := true
			for k, ts := range m {
				if first || ts.Before(oldestTS) {
					oldestKey, oldestTS, first = k, ts, false
				}
			}
			delete(m, oldestKey)
		}
	}
	m[key] = now
}
