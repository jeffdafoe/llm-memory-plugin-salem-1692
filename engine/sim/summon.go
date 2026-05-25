package sim

import (
	"fmt"
	"log"
	"time"
)

// summon.go — the summon messenger-errand substrate (ZBBS-HOME-311). v2
// in-memory port of v1's summon(target, reason?) tool. An NPC asks the
// engine to fetch another villager; the engine does NOT teleport anyone —
// it runs a multi-leg messenger errand:
//
//  1. the summoner walks to a summon_point village object,
//  2. a free messenger-attribute NPC walks to the same summon_point,
//  3. the messenger pauses (chat beat), the summoner commissions it and the
//     messenger acknowledges ("At once"),
//  4. the messenger walks to the target's location,
//  5. the messenger pauses (chat beat), then delivers
//     "<target>, <summoner> summons you. <reason>,"
//  6. the messenger walks back to where it started — errand done.
//
// Refusal branch: if at the delivery dispatch the target can no longer be
// located (gone, or has no walkable location), the messenger turns around
// and walks back to the summoner, delivering "<target> could not be found"
// on arrival.
//
// ARCHITECTURE (locked):
//   - In-memory only. The errand lives in World.SummonErrands
//     (map[ErrandID]*summonErrand); NO DB table, NO migration. Restart-loss
//     is accepted (matches v1's transient ticker). Precedent:
//     World.BusinessownerCooldowns / World.ActiveRoutes.
//   - Walk-leg completion advances the machine off the ActorArrived event
//     (RegisterSummonSubscriber). The two chat beats (at the point before
//     commissioning, at the target before delivery) are scheduled via the
//     existing time.AfterFunc → world.SendContext pattern (see
//     reactor_evaluator.go / world_phase.go).
//   - BOUNDED MEMBERSHIP: every terminal path removes the errand from the
//     map (see finishErrand). A leaked errand would suppress the
//     summoner's arrival warrants forever (see suppressArrivalWarrant
//     below), so there is no exit that leaves a stale entry.

// ErrandID is the monotonic per-run identifier for a summon errand. Same
// shape and rules as the other per-run seq counters (eventSeq, quoteSeq):
// world-goroutine-only, first minted id is 1, ErrandID(0) is the unset
// sentinel.
type ErrandID uint64

// AttrMessenger is the actor attribute slug marking an NPC eligible to run
// summon errands. The messenger must additionally be a non-VA NPC
// (LLMAgent == "") so dispatching it never burns an LLM tick, and must not
// already be busy on another errand.
const AttrMessenger = "messenger"

// SummonPointTag is the VillageObject tag marking a structure-visit anchor
// where the summoner and messenger rendezvous before the errand fans out.
const SummonPointTag = "summon_point"

// summonState enumerates the legs of an errand. Transitions:
//
//	dispatched          --ActorArrived(summoner)--> summonerAtPoint
//	summonerAtPoint      --ActorArrived(messenger)--> messengerAtPoint
//	messengerAtPoint     --chat-pause callback------> messengerToTarget
//	messengerToTarget    --ActorArrived(messenger)--> messengerAtTarget
//	messengerAtTarget    --chat-pause callback------> messengerReturning
//	messengerReturning   --ActorArrived(messenger)--> done (errand removed)
//
// Refusal branch (entered from messengerAtPoint's callback when the target
// can no longer be located):
//
//	messengerToSummoner  --ActorArrived(messenger)--> done (errand removed)
type summonState string

const (
	summonDispatched          summonState = "dispatched"
	summonSummonerAtPoint     summonState = "summoner_at_point"
	summonMessengerAtPoint    summonState = "messenger_at_point"
	summonMessengerToTarget   summonState = "messenger_to_target"
	summonMessengerAtTarget   summonState = "messenger_at_target"
	summonMessengerReturning  summonState = "messenger_returning"
	summonMessengerToSummoner summonState = "messenger_to_summoner"
)

// summonChatPause is the beat the messenger holds at the summon point
// (before commissioning) and at the target (before delivering) so the
// canned speech reads as turn-taking rather than instant teleport-chatter.
// Fixed (not settings-driven) — the errand is rare and the pause is pure
// pacing flavor, the same posture as the businessowner speech beats.
const summonChatPause = 1500 * time.Millisecond

