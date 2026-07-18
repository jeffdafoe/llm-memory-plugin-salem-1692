package sim

import (
	"fmt"
	"log"
	"time"
)

// turn_in.go — LLM-447. The voluntary bed-down: an agent NPC ending its own
// evening.
//
// Sleep used to be entirely deterministic — there was no sleep verb in the tool
// registry, so an NPC went to bed only when the engine put it there
// (npcSleepHere / AutoBedAtHomeNPCs at the civil bedtime hour, or the red-
// tiredness march). LLM-148/LLM-352 deliberately widened the gap between dusk
// and that bedtime hour so NPCs would get an evening — but the evening had no
// exit. Home is the terminal destination of everyone's day, so a household that
// had finished its day could not end the conversation by anyone LEAVING, and the
// only affordance left was talk.
//
// The live failure (2026-07-16) was the Walker "Long Goodnight": three women at
// the Walker Residence cycling six huddles of wind-down talk over ~80 minutes —
// "let's be off then" three times without moving, then 26 goodnights in two
// minutes — and nobody went to bed. Every topic the models generated was
// ending-shaped and not one mapped to a tool. turn_in is that missing tool: the
// third act of a family evening, reachable from dusk.
//
// It adds no new sleep machinery. The gate (npcMayTurnIn) is the auto-bed's own
// residency/off-shift predicate with the night window's open widened to dusk, the
// bed-down is executeNPCSleep, and waking stays entirely on WakeExpiredNPCSleepers.
// The deterministic backstops are untouched — turn_in only opens a voluntary
// window earlier.

// TurnIn is the commit for the turn_in tool: the actor bids any companions
// goodnight and goes to bed. Terminal-on-success — the tick ends here, because
// the actor is asleep.
//
// say is the model's goodnight, spoken to the huddle before it leaves. It rides
// THIS tool rather than a separate speak call per the terminal-verb rule: speak
// is itself terminal, so a cue that asked for both could never be obeyed — the
// first call to land would end the tick and the harness would skip the rest of
// the batch. Folding the utterance in is what makes "say goodnight AND actually
// go to bed" a single expressible act.
//
// MUST run on the world goroutine.
func TurnIn(actorID ActorID, say string, hasNewNews bool, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok || actor == nil {
				return nil, fmt.Errorf("TurnIn: actor %q not in world", actorID)
			}
			if actor.SleepingUntil != nil {
				return nil, ModelFacingError{Msg: "you are already abed."}
			}
			if actor.MoveIntent != nil {
				return nil, ModelFacingError{Msg: "you are walking — get where you're going before you turn in."}
			}
			// Land a finished-but-not-yet-swept activity window first so a stale one
			// doesn't spuriously read as "still busy" (matches StartOrJoinBake/StartStoke).
			completeIfDue(w, actorID, actor, now)
			if actor.SourceActivity != nil {
				return nil, ModelFacingError{Msg: "you are in the middle of something — finish it before you go to bed."}
			}
			// Re-validate the advertised gate at the substrate. The perception
			// TurnInChoice gate is an optimization; this Command is the authority, so a
			// stale or forged call can't bed an actor in the wrong place or at noon
			// (the same posture StartOrJoinBake/StartStoke take).
			if !npcMayTurnIn(w, actor, now) {
				return nil, ModelFacingError{Msg: turnInRefusal(w, actor, now)}
			}
			// The goodnight rides the tool: speak it to the huddle, THEN leave with the
			// departure classified as a retire, so the others read "X has turned in for
			// the night" rather than "X stepped away" (LLM-438 peer stamps, LLM-447
			// classification). Best-effort on the say, like bake's and scene_quote's —
			// a refused utterance must not strand the actor awake mid-goodnight.
			//
			// Leaving BEFORE executeNPCSleep is deliberate: executeNPCSleep speaks its
			// own deterministic engine-authored retire line when it finds the bedding
			// actor still in an active huddle. Having already left, the actor's own
			// words are the goodnight the room hears — the engine line would be a
			// second, redundant farewell in the model's voice's place.
			// The say is attempted whenever there IS one, not only inside a huddle: an
			// actor can be co-present with others it has not yet huddled with, and its
			// goodnight should still be heard. SpeakTo's own audience gate refuses the
			// genuinely-empty room, which is the only case that logs.
			if say != "" {
				if _, serr := SpeakTo(actorID, say, "", nil, hasNewNews, now).Fn(w); serr != nil {
					log.Printf("sim: turn_in %q bid goodnight but the say was refused: %v", actorID, serr)
				}
			}
			if actor.CurrentHuddleID != "" {
				leaveCurrentHuddleAs(w, actor, now, WarrantKindHuddlePeerRetired)
			}
			if !executeNPCSleep(w, actor, now) {
				// Only reachable if something bedded the actor between the check above
				// and here; both run inside this Fn on the world goroutine, so it can't
				// happen today. Reported rather than silently swallowed.
				return nil, ModelFacingError{Msg: "you are already abed."}
			}
			return TurnInResult{BeddedAt: actor.InsideStructureID, Until: *actor.SleepingUntil}, nil
		},
	}
}

// TurnInResult is the tool result for a successful voluntary bed-down. The
// mechanical detail (where, until when) belongs here rather than in the
// deliberation scene — precision after the decision, prose during it.
type TurnInResult struct {
	// BeddedAt is the structure the actor went to bed in.
	BeddedAt StructureID
	// Until is the sleep cap. The actual wake is whichever of the cap, shift
	// start, or morning fires first (WakeExpiredNPCSleepers) — this is the bound,
	// not a promise.
	Until time.Time
}

// turnInRefusal explains a refused turn_in in the actor's own terms, so a model
// that called it off-gate learns WHICH condition failed rather than being told a
// flat "no". Ordered most-specific first; the arms mirror npcMayTurnIn.
func turnInRefusal(w *World, a *Actor, now time.Time) string {
	if npcSleepArmFor(w, a, now) == npcSleepArmNone {
		if a.InsideStructureID == "" {
			return "you are out in the open — you have no bed here."
		}
		// actorOnShift, not isActorOnShift — it is the predicate the arm itself
		// used, so an unscheduled worker inside its day-active window (which has no
		// schedule row, and which isActorOnShift would call off-shift) is told the
		// truth: it is still its working day.
		if actorOnShift(w, a, localMinuteOfDay(w, now)) {
			return "your work is not done — you cannot go to bed mid-shift."
		}
		return "this is not where you sleep — go home first."
	}
	return "it is too early to turn in — the evening has not yet come."
}
