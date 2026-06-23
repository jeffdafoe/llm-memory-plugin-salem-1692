package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_object_control.go — the operator-gated village-object lifecycle on the
// umbilical control surface (LLM-61): create / move / delete / set-display-name.
//
// These mirror the editor's /api/village/admin/object/* routes, which issue the
// same sim object Commands but are gated by adminCommand → an in-world admin
// ACTOR (sim.Actor.IsAdmin). Operators (work / home / jeff) have no salem actor
// row, so those routes 403 for them — meaning a live data/placement defect could
// be diagnosed over the umbilical but not fixed live (it had to be handed to an
// engine-stopped migration; the LLM-60 blueberry-bush naming was exactly this).
//
// Here the gate is the umbilical's own requireOperator (plugins/administer),
// applied at registration by Server.Handler — so the handlers issue the object
// Command DIRECTLY, with no adminCommand actor wrapper. Everything else matches
// the control-surface contract: a 4 KiB body cap + strict decode, an audit line
// BEFORE the command runs, and the control-flag second opt-in (these routes are
// registered, and listed in the manifest, only when controlEnabled).
//
// The request/response DTOs and validateObjectPosition are REUSED from the admin
// handlers (write_handlers.go): they are the same package and the object contract
// is identical across the two surfaces — only the authz gate differs. The lone
// behavioral difference is that the admin handlers also map errAdminForbidden →
// 403, which can't arise here (no adminCommand), so the shared object-error mapper
// below omits it.

// writeUmbilicalObjectError maps a village-object command error to its HTTP status
// and writes it, returning true when it handled err (so the caller returns). All
// the object commands draw from one shared sentinel vocabulary, so a single mapper
// serves every object control route. A context cancellation writes nothing — the
// caller is already gone — but still returns true so the handler stops.
func writeUmbilicalObjectError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// Caller gone / timed out; the response is moot.
	case errors.Is(err, sim.ErrVillageObjectNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, sim.ErrUnknownAsset),
		errors.Is(err, sim.ErrInvalidObjectPosition),
		errors.Is(err, sim.ErrInvalidDisplayName):
		// Bad input the command is the authority on (unknown asset, non-finite
		// position, over-cap/control-char name).
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		// sim.ErrVillageObjectIsStructure (delete of a structure-backed object) and
		// any other command rejection are client-correctable 422s.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	}
	return true
}

// handleUmbilicalObjectCreate places a new village object — the operator-reachable
// counterpart to /admin/object/create. Reuses adminObjectCreateRequest/Response
// and validateObjectPosition; issues sim.CreateVillageObject directly. 400 missing
// asset_id / empty attached_to / non-finite position; 404 unknown attached_to
// parent; 422 off-map / other rejection; 200 with the minted object.
func (s *Server) handleUmbilicalObjectCreate(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req adminObjectCreateRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.AssetID == "" {
		writeError(w, http.StatusBadRequest, "asset_id is required")
		return
	}
	// attached_to is optional (omitted = root placement); an explicitly present
	// empty string is malformed (the pointer distinguishes omitted from "").
	if req.AttachedTo != nil && *req.AttachedTo == "" {
		writeError(w, http.StatusBadRequest, "attached_to must be non-empty when provided")
		return
	}
	if status, msg := validateObjectPosition(req.X, req.Y); msg != "" {
		writeError(w, status, msg)
		return
	}
	var attachedTo sim.VillageObjectID
	if req.AttachedTo != nil {
		attachedTo = sim.VillageObjectID(*req.AttachedTo)
	}

	auditUmbilical(user.Username, "object.create", fmt.Sprintf("asset=%s pos=(%g,%g)", req.AssetID, req.X, req.Y))

	res, err := s.world.SendContext(r.Context(), sim.CreateVillageObject(sim.AssetID(req.AssetID), req.X, req.Y, attachedTo, user.Username))
	if writeUmbilicalObjectError(w, err) {
		return
	}
	out, ok := res.(sim.CreateObjectResult)
	if !ok || out.Object == nil {
		writeError(w, http.StatusInternalServerError, "unexpected create result")
		return
	}
	writeJSON(w, adminObjectCreateResponse{
		ID:           string(out.Object.ID),
		AssetID:      string(out.Object.AssetID),
		CurrentState: out.Object.CurrentState,
		X:            out.Object.Pos.X,
		Y:            out.Object.Pos.Y,
		PlacedBy:     out.Object.PlacedBy,
		EntryPolicy:  string(out.Object.EntryPolicy),
		AttachedTo:   string(out.Object.AttachedTo),
	})
}

