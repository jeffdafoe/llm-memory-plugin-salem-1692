package sim

import (
	"fmt"
	"log"
	"time"
)

// disperse.go — LLM-453. The daytime analog of a voluntary bed-down (LLM-447's
// turn_in): a terminal verb that lets an agent NPC gracefully take its leave of a
// conversation that has wound down, instead of echoing farewells at it forever —
// the Walker "see you at supper" / stew-coordination turboyap loop, where every
// line the models generated was ending-shaped and not one mapped to a tool. Sleep
// is the evening's terminal state; disperse is the daytime's — an actor with
// nothing more to say steps out of the conversation and turns back to its own
// affairs.
//
// The say rides the tool (the terminal-verb rule): the parting line is spoken to
// the room while the actor is still a member, THEN it leaves. Leaving is
// classified "for business" so the remaining members read "X took their leave,
// turning back to their own affairs" — the cue that legitimizes the rest of the
// household following, one after another, until the room falls quiet.
//
// Why it does NOT move the actor: an off-shift NPC at home has nowhere to walk to
// and no chore to do (that content is LLM-447's deferred solo-occupation half).
// Leaving in place would let the next housemate's speak re-pull it straight back
// in — EnsureColocatedHuddle pulls every unhuddled co-located conversationalist
// into a huddle on a speak. So a disperse stamps a short, structure-scoped
// re-huddle cooldown (Actor.DispersedUntil / DispersedFromStructureID, read by
// dispersedFrom from colocatedConversationalActors and EnsureColocatedHuddle): the
// "companionable silence" window in which the household can sit together without
// re-opening the settled conversation.

// DisperseRehuddleCooldown is how long a just-dispersed actor stays out of any
// NPC-formed huddle at the structure it left. Pinned to the LLM-170 conversation
// carry-over window: for exactly that window a re-form among the same clique would
// inherit the wound-down ring (and resume the loop), so the disperser stays apart
// until a re-form there would start fresh. A PC may re-engage it sooner
// (colocatedConversationalActors exempts a PC speaker). Transient state, tunable by
// live observation.
const DisperseRehuddleCooldown = HuddleContinuityWindowDefault

// Disperse is the commit for the disperse tool (LLM-453). It voices the parting
// say to the current huddle (best-effort), leaves that huddle with the
// "for business" classification, and stamps the re-huddle cooldown. Rejects with a
// ModelFacingError when the actor is not in a conversation to leave — the tool is
// gated to wound-down huddles, so that path is defensive. MUST run on the world
// goroutine.
func Disperse(actorID ActorID, say string, hasNewNews bool, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok || actor == nil {
				return nil, fmt.Errorf("Disperse: actor %q not in world", actorID)
			}
			if actor.CurrentHuddleID == "" {
				return nil, ModelFacingError{Msg: "you are not in a conversation to take your leave of."}
			}
			// Capture the structure BEFORE leaving — leaveCurrentHuddleFor clears the
			// membership the cooldown scope is derived from.
			var fromStructure StructureID
			if h, ok := w.Huddles[actor.CurrentHuddleID]; ok && h != nil {
				fromStructure = h.StructureID
			}
			// Say the parting line to the room while still a member, so the peers
			// hear it. Best-effort — the same posture as scene_quote's say (LLM-343):
			// SpeakTo has reachable rejections (the vocative / turn-state gates), and
			// a refused farewell must not strand a half-done disperse. An empty say is
			// skipped defensively (the schema requires one).
			if say != "" {
				if _, serr := SpeakTo(actorID, say, "", nil, hasNewNews, at).Fn(w); serr != nil {
					log.Printf("sim: disperse %q took its leave but the say was refused: %v", actorID, serr)
				}
			}
			leaveCurrentHuddleFor(w, actor, at, WarrantKindHuddleLeftForBusiness, WarrantKindHuddlePeerLeftForBusiness)
			until := at.Add(DisperseRehuddleCooldown)
			actor.DispersedUntil = &until
			actor.DispersedFromStructureID = fromStructure
			return nil, nil
		},
	}
}

// dispersedFrom reports whether actor a is inside its post-disperse re-huddle
// cooldown for structureID (LLM-453) — it took its leave of a conversation there
// less than DisperseRehuddleCooldown ago and must not be re-pulled into (or re-form)
// a huddle at that structure yet. The scope is one structure, so a dispersed actor
// that moves elsewhere converses normally. Read on the world goroutine.
func dispersedFrom(a *Actor, structureID StructureID, now time.Time) bool {
	return a.DispersedUntil != nil && a.DispersedUntil.After(now) && a.DispersedFromStructureID == structureID
}