// summonErrandTTL bounds an errand's lifetime. The arrival-warrant suppression
// hook (actorInActiveSummonErrand) keys off errand MEMBERSHIP with no time
// bound, so an errand that never reaches a terminal arrival — a walk leg
// superseded by an out-of-band MoveActor before its ActorArrived fires, or any
// other stall — would suppress the summoner's arrival warrants forever (a dead
// NPC). v1 avoided this implicitly: its suppression was a time-boxed
// agent_override_until that expired on its own even when a summon_errand row
// got stuck. This is the v2 analog: on dispatch we arm a one-shot timer that
// removes the errand if it's still in flight at the cap, guaranteeing bounded
// membership regardless of cause. Generous — a legitimate errand (a few short
// walks + two 1.5s beats) completes in well under a minute; the TTL only ever
// fires on a genuinely stuck errand.
const summonErrandTTL = 10 * time.Minute

// summonErrand is the in-flight state of one summon. Stored in
// World.SummonErrands keyed by ErrandID. Owned by the world goroutine
// (mutated only from inside Command.Fn or an inline subscriber, both of
// which run on the world goroutine).
type summonErrand struct {
	ID          ErrandID
	SummonerID  ActorID
	MessengerID ActorID
	TargetID    ActorID
	Reason      string // optional; "" when the summoner gave none

	State summonState

	// SummonPointID is the village object the rendezvous happens at — kept
	// for the place label in the target-side perception cue.
	SummonPointID VillageObjectID

	// MessengerOrigin is where the messenger started, so the final return
	// leg sends it home (a Position destination — the messenger may be a
	// homeless decorative NPC, so we don't assume a HomeStructureID).
	MessengerOrigin Position

	// LegAttemptID is the MovementAttemptID of the walk leg currently in
	// flight for the actor we're waiting on. ActorArrived advances the
	// machine only when its MovementAttemptID matches — a superseded or
	// stale arrival (admin force-move, an out-of-band MoveActor) is ignored,
	// same guard posture as npc_route's WalkTo check.
	LegAttemptID MovementAttemptID
}

// RegisterSummonSubscriber wires the summon errand machine into the world:
// the ActorArrived subscriber that advances walk legs, the response-fade
// subscriber that drops a summon cue when its holder acts (move/speak/break),
// and the arrival-warrant suppression hook that keeps an errand participant
// from LLM-ticking mid-errand. Must run on the world goroutine (call before
// World.Run or from inside a Command.Fn).
//
// Idempotency: the suppression hook is installed unconditionally (last
// writer wins; the predicate is stateless over the errand map). Registering
// the subscriber twice would advance the machine twice per ActorArrived —
// the second pass sees the leg already advanced (LegAttemptID no longer
// matches) and no-ops. Worth not doing, but not a correctness violation.
func RegisterSummonSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterSummonSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleSummonArrival))
	w.Subscribe(SubscriberFunc(handleSummonResponseFade))
	w.suppressArrivalWarrant = func(a *Actor) bool {
		return actorInActiveSummonErrand(w, a)
	}
}

// actorInActiveSummonErrand reports whether actor is the summoner or
// messenger of any active errand. Consulted by the locomotion ticker's
// finishArrival (via World.suppressArrivalWarrant) to suppress the arrival
// warrant on an errand participant: the summoner shouldn't LLM-tick and
// wander off mid-errand. (The messenger is a non-VA NPC and never
// LLM-ticks anyway, but suppressing it too costs nothing and keeps the
// predicate simple.)
//
// MUST be called from inside a Command.Fn / inline subscriber (reads the
// errand map).
func actorInActiveSummonErrand(w *World, actor *Actor) bool {
	if actor == nil {
		return false
	}
	for _, e := range w.SummonErrands {
		if e == nil {
			continue
		}
		if e.SummonerID == actor.ID || e.MessengerID == actor.ID {
			return true
		}
	}
	return false
}

// summonerHasActiveErrand / messengerIsBusy are the one-active-errand-per-
// participant guards. A summoner with an in-flight errand can't dispatch a
// second; a messenger already running one isn't "free" for selection.
func summonerHasActiveErrand(w *World, summoner ActorID) bool {
	for _, e := range w.SummonErrands {
		if e != nil && e.SummonerID == summoner {
			return true
		}
	}
	return false
}

