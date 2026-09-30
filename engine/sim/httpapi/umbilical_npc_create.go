package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_npc_create.go — LLM-689. POST /api/village/umbilical/npc/create:
// the operator-gated path to place a sprite-only NPC — in practice an animal
// into a pen — on the running engine. The editor's /admin/npc/create needs an
// in-world admin actor, which an operator does not have, and the only other way
// in was stop-engine → INSERT actor → start-engine. Same command underneath
// (sim.CreateNPC), so the placement is born decorative, is broadcast to
// connected clients, and is persisted on the next checkpoint.

// handleUmbilicalNPCCreate places an NPC. Body {sprite_id, x, y, name?}; x/y are
// world pixels (the editor's placement coordinates). A blank name takes the
// default — for an animal sprite its species, deduped ("Sheep", "Sheep 2").
// 400 missing sprite / bad position / invalid name / unknown sprite; 422 off-map;
// 200 {id}.
func (s *Server) handleUmbilicalNPCCreate(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req adminNPCCreateRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.SpriteID) == "" {
		writeError(w, http.StatusBadRequest, "sprite_id is required")
		return
	}
	if status, msg := validateObjectPosition(req.X, req.Y); msg != "" {
		writeError(w, status, msg)
		return
	}

	auditUmbilical(user.Username, "npc.create", fmt.Sprintf("sprite=%s pos=(%g,%g) name=%q", req.SpriteID, req.X, req.Y, req.Name))

	res, err := s.world.SendContext(r.Context(), sim.CreateNPC(req.Name, req.SpriteID, sim.WorldPos{X: req.X, Y: req.Y}, time.Now()))
	if err != nil {
		writeActorAdminError(w, err)
		return
	}
	out, ok := res.(sim.CreateNPCResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected create result")
		return
	}
	writeJSON(w, adminNPCCreateResponse{ID: string(out.ActorID)})
}
