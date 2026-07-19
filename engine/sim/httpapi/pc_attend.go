package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_attend.go — LLM-466. POST /api/village/pc/attend is the candle prompt's
// answer: the player clicked the overlay, proving a human is watching, so the
// eco-mode audience horizon starts over.
//
// Deliberately its own route rather than a piggyback on an action route. The
// prompt must be answerable by a player who wants to keep WATCHING, not playing
// — routing the ack through /pc/move would make "I'm still here" cost a walk
// across the village. It stamps the activity cursor only; it is not an in-world
// act, so it leaves LastPCInputAt (and with it the idle-auto-bed timer) alone.
//
// The client does not dismiss its own overlay on the 200: the world emits
// PCIdlePromptCleared, translate.go turns it into the pc_idle_prompt_cleared
// frame, and the client hides on that — the same server-is-the-truth contract
// the sleep overlay keeps with the Wake button. Ignores the request body; the
// caller is the session's own PC, so there are no parameters.

// pcAttendResponse acks /pc/attend. Idempotent — "ok" whether or not a prompt
// was actually pending (a click that races an in-world action which already
// cleared it is not an error). Cleared reports whether THIS call was the one
// that dismissed a live prompt, for tests and operator curl.
type pcAttendResponse struct {
	Result  string `json:"result"`
	Cleared bool   `json:"cleared"`
}

// handlePCAttend answers the caller's candle prompt. requireAuth has populated
// the session AuthUser. 404 when the session has no PC; 200 otherwise.
func (s *Server) handlePCAttend(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	res, err := s.world.SendContext(r.Context(), attendPCCommand(user.Username))
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

	cleared, _ := res.(bool)
	writeJSON(w, pcAttendResponse{Result: "ok", Cleared: cleared})
}

// attendPCCommand resolves username → PC actor (on the world goroutine) and
// delegates to sim.AckPCIdlePrompt. Same session→actor identity rule as
// wakePCCommand; clock captured inside the Fn so the refreshed horizon starts
// at execution time rather than at request receipt.
func attendPCCommand(username string) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return nil, errPCNotFound
			}
			return sim.AckPCIdlePrompt(actorID, time.Now().UTC()).Fn(world)
		},
	}
}