func messengerIsBusy(w *World, messenger ActorID) bool {
	for _, e := range w.SummonErrands {
		if e != nil && e.MessengerID == messenger {
			return true
		}
	}
	return false
}

// nextErrandSeq increments the per-run errand counter and returns the new
// ErrandID. World-goroutine-only. First id is 1.
func (w *World) nextErrandSeq() ErrandID {
	w.errandSeq++
	return ErrandID(w.errandSeq)
}

// DispatchSummon returns a Command that runs the summon pre-checks and, on
// success, starts the errand (summoner walks to the summon point). It is the
// world-goroutine half of the summon tool — the handler (handlers.HandleSummon)
// is a pure builder that returns this command.
//
// Rejections (surfaced to the model as tool errors so it can retry / pick
// another action):
//   - summoner not in world (wiring bug — defense in depth).
//   - target is the summoner itself ("you cannot summon yourself").
//   - target names no actor in the world.
//   - the summoner already has an errand in flight.
//   - no summon_point village object exists.
//   - no free messenger NPC is available.
//   - the summoner can't path to the summon point.
//
// On success the summoner is dispatched (MoveActor visit → summon point) and
// the errand is inserted at state `dispatched`.
func DispatchSummon(summonerID, targetID ActorID, reason string, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if _, ok := w.Actors[summonerID]; !ok {
				return nil, fmt.Errorf("summon: actor %q not in world", summonerID)
			}
			if targetID == summonerID {
				return nil, fmt.Errorf("you cannot summon yourself")
			}
			// Existence is the only target pre-check; the target's location
			// is re-resolved at the delivery leg (it may move between now and
			// the messenger's arrival).
			if _, ok := w.Actors[targetID]; !ok {
				return nil, fmt.Errorf(
					"there is no one called %q to summon — name someone you know is in the village", targetID)
			}
			if summonerHasActiveErrand(w, summonerID) {
				return nil, fmt.Errorf("you have already sent for someone — wait for your messenger to return")
			}

			pointID, ok := findSummonPoint(w)
			if !ok {
				return nil, fmt.Errorf("there is nowhere to send for someone from — no summoning place exists in the village")
			}
			pointStructure := StructureID(pointID)
			if _, ok := w.Structures[pointStructure]; !ok {
				// A summon_point tag on an object with no backing structure
				// can't be walked to via the structure-visit destination.
				// Treat as "no summon point" rather than dispatching an
				// unreachable errand.
				return nil, fmt.Errorf("there is nowhere to send for someone from — the summoning place cannot be reached")
			}

			messengerID, ok := findFreeMessenger(w, summonerID, targetID)
			if !ok {
				return nil, fmt.Errorf("there is no messenger free to carry your summons right now")
			}
			messenger := w.Actors[messengerID]

			// Dispatch the summoner to the summon point (visit slot — they
			// stand outside, no entry required). leaveHuddleFirst=true: the
			// summoner leaves any conversation to go wait for the messenger,
			// matching move_to's "choosing to walk away leaves it" posture.
			res, err := MoveActor(summonerID, NewStructureVisitDestination(pointStructure), true, now).Fn(w)
			if err != nil {
				return nil, fmt.Errorf("you cannot reach the summoning place from here: %w", err)
			}
			moveRes := res.(MoveActorResult)

			errand := &summonErrand{
				ID:              w.nextErrandSeq(),
				SummonerID:      summonerID,
				MessengerID:     messengerID,
				TargetID:        targetID,
				Reason:          reason,
				State:           summonDispatched,
				SummonPointID:   pointID,
				MessengerOrigin: Position{X: messenger.Pos.X, Y: messenger.Pos.Y},
				LegAttemptID:    moveRes.MovementAttemptID,
			}
			if w.SummonErrands == nil {
				w.SummonErrands = map[ErrandID]*summonErrand{}
			}
			w.SummonErrands[errand.ID] = errand
			armSummonErrandTTL(w, errand.ID)

			log.Printf("sim/summon: errand %d dispatched — %q summons %q via messenger %q",
				errand.ID, summonerID, targetID, messengerID)
			return errand.ID, nil
		},
	}
}

