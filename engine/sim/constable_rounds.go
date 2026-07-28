package sim

import (
	"hash/fnv"
	"time"
)

// constable_rounds.go — LLM-514. The one genuinely new cadence behind the
// constable "walk the rounds" behavior: a fixed wall-clock INTERVAL trigger,
// unlike the lamplighter (event) and washerwoman / town_crier (schedule-window
// boundary, RouteBoundaryDue) routes. The route SUBSTRATE (RouteStop enter/loiter,
// StartNPCRoute, AdvanceNPCRoute, the beat advancer) lives in npc_route.go; the
// candidate builder and the driver live in the cascade (cascade/npc_route.go).
// This file is the pure sim-side "is a rounds tour due right now?" decision.
//
// The dwell and quiet-window tunables that used to live here went with the dwell
// itself (LLM-548): a beat dispatches no walk, so there is nothing to hold off and
// no conversation to be dragged out of. What keeps him at a populated stop now is
// the conversation — he is available for an ordinary arrival encounter throughout,
// which under the dispatched round he was not.

// Constable rounds defaults. Seeded into WorldSettings by the environment loaders
// so GET /settings reports a concrete value and the checkpoint round-trips one.
const (
	// DefaultConstableRoundsInterval is how often the on-shift constable leaves
	// his post to walk the businesses. 2h — frequent enough to feel like a
	// present watch, sparse enough not to keep him perpetually away from the
	// Meeting House.
	DefaultConstableRoundsInterval = 2 * time.Hour
)

// ConstableRoundsWarrantReason is the wake stamped for a beat carrier standing
// still with a round still owed (LLM-549). It is a WAKE, never a march: it runs a
// tick whose prompt already carries what he owes and where to go next, and he
// chooses. Nothing here moves him.
//
// It exists because a beat dispatches no walk, so removing the dispatch removed
// the thing that used to start a round. On-shift AT his post is the hole: shift
// duty stamps nothing for an actor already at work (buildShiftDutyTarget's switch
// has no at-work arm) and the idle backstop only covers actors OUTDOORS. Between
// them a constable stood in the Meeting House with eight doors unwalked and no
// recurring wake at all — live, twelve minutes without a tick.
//
// Zero-sourced (a standing obligation is not an event) with a zero
// DedupDiscriminator, the ShiftDutyWarrantReason shape. There is nothing to
// discriminate: at most one round is owed at a time, so a second stamp under the
// same conditions IS the same wake.
type ConstableRoundsWarrantReason struct{}

func (ConstableRoundsWarrantReason) isWarrantReason()           {}
func (ConstableRoundsWarrantReason) Kind() WarrantKind          { return WarrantKindConstableRounds }
func (ConstableRoundsWarrantReason) DedupDiscriminator() uint64 { return 0 }

// BeatNeedsAWake reports whether a beat carrier should be woken to get on with a
// round he still owes. Pure read; the caller stamps.
//
// True only when ALL hold:
//
//   - He is carrying a BEAT. A dispatched route walks its carrier, so it needs no
//     nudge, and a carrier with no route owes nothing.
//
//   - He is STANDING STILL (MoveIntent == nil). A man already walking is on his way
//     — waking him mid-leg is the nag this design exists to avoid, and his arrival
//     will tick him anyway (finishArrival stamps on every arrival).
//
//   - He is NOT in a live conversation. Interrupting one to say "you have rounds to
//     walk" is exactly the intrusion the dwell removal was meant to end. LIVENESS,
//     not lifecycle — a huddle stays open 2h after its last word, so the lifecycle
//     test would gag the wake for the rest of the afternoon over a conversation
//     that ended before noon (the LLM-537 lesson, one layer over).
//
//   - He is ON SHIFT. In practice runConstableRounds' loop clears an off-shift beat
//     before asking, so this is belt-and-braces there — but the predicate is
//     exported and its name promises a complete answer, so it enforces the
//     invariant rather than inheriting it from one caller's ordering. A future
//     producer calling this directly must not be able to stamp an off-shift wake
//     against a beat the sweep has not reached yet (code_review).
//
// **No pace interval, deliberately.** The repeat rate is the stale-wake ledger's
// (LLM-233): WarrantKindConstableRounds is ambient, so an unchanged situation
// decays 1m → 2m → … → 30m while any real change — crucially his LOCATION, which
// the fingerprint hashes — resets it to full rate. That is strictly better than a
// fixed quiet window here, which could not tell "frozen at his post" from "walking
// his round" and would delay the first nudge by its own length, which is the very
// latency this fixes.
//
// The gates above are what make that safe, and it is worth spelling out why, since
// "the ledger paces it" is only true if the fingerprint cannot churn under a man
// who is genuinely stuck. Read actorSituationFingerprint: EVERY conversation-derived
// component — huddle id, member set, newest foreign utterance, last PC utterance —
// is nested inside `if a.CurrentHuddleID != ""`, and the loudest of them can only
// change when somebody speaks. But somebody speaking is precisely what makes the
// huddle LIVE again, which turns this predicate off. The gate and the fingerprint
// move together. What is left — position, inside-structure, macro-state, coins,
// inventory, on-shift — cannot churn under a stationary actor except via a discrete
// event (a transaction, a state change, the shift boundary), each of which is
// legitimately worth a constable's attention and none of which recurs every minute.
func BeatNeedsAWake(w *World, a *Actor, now time.Time) bool {
	if a == nil || a.MoveIntent != nil {
		return false
	}
	route := w.ActiveRoutes[a.ID]
	if route == nil || route.Phase != RoutePhaseBeat {
		return false
	}
	if !actorOnShift(w, a, localMinuteOfDay(w, now)) {
		return false
	}
	return !actorInLiveHuddle(w, a, now)
}

