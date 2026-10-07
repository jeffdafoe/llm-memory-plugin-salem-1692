package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_repair.go — the player's entry into the town's repair work (LLM-690).
// Three routes, all on the caller's own PC resolved from the session:
//
//   - GET  pc/repair/offer — the work at the damaged site the PC stands at, or
//     {"repair": null}. The click path of the repair dialog; the arrival path
//     reads the same shape off the object_condition room_event.
//   - POST pc/repair/start — take it. Returns the terms the game runs on.
//   - POST pc/repair/step  — one mini-game round. 429 when it comes sooner
//     than step_gap_ms after the last (not counted; send it again later).
//
// sim owns every world-state gate (sim/pc_repair.go); these handlers own only
// the session → PC resolution and the status mapping. start and step are
// deliberate acts, so they stamp the input cursor (TouchPCInput) — which also
// keeps the idle auto-bed off a player who is playing.

// repairOfferWireDTO is sim.PCRepairOffer on the wire.
type repairOfferWireDTO struct {
	ObjectID    string  `json:"object_id"`
	SiteKind    string  `json:"site_kind"`
	Form        string  `json:"form,omitempty"`
	Fact        string  `json:"fact"`
	Bounty      int     `json:"bounty"`
	ChestCanPay bool    `json:"chest_can_pay"`
	MenderName  string  `json:"mender_name,omitempty"`
	Steps       int     `json:"steps"`
	StepGapMs   int64   `json:"step_gap_ms"`
	Yours       bool    `json:"yours"`
	StepsDone   int     `json:"steps_done"`
	Difficulty  float64 `json:"difficulty"`
}

// repairOfferWire maps an offer to the wire, nil for nil.
func repairOfferWire(o *sim.PCRepairOffer) *repairOfferWireDTO {
	if o == nil {
		return nil
	}
	return &repairOfferWireDTO{
		ObjectID:    string(o.ObjectID),
		SiteKind:    o.SiteKind,
		Form:        o.Form,
		Fact:        o.Fact,
		Bounty:      o.Bounty,
		ChestCanPay: o.ChestCanPay,
		MenderName:  o.MenderName,
		Steps:       o.Steps,
		StepGapMs:   o.StepGap.Milliseconds(),
		Yours:       o.Yours,
		StepsDone:   o.StepsDone,
		Difficulty:  o.Difficulty,
	}
}

// pcRepairOfferResponse wraps the offer so "nothing here" is an explicit null.
type pcRepairOfferResponse struct {
	Repair *repairOfferWireDTO `json:"repair"`
}

// pcRepairStepResponse is sim.PCRepairStepResult on the wire.
type pcRepairStepResponse struct {
	StepsDone int  `json:"steps_done"`
	Steps     int  `json:"steps"`
	Done      bool `json:"done"`
	Landed    bool `json:"landed"`
	Paid      int  `json:"paid"`
}

func (s *Server) handlePCRepairOffer(w http.ResponseWriter, r *http.Request) {
	res, ok := s.runPCRepairCommand(w, r, func(world *sim.World, actorID sim.ActorID, _ time.Time) (any, error) {
		return sim.PCRepairOfferAt(actorID).Fn(world)
	})
	if !ok {
		return
	}
	offer, _ := res.(*sim.PCRepairOffer)
	writeJSON(w, pcRepairOfferResponse{Repair: repairOfferWire(offer)})
}

func (s *Server) handlePCRepairStart(w http.ResponseWriter, r *http.Request) {
	res, ok := s.runPCRepairCommand(w, r, func(world *sim.World, actorID sim.ActorID, now time.Time) (any, error) {
		sim.TouchPCInput(world, actorID, now)
		return sim.StartPCRepair(actorID, now).Fn(world)
	})
	if !ok {
		return
	}
	offer, isOffer := res.(*sim.PCRepairOffer)
	if !isOffer || offer == nil {
		writeError(w, http.StatusInternalServerError, "unexpected repair start result")
		return
	}
	writeJSON(w, pcRepairOfferResponse{Repair: repairOfferWire(offer)})
}

func (s *Server) handlePCRepairStep(w http.ResponseWriter, r *http.Request) {
	res, ok := s.runPCRepairCommand(w, r, func(world *sim.World, actorID sim.ActorID, now time.Time) (any, error) {
		sim.TouchPCInput(world, actorID, now)
		return sim.StepPCRepair(actorID, now).Fn(world)
	})
	if !ok {
		return
	}
	step, isStep := res.(sim.PCRepairStepResult)
	if !isStep {
		writeError(w, http.StatusInternalServerError, "unexpected repair step result")
		return
	}
	writeJSON(w, pcRepairStepResponse{
		StepsDone: step.StepsDone,
		Steps:     step.Steps,
		Done:      step.Done,
		Landed:    step.Landed,
		Paid:      step.Paid,
	})
}

// runPCRepairCommand resolves the session's PC on the world goroutine, runs fn
// with the clock captured at execution, and writes any error response. ok is
// false when a response (or nothing, for a dropped request) has been written.
func (s *Server) runPCRepairCommand(w http.ResponseWriter, r *http.Request, fn func(*sim.World, sim.ActorID, time.Time) (any, error)) (any, bool) {
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
		case errors.Is(err, sim.ErrPCRepairStepTooSoon):
			writeError(w, http.StatusTooManyRequests, err.Error())
		default:
			// Not at a site, already taken, the chest cannot pay, nothing under
			// way: well-formed, but the world will not do it now.
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		}
		return nil, false
	}
	return res, true
}
