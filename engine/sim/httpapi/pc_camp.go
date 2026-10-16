package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_camp.go — the player's camp routes (LLM-798). POST /api/village/pc/camp/pitch
// starts pitching the tent where the PC stands, /strike starts taking the camp
// down, and /light lights the camp fire — the landing of the client's fire game.
// Each resolves the session's PC on the world goroutine, stamps the input cursor
// (a deliberate act, which also wakes a sleeper), and delegates to the sim
// command. Pitch and strike answer at once; the camp goes up or comes down beat
// by beat over the npc_source_activity_changed and object frames.

// pcCampWorkResponse acks a pitch or strike: the tent and when the work is done
// if nothing interrupts it.
type pcCampWorkResponse struct {
	Result string `json:"result"`
	TentID string `json:"tent_id"`
	DoneAt string `json:"done_at"`
}

// pcCampLightResponse acks a lit fire: when it starts dying down, and when it
// is out.
type pcCampLightResponse struct {
	Result     string `json:"result"`
	FireID     string `json:"fire_id"`
	BurnsUntil string `json:"burns_until"`
	OutAt      string `json:"out_at"`
}

func (s *Server) handlePCCampPitch(w http.ResponseWriter, r *http.Request) {
	s.handlePCCampWork(w, r, sim.PitchTent)
}

func (s *Server) handlePCCampStrike(w http.ResponseWriter, r *http.Request) {
	s.handlePCCampWork(w, r, sim.StrikeTent)
}

func (s *Server) handlePCCampWork(w http.ResponseWriter, r *http.Request, start func(sim.ActorID, time.Time) sim.Command) {
	res, ok := s.runPCCampCommand(w, r, func(world *sim.World, actorID sim.ActorID, now time.Time) (any, error) {
		sim.TouchPCInput(world, actorID, now)
		return start(actorID, now).Fn(world)
	})
	if !ok {
		return
	}
	out, isResult := res.(sim.CampStartResult)
	if !isResult {
		writeError(w, http.StatusInternalServerError, "unexpected camp result")
		return
	}
	writeJSON(w, pcCampWorkResponse{
		Result: "ok",
		TentID: string(out.TentID),
		DoneAt: out.DoneAt.UTC().Format(time.RFC3339),
	})
}

func (s *Server) handlePCCampLight(w http.ResponseWriter, r *http.Request) {
	res, ok := s.runPCCampCommand(w, r, func(world *sim.World, actorID sim.ActorID, now time.Time) (any, error) {
		sim.TouchPCInput(world, actorID, now)
		return sim.LightCampFire(actorID, now).Fn(world)
	})
	if !ok {
		return
	}
	out, isResult := res.(sim.LightCampFireResult)
	if !isResult {
		writeError(w, http.StatusInternalServerError, "unexpected fire result")
		return
	}
	writeJSON(w, pcCampLightResponse{
		Result:     "ok",
		FireID:     string(out.FireID),
		BurnsUntil: out.BurnsUntil.UTC().Format(time.RFC3339),
		OutAt:      out.OutAt.UTC().Format(time.RFC3339),
	})
}

// runPCCampCommand resolves the session's PC on the world goroutine, runs fn
// with the clock captured at execution, and writes any error response: 404 with
// no PC, 422 when the world won't do it now (no tent, wrong ground, busy, …).
// ok is false when a response (or nothing, for a dropped request) was written.
func (s *Server) runPCCampCommand(w http.ResponseWriter, r *http.Request, fn func(*sim.World, sim.ActorID, time.Time) (any, error)) (any, bool) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return nil, false
	}
	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		actorID, found := findPCByLogin(world, user.Username)
		if !found {
			return nil, errPCNotFound
		}
		return fn(world, actorID, time.Now().UTC())
	}})
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		case errors.Is(err, errPCNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		default:
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		}
		return nil, false
	}
	return res, true
}