// findSummonPoint returns the VillageObjectID of a summon_point-tagged
// object, deterministically (lowest id) when several exist, or ok=false
// when none do.
func findSummonPoint(w *World) (VillageObjectID, bool) {
	var best VillageObjectID
	found := false
	for id, obj := range w.VillageObjects {
		if obj == nil || !obj.HasTag(SummonPointTag) {
			continue
		}
		if !found || id < best {
			best = id
			found = true
		}
	}
	return best, found
}

// findFreeMessenger returns the id of a free messenger NPC: carries the
// messenger attribute, is a non-VA NPC (LLMAgent == ""), and is not already
// running an errand. Excludes the summoner and target so neither is dispatched
// to fetch itself — a self-messenger can never be observed in the messenger
// role (errandForArrival resolves the summoner role first), stranding the
// machine; a target-messenger would be sent to fetch itself. v1's
// findNearestMessenger likewise excluded the summoner. Deterministic (lowest
// id) when several qualify, ok=false when none do.
func findFreeMessenger(w *World, summonerID, targetID ActorID) (ActorID, bool) {
	var best ActorID
	found := false
	for id, a := range w.Actors {
		if a == nil {
			continue
		}
		if id == summonerID || id == targetID {
			continue
		}
		if _, ok := a.Attributes[AttrMessenger]; !ok {
			continue
		}
		if a.LLMAgent != "" { // VA-backed actor — not eligible (we don't burn LLM ticks on errands)
			continue
		}
		if messengerIsBusy(w, id) {
			continue
		}
		if !found || id < best {
			best = id
			found = true
		}
	}
	return best, found
}

// handleSummonArrival is the inline ActorArrived subscriber. Runs on the
// world goroutine during emit; advances the matching errand leg. Non-arrival
// events and arrivals for actors in no errand fall through.
func handleSummonArrival(w *World, evt Event) {
	arrived, ok := evt.(*ActorArrived)
	if !ok {
		return
	}
	e, role := errandForArrival(w, arrived.ActorID)
	if e == nil {
		return
	}
	// Stale-arrival guard: only the leg we dispatched advances the machine.
	// A superseded MoveActor (admin force-move, out-of-band walk) carries a
	// different MovementAttemptID and is ignored — the next legitimate
	// arrival resyncs.
	if arrived.MovementAttemptID != e.LegAttemptID {
		return
	}
	now := arrived.At
	switch e.State {
	case summonDispatched:
		if role == summonRoleSummoner {
			onSummonerAtPoint(w, e, now)
		}
	case summonSummonerAtPoint:
		if role == summonRoleMessenger {
			onMessengerAtPoint(w, e, now)
		}
	case summonMessengerToTarget:
		if role == summonRoleMessenger {
			onMessengerAtTarget(w, e, now)
		}
	case summonMessengerReturning:
		if role == summonRoleMessenger {
			finishErrand(w, e, "delivered")
		}
	case summonMessengerToSummoner:
		if role == summonRoleMessenger {
			onMessengerBackAtSummoner(w, e, now)
		}
	}
}

type summonRole int

const (
	summonRoleNone summonRole = iota
	summonRoleSummoner
	summonRoleMessenger
)

// errandForArrival finds the active errand an arriving actor participates in
// and which role it plays. Returns nil when the actor is in no errand.
func errandForArrival(w *World, actorID ActorID) (*summonErrand, summonRole) {
	for _, e := range w.SummonErrands {
		if e == nil {
			continue
		}
		if e.SummonerID == actorID {
			return e, summonRoleSummoner
		}
		if e.MessengerID == actorID {
			return e, summonRoleMessenger
		}
	}
	return nil, summonRoleNone
}

