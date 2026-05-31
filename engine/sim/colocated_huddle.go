package sim

import (
	"log"
	"sort"
	"time"
)

// colocated_huddle.go — ZBBS-HOME-358 (PC) + ZBBS-HOME-363 (NPC). The
// explicit-talk huddle bootstrap for an actor standing inside an OPEN structure.
//
// An actor who walks into an open structure forms NO huddle on arrival: the
// arrival-encounter cascade (cascade/arrival_encounter.go) is OUTDOOR-only
// (it skips any arriver with InsideStructureID != "") and forms huddles via
// StartOutdoorHuddle. The indoor counterpart was the explicit talk/knock path —
// but EnterOrKnock only forms a huddle on a KNOCK (owner-only structure,
// non-member); a plain walk-in through an open door joins nobody. So an actor
// standing in the Tavern with others had CurrentHuddleID == "", and sim.Speak
// (audience = huddle peers) either rejected a name-address (the vocative gate
// sees the other as a non-peer → 422) or emitted to no one — and the
// transaction paths (pay / order / scene_quote), which all gate on
// CurrentHuddleID, rejected with "you're not in a conversation."
//
// EnsureColocatedHuddle closes that gap: run from the speak path, it forms the
// conversation ON the talk action. It delegates to JoinHuddle, which
// find-or-creates the single active huddle at a structure (the same primitive
// EnterOrKnock uses for the knock service-huddle, called with an empty
// sceneID) — so an actor whose structure already has an active huddle JOINS it
// rather than minting a second.
//
// Scope: INDOOR only (the actor must be inside a structure). Outdoor speech
// among co-located actors would mirror the cascade's StartOutdoorHuddle with a
// speak radius; not handled here.
//
// ZBBS-HOME-363 widened the original PC-only restriction to conversational NPCs
// (stateful + shared). The live Tavern bug: a starving NPC walked in to buy from
// the keeper, but with no huddle every `pay`/`speak` died — there was NO indoor
// NPC huddle-formation path at all (the encounter cascade is outdoor-only;
// EnterOrKnock only fires on an owner-only knock). The original "NPC conversation
// forms through the cascade and the reactor" reasoning held only outdoors. The
// trigger boundary stays tight: this runs only from a deliberate speak (a PC
// click-to-talk or an NPC's own speak tool), only indoors, and is idempotent +
// pulls in only UNHUDDLED co-located actors, so it can't churn or mint a second
// huddle.

// EnsureColocatedHuddle joins actorID (a PC or conversational NPC) into the
// active huddle at the structure it is standing in, together with the other
// co-located conversational actors, when it has no huddle of its own. No-op when
// the actor is missing, not a conversational kind, already in a huddle, not
// inside a structure, or alone inside. Idempotent. MUST run on the world
// goroutine (call inside a Command.Fn).
func EnsureColocatedHuddle(actorID ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok || actor == nil {
				return nil, nil
			}
			// Conversational kinds only (ZBBS-HOME-363: PC + stateful/shared
			// NPC). A decorative NPC speaking is not a real conversation, so it
			// must not mint a huddle.
			switch actor.Kind {
			case KindPC, KindNPCStateful, KindNPCShared:
			default:
				return nil, nil
			}
			if actor.CurrentHuddleID != "" {
				return nil, nil // already conversing — leave the existing huddle intact
			}
			structureID := actor.InsideStructureID
			if structureID == "" {
				return nil, nil // outdoor — out of scope (see file doc)
			}
			others := colocatedConversationalActors(w, actor, structureID, now)
			// ZBBS-HOME-363: the speaker must also join an ALREADY-ACTIVE
			// structure huddle even when there are no UNHUDDLED co-located
			// actors to pull in. This was the live Tavern bug: John + Ezekiel
			// were already huddled, so colocatedConversationalActors (which
			// excludes already-huddled actors, to avoid leave-first yanking
			// them) returned empty — and the old `len(others) == 0` early
			// return made Prudence bail, never joining the conversation she was
			// standing in, so she could never transact. find-or-create returns
			// that existing huddle, so the speaker joins the people already
			// here. Only bail when there is genuinely nothing to join: no
			// active huddle AND no unhuddled peer to start one with.
			_, hasActiveHuddle := findActiveHuddleAt(w, structureID)
			if len(others) == 0 && !hasActiveHuddle {
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
