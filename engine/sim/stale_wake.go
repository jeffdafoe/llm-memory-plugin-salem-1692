package sim

import (
	"hash/fnv"
	"sort"
	"strconv"
	"time"
)

// stale_wake.go — LLM-233. Staleness decay for level-triggered warrants.
//
// The invariant (Jeff, 2026-07-02): "X people in your venue, none of which
// have changed and none of which are active PCs, shouldn't generate ten
// trillion talk things." A level-triggered warrant producer (restock is the
// canonical one: a per-minute scan that re-stamps while stock is low) re-wakes
// an actor at full rate forever, even when NOTHING the actor could react to
// has changed since its last wake — the live John Ellis case: 256 restock
// wakes in two hours pitched at a shelved laboring worker who could not
// answer, ticks down to the 5s min-gap floor.
//
// None of the existing bounds see this shape. The degeneracy observer
// (degeneracy.go) scores a successful speak with an audience as productive
// every time; the huddle loop sweep (huddle_loop_sweep.go) concluded the
// huddle once but the huddle was never the wake driver, and its token-level
// near-dup detection loses to the model's paraphrase variety. Both are
// DETECTORS of bad output; this file bounds the INPUT rate instead, with a
// deterministic check that needs no judgement call: has the actor's situation
// changed since the last time we paid for a wake of this same kind?
//
// Mechanism: at emit time, an all-AMBIENT warrant cycle (isAmbientWarrantKind
// — the same classification the Stage-2 degeneracy throttle defers, so a
// salient warrant or an operator Force always cuts through) is checked
// against a per-actor, per-warrant-kind ledger of {situation fingerprint,
// consecutive same-fingerprint emits, last emit time}. If EVERY kind in the
// cycle has already been emitted under the current fingerprint, the wake is
// deferred to lastEmit + base·2^streak (capped) — the decay. Any fingerprint
// change, or a kind with no ledger entry yet (e.g. the day's first
// shift_duty), passes at full rate and resets that kind's streak on emit.
//
// The fingerprint hashes what the actor could newly react to: its location,
// macro-state, purse and inventory, its huddle's membership, the newest
// utterance by anyone OTHER than the actor itself (its own re-pitches must
// not count as change), and the huddle's last PC utterance time. Needs are
// deliberately EXCLUDED — they drift every minute by design, would defeat
// the decay entirely, and red-need thresholds have their own SALIENT
// warrants that bypass this gate anyway.
//
// The ledger is transient (never checkpointed) and wiped on load with the
// rest of the reactor state — after a restart the decay re-learns from base
// rate, mirroring the red-need / seek-work backstop pacing.
//
// Enable/disable: StaleWakeDecayBase > 0 enables (the pg loader defaults it
// ON at 1m — unlike the degeneracy observer this gate's trigger is an exact
// equality check, not a heuristic, and any real change lifts it instantly);
// 0 disables, which is the zero-value default for tests constructing
// WorldSettings directly.

const (
	// defaultStaleWakeDecayCap bounds the backoff growth: a fully-decayed
	// unchanged situation is still re-observed this often, so a stuck actor
	// is never silenced outright — just re-examined at village-idle cadence.
	defaultStaleWakeDecayCap = 30 * time.Minute
)

// StaleWakeEntry is one ambient warrant kind's decay state on an actor:
// the situation fingerprint at its last emitted tick, how many consecutive
// emits have seen that same fingerprint, and when the last emit happened.
type StaleWakeEntry struct {
	Fingerprint uint64
	Streak      int
	LastEmitAt  time.Time
}

// staleWakeDecayEnabled reports whether the decay gate is active. Positive
// base enables — the same one-knob posture as the degeneracy observer's
// DegeneracyThinAfterTicks.
func (s WorldSettings) staleWakeDecayEnabled() bool {
	return s.StaleWakeDecayBase > 0
}

// staleWakeDecayCap resolves the backoff ceiling, defaulting when unset.
func (s WorldSettings) staleWakeDecayCap() time.Duration {
	if s.StaleWakeDecayCap > 0 {
		return s.StaleWakeDecayCap
	}
	return defaultStaleWakeDecayCap
}

// staleWakeBackoff returns the allowed re-wake interval after `streak`
// consecutive same-fingerprint emits of a kind: base·2^streak, capped.
// Streak 1 (one prior emit under this fingerprint) → 2·base, so with the
// 1m default the wake sequence runs 1m → 2m → 4m → … → cap.
func staleWakeBackoff(s WorldSettings, streak int) time.Duration {
	d := s.StaleWakeDecayBase
	if d <= 0 {
		return 0
	}
	max := s.staleWakeDecayCap()
	for i := 0; i < streak && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}

