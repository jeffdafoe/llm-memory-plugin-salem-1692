package sim

import (
	"fmt"
	"time"
)

// RecordInteraction returns a Command that appends a SalientFact to the
// per-pair Relationship between actorID (the rememberer) and otherID
// (the remembered), creates the Relationship on first interaction,
// bumps InteractionCount, and stamps LastInteractionAt. Mirrors v1's
// engine/actor_narrative.go recordInteraction.
//
// Gate: actor.Kind == KindNPCShared. Stateful-VA actors get their
// per-peer continuity through their own VA's memory on memory-api;
// duplicating it here would diverge from their VA's view. Silently
// no-ops on a non-shared actor — not an error, the caller doesn't have
// to gate.
//
// Visitor skip: a relationship between two actors is only recorded when
// NEITHER side is a transient salem-visitor (VisitorState == nil on both).
// Visitors don't accumulate relationship rows (stateless by design), and
// persistent NPCs don't accumulate rows keyed by visitor IDs that will
// be deleted at cleanup. See shared/notes/codebase/salem-engine-v2/visitor.
//
// Self-interactions are silently no-op'd (an actor's relationship with
// themselves is not a meaningful primitive).
//
// Returns an error when either actor isn't in the world, or when an ID
// is empty — those are caller bugs the speak/pay/serve/deliver handlers
// should never trigger.
//
// Text is rune-truncated to MaxSalientFactTextLen via NewSalientFact at
// write time; callers don't need to pre-truncate.
func RecordInteraction(actorID, otherID ActorID, kind InteractionKind, text string, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if actorID == "" {
				return nil, fmt.Errorf("RecordInteraction: actorID required")
			}
			if otherID == "" {
				return nil, fmt.Errorf("RecordInteraction: otherID required")
			}
			if actorID == otherID {
				return nil, nil
			}
			actor, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("RecordInteraction: actor %q not found", actorID)
			}
			other, ok := w.Actors[otherID]
			if !ok {
				return nil, fmt.Errorf("RecordInteraction: other actor %q not found", otherID)
			}
			if actor.Kind != KindNPCShared {
				return nil, nil
			}
			// Transient-visitor skip: visitors don't accumulate relationship
			// rows (stateless by design — the shared salem-visitor VA has
			// dream_mode='none'), and persistent NPCs don't accumulate
			// relationship rows keyed by transient visitor IDs that will be
			// deleted at cleanup. Both sides skip — eliminates the orphan-ref
			// case (persistent NPC's Relationship still pointing at a
			// visitor's ActorID after the visitor row is gone).
			if actor.VisitorState != nil || other.VisitorState != nil {
				return nil, nil
			}

			rel, exists := actor.Relationships[otherID]
			if !exists {
				rel = &Relationship{CreatedAt: at}
				if actor.Relationships == nil {
					actor.Relationships = make(map[ActorID]*Relationship)
				}
				actor.Relationships[otherID] = rel
			}
			rel.SalientFacts = append(rel.SalientFacts, NewSalientFact(at, kind, text))
			if len(rel.SalientFacts) > MaxSalientFactsPerRelationship {
				rel.SalientFacts = rel.SalientFacts[1:]
				rel.DroppedFactCount++
			}
			rel.InteractionCount++
			lastAt := at
			rel.LastInteractionAt = &lastAt
			rel.UpdatedAt = at
			return nil, nil
		},
	}
}
