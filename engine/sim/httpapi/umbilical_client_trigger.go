package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_client_trigger.go — LLM-881. POST /umbilical/client-trigger: an
// operator runs a client-only effect on one player's client (first use: show
// the LLM-844 figure at the treeline to test and tune it).
//
// A thin caller of the reverse channel. It issues sim.SendClientTrigger, which
// emits the same ClientTrigger event engine features emit; the hub sends the
// frame to that player's sockets only. The route has no frame or hub path of
// its own.

// umbilicalClientTriggerRequest is the POST body. Detail is optional; when
// present it must be a JSON object.
type umbilicalClientTriggerRequest struct {
	ActorID string          `json:"actor_id"`
	Kind    string          `json:"kind"`
	Detail  json.RawMessage `json:"detail,omitempty"`
}

// umbilicalClientTriggerResponse reports the emitted trigger. Connected is
// whether the player's login held a live socket when the route answered; false
// means nobody could receive it (the event is not queued for later).
type umbilicalClientTriggerResponse struct {
	ActorID   string `json:"actor_id"`
	Login     string `json:"login"`
	Kind      string `json:"kind"`
	Connected bool   `json:"connected"`
}

// handleUmbilicalClientTrigger emits a ClientTrigger for a PC. 400 missing
// actor_id, unknown kind, or a detail that is not a small JSON object; 404
// unknown actor; 422 the actor is not a PC with a login; 200 with the trigger.
func (s *Server) handleUmbilicalClientTrigger(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalClientTriggerRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	req.ActorID = strings.TrimSpace(req.ActorID)
	req.Kind = strings.TrimSpace(req.Kind)
	if req.ActorID == "" {
		writeError(w, http.StatusBadRequest, "actor_id is required")
		return
	}
	if !sim.IsClientTriggerKind(req.Kind) {
		writeError(w, http.StatusBadRequest, "unknown client trigger kind")
		return
	}
	detail, ok := clientTriggerDetail(req.Detail)
	if !ok {
		writeError(w, http.StatusBadRequest, sim.ErrClientTriggerDetail.Error())
		return
	}

	// Audit the attempt up front so a later rejection is still on the record.
	auditUmbilical(user.Username, "client-trigger", fmt.Sprintf("actor=%s kind=%s", req.ActorID, req.Kind))

	res, err := s.world.SendContext(r.Context(), sim.SendClientTrigger(sim.ActorID(req.ActorID), req.Kind, detail, time.Now().UTC()))
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return
		case errors.Is(err, sim.ErrActorNotFound):
			writeError(w, http.StatusNotFound, "actor not found")
		case errors.Is(err, sim.ErrClientTriggerNoPlayer):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		case errors.Is(err, sim.ErrUnknownClientTriggerKind), errors.Is(err, sim.ErrClientTriggerDetail):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			log.Printf("umbilical client-trigger: actor=%s kind=%s: %v", req.ActorID, req.Kind, err)
			writeError(w, http.StatusInternalServerError, "client-trigger failed")
		}
		return
	}
	out, ok := res.(sim.SendClientTriggerResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected client-trigger result")
		return
	}
	connected := false
	if s.hub != nil {
		_, connected = s.hub.ConnectedLogins()[out.Login]
	}
	writeJSON(w, umbilicalClientTriggerResponse{
		ActorID:   string(out.ActorID),
		Login:     out.Login,
		Kind:      out.Kind,
		Connected: connected,
	})
}

// clientTriggerDetail decodes the optional detail. Absent or null is no detail;
// anything else must be a JSON object. The size cap is checked by the sim
// command on the encoded object.
func clientTriggerDetail(raw json.RawMessage) (map[string]any, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, true
	}
	var obj map[string]any
	if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
		return nil, false
	}
	if _, err := sim.EncodeClientTriggerDetail(obj); err != nil {
		return nil, false
	}
	return obj, true
}
