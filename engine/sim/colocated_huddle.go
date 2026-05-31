package sim

import (
	"log"
	"sort"
	"time"
)

// colocated_huddle.go — ZBBS-HOME-358. The PC explicit-talk huddle bootstrap.
//
// A PC who walks into an open structure forms NO huddle on arrival: the
// arrival-encounter cascade (cascade/arrival_encounter.go) is OUTDOOR-only
// (it skips any arriver with InsideStructureID != "") and forms huddles via
// StartOutdoorHuddle. The indoor counterpart was the explicit talk/knock path —
// but EnterOrKnock only forms a huddle on a KNOCK (owner-only structure,
// non-member); a plain walk-in through an open door joined nobody. So a PC
// standing in the Tavern with NPCs had CurrentHuddleID == "", and sim.Speak
// (audience = huddle peers) either rejected a name-address (the vocative gate
// sees the NPC as a non-peer → 422) or emitted to no one. The player "can't
// talk to them."
//
// EnsureColocatedHuddle closes that gap: run from the PC speak path, it forms
// the conversation ON the talk action. It delegates to JoinHuddle, which
// already find-or-creates the single active huddle at a structure (the same
// primitive EnterOrKnock uses for the knock service-huddle, called with an
// empty sceneID). Because it forms a REAL structure huddle, it also unblocks
// the transaction paths (pay / order / scene_quote) that gate on
// CurrentHuddleID — not just speech.
//
// Scope: INDOOR only (the live reported case — a PC in the Tavern). An outdoor
// PC speaking among co-located actors is the symmetric follow-on (it would
// mirror the cascade's StartOutdoorHuddle with a speak radius); not handled
// here.

// EnsureColocatedHuddle joins actorID (a PC) into the active huddle at the
// structure it is standing in, together with the other co-located conversational
// actors, when it has no huddle of its own. No-op when the actor is missing, not
// a PC, already in a huddle, not inside a structure, or alone inside. Idempotent.
// MUST run on the world goroutine (call inside a Command.Fn).
//
// PC-only (code_review, ZBBS-HOME-358): this is the explicit-talk bootstrap for
// the human player; NPC conversation forms through the arrival-encounter cascade
// and the reactor. Restricting to KindPC keeps an NPC caller from minting indoor
// huddles outside the intended trigger boundary even though the func is exported.
func EnsureColocatedHuddle(actorID ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok || actor == nil {
				return nil, nil
			}
			if actor.Kind != KindPC {
				return nil, nil // PC explicit-talk bootstrap only (see doc)
			}
			if actor.CurrentHuddleID != "" {
				return nil, nil // already conversing — leave the existing huddle intact
			}
			structureID := actor.InsideStructureID
			if structureID == "" {
				return nil, nil // outdoor — out of scope (see file doc)
			}
			others := colocatedConversationalActors(w, actor, structureID, now)
			if len(others) == 0 {
				return nil, nil // genuinely alone inside — speak-to-no-one stays valid
			}

			// Join the SPEAKER first and bail on failure (code_review): the
			// speaker's join is load-bearing, and joining the others when the
			// speaker stayed out would pollute conversation state among NPCs while
			// the speaker still falls back to speak-to-no-one — worse than not
			// bootstrapping at all. JoinHuddle find-or-creates the structure's
			// active huddle; empty sceneID matches EnterOrKnock's knock-huddle join
			// (no scene minted for an explicit-talk huddle).
			if _, err := JoinHuddle(actor.ID, structureID, "", now).Fn(w); err != nil {
				log.Printf("sim: EnsureColocatedHuddle join speaker %q at %q: %v", actor.ID, structureID, err)
				return nil, nil
			}
			// Pull in each co-located other. JoinHuddle is find-or-create +
			// idempotent, so ordering only affects HuddleJoined/ActorMet "who was
			// already here" payloads. A per-other failure is logged and skipped —
			// the speaker is already in, so the speak reaches whoever did join.
			for _, id := range others {
				if _, err := JoinHuddle(id, structureID, "", now).Fn(w); err != nil {
					log.Printf("sim: EnsureColocatedHuddle join %q at %q: %v", id, structureID, err)
				}
			}
			return nil, nil
		},
	}
}

// colocatedConversationalActors returns the ids (sorted) of conversational,
// currently-unhuddled actors other than self inside structureID. Conversational
// = a stateful/shared NPC or a PC, not asleep. Decorative NPCs and sleepers are
// excluded. Sorted for deterministic huddle-join order and reproducible tests.
//
// CurrentHuddleID == "" is REQUIRED (code_review, ZBBS-HOME-358): JoinHuddle is
// leave-first, so pulling an actor who is ALREADY in a huddle (a knock service-
// huddle with a keeper, another PC's talk-huddle, a not-yet-cleared outdoor
// huddle) into this speaker's huddle would yank them out of their existing
// conversation. We only pull in genuinely unattached co-located actors; an
// already-conversing actor is left alone. (The speaker itself is guaranteed
// unhuddled by EnsureColocatedHuddle's early return, so its own find-or-create
// join is safe.)
func colocatedConversationalActors(w *World, self *Actor, structureID StructureID, now time.Time) []ActorID {
	staleAfter := PCPresenceStaleAfter(w)
	var out []ActorID
	for id, a := range w.Actors {
		if id == self.ID || a == nil {
			continue
		}
		if a.InsideStructureID != structureID {
			continue
		}
		if a.CurrentHuddleID != "" {
			continue // already conversing — never leave-first them out (code_review)
		}
		if !colocatedConversational(a, now, staleAfter) {
			continue
		}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// colocatedConversational reports whether a can be pulled into a co-located
// huddle: a conversational kind (stateful/shared NPC or PC) that is not asleep,
// and — for a PC — not stale/absent (a closed-tab player whose presence stamp
// has gone stale must not be resurrected into a conversation, ZBBS-WORK-326 /
// code_review).
func colocatedConversational(a *Actor, now time.Time, staleAfter time.Duration) bool {
	if a == nil {
		return false
	}
	switch a.Kind {
	case KindNPCStateful, KindNPCShared:
		// conversational NPC kinds
	case KindPC:
		if PCPresenceStale(a.LastPCSeenAt, now, staleAfter) {
			return false // absent player — do not pull into a huddle
		}
	default:
		return false // decorative / unknown
	}
	return a.State != StateSleeping
}