// actorInLiveHuddle reports whether the actor's conversation has said anything
// recently — the liveness question, over HuddleLiveWindow. Deliberately NOT
// actorInActiveHuddle, which asks whether the huddle has CONCLUDED and reads true
// across the full 2h silence timeout.
//
// Borrowing the shared window IS right here, and was wrong for the constable's old
// dwell, for a reason worth stating: the two gates fail in opposite directions. The
// dwell needed its own window because erring LONG parked him at every stop for
// minutes after both parties said goodbye — the LLM-537 defect. This gate only
// defers a NUDGE, so erring long costs at most a few extra minutes before one wake
// on a 4h round, while erring short interrupts a live conversation. Long is the safe
// error here, which is exactly what a generous shared window gives. It is also the
// same question the noop-skip preflight asks of this window — "is anyone actually
// talking" — rather than a pace the constable owns.
func actorInLiveHuddle(w *World, a *Actor, now time.Time) bool {
	if a == nil || a.CurrentHuddleID == "" {
		return false
	}
	h, ok := w.Huddles[a.CurrentHuddleID]
	if !ok || h.ConcludedAt != nil {
		return false
	}
	return HuddleIsLive(h, now, EffectiveHuddleLiveWindow(w.Settings))
}

// ConstableRoundsDue reports whether the constable actor a should start a rounds
// tour as of now. True only when ALL hold:
//
//   - He is settled AT his post: WorkStructureID set and InsideStructureID equal to
//     it — rounds start from the Meeting House, never mid-walk.
//   - He is on shift (actorOnShift, the worker-aware dawn/dusk fallback included).
//   - He is not already routing (w.ActiveRoutes[a.ID] == nil).
//   - The interval has elapsed since his last rounds, WITH a per-carrier
//     deterministic phase offset (constableRoundsOffset) so two carriers never
//     fire on the same tick. Rounds are "due" at wall-clock instants
//     k*interval + offset(actorID); the most recent such instant at-or-before now
//     fires when it is AFTER this actor's stored last-rounds stamp
//     (w.ConstableRoundsStamps[a.ID] — PER ACTOR, so one carrier can't suppress
//     another's beat). A missing stamp fires immediately — the boot catch-up (the
//     stamp is in-memory and restart-lossy on purpose).
//
// interval <= 0 disables rounds (the off-switch). Pure read of world + actor
// state; the caller (runConstableRounds) dispatches + stamps. MUST be called from
// inside a Command.Fn.
func ConstableRoundsDue(w *World, a *Actor, interval time.Duration, now time.Time) bool {
	if a == nil || interval <= 0 {
		return false
	}
	if a.WorkStructureID == "" || a.InsideStructureID != a.WorkStructureID {
		return false
	}
	if !actorOnShift(w, a, localMinuteOfDay(w, now)) {
		return false
	}
	// A beat never reads in-flight (LLM-548), so a part-walked round does not block
	// a fresh one — the next interval supersedes it outright. That is what bounds
	// how long one can sit part-walked: at most a single interval, after which he
	// starts a fresh circuit rather than carrying yesterday's around. It cannot
	// supersede him mid-round in practice, because being due also requires him to be
	// settled back at his post, which a man still walking his beat is not.
	if RouteInFlight(w, a.ID) {
		return false
	}
	instant := mostRecentRoundsInstant(now, interval, constableRoundsOffset(a.ID, interval))
	if last, has := w.ConstableRoundsStamps[a.ID]; has && !last.Before(instant) {
		return false
	}
	return true
}

