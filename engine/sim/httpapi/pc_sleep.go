package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_sleep.go — the PC sleep control routes: POST /api/village/pc/sleep beds the
// caller's PC (the explicit counterpart to the passive idle-auto-bed sweep), and
// POST /api/village/pc/wake gets them up early (the top-bar Wake button the v2
// client already POSTs here). Both resolve the PC from the authenticated session
// inside the command (world goroutine, no TOCTOU) and delegate to sim.SleepPC /
// sim.WakePC; the world emits PCSleepStarted / PCSleepEnded, which translate.go
// turns into the pc_sleep_started / pc_sleep_ended WS frames the client renders
// (sleep-fade overlay + "Sleeping — wake HH:MM" chip).
//
// Neither route stamps the input cursor (TouchPCInput) — they manage sleep state
// authoritatively so the broadcast carries the right reason ("manual" wake, a
// fresh "started"), not an incidental "input" wake. Only the action routes
// (move / speak / pay) touch the cursor. Both routes ignore the request body —
// the caller is the session's own PC, so there are no parameters.

// pcSleepResponse acks /pc/sleep. Result is "ok" on success; wake_at (RFC3339)
// is present only on a fresh bed-down (the safety-cap instant the client renders
// as the countdown) and omitted on the idempotent already-sleeping no-op,
// matching v1's "no fresh transition" signal.
type pcSleepResponse struct {
	Result string `json:"result"`
	WakeAt string `json:"wake_at,omitempty"`
}

// pcWakeResponse acks /pc/wake. Idempotent — "ok" whether or not the PC was
// actually sleeping; the client updates off the pc_sleep_ended broadcast.
type pcWakeResponse struct {
	Result string `json:"result"`
}

// handlePCSleep beds the caller's PC. requireAuth has populated the session
// AuthUser. 404 when the session has no PC; 422 when the PC isn't in a paid
// private bedroom (sim.ErrPCCannotSleepHere); 200 otherwise (with wake_at on a
// fresh bed-down, omitted if already sleeping).
func (s *Server) handlePCSleep(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	res, err := s.world.SendContext(r.Context(), sleepPCCommand(user.Username))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errPCNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		// sim.ErrPCCannotSleepHere (not in a paid bedroom) and any other
		// rejection are state-validation failures — the request was well-formed
		// but the PC can't sleep in its current state.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.PCSleepResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected sleep result")
		return
	}
	resp := pcSleepResponse{Result: "ok"}
	if out.Bedded {
		resp.WakeAt = out.WakeAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, resp)
}

// handlePCWake wakes the caller's PC (the Wake button). requireAuth has
// populated the session AuthUser. 404 when the session has no PC; 200 otherwise
// (idempotent — a no-op when the PC wasn't sleeping).
func (s *Server) handlePCWake(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	_, err := s.world.SendContext(r.Context(), wakePCCommand(user.Username))
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
	writeJSON(w, pcWakeResponse{Result: "ok"})
}

// sleepPCCommand resolves username → PC actor (on the world goroutine) and
// delegates to sim.SleepPC. Same session→actor identity rule as movePCCommand;
// the clock is captured inside the Fn so the bed-down + wake-cap reflect
// execution time, not how long the command sat in the channel.
func sleepPCCommand(username string) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return nil, errPCNotFound
			}
			return sim.SleepPC(actorID, time.Now().UTC()).Fn(world)
		},
	}
}

// wakePCCommand resolves username → PC actor and delegates to sim.WakePC. Same
// identity rule; clock captured inside the Fn.
func wakePCCommand(username string) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return nil, errPCNotFound
			}
			return sim.WakePC(actorID, time.Now().UTC()).Fn(world)
		},
	}
}