// onSummonerAtPoint: the summoner reached the summon point. Dispatch the
// messenger to the same point. Advance to summonerAtPoint, tracking the
// messenger's walk-leg attempt id.
func onSummonerAtPoint(w *World, e *summonErrand, now time.Time) {
	pointStructure := StructureID(e.SummonPointID)
	res, err := MoveActor(e.MessengerID, NewStructureVisitDestination(pointStructure), true, now).Fn(w)
	if err != nil {
		// The messenger can't reach the rendezvous (path blocked). Abandon
		// the errand cleanly — better than a dangling entry no arrival can
		// advance. The summoner waited in vain; no refusal speech (the
		// messenger never set out), the summoner's perception simply has no
		// summons cue.
		log.Printf("sim/summon: errand %d abandoned — messenger %q cannot reach summon point: %v",
			e.ID, e.MessengerID, err)
		finishErrand(w, e, "messenger_unreachable")
		return
	}
	e.LegAttemptID = res.(MoveActorResult).MovementAttemptID
	e.State = summonSummonerAtPoint
}

// onMessengerAtPoint: the messenger reached the summon point. Hold the chat
// beat, then commission the messenger. Advance to messengerAtPoint and
// schedule the commissioning callback.
func onMessengerAtPoint(w *World, e *summonErrand, now time.Time) {
	e.State = summonMessengerAtPoint
	scheduleSummonChatPause(w, e.ID, summonCommission)
}

// onMessengerAtTarget: the messenger reached the target's location. Hold the
// chat beat, then deliver. Advance to messengerAtTarget and schedule the
// delivery callback.
func onMessengerAtTarget(w *World, e *summonErrand, now time.Time) {
	e.State = summonMessengerAtTarget
	scheduleSummonChatPause(w, e.ID, summonDeliver)
}

// onMessengerBackAtSummoner: the messenger returned to the summoner on the
// refusal branch. Emit the refusal line, stamp the summoner-side perception
// cue, and finish.
func onMessengerBackAtSummoner(w *World, e *summonErrand, now time.Time) {
	summoner := w.Actors[e.SummonerID]
	messenger := w.Actors[e.MessengerID]
	if summoner != nil && messenger != nil {
		emitSummonSpoke(w, e.MessengerID, summoner.CurrentHuddleID, []ActorID{e.SummonerID},
			summonRefusalText(targetDisplayName(w, e.TargetID)), now)
		summoner.SummonRefusal = &SummonRefusal{
			TargetName: targetDisplayName(w, e.TargetID),
			At:         now,
		}
	}
	finishErrand(w, e, "refused")
}

// summonChatPauseKind selects which canned beat a scheduled chat-pause
// callback runs when it fires on the world goroutine.
type summonChatPauseKind int

const (
	summonCommission summonChatPauseKind = iota // commissioning at the summon point
	summonDeliver                               // delivery at the target
)

// scheduleSummonChatPause arms a one-shot time.AfterFunc that, after the
// chat beat, sends the matching follow-up command onto the world goroutine.
// Mirrors the reactor evaluator / phase flip scheduled-callback pattern:
// the timer callback uses LifecycleContext so a shutdown-while-armed aborts
// cleanly via ctx.Err() instead of parking on a dead cmds channel.
//
// The follow-up command re-reads the errand by id and bails if it's gone or
// has moved on (superseded by an abandon, or the world advanced) — the
// errand-generation check is the WorldEventGen-free analog of the phase
// flip's guard: identity by (id, expected state) rather than a generation
// counter, since an errand is single-owner and can only legitimately be in
// one state when its beat fires.
//
// MUST be called from inside a Command.Fn / inline subscriber.
func scheduleSummonChatPause(w *World, id ErrandID, kind summonChatPauseKind) {
	time.AfterFunc(summonChatPause, func() {
		ctx := w.LifecycleContext()
		if ctx.Err() != nil {
			return
		}
		_, err := w.SendContext(ctx, runSummonChatPause(id, kind, time.Now().UTC()))
		if err != nil && ctx.Err() == nil {
			log.Printf("sim/summon: chat-pause callback for errand %d failed: %v", id, err)
		}
	})
}

// runSummonChatPause is the body of a scheduled chat-pause callback, run on
// the world goroutine. Re-resolves the errand and fires the commissioning or
// delivery beat. Factored as a Command so tests can drive it synchronously
// (via .Fn(w)) without real-time waits.
func runSummonChatPause(id ErrandID, kind summonChatPauseKind, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			e, ok := w.SummonErrands[id]
			if !ok || e == nil {
				return nil, nil // errand already terminal (abandoned/superseded) — no-op
			}
			switch kind {
			case summonCommission:
				if e.State == summonMessengerAtPoint {
					summonDoCommission(w, e, now)
				}
			case summonDeliver:
				if e.State == summonMessengerAtTarget {
					summonDoDeliver(w, e, now)
				}
			}
			return nil, nil
		},
	}
}

