package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// maxMoveBodyBytes caps the pc/move request body. The payload is tiny (a kind
// tag + a coord pair or a structure id); 64 KiB is generous headroom while
// still rejecting an attacker-controlled flood before it's buffered/decoded.
const maxMoveBodyBytes = 64 << 10

// write_handlers.go — the client surface's write routes. Unlike the reads
// (which serve the published snapshot lock-free), a write goes through the
// world's command channel: the handler builds a sim.Command and submits it via
// w.SendContext, so the mutation runs serialized on the world goroutine and any
// events it emits (e.g. ActorMoveStarted → npc_walking) fan out over the hub.
//
// pc/move is the first write route. Design + the registry of deferred v1
// behaviors lives in work note tasks/engine-http-api/pc-move-design.

// errPCNotFound / errStructureNotFound are sentinels the move command returns so
// the handler maps them to 404 by identity (errors.Is), not by string-matching
// the message. Every other sim rejection (no path, closed, members-only, in an
// active huddle) is a 422 — the request named a real entity but the move isn't
// valid in the current world state.
var (
	errPCNotFound        = errors.New("pc not found for session")
	errStructureNotFound = errors.New("structure not found")
)

// pcMoveRequest is the POST /api/village/pc/move body. There is deliberately no
// actor_id: the PC is resolved from the authenticated session's username (the
// actor whose LoginUsername matches), so a caller can only ever move their own
// PC — ownership is structural, not a checked field.
type pcMoveRequest struct {
	Destination      moveDestinationRequest `json:"destination"`
	LeaveHuddleFirst bool                   `json:"leave_huddle_first"`
}

// moveDestinationRequest mirrors sim.MoveDestination on the wire: a kind tag
// plus exactly one payload. Coordinates are internal-grid TILE coords including
// pad — the same space the read surface emits for agent x/y, so the client
// echoes back what it received with no conversion.
type moveDestinationRequest struct {
	Kind        string           `json:"kind"` // position | structure_enter | structure_visit
	Position    *positionRequest `json:"position,omitempty"`
	StructureID string           `json:"structure_id,omitempty"`
}

type positionRequest struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// pcMoveResponse is the accepted-move outcome (sim.MoveActorResult on the wire).
type pcMoveResponse struct {
	MovementAttemptID   uint64 `json:"movement_attempt_id"`
	SupersededAttemptID uint64 `json:"superseded_attempt_id"`
	LeftHuddleID        string `json:"left_huddle_id,omitempty"`
}

// handlePCMove walks the caller's PC to a destination. The route is wrapped in
// requireAuth, so an authenticated salem-realm AuthUser is in the request
// context. The PC is resolved from that user inside the command (on the world
// goroutine) — see movePCCommand.
func (s *Server) handlePCMove(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		// requireAuth always populates this; guard defensively rather than nil-deref.
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxMoveBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req pcMoveRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Reject trailing content after the JSON object — a write route shouldn't
	// silently accept `{...} garbage` or a second object. A clean body leaves
	// exactly io.EOF on the next read.
	if dec.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	dest, status, msg := buildMoveDestination(req.Destination)
	if msg != "" {
		writeError(w, status, msg)
		return
	}

	res, err := s.world.SendContext(r.Context(), movePCCommand(user.Username, dest, req.LeaveHuddleFirst))
	if err != nil {
		// The client disconnected (or the deadline lapsed) before the world
		// accepted the command or replied — there's nothing useful to write back.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errPCNotFound) || errors.Is(err, errStructureNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		// Any other rejection is a sim state-validation failure: the move can't
		// happen right now (no path, closed door, members-only, in a huddle).
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.MoveActorResult)
	if !ok {
		// movePCCommand always returns a MoveActorResult on success; a mismatch
		// is a wiring bug, not a client error.
		writeError(w, http.StatusInternalServerError, "unexpected move result")
		return
	}
	writeJSON(w, pcMoveResponse{
		MovementAttemptID:   uint64(out.MovementAttemptID),
		SupersededAttemptID: uint64(out.SupersededAttemptID),
		LeftHuddleID:        string(out.LeftHuddleID),
	})
}

