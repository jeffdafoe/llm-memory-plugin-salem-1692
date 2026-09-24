package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_object_damage.go — LLM-654. The operator's hand on a damage event:
// break a well now (no roll, no guards) or mend it without a bounty. For live
// testing the public-works loop and for clearing a break by hand.

// umbilicalObjectDamageRequest is the body of POST
// /api/village/umbilical/object/damage.
type umbilicalObjectDamageRequest struct {
	ID     string `json:"id"`
	Action string `json:"action"` // "damage" | "repair"
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