// actorSituationFingerprint hashes the actor's reactable situation. Two
// equal fingerprints mean "nothing the actor could newly react to has
// changed" — the deterministic staleness test the decay gate keys on. See
// the file comment for what's in and what's deliberately out (needs).
func actorSituationFingerprint(w *World, a *Actor) uint64 {
	h := fnv.New64a()
	write := func(field string) {
		h.Write([]byte(field))
		h.Write([]byte{0})
	}
	write(string(a.InsideStructureID))
	write(strconv.Itoa(a.Pos.X))
	write(strconv.Itoa(a.Pos.Y))
	write(string(a.State))
	write(strconv.Itoa(a.Coins))
	items := make([]string, 0, len(a.Inventory))
	for k, v := range a.Inventory {
		items = append(items, string(k)+"="+strconv.Itoa(v))
	}
	sort.Strings(items)
	for _, it := range items {
		write(it)
	}
	write(string(a.CurrentHuddleID))
	if hud := w.Huddles[a.CurrentHuddleID]; a.CurrentHuddleID != "" && hud != nil {
		members := make([]string, 0, len(hud.Members))
		for id := range hud.Members {
			members = append(members, string(id))
		}
		sort.Strings(members)
		for _, m := range members {
			write(m)
		}
		// Newest utterance by anyone else — the actor's own lines must not
		// read as change, or a re-pitching NPC would reset its own decay.
		for i := len(hud.RecentUtterances) - 1; i >= 0; i-- {
			u := hud.RecentUtterances[i]
			if u.SpeakerID != a.ID {
				write(string(u.SpeakerID))
				write(u.At.UTC().Format(time.RFC3339Nano))
				break
			}
		}
		write(hud.LastPCUtteranceAt.UTC().Format(time.RFC3339Nano))
	}
	return h.Sum64()
}

// staleWakeDeferUntil reports whether the actor's due warrant cycle should be
// deferred as stale, and until when. Stale means EVERY kind in the cycle has
// a ledger entry under the current fingerprint whose backoff hasn't elapsed.
// Any kind with no entry, or an entry from a different fingerprint, makes the
// whole cycle fresh (something changed, or this kind hasn't been paid for
// under this situation yet) — full rate. Callers gate on
// warrantCycleAllAmbient + !hasForcedWarrant first, so a salient signal or an
// operator nudge never reaches this check.
func staleWakeDeferUntil(s WorldSettings, a *Actor, fp uint64, now time.Time) (time.Time, bool) {
	if len(a.Warrants) == 0 {
		return time.Time{}, false
	}
	var until time.Time
	for _, m := range a.Warrants {
		e := a.StaleWake[m.Kind()]
		if e == nil || e.Fingerprint != fp {
			return time.Time{}, false
		}
		allowed := e.LastEmitAt.Add(staleWakeBackoff(s, e.Streak))
		if allowed.After(until) {
			until = allowed
		}
	}
	if now.Before(until) {
		return until, true
	}
	return time.Time{}, false
}

// recordStaleWake advances the decay ledger for an EMITTED all-ambient cycle:
// each kind's entry either extends its same-fingerprint streak or resets to a
// fresh streak of 1 under the new fingerprint. Called only at the emit
// chokepoint (never on a deferral), so a streak counts real LLM calls — the
// thing the decay exists to bound.
func recordStaleWake(a *Actor, warrants []WarrantMeta, fp uint64, now time.Time) {
	for _, m := range warrants {
		k := m.Kind()
		if !isAmbientWarrantKind(k) {
			continue
		}
		if a.StaleWake == nil {
			a.StaleWake = make(map[WarrantKind]*StaleWakeEntry)
		}
		e := a.StaleWake[k]
		if e == nil || e.Fingerprint != fp {
			a.StaleWake[k] = &StaleWakeEntry{Fingerprint: fp, Streak: 1, LastEmitAt: now}
			continue
		}
		e.Streak++
		e.LastEmitAt = now
	}
}

// clearStaleWake drops the actor's decay ledger. Load-time reset — the
// ledger is transient pacing state, like the rate-gate history.
func clearStaleWake(a *Actor) {
	a.StaleWake = nil
}
