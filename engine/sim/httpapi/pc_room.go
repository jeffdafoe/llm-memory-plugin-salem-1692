package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/roomlayout"
)

// pc_room.go — POST /api/village/pc/move_inside (LLM-768): the player taps the
// floor or a free seat of the room their character is in, and the character
// walks there. sim.MovePCInRoom owns the checks (awake, in a room with a floor
// plan, a square they may stand on, free, reachable) and the change; every
// viewer learns of it from the npc_room_square_changed frame, path included.

const maxMoveInsideBodyBytes = 4 << 10

// pcMoveInsideRequest: the square to walk to, as [x, y] on the room's grid.
// No actor and no structure — the caller's own PC, in the room it is in.
type pcMoveInsideRequest struct {
	Square *roomlayout.Square `json:"square"`
}

type pcMoveInsideResponse struct {
	Square roomlayout.Square   `json:"square"`
	Path   []roomlayout.Square `json:"path"`
}

// handlePCMoveInside walks the caller's PC to a square of its room. 400 for a
// bad body, 404 when the session has no PC, 422 when the step is refused.
func (s *Server) handlePCMoveInside(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxMoveInsideBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req pcMoveInsideRequest
	if err := dec.Decode(&req); err != nil || req.Square == nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	res, err := s.world.SendContext(r.Context(), moveInsidePCCommand(user.Username, *req.Square))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errPCNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.RoomMoveResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected move result")
		return
	}
	writeJSON(w, pcMoveInsideResponse{Square: out.Square, Path: out.Path})
}

// pcGoRoomRequest: the room to go to through a door or the stairs of the room
// the caller's PC is in — "common", "hall", "bedroom_2" (LLM-801).
type pcGoRoomRequest struct {
	Room string `json:"room"`
}

type pcGoRoomResponse struct {
	Room   string             `json:"room"`
	Square *roomlayout.Square `json:"square,omitempty"`
}

// handlePCGoRoom — POST /api/village/pc/go_room (LLM-801): the player taps a
// door or the stairs and their character goes through. sim.MovePCToRoom owns
// the checks and the change; every viewer learns of it from the
// npc_room_square_changed frame, which names the room. 400 for a bad body,
// 404 when the session has no PC, 403 with the door's own line when it will
// not let them through ("The rooms upstairs are for guests."), 422 otherwise.
func (s *Server) handlePCGoRoom(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxMoveInsideBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req pcGoRoomRequest
	if err := dec.Decode(&req); err != nil || req.Room == "" {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	now := time.Now().UTC()
	res, err := s.world.SendContext(r.Context(), sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, user.Username)
			if !ok {
				return nil, errPCNotFound
			}
			// Asleep they are in bed: a tap on a door cannot reach them, and the
			// input stamp would wake them (as move_inside).
			if world.Actors[actorID].State == sim.StateSleeping {
				return nil, sim.ErrRoomSquareRefused
			}
			sim.TouchPCInput(world, actorID, now)
			return sim.MovePCToRoom(actorID, req.Room, now).Fn(world)
		},
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errPCNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if line, ok := sim.IsRoomRefused(err); ok {
			writeError(w, http.StatusForbidden, line)
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.RoomGoResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected room result")
		return
	}
	writeJSON(w, pcGoRoomResponse{Room: out.Room, Square: out.Square})
}

// moveInsidePCCommand resolves username → PC actor on the world goroutine and
// delegates to sim.MovePCInRoom. A deliberate act, so it stamps the input
// cursor first, like the other action routes — but a sleeping PC is refused
// before that, since the stamp would wake them (they are upstairs, not in the
// room, and a tap there cannot reach them).
func moveInsidePCCommand(username string, to roomlayout.Square) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return nil, errPCNotFound
			}
			if world.Actors[actorID].State == sim.StateSleeping {
				return nil, sim.ErrRoomSquareRefused
			}
			sim.TouchPCInput(world, actorID, time.Now().UTC())
			return sim.MovePCInRoom(actorID, to).Fn(world)
		},
	}
}