// armSummonErrandTTL starts the one-shot bounded-lifetime timer for an errand
// (see summonErrandTTL). Mirrors scheduleSummonChatPause: a time.AfterFunc hops
// onto the world goroutine via SendContext, guarded by LifecycleContext so a
// shutdown-while-armed aborts cleanly. The fire re-resolves the errand by id
// (no-op if it already reached a terminal path and was removed), so no explicit
// cancel is needed on normal completion — monotonic ErrandIDs are never reused.
// MUST be called from inside a Command.Fn / inline subscriber.
func armSummonErrandTTL(w *World, id ErrandID) {
	time.AfterFunc(summonErrandTTL, func() {
		ctx := w.LifecycleContext()
		if ctx.Err() != nil {
			return
		}
		if _, err := w.SendContext(ctx, expireSummonErrand(id)); err != nil && ctx.Err() == nil {
			log.Printf("sim/summon: TTL expiry for errand %d failed: %v", id, err)
		}
	})
}

// expireSummonErrand is the body of the TTL timer, run on the world goroutine.
// If the errand is still in flight at the cap, it finishes it — removing the
// map entry (which lifts the arrival-warrant suppression on the summoner, the
// whole point) and abandoning whatever leg stalled. A no-op when the errand
// already completed normally (re-resolved by id, gone from the map). Factored
// as a Command so tests can drive it synchronously without a real-time wait.
func expireSummonErrand(id ErrandID) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			e, ok := w.SummonErrands[id]
			if !ok || e == nil {
				return nil, nil // already terminal — nothing to clean up
			}
			finishErrand(w, e, "expired")
			return nil, nil
		},
	}
}

// summonDoCommission emits the commissioning beat (summoner asks the
// messenger to fetch the target; messenger acknowledges) and dispatches the
// messenger toward the target. If the target can no longer be located, it
// turns the messenger around toward the summoner (refusal branch). Advances
// the errand to messengerToTarget or messengerToSummoner.
func summonDoCommission(w *World, e *summonErrand, now time.Time) {
	summoner := w.Actors[e.SummonerID]
	messenger := w.Actors[e.MessengerID]
	if summoner == nil || messenger == nil {
		// A participant vanished mid-errand. Nothing to commission; finish.
		finishErrand(w, e, "participant_gone")
		return
	}

	targetName := targetDisplayName(w, e.TargetID)
	// Commissioning line (summoner → messenger) and the messenger's "At once."
	emitSummonSpoke(w, e.SummonerID, messenger.CurrentHuddleID, []ActorID{e.MessengerID},
		summonCommissionText(targetName, e.Reason), now)
	emitSummonSpoke(w, e.MessengerID, messenger.CurrentHuddleID, []ActorID{e.SummonerID},
		summonAcknowledgeText(), now)

	dest, ok := summonTargetDestination(w, e.TargetID)
	if !ok {
		// Target can't be located — turn the messenger around. The refusal
		// is carried back to the summoner on the return walk's arrival.
		startRefusalReturn(w, e, now)
		return
	}
	res, err := MoveActor(e.MessengerID, dest, true, now).Fn(w)
	if err != nil {
		// Path to the target failed (e.g. target indoors behind a locked
		// door we can't visit). Treat as not-found → refusal return.
		startRefusalReturn(w, e, now)
		return
	}
	e.LegAttemptID = res.(MoveActorResult).MovementAttemptID
	e.State = summonMessengerToTarget
}

