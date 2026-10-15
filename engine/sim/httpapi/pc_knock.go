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

// pc_knock.go — POST /api/village/pc/knock (LLM-785): the door popup's Knock
// button. A click on a house the player may not enter only walks them to its
// door; there they choose to knock. sim.KnockAtDoor owns the checks (at that
// door, a door to knock on) and the knock itself — the doorway conversation
// when someone answers, a private knock_unanswered line when nobody does.

const maxKnockBodyBytes = 4 << 10

type pcKnockRequest struct {
	StructureID string `json:"structure_id"`
}

type pcKnockResponse struct {
	Answered bool `json:"answered"`
}

// handlePCKnock knocks on a door for the caller's PC. 400 for a bad body, 404
// when the session has no PC, 422 when the knock is refused.
func (s *Server) handlePCKnock(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxKnockBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req pcKnockRequest
	if err := dec.Decode(&req); err != nil || req.StructureID == "" {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	res, err := s.world.SendContext(r.Context(), knockPCCommand(user.Username, sim.StructureID(req.StructureID)))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		status, msg := knockErrorResponse(err)
		writeError(w, status, msg)
		return
	}
	out, ok := res.(sim.KnockResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected knock result")
		return
	}
	writeJSON(w, pcKnockResponse{Answered: out.Answered})
}

// knockErrorResponse maps a knock's error to its status and message: no PC →
// 404, a refusal (sim.ErrKnockRefused: not at that door, a door they may walk
// through) → 422 with the reason, anything else → 500 with no internal detail.
func knockErrorResponse(err error) (int, string) {
	switch {
	case errors.Is(err, errPCNotFound):
		return http.StatusNotFound, err.Error()
	case errors.Is(err, sim.ErrKnockRefused):
		return http.StatusUnprocessableEntity, err.Error()
	}
	return http.StatusInternalServerError, "knock failed"
}

// knockPCCommand resolves username → PC actor on the world goroutine and
// delegates to sim.KnockAtDoor. A deliberate act, so it stamps the input
// cursor first, like the other action routes — which also keeps a live
// player's knock from reading as a ghost's.
func knockPCCommand(username string, structureID sim.StructureID) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return nil, errPCNotFound
			}
			now := time.Now().UTC()
			sim.TouchPCInput(world, actorID, now)
			return sim.KnockAtDoor(actorID, structureID, now).Fn(world)
		},
	}
}
