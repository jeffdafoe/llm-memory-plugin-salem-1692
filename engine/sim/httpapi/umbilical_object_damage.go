package httpapi

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_object_damage.go — LLM-654, LLM-675. The operator's hand on a
// damage event: break a well or an owned business now (no roll, no guards),
// with the storm trigger ("storm" — a business gets the storm debris), or mend
// it without a bounty. For live testing the public-works loop and for clearing
// a break by hand.

// umbilicalObjectDamageRequest is the body of POST
// /api/village/umbilical/object/damage.
type umbilicalObjectDamageRequest struct {
	ID     string `json:"id"`
	Action string `json:"action"` // "damage" | "storm" | "repair"
}

// umbilicalObjectDamageResponse echoes the object's state after the change.
type umbilicalObjectDamageResponse struct {
	ID      string `json:"id"`
	Damaged bool   `json:"damaged"`
}

func (s *Server) handleUmbilicalObjectDamage(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalObjectDamageRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	auditUmbilical(user.Username, "object.damage."+req.Action, id)

	res, err := s.world.SendContext(r.Context(), sim.SetObjectDamage(sim.VillageObjectID(id), req.Action))
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return
		case errors.Is(err, sim.ErrVillageObjectNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, sim.ErrNotDamageable), errors.Is(err, sim.ErrUnknownDamageAction):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		}
		return
	}
	damaged, _ := res.(bool)
	writeJSON(w, umbilicalObjectDamageResponse{ID: id, Damaged: damaged})
}

// umbilicalRoadDamageRequest is the body of POST
// /api/village/umbilical/road/damage (LLM-677).
type umbilicalRoadDamageRequest struct {
	Action string `json:"action"` // "damage" | "storm"
}

// umbilicalRoadDamageResponse names the road obstacle that came down, so the
// operator can mend it through /object/damage {id, action: "repair"}.
type umbilicalRoadDamageResponse struct {
	ID string `json:"id"`
}

// globalDamageRoller rolls on math/rand/v2's global source — the operator's
// pick of asset and site has no need to be reproducible.
type globalDamageRoller struct{}

func (globalDamageRoller) Float64() float64 { return rand.Float64() }

// handleUmbilicalRoadDamage brings a tree down across a road now: a random
// fallen-tree asset at a random qualifying site, no roll and no guards beyond
// a free site. "storm" files it under the storm trigger.
func (s *Server) handleUmbilicalRoadDamage(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalRoadDamageRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	trigger := sim.DamageTriggerForce
	switch req.Action {
	case "damage":
	case "storm":
		trigger = sim.DamageTriggerStorm
	default:
		writeError(w, http.StatusBadRequest, `action must be "damage" or "storm"`)
		return
	}
	auditUmbilical(user.Username, "road.damage."+req.Action, "")

	res, err := s.world.SendContext(r.Context(), sim.ForceRoadDamage(trigger, globalDamageRoller{}))
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return
		case errors.Is(err, sim.ErrNoRoadSite):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		}
		return
	}
	id, _ := res.(sim.VillageObjectID)
	writeJSON(w, umbilicalRoadDamageResponse{ID: string(id)})
}

// umbilicalMinorDamageRequest is the body of POST
// /api/village/umbilical/minor/damage (LLM-790).
type umbilicalMinorDamageRequest struct {
	ID   string `json:"id"`   // optional: this object
	Form string `json:"form"` // optional: "fence" | "signpost" | "crate"
}

// umbilicalMinorDamageResponse names the minor work that broke, so the
// operator can mend it through /object/damage {id, action: "repair"}.
type umbilicalMinorDamageResponse struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Form  string `json:"form"`
}

// handleUmbilicalMinorDamage breaks a minor work now (sim.ForceMinorWork): no
// roll and no open cap. 400 an unknown form; 404 an unknown id; 409 when
// nothing (or not that object) can break into a minor work of the form.
func (s *Server) handleUmbilicalMinorDamage(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalMinorDamageRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	id := strings.TrimSpace(req.ID)
	auditUmbilical(user.Username, "minor.damage", "id="+id+" form="+req.Form)

	res, err := s.world.SendContext(r.Context(), sim.ForceMinorWork(sim.VillageObjectID(id), req.Form, globalDamageRoller{}))
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return
		case errors.Is(err, sim.ErrUnknownMinorForm):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, sim.ErrVillageObjectNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, sim.ErrNoMinorWorkSite), errors.Is(err, sim.ErrNotMinorWorkCandidate):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		}
		return
	}
	out, _ := res.(sim.ForcedMinorWork)
	writeJSON(w, umbilicalMinorDamageResponse{ID: string(out.ID), State: out.State, Form: out.Form})
}
