package sim

import (
	"errors"
	"log"
	"time"
)

// visitor_depart.go — the operator's lever to send a visitor away now (LLM-701).
// Before it, a traveler left only when his stay ran out; one spreading a false
// story (the 2026-10-02 messenger who told the village John Ellis had died) could
// not be stopped short of an engine stop and a SQL delete.

// ErrNotAVisitor is returned by DepartVisitor for an actor with no VisitorState —
// a resident, the PC, or a decorative.
var ErrNotAVisitor = errors.New("actor is not a visitor")

// VisitorDepartWalk reports what DepartVisitor did about the visitor's walk out.
type VisitorDepartWalk string

const (
	// VisitorDepartWalkStarted — the walk to a map edge was dispatched now.
	VisitorDepartWalkStarted VisitorDepartWalk = "started"
	// VisitorDepartWalkNoRoute — no anchor, grid or edge tile could be had, so no
	// walk was issued; cleanup removes him after the grace window where he stands.
	VisitorDepartWalkNoRoute VisitorDepartWalk = "no_route"
	// VisitorDepartWalkAlreadyDeparting — he was already walking out on the natural
	// departure; that walk stands and only the silencing was added.
	VisitorDepartWalkAlreadyDeparting VisitorDepartWalk = "already_departing"
)

// DepartVisitorResult is DepartVisitor's success payload.
type DepartVisitorResult struct {
	ID          ActorID
	DisplayName string
	Walk        VisitorDepartWalk
	// ExpiresAt is the stay deadline after the call. Cleanup hard-removes the
	// visitor VisitorCleanupGraceMinutes past it, wherever he is by then.
	ExpiresAt time.Time
}

// DepartVisitor sends a visitor away now (LLM-701):
//
//   - marks him VisitorDepartCauseOperator, which closes the warrant funnel to him
//     for the rest of his stay (tryStampWarrant) — he takes no further turn;
//   - wipes his reactor state: the open warrant cycle goes, and an LLM call already
//     in flight is voided (its attempt id is cleared, so its tool calls are refused
//     as stale when it returns);
//   - ends his stay at now and starts the walk to a map edge through the same
//     beginVisitorDespawn the daybreak departure uses, so cleanup, the
//     ActorDeparted event and a returner's comeback schedule all stay on the one
//     path.
//
// A visitor already on his natural walk out keeps that walk; he is only silenced.
// Idempotent for an operator-departed visitor. ErrActorNotFound for an unknown id,
// ErrNotAVisitor for a non-visitor.
func DepartVisitor(id ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a := w.Actors[id]
			if a == nil {
				return nil, ErrActorNotFound
			}
			vs := a.VisitorState
			if vs == nil {
				return nil, ErrNotAVisitor
			}
			vs.DepartCause = VisitorDepartCauseOperator
			// The full reset, as RetireWorker's to-decorative leg uses it: a dismissed
			// traveler must keep no open cycle and no in-flight attempt.
			resetReactorStateOnLoad(a)

			walk := VisitorDepartWalkAlreadyDeparting
			if vs.Phase != VisitorPhaseDeparting {
				if now.Before(vs.ExpiresAt) {
					vs.ExpiresAt = now
				}
				walk = VisitorDepartWalkNoRoute
				if beginVisitorDespawn(w, id, a, now, inputsRandOrDefault(nil)) {
					walk = VisitorDepartWalkStarted
				}
			}
			log.Printf("sim/visitor: operator departed %s (%s), walk %s", a.DisplayName, id, walk)
			return DepartVisitorResult{
				ID:          id,
				DisplayName: a.DisplayName,
				Walk:        walk,
				ExpiresAt:   vs.ExpiresAt,
			}, nil
		},
	}
}
