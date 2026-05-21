package httpapi

import "github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"

// translate.go — the production EventTranslator: it maps the v2 sim event bus
// to client wire frames ({type, data}, matching the client's _handle_message
// dispatch). Pass TranslateEvent to NewHub.
//
// This slice maps MOVEMENT only — the headline live signal — using the
// destination-based model: the engine announces where an actor is heading
// (npc_walking) and is authoritative on the outcome (npc_arrived /
// npc_move_stopped); the client paths to the destination locally and snaps to
// the engine's arrival. Per-tile ActorMoved is deliberately NOT mapped — it
// stays internal to the engine. Phase / speech / object events are an additive
// follow-on: an unmapped event returns ok=false and is dropped, so adding cases
// later needs no change here or in the hub. Wire shapes are documented in
// shared/notes/codebase/salem-engine-v2/client-contract.

// TranslateEvent maps a sim.Event to a client WireFrame. ok=false drops the
// event (the common case — most bus events are engine-internal). Pure and
// non-blocking: it runs on the world goroutine via Hub.Handle.
func TranslateEvent(evt sim.Event) (WireFrame, bool) {
	switch e := evt.(type) {
	case *sim.ActorMoveStarted:
		return WireFrame{Type: "npc_walking", Data: walkWireDTO{
			ID:          string(e.ActorID),
			FromX:       e.FromPosition.X,
			FromY:       e.FromPosition.Y,
			TargetX:     e.TargetPosition.X,
			TargetY:     e.TargetPosition.Y,
			DestKind:    string(e.DestinationKind),
			StructureID: string(e.StructureID),
			AttemptID:   uint64(e.MovementAttemptID),
		}}, true
	case *sim.ActorArrived:
		return WireFrame{Type: "npc_arrived", Data: arrivedWireDTO{
			ID:          string(e.ActorID),
			X:           e.FinalPosition.X,
			Y:           e.FinalPosition.Y,
			StructureID: string(e.FinalStructureID),
			AttemptID:   uint64(e.MovementAttemptID),
		}}, true
	case *sim.ActorMoveStopped:
		return WireFrame{Type: "npc_move_stopped", Data: moveStoppedWireDTO{
			ID:        string(e.ActorID),
			X:         e.Position.X,
			Y:         e.Position.Y,
			Reason:    string(e.Reason),
			AttemptID: uint64(e.MovementAttemptID),
		}}, true
	default:
		return WireFrame{}, false
	}
}

// walkWireDTO is the npc_walking payload — the client navigates the actor from
// (from_x, from_y) to the resolved goal (target_x, target_y) locally. dest_kind
// is structure_enter | structure_visit | position; structure_id is present for
// the structure kinds. attempt_id correlates with the npc_arrived /
// npc_move_stopped that conclude this walk.
type walkWireDTO struct {
	ID          string `json:"id"`
	FromX       int    `json:"from_x"`
	FromY       int    `json:"from_y"`
	TargetX     int    `json:"target_x"`
	TargetY     int    `json:"target_y"`
	DestKind    string `json:"dest_kind"`
	StructureID string `json:"structure_id,omitempty"`
	AttemptID   uint64 `json:"attempt_id"`
}

// arrivedWireDTO is the npc_arrived payload — the authoritative end of a walk.
// The client snaps the actor to (x, y) and goes idle regardless of where its
// local nav reached. structure_id is the structure the actor ended inside (empty
// for a bare position or a visitor slot). No facing — the client derives it from
// its movement delta, falling back to last-known.
type arrivedWireDTO struct {
	ID          string `json:"id"`
	X           int    `json:"x"`
	Y           int    `json:"y"`
	StructureID string `json:"structure_id,omitempty"`
	AttemptID   uint64 `json:"attempt_id"`
}

// moveStoppedWireDTO is the npc_move_stopped payload — an accepted walk that
// failed to reach its goal (blocked | unreachable | invalidated). The client
// stops its local nav and snaps to (x, y). Distinct from npc_arrived so a viewer
// doesn't render an arrival that never happened.
type moveStoppedWireDTO struct {
	ID        string `json:"id"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Reason    string `json:"reason"`
	AttemptID uint64 `json:"attempt_id"`
}