// summonDoDeliver emits the delivery beat at the target, stamps the
// target-side perception cue + the action-log entry, then sends the
// messenger home (final leg). Advances the errand to messengerReturning.
func summonDoDeliver(w *World, e *summonErrand, now time.Time) {
	messenger := w.Actors[e.MessengerID]
	target := w.Actors[e.TargetID]
	if messenger == nil || target == nil {
		// Target left between arrival and the delivery beat. No one to
		// deliver to → refusal return to the summoner.
		startRefusalReturn(w, e, now)
		return
	}

	summonerName := summonerDisplayName(w, e.SummonerID)
	deliveryText := summonDeliveryText(target.DisplayName, summonerName, e.Reason)
	emitSummonSpoke(w, e.MessengerID, target.CurrentHuddleID, []ActorID{e.TargetID}, deliveryText, now)

	// Target-side perception cue — drives the summoned NPC to move_to the
	// summon point. Faded after the target next acts (see consumeSummonCues).
	target.PendingSummon = &PendingSummon{
		SummonerName: summonerName,
		Place:        summonPointLabel(w, e.SummonPointID),
		Reason:       e.Reason,
		At:           now,
	}

	// Action-log row: the delivery is the target's summons event. Best-effort
	// — a log-append failure must not abort the errand (the delivery already
	// landed), so log and continue rather than propagate.
	if _, err := AppendActionLogEntry(ActionLogEntry{
		ActorID:    e.TargetID,
		OccurredAt: now,
		ActionType: ActionTypeSummoned,
		Text:       deliveryText,
		HuddleID:   target.CurrentHuddleID,
	}).Fn(w); err != nil {
		log.Printf("sim/summon: errand %d action-log append failed: %v", e.ID, err)
	}

	// Final leg: messenger walks back to where it started.
	res, err := MoveActor(e.MessengerID, NewPositionDestination(e.MessengerOrigin), true, now).Fn(w)
	if err != nil {
		// Can't get home (origin tile now blocked). The delivery already
		// landed, so this is a clean done — just remove the errand rather
		// than stranding it.
		log.Printf("sim/summon: errand %d delivered but messenger %q cannot return home: %v — finishing",
			e.ID, e.MessengerID, err)
		finishErrand(w, e, "delivered_no_return")
		return
	}
	e.LegAttemptID = res.(MoveActorResult).MovementAttemptID
	e.State = summonMessengerReturning
}

// startRefusalReturn turns the messenger toward the summoner to deliver the
// "could not be found" refusal. If the messenger can't even path back, the
// errand is finished in place (the summoner gets the refusal perception cue
// directly so the failure still surfaces). Advances to messengerToSummoner.
func startRefusalReturn(w *World, e *summonErrand, now time.Time) {
	summoner := w.Actors[e.SummonerID]
	if summoner == nil {
		finishErrand(w, e, "participant_gone")
		return
	}
	// Walk to the summoner's current tile.
	dest := NewPositionDestination(Position{X: summoner.Pos.X, Y: summoner.Pos.Y})
	res, err := MoveActor(e.MessengerID, dest, true, now).Fn(w)
	if err != nil {
		// Can't return — stamp the refusal cue directly and finish.
		summoner.SummonRefusal = &SummonRefusal{
			TargetName: targetDisplayName(w, e.TargetID),
			At:         now,
		}
		finishErrand(w, e, "refused_no_return")
		return
	}
	e.LegAttemptID = res.(MoveActorResult).MovementAttemptID
	e.State = summonMessengerToSummoner
}

// finishErrand removes the errand from the world map. THE bounded-membership
// chokepoint: every terminal path (normal completion, refusal, abandon,
// participant-gone) routes through here, so no exit leaves a stale entry that
// would suppress the summoner's arrival warrants forever.
func finishErrand(w *World, e *summonErrand, outcome string) {
	delete(w.SummonErrands, e.ID)
	log.Printf("sim/summon: errand %d finished (%s)", e.ID, outcome)
}

// summonTargetDestination resolves where the messenger should walk to reach
// the target: the target's structure (visit slot) when it's inside or anchored
// to one, else its current tile. ok=false when the target actor is gone.
func summonTargetDestination(w *World, targetID ActorID) (MoveDestination, bool) {
	target, ok := w.Actors[targetID]
	if !ok {
		return MoveDestination{}, false
	}
	// Prefer the structure the target is inside (visit its slot — the
	// messenger stands outside to deliver). Fall back to the target's home
	// structure if it has one and the structure exists, else the target's
	// bare tile.
	if target.InsideStructureID != "" {
		if _, ok := w.Structures[target.InsideStructureID]; ok {
			return NewStructureVisitDestination(target.InsideStructureID), true
		}
	}
	return NewPositionDestination(Position{X: target.Pos.X, Y: target.Pos.Y}), true
}