// handleUmbilicalObjectMove repositions a placed object to a new world-pixel
// anchor — the operator counterpart to /admin/object/move. 400 missing object_id /
// non-finite position; 404 object not found; 422 off-map; 200 with the new anchor.
func (s *Server) handleUmbilicalObjectMove(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req adminObjectMoveRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.ObjectID == "" {
		writeError(w, http.StatusBadRequest, "object_id is required")
		return
	}
	if status, msg := validateObjectPosition(req.X, req.Y); msg != "" {
		writeError(w, status, msg)
		return
	}

	auditUmbilical(user.Username, "object.move", fmt.Sprintf("object=%s to=(%g,%g)", req.ObjectID, req.X, req.Y))

	res, err := s.world.SendContext(r.Context(), sim.MoveVillageObject(sim.VillageObjectID(req.ObjectID), req.X, req.Y))
	if writeUmbilicalObjectError(w, err) {
		return
	}
	out, ok := res.(sim.MoveObjectResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected move result")
		return
	}
	writeJSON(w, adminObjectMoveResponse{ID: string(out.ID), X: out.X, Y: out.Y})
}

// handleUmbilicalObjectDelete removes a placed object (and its attached overlays)
// — the operator counterpart to /admin/object/delete. 400 missing object_id; 404
// object not found; 422 the object backs a structure (refused); 200 with the
// removed ids (children first).
func (s *Server) handleUmbilicalObjectDelete(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req adminObjectDeleteRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.ObjectID == "" {
		writeError(w, http.StatusBadRequest, "object_id is required")
		return
	}

	auditUmbilical(user.Username, "object.delete", "object="+req.ObjectID)

	res, err := s.world.SendContext(r.Context(), sim.DeleteVillageObject(sim.VillageObjectID(req.ObjectID)))
	if writeUmbilicalObjectError(w, err) {
		return
	}
	out, ok := res.(sim.DeleteObjectResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected delete result")
		return
	}
	ids := make([]string, len(out.DeletedIDs))
	for i, id := range out.DeletedIDs {
		ids[i] = string(id)
	}
	writeJSON(w, adminObjectDeleteResponse{DeletedIDs: ids})
}

// handleUmbilicalObjectSetDisplayName sets or clears a placed object's display-name
// override — the operator counterpart to /admin/object/set-display-name, and the
// route that closes the LLM-60 loop (rename a nameless gather/eat source live
// instead of via an engine-stopped migration). 400 missing object_id / invalid
// name; 404 object not found; 200 with the applied name.
func (s *Server) handleUmbilicalObjectSetDisplayName(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req adminObjectDisplayNameRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.ObjectID == "" {
		writeError(w, http.StatusBadRequest, "object_id is required")
		return
	}

	auditUmbilical(user.Username, "object.set-display-name", fmt.Sprintf("object=%s name=%q", req.ObjectID, req.DisplayName))

	res, err := s.world.SendContext(r.Context(), sim.SetVillageObjectDisplayName(sim.VillageObjectID(req.ObjectID), req.DisplayName))
	if writeUmbilicalObjectError(w, err) {
		return
	}
	out, ok := res.(sim.SetDisplayNameResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-display-name result")
		return
	}
	writeJSON(w, adminObjectDisplayNameResponse{ID: string(out.ID), DisplayName: out.DisplayName})
}
