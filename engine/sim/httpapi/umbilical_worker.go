package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_worker.go — the operator-gated Worker-provisioning route on the
// umbilical control surface. Minting a Worker (assign a backing VA + grant the
// `worker` attribute + bring the actor online) was previously only possible by
// editing the DB under a coordinated engine stop → write → start, because the
// in-memory Kind is derived at load and a live agent-link change left a
// decorative still classified KindDecorative (never ticked). sim.ProvisionWorker
// closes that gap by reclassifying Kind in memory, so this route activates a
// worker live with no village pause. Gate + body-cap + audit + control-flag
// opt-in match the rest of the control surface (see umbilical_object_control.go).

type provisionWorkerRequest struct {
	ActorID string `json:"actor_id"`
	Agent   string `json:"agent"`
}

type provisionWorkerResponse struct {
	ActorID    string   `json:"actor_id"`
	Agent      string   `json:"agent"`
	Kind       string   `json:"kind"`
	Attributes []string `json:"attributes"`
}

// handleUmbilicalProvisionWorker mints a sprite-only decorative into a live
// Worker: assign a backing VA (default salem-vendor when agent omitted), grant
// the `worker` attribute, and reclassify the actor's Kind in memory so it comes
// online without a restart. 400 missing actor_id / invalid agent; 404 actor not
// found or a PC; 409 the actor is already a live NPC (not a decorative); 422 the
// `worker` attribute is unseeded; 200 with the actor's new driver state.
func (s *Server) handleUmbilicalProvisionWorker(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req provisionWorkerRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.ActorID == "" {
		writeError(w, http.StatusBadRequest, "actor_id is required")
		return
	}
	agent := strings.TrimSpace(req.Agent)
	if agent == "" {
		agent = sim.VendorAgentName
	}
	auditUmbilical(user.Username, "worker.provision", fmt.Sprintf("actor=%s agent=%s", req.ActorID, agent))

	res, err := s.world.SendContext(r.Context(), sim.ProvisionWorker(sim.ActorID(req.ActorID), agent))
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			// Caller gone / timed out; the response is moot.
		case errors.Is(err, sim.ErrActorNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, sim.ErrActorNotProvisionable):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, sim.ErrInvalidAgentLink):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, sim.ErrUnknownAttribute):
			writeError(w, http.StatusUnprocessableEntity, "worker attribute is not seeded")
		default:
			writeError(w, http.StatusInternalServerError, "provision failed")
		}
		return
	}
	out, ok := res.(sim.ProvisionWorkerResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected provision result")
		return
	}
	writeJSON(w, provisionWorkerResponse{
		ActorID:    string(out.ID),
		Agent:      out.LLMAgent,
		Kind:       actorKindString(out.Kind),
		Attributes: out.Attributes,
	})
}