// targetDisplayName / summonerDisplayName resolve a display name, falling
// back to the raw id when the actor is missing (defensive — an actor could
// be deleted mid-errand).
func targetDisplayName(w *World, id ActorID) string {
	if a, ok := w.Actors[id]; ok && a.DisplayName != "" {
		return a.DisplayName
	}
	return string(id)
}

func summonerDisplayName(w *World, id ActorID) string {
	return targetDisplayName(w, id)
}

// summonPointLabel resolves the human label for the summon point used in the
// target's "come to <place>" cue. Falls back to a generic phrase.
func summonPointLabel(w *World, id VillageObjectID) string {
	if obj, ok := w.VillageObjects[id]; ok && obj != nil && obj.DisplayName != "" {
		return obj.DisplayName
	}
	if st, ok := w.Structures[StructureID(id)]; ok && st != nil && st.DisplayName != "" {
		return st.DisplayName
	}
	return "the summoning place"
}

// emitSummonSpoke emits a canned (non-LLM) Spoke from the errand machine.
// Mirrors businessowner.go's engine-authored Spoke emission: the standard
// Spoke subscribers (action log, speech reactor) render it identically to
// any speech. recipients is the single addressed listener; HuddleID is the
// speaker's current huddle context (may be empty — a valid state).
func emitSummonSpoke(w *World, speaker ActorID, huddle HuddleID, recipients []ActorID, text string, now time.Time) {
	if recipients == nil {
		recipients = []ActorID{}
	}
	w.emit(&Spoke{
		SpeakerID:    speaker,
		HuddleID:     huddle,
		RecipientIDs: recipients,
		Text:         text,
		At:           now,
	})
}

// --- canned speech ---------------------------------------------------------

func summonCommissionText(targetName, reason string) string {
	if reason != "" {
		return fmt.Sprintf("Go and fetch %s for me. %s", targetName, reason)
	}
	return fmt.Sprintf("Go and fetch %s for me.", targetName)
}

func summonAcknowledgeText() string {
	return "At once."
}

func summonDeliveryText(targetName, summonerName, reason string) string {
	if reason != "" {
		return fmt.Sprintf("%s, %s summons you. %s", targetName, summonerName, reason)
	}
	return fmt.Sprintf("%s, %s summons you.", targetName, summonerName)
}

func summonRefusalText(targetName string) string {
	return fmt.Sprintf("I could not find %s anywhere.", targetName)
}

// clearSummonCues drops the actor's summon perception cues. Called from the
// response-fade subscriber (handleSummonResponseFade) when the actor commits a
// move_to / speak / take_break — its "answer" to the summons in v1's sense.
// No-op when the actor carries no cue (the overwhelming common case).
func clearSummonCues(a *Actor) {
	if a == nil {
		return
	}
	a.PendingSummon = nil
	a.SummonRefusal = nil
}

// handleSummonResponseFade is the inline subscriber that fades a summon cue
// once its holder RESPONDS. v1 dropped a summons from perception only after
// the target committed a move_to / take_break / speak (the three "I acted"
// verbs) — crucially NOT on any tick, so a summoned NPC that ticked for an
// unrelated reason (e.g. ate because it was hungry) kept seeing the summons.
// v2 mirrors that: the cue is a persistent ActorSnapshot field (it survives
// ticks) and is cleared here on ActorMoveStarted / TookBreak / Spoke for the
// acting actor. The errand-driven moves/speech of the messenger and summoner
// also pass through, but neither holds a target-side cue at that point (the
// messenger is never a summon target; the summoner's only cue is a prior
// refusal it has rightly acted past when it moves/speaks again), so clearing
// is correct in every case. Cheap: one map lookup + a nil-field check per
// response event. Runs on the world goroutine during emit.
func handleSummonResponseFade(w *World, evt Event) {
	var actorID ActorID
	switch e := evt.(type) {
	case *ActorMoveStarted:
		actorID = e.ActorID
	case *TookBreak:
		actorID = e.ActorID
	case *Spoke:
		actorID = e.SpeakerID
	default:
		return
	}
	if a, ok := w.Actors[actorID]; ok && a != nil {
		clearSummonCues(a)
	}
}
