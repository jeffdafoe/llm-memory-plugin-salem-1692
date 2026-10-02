package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_visitor.go — the operator-gated route that sends a visitor away now
// (LLM-701). A traveler otherwise leaves only when his stay runs out; this ends
// the stay at once, silences him, and starts his walk out through the normal
// departure path (sim.DepartVisitor). Gate + body-cap + audit match the rest of
// the control surface.

type departVisitorRequest struct {
	ActorID string `json:"actor_id"`
}

type departVisitorResponse struct {
	ActorID     string    `json:"actor_id"`
	DisplayName string    `json:"display_name"`
	Walk        string    `json:"walk"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// handleUmbilicalDepartVisitor sends one visitor away. 400 missing actor_id; 404
// actor not found; 409 the actor is not a visitor; 200 with what was done about
// his walk out ("started", "no_route", "already_departing") and the stay
// deadline cleanup removes him by.
func (s *Server) handleUmbilicalDepartVisitor(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req departVisitorRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.ActorID == "" {
		writeError(w, http.StatusBadRequest, "actor_id is required")
		return
	}
	auditUmbilical(user.Username, "visitor.depart", fmt.Sprintf("actor=%s", req.ActorID))

	res, err := s.world.SendContext(r.Context(), sim.DepartVisitor(sim.ActorID(req.ActorID), time.Now().UTC()))
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			// Caller gone / timed out; the response is moot.
		case errors.Is(err, sim.ErrActorNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, sim.ErrNotAVisitor):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "depart failed")
		}
		return
	}
	out, ok := res.(sim.DepartVisitorResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected depart result")
		return
	}
	writeJSON(w, departVisitorResponse{
		ActorID:     string(out.ID),
		DisplayName: out.DisplayName,
		Walk:        string(out.Walk),
		ExpiresAt:   out.ExpiresAt,
	})
}
