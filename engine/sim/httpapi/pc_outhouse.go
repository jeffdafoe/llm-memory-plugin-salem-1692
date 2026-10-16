package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_outhouse.go — LLM-806. The client posts here when the player arrives at an
// outhouse they clicked. sim.UseOuthouse owns the world checks (an outhouse, the
// player at its door, nobody inside) and emits the outhouse_used broadcast.

const maxOuthouseBodyBytes = 4 << 10

// pcOuthouseRequest is the POST /api/village/pc/outhouse body: the outhouse's
// placement id. The user is the caller's own PC.
type pcOuthouseRequest struct {
	ObjectID string `json:"object_id"`
}

type pcOuthouseResponse struct {
	Seconds float64 `json:"seconds"`
}

func (s *Server) handlePCOuthouse(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxOuthouseBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req pcOuthouseRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	objectID := strings.TrimSpace(req.ObjectID)
	if objectID == "" {
		writeError(w, http.StatusBadRequest, "object_id is required")
		return
	}
	res, err := s.world.SendContext(r.Context(), outhousePCCommand(user.Username, sim.VillageObjectID(objectID)))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errPCNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		// Not an outhouse / not at its door / someone inside: the message is
		// player-facing.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.OuthouseResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected outhouse result")
		return
	}
	writeJSON(w, pcOuthouseResponse{Seconds: out.Stay.Seconds()})
}

// outhousePCCommand resolves username → PC on the world goroutine and goes in.
// A deliberate PC action, so it stamps the input cursor like pc/feed.
func outhousePCCommand(username string, objectID sim.VillageObjectID) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return nil, errPCNotFound
			}
			now := time.Now().UTC()
			sim.TouchPCInput(world, actorID, now)
			return sim.UseOuthouse(actorID, objectID, now).Fn(world)
		},
	}
}