// ClearBeatRouteIfOffShift drops a part-walked beat once its carrier is off shift,
// returning whether it cleared one (LLM-531). This is what bounds the duty
// exemption a beat carries: while a round is owed, buildDutySteer leaves him alone
// so he can choose where to go next, and without this sweep that exemption would
// follow him into the night and leave him standing wherever he finished instead of
// walking home. His watch is over; tomorrow's round starts fresh rather than
// resuming yesterday's.
// MUST be called from inside a Command.Fn (reads + mutates world state).
func ClearBeatRouteIfOffShift(w *World, a *Actor, now time.Time) bool {
	if a == nil {
		return false
	}
	route := w.ActiveRoutes[a.ID]
	if route == nil || route.Phase != RoutePhaseBeat {
		return false
	}
	if actorOnShift(w, a, localMinuteOfDay(w, now)) {
		return false
	}
	clearActiveRoute(w, a.ID)
	return true
}

// StampConstableRoundsWake returns a Command that stamps the beat wake for actorID,
// reporting whether a warrant was actually recorded. The stamp goes through
// tryStampWarrant like every other producer, so it inherits the agent-kind gate (a
// decorative or a PC is never warranted), the per-actor WarrantedSince dedup, and
// the stale-wake decay that paces repeats.
//
// A Command rather than a bare helper because tryStampWarrant is unexported and the
// caller is the cascade package. MUST be run on the world goroutine.
func StampConstableRoundsWake(actorID ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor := w.Actors[actorID]
			if actor == nil {
				return false, nil
			}
			return tryStampWarrant(w, actor, WarrantMeta{
				TriggerActorID: actorID,
				Force:          false,
				Reason:         ConstableRoundsWarrantReason{},
			}, now), nil
		},
	}
}

// StampConstableRounds records that actor a dispatched rounds at t, so
// ConstableRoundsDue won't re-fire the same beat for that carrier. Lazy-allocates
// the map. Per-actor by design (see World.ConstableRoundsStamps). MUST be called
// from inside a Command.Fn (mutates world state).
func StampConstableRounds(w *World, actorID ActorID, t time.Time) {
	if w.ConstableRoundsStamps == nil {
		w.ConstableRoundsStamps = map[ActorID]time.Time{}
	}
	w.ConstableRoundsStamps[actorID] = t
}

// constableRoundsOffset returns a per-actor phase offset in [0, interval) seeded
// by ActorID — the same idea as shiftLatenessOffset's arrival stagger, so multiple
// carriers desync onto different rounds cadences instead of all firing together. It
// uses a 64-bit FNV-1a hash (vs. shiftLatenessOffset's 32-bit) because the modulus
// is a nanosecond-scale interval (2h ≈ 7.2e12 ns) that dwarfs a uint32 — a 32-bit
// hash would only ever spread offsets across the first ~4.3s of the interval,
// leaving carriers effectively synchronized. interval <= 0 returns 0.
func constableRoundsOffset(id ActorID, interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return time.Duration(h.Sum64() % uint64(interval))
}

// mostRecentRoundsInstant returns the latest instant at-or-before now of the form
// k*interval + offset (aligned to the Unix epoch), i.e. the rounds "beat" the
// current tick belongs to. ConstableRoundsDue compares it against the last-rounds
// stamp: a stamp older than this instant means a fresh beat has passed unserved.
// The guard handles the (never-in-practice) pre-epoch case where Go's
// truncated-toward-zero division could otherwise land past now.
func mostRecentRoundsInstant(now time.Time, interval, offset time.Duration) time.Time {
	nowNanos := now.UnixNano()
	k := (nowNanos - int64(offset)) / int64(interval)
	inst := k*int64(interval) + int64(offset)
	if inst > nowNanos {
		inst -= int64(interval)
	}
	return time.Unix(0, inst).UTC()
}
