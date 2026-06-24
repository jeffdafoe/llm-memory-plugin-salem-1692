package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_restock.go — the restock-policy control routes (LLM-95): live,
// durable per-entry editing of an actor's restock list (the "stuff an NPC
// produces / restocks / forages at work"). Operator-gated, audited, armed only
// when control is enabled — same posture as the rest of umbilical_control.go.
//
// The edit is a normal world Command (sim.SetRestockEntry / RemoveRestockEntry)
// that mutates the actor's attribute params and re-projects RestockPolicy;
// durability rides the existing attribute checkpoint. Covers all three sources
// (produce / buy / forage) — they share the same RestockEntry shape.

// umbilicalRestockSetRequest is the POST /api/village/umbilical/restock/set
// body: add or update one entry. source is produce | buy | forage; cap is the
// personal-carry cap (0 = no cap).
type umbilicalRestockSetRequest struct {
	ActorID string `json:"actor_id"`
	Item    string `json:"item"`
	Source  string `json:"source"`
	Cap     int    `json:"cap"`
}

// umbilicalRestockRemoveRequest is the POST /api/village/umbilical/restock/remove
// body: drop the entry for one item.
type umbilicalRestockRemoveRequest struct {
	ActorID string `json:"actor_id"`
	Item    string `json:"item"`
}

// umbilicalRestockEntry is one entry on the response wire.
type umbilicalRestockEntry struct {
	Item   string `json:"item"`
	Source string `json:"source"`
	Cap    int    `json:"cap"`
}

// umbilicalRestockResponse echoes the actor's full post-mutation restock list.
type umbilicalRestockResponse struct {
	ActorID string                  `json:"actor_id"`
	Entries []umbilicalRestockEntry `json:"entries"`
}

// handleUmbilicalRestockSet adds or updates one restock entry on an actor. The
// item is resolved against the catalog inside the command (key or label); a
// produce entry requires a recipe (a produce entry without one never fires). 400
// missing actor_id/item, bad source, or negative cap; 404 unknown actor; 422
// unknown item / no recipe for produce / no attribute to hold the entry; 200
// with the post-mutation entries.
func (s *Server) handleUmbilicalRestockSet(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalRestockSetRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.ActorID == "" {
		writeError(w, http.StatusBadRequest, "actor_id is required")
		return
	}
	if req.Item == "" {
		writeError(w, http.StatusBadRequest, "item is required")
		return
	}
	source := sim.RestockSource(req.Source)
	if source != sim.RestockSourceProduce && source != sim.RestockSourceBuy &&
		source != sim.RestockSourceForage {
		writeError(w, http.StatusBadRequest, `source must be "produce", "buy", or "forage"`)
		return
	}
	if req.Cap < 0 {
		writeError(w, http.StatusBadRequest, "cap must be >= 0")
		return
	}

	auditUmbilical(user.Username, "restock.set",
		fmt.Sprintf("actor=%s item=%s source=%s cap=%d", req.ActorID, req.Item, req.Source, req.Cap))

	res, err := s.world.SendContext(r.Context(), sim.SetRestockEntry(
		sim.ActorID(req.ActorID), req.Item, source, req.Cap))
	if err != nil {
		writeRestockError(w, err)
		return
	}
	out, ok := res.(sim.RestockPolicyResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected restock result")
		return
	}
	writeJSON(w, restockResponse(req.ActorID, out))
}

// handleUmbilicalRestockRemove removes one entry (by item) from an actor's
// restock policy. 400 missing actor_id/item; 404 unknown actor / no such entry;
// 422 unknown item; 200 with the post-mutation entries.
func (s *Server) handleUmbilicalRestockRemove(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalRestockRemoveRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.ActorID == "" {
		writeError(w, http.StatusBadRequest, "actor_id is required")
		return
	}
	if req.Item == "" {
		writeError(w, http.StatusBadRequest, "item is required")
		return
	}

	auditUmbilical(user.Username, "restock.remove", fmt.Sprintf("actor=%s item=%s", req.ActorID, req.Item))

	res, err := s.world.SendContext(r.Context(), sim.RemoveRestockEntry(sim.ActorID(req.ActorID), req.Item))
	if err != nil {
		writeRestockError(w, err)
		return
	}
	out, ok := res.(sim.RestockPolicyResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected restock result")
		return
	}
	writeJSON(w, restockResponse(req.ActorID, out))
}

// writeRestockError maps the sim restock-edit error sentinels to HTTP status.
// Context cancellation writes nothing (the client went away mid-command).
func writeRestockError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	case errors.Is(err, sim.ErrActorNotFound), errors.Is(err, sim.ErrRestockEntryNotFound):
		// Unknown actor and "no such entry" are both 404 — the addressed
		// resource doesn't exist.
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, sim.ErrInvalidRestockSource):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		// Unknown item, no recipe for produce, no attribute to hold the entry —
		// client-correctable; the error message names the specific failure.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	}
}

// restockResponse builds the wire response from the command result, mapping each
// entry's effective cap (Max, falling back to the legacy Target alias).
func restockResponse(actorID string, res sim.RestockPolicyResult) umbilicalRestockResponse {
	out := umbilicalRestockResponse{ActorID: actorID, Entries: make([]umbilicalRestockEntry, 0, len(res.Entries))}
	for _, e := range res.Entries {
		out.Entries = append(out.Entries, umbilicalRestockEntry{
			Item:   string(e.Item),
			Source: string(e.Source),
			Cap:    e.Cap(),
		})
	}
	return out
}