// buildMoveDestination validates the wire destination and converts it to a
// sim.MoveDestination. On rejection it returns a non-empty msg with the HTTP
// status to use (400 for a malformed/empty payload, 422 for an out-of-bounds
// tile — well-formed but unreachable).
func buildMoveDestination(d moveDestinationRequest) (sim.MoveDestination, int, string) {
	switch sim.MoveDestinationKind(d.Kind) {
	case sim.MoveDestinationPosition:
		if d.Position == nil {
			return sim.MoveDestination{}, http.StatusBadRequest, "position destination requires a position"
		}
		if d.StructureID != "" {
			return sim.MoveDestination{}, http.StatusBadRequest, "position destination must not include structure_id"
		}
		if d.Position.X < 0 || d.Position.X >= sim.MapW || d.Position.Y < 0 || d.Position.Y >= sim.MapH {
			return sim.MoveDestination{}, http.StatusUnprocessableEntity, "position is outside the map"
		}
		return sim.NewPositionDestination(sim.Position{X: d.Position.X, Y: d.Position.Y}), 0, ""
	case sim.MoveDestinationStructureEnter:
		if d.StructureID == "" {
			return sim.MoveDestination{}, http.StatusBadRequest, "structure_enter destination requires a structure_id"
		}
		if d.Position != nil {
			return sim.MoveDestination{}, http.StatusBadRequest, "structure_enter destination must not include position"
		}
		return sim.NewStructureEnterDestination(sim.StructureID(d.StructureID)), 0, ""
	case sim.MoveDestinationStructureVisit:
		if d.StructureID == "" {
			return sim.MoveDestination{}, http.StatusBadRequest, "structure_visit destination requires a structure_id"
		}
		if d.Position != nil {
			return sim.MoveDestination{}, http.StatusBadRequest, "structure_visit destination must not include position"
		}
		return sim.NewStructureVisitDestination(sim.StructureID(d.StructureID)), 0, ""
	default:
		return sim.MoveDestination{}, http.StatusBadRequest, "unknown destination kind"
	}
}

// movePCCommand resolves username → PC actor and delegates to sim.MoveActor.
// The resolution runs inside the command Fn (on the world goroutine) so it
// reads authoritative live state with no TOCTOU and needs no LoginUsername on
// the read DTO. Keeping it here — not in sim — means the session→actor identity
// rule lives in the auth-aware httpapi layer; sim.MoveActor stays a pure
// actor-id operation.
//
// The move timestamp is captured INSIDE the Fn (not at request receipt) so the
// stamped MoveIntent + the ActorMoveStarted it emits reflect the command's
// execution time, not how long it sat in a backed-up command channel.
func movePCCommand(username string, dest sim.MoveDestination, leaveHuddleFirst bool) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return sim.MoveActorResult{}, errPCNotFound
			}
			// Map a missing structure to our sentinel (→ 404) before MoveActor
			// rejects it with a generic message; MoveActor's own checks still
			// cover the rest (closed/owner/door/path → 422).
			if dest.StructureID != nil {
				if _, ok := world.Structures[*dest.StructureID]; !ok {
					return sim.MoveActorResult{}, errStructureNotFound
				}
			}
			return sim.MoveActor(actorID, dest, leaveHuddleFirst, time.Now().UTC()).Fn(world)
		},
	}
}

// findPCByLogin returns the id of the PC actor bound to loginUsername. Only
// KindPC actors carry a login binding, so the kind check guards against an
// NPC that somehow shares the value. Runs on the world goroutine (called from a
// command Fn), so the map read is safe.
func findPCByLogin(world *sim.World, loginUsername string) (sim.ActorID, bool) {
	if loginUsername == "" {
		return "", false
	}
	for id, a := range world.Actors {
		if a.Kind == sim.KindPC && a.LoginUsername == loginUsername {
			return id, true
		}
	}
	return "", false
}

// writeError writes a JSON {error} body with the given status.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
