package sim

import (
	"hash/fnv"
	"time"
)

// constable_rounds.go — LLM-514. The one genuinely new cadence behind the
// constable "walk the rounds" behavior: a fixed wall-clock INTERVAL trigger,
// unlike the lamplighter (event) and washerwoman / town_crier (schedule-window
// boundary, RouteBoundaryDue) routes. The route SUBSTRATE (RouteStop enter/loiter,
// StartNPCRoute, AdvanceNPCRoute) lives in npc_route.go; the candidate builder and
// dwell driver live in the cascade (cascade/npc_route.go). This file is the pure
// sim-side "is a rounds tour due right now?" decision plus the tunable resolvers.

// Constable rounds defaults. Seeded into WorldSettings by the environment loaders
// so GET /settings reports a concrete value and the checkpoint round-trips one.
const (
	// DefaultConstableRoundsInterval is how often the on-shift constable leaves
	// his post to walk the businesses. 2h — frequent enough to feel like a
	// present watch, sparse enough not to keep him perpetually away from the
	// Meeting House.
	DefaultConstableRoundsInterval = 2 * time.Hour

	// DefaultConstableRoundsDwell is how long he pauses at each business so the
	// reactor can tick him in character (a suspicious constable eyeing the keeper).
	DefaultConstableRoundsDwell = 45 * time.Second

	// DefaultConstableRoundsQuiet is how long a stop's conversation must have
	// gone silent before the dwell driver stops deferring the route advance and
	// walks him on (LLM-537). It is a LIVENESS window over the huddle's last
	// activity, deliberately NOT the huddle's lifecycle flag: a huddle stays open
	// for HuddleSilenceTimeout (2h) after its last word so a returning patron
	// resumes the same conversation, so gating the advance on "not concluded"
	// parked him at the stop for as long as the huddle lived — a keeper who
	// correctly said nothing was enough to hold him.
	//
	// 90s is bracketed the same way HuddleLiveWindowDefault is. It sits above
	// DefaultNPCAwaitReplyWindow (60s) with margin, so a conversation whose
	// participants are still trading turns at NPC speed never reads as quiet
	// mid-exchange; and it stays near the 45s dwell the beat is designed around,
	// rather than borrowing the 5-minute HuddleLiveWindow — that knob is sized
	// for the noop-skip preflight's "is anyone here to talk to before I spend an
	// LLM call" question, and coupling the constable's pace to it would mean
	// retuning one retunes the other. Since the driver re-checks on the dwell
	// cadence, release lands 90-135s after the last word.
	DefaultConstableRoundsQuiet = 90 * time.Second
)

// EffectiveConstableRoundsDwell resolves the per-stop dwell duration, applying
// the default for a non-positive setting. A zero dwell is defaulted (not treated
// as an off-switch) because dwelling is what lets the reactor engage the constable
// at a populated stop — a zero-length pause would defeat the whole design. To turn
// rounds OFF, set ConstableRoundsInterval <= 0 (ConstableRoundsDue's gate).
func EffectiveConstableRoundsDwell(w *World) time.Duration {
	if w.Settings.ConstableRoundsDwell > 0 {
		return w.Settings.ConstableRoundsDwell
	}
	return DefaultConstableRoundsDwell
}

// EffectiveConstableRoundsQuiet resolves the stop quiet-window, applying the
// default for a non-positive setting. Same lazy-default posture as the dwell, and
// for the same reason: a zero window is defaulted rather than treated as an
// off-switch, because zero would advance him the instant the huddle's last word
// landed — dragging him out mid-exchange, which is exactly what the deferral is
// there to prevent. To turn rounds OFF, set ConstableRoundsInterval <= 0.
func EffectiveConstableRoundsQuiet(w *World) time.Duration {
	if w.Settings.ConstableRoundsQuiet > 0 {
		return w.Settings.ConstableRoundsQuiet
	}
	return DefaultConstableRoundsQuiet
}

// ConstableStopStillTalking reports whether the constable's current stop
// conversation should keep deferring the rounds advance (LLM-537). True when he is
// in an unconcluded huddle that EITHER saw activity within `quiet` — a spoken line,
// a member joining, a completed transaction — OR is player-attended.
//
// The player arm is not a special case bolted on: a human reads and types, so
// DefaultPCAwaitReplyWindow is 5 minutes against an NPC's 60 seconds, and a
// quiet window sized for NPC turn-taking would walk the constable off mid-sentence
// on a player. huddlePCAttended is the same test the loop sweep and the
// ConversationLooping steer already use to leave player conversations alone
// (LLM-185), so all three agree on what "a player is in this conversation" means.
//
// It is a recent-SPEECH grace period, not evidence the player is at this moment
// reading or composing — that is not observable. A player who has been quiet longer
// than huddlePCAttentionWindow loses the arm and the constable walks on, which is
// the deliberate trade: keying on mere PC membership would let one parked, silent
// player hold every stop he stands in for as long as he stays there.
//
// Deliberately NOT ActorInActiveHuddle: that helper answers the lifecycle question
// (has this huddle concluded), which stays right for its own callers — the
// rest/sleep fallbacks and the StartOutdoorHuddle participant gate — and is wrong
// here. MUST be called from inside a Command.Fn (reads w.Huddles).
func ConstableStopStillTalking(w *World, actor *Actor, now time.Time, quiet time.Duration) bool {
	if actor == nil || actor.CurrentHuddleID == "" {
		return false
	}
	h, ok := w.Huddles[actor.CurrentHuddleID]
	if !ok || h.ConcludedAt != nil {
		return false
	}
	return HuddleIsLive(h, now, quiet) || huddlePCAttended(h, now)
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
	// Only an IN-FLIGHT round blocks a fresh one. A suspended round (LLM-531) is
	// superseded by the next beat — which is also what bounds how long one can sit
	// paused: at most a single interval, after which he starts a fresh circuit
	// rather than carrying yesterday's half-walked one around.
	if RouteInFlight(w, a.ID) {
		return false
	}
	instant := mostRecentRoundsInstant(now, interval, constableRoundsOffset(a.ID, interval))
	if last, has := w.ConstableRoundsStamps[a.ID]; has && !last.Before(instant) {
		return false
	}
	return true
}

// ClearSuspendedRoundIfOffShift drops a SUSPENDED rounds route once its carrier is
// off shift, returning whether it cleared one (LLM-531). This is what bounds the
// duty exemption a suspended round carries: while it sits part-walked, shiftDuty
// leaves him alone so he can choose to resume, and without this sweep that
// exemption would follow him into the night and leave him standing wherever he
// broke off instead of walking home. Only suspended rounds — an in-flight one is
// the route machinery's own business and clears through its normal paths.
// MUST be called from inside a Command.Fn (reads + mutates world state).
func ClearSuspendedRoundIfOffShift(w *World, a *Actor, now time.Time) bool {
	if a == nil {
		return false
	}
	route := w.ActiveRoutes[a.ID]
	if route == nil || route.Phase != RoutePhaseSuspended {
		return false
	}
	if actorOnShift(w, a, localMinuteOfDay(w, now)) {
		return false
	}
	clearActiveRoute(w, a.ID)
	return true
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
