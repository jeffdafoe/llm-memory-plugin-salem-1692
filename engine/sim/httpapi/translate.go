package httpapi

import (
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// translate.go — the production EventTranslator: it maps the v2 sim event bus
// to client wire frames ({type, data}, matching the client's _handle_message
// dispatch). Pass TranslateEvent to NewHub.
//
// Mapped so far: MOVEMENT (the engine announces a walk with the full
// cost-weighted tile path it computed — npc_walking — and is authoritative on
// the outcome via npc_arrived / npc_move_stopped; the client follows the path
// tile by tile and snaps to the engine's arrival; broadcasting the path vs only
// the destination keeps road-preferring / building-avoiding routing on screen
// without the client re-implementing the cost model) and SPEECH (Spoke →
// npc_spoke, the huddle-scoped utterance the player's pc/speak and NPC speak
// both produce). Per-tile ActorMoved is deliberately NOT mapped — it stays
// internal. Phase / object events remain an additive follow-on: an unmapped
// event returns ok=false and is dropped, so adding cases later needs no change
// here or in the hub. Wire shapes: shared/notes/codebase/salem-engine-v2/client-contract.

// TranslateEvent maps a sim.Event to a client WireFrame. ok=false drops the
// event (the common case — most bus events are engine-internal). Pure and
// non-blocking: it runs on the world goroutine via Hub.Handle.
func TranslateEvent(evt sim.Event) (WireFrame, bool) {
	switch e := evt.(type) {
	case *sim.ActorMoveStarted:
		path := make([]tilePointDTO, len(e.Path))
		for i, p := range e.Path {
			path[i] = tilePointDTO{X: p.X, Y: p.Y}
		}
		return WireFrame{Type: "npc_walking", Data: walkWireDTO{
			ID:          string(e.ActorID),
			Path:        path,
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
	case *sim.Spoke:
		recipients := make([]string, len(e.RecipientIDs))
		for i, id := range e.RecipientIDs {
			recipients[i] = string(id)
		}
		return WireFrame{Type: "npc_spoke", Data: spokeWireDTO{
			ID:           string(e.SpeakerID),
			HuddleID:     string(e.HuddleID),
			RecipientIDs: recipients,
			Text:         e.Text,
			SpeechID:     uint64(e.EventID()),
			At:           e.At.UTC().Format(time.RFC3339),
		}}, true
	default:
		return WireFrame{}, false
	}
}

// walkWireDTO is the npc_walking payload — the engine's full cost-weighted tile
// path (roads preferred, buildings avoided), which the client follows tile by
// tile. Path is in TILE coordinates (matching AgentDTO's tile x/y convention);
// the client converts to world-pixels with the pad/tile_size it already gets
// from the terrain DTO. Path[0] is the walk start, Path[len-1] the resolved
// goal. dest_kind is structure_enter | structure_visit | position; structure_id
// is present for the structure kinds. attempt_id correlates with the
// npc_arrived / npc_move_stopped that conclude this walk; a fresh attempt_id for
// the same actor supersedes any earlier in-flight walk.
type walkWireDTO struct {
	ID          string         `json:"id"`
	Path        []tilePointDTO `json:"path"`
	DestKind    string         `json:"dest_kind"`
	StructureID string         `json:"structure_id,omitempty"`
	AttemptID   uint64         `json:"attempt_id"`
}

// tilePointDTO is a single tile waypoint in a walk path.
type tilePointDTO struct {
	X int `json:"x"`
	Y int `json:"y"`
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

// spokeWireDTO is the npc_spoke payload — one utterance the client renders as a
// speech bubble over the speaker. id is the speaker's actor id (the client
// resolves the display name + position from its agent roster). huddle_id scopes
// the conversation; recipient_ids is the huddle audience at commit time (empty
// when speaking to no one — a valid v2 state). speech_id (= the event id) lets a
// client correlate its own optimistic bubble with this authoritative event and
// dedupe. No x/y — v2 speech is huddle-scoped, not proximity-based (unlike v1).
type spokeWireDTO struct {
	ID           string   `json:"id"`
	HuddleID     string   `json:"huddle_id,omitempty"`
	RecipientIDs []string `json:"recipient_ids"`
	Text         string   `json:"text"`
	SpeechID     uint64   `json:"speech_id"`
	At           string   `json:"at"`
}
