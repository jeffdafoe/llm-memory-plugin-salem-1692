package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_object_control.go — the operator-gated village-object lifecycle on the
// umbilical control surface (LLM-61): create / move / delete plus the field setters
// (display-name, state, owner, loiter-offset, entry-policy, tags, refresh).
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
		errors.Is(err, sim.ErrInvalidDisplayName),
		errors.Is(err, sim.ErrInvalidEntryPolicy),
		errors.Is(err, sim.ErrInvalidTag),
		errors.Is(err, sim.ErrInvalidRefresh):
		// Bad input the command is the authority on (unknown asset, non-finite
		// position, over-cap/control-char name/tag, unknown entry policy, refresh
		// row violating the object_refresh CHECK constraints).
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		// sim.ErrVillageObjectIsStructure (delete of a structure-backed object),
		// sim.ErrOwnerActorNotFound (a dangling set-owner target), and any other
		// command rejection are client-correctable 422s.
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

// handleUmbilicalObjectSetState sets a placed object's current_state — the
// operator counterpart to /admin/object/set-state. The state is a free-form
// catalog string (an admin override is trusted; an unknown state simply renders
// as the asset fallback). 400 missing object_id / state; 404 object not found;
// 200 ok (Applied=false when already at the target state).
func (s *Server) handleUmbilicalObjectSetState(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req adminObjectStateRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.ObjectID == "" {
		writeError(w, http.StatusBadRequest, "object_id is required")
		return
	}
	if req.State == "" {
		writeError(w, http.StatusBadRequest, "state is required")
		return
	}

	auditUmbilical(user.Username, "object.set-state", fmt.Sprintf("object=%s state=%s", req.ObjectID, req.State))

	// SetVillageObjectState reports a missing object as a result Reason
	// (Applied=false, nil error), not an error — a shape that suits its
	// scheduled-flip callers. Translate it to the shared not-found sentinel so
	// this route maps a missing object to 404 like the rest of the surface.
	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		out, err := sim.SetVillageObjectState(sim.VillageObjectID(req.ObjectID), req.State).Fn(world)
		if err != nil {
			return nil, err
		}
		sr := out.(sim.SetStateResult)
		if sr.Reason == "not_found" {
			return nil, sim.ErrVillageObjectNotFound
		}
		return sr, nil
	}})
	if writeUmbilicalObjectError(w, err) {
		return
	}
	out, ok := res.(sim.SetStateResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-state result")
		return
	}
	writeJSON(w, adminObjectStateResponse{ID: req.ObjectID, State: req.State, Applied: out.Applied})
}

// handleUmbilicalObjectSetOwner sets (or clears) a placed object's owning actor —
// the operator counterpart to /admin/object/set-owner. An empty owner_actor_id
// clears ownership; a non-empty one must resolve to a live actor. 400 missing
// object_id; 404 object not found; 422 owner actor not found; 200 with the applied
// owner.
func (s *Server) handleUmbilicalObjectSetOwner(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req adminObjectOwnerRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.ObjectID == "" {
		writeError(w, http.StatusBadRequest, "object_id is required")
		return
	}

	owner := req.OwnerActorID
	if owner == "" {
		owner = "(cleared)"
	}
	auditUmbilical(user.Username, "object.set-owner", fmt.Sprintf("object=%s owner=%s", req.ObjectID, owner))

	res, err := s.world.SendContext(r.Context(), sim.SetVillageObjectOwner(sim.VillageObjectID(req.ObjectID), sim.ActorID(req.OwnerActorID)))
	if writeUmbilicalObjectError(w, err) {
		return
	}
	out, ok := res.(sim.SetOwnerResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-owner result")
		return
	}
	writeJSON(w, adminObjectOwnerResponse{ID: string(out.ID), OwnerActorID: string(out.OwnerActorID)})
}

// handleUmbilicalObjectSetLoiterOffset sets (or clears) a placed object's loiter
// offset — the operator counterpart to /admin/object/set-loiter-offset. x and y
// are both-or-neither (the offset is an (x, y) pair); both omitted clears it back
// to the catalog default. 400 missing object_id / only one axis; 404 object not
// found; 200 with the applied offset.
func (s *Server) handleUmbilicalObjectSetLoiterOffset(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req adminObjectLoiterOffsetRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.ObjectID == "" {
		writeError(w, http.StatusBadRequest, "object_id is required")
		return
	}
	if (req.X == nil) != (req.Y == nil) {
		writeError(w, http.StatusBadRequest, "x and y must both be set or both omitted")
		return
	}

	offset := "cleared"
	if req.X != nil {
		offset = fmt.Sprintf("(%d,%d)", *req.X, *req.Y)
	}
	auditUmbilical(user.Username, "object.set-loiter-offset", fmt.Sprintf("object=%s offset=%s", req.ObjectID, offset))

	res, err := s.world.SendContext(r.Context(), sim.SetVillageObjectLoiterOffset(sim.VillageObjectID(req.ObjectID), req.X, req.Y))
	if writeUmbilicalObjectError(w, err) {
		return
	}
	out, ok := res.(sim.SetLoiterOffsetResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-loiter-offset result")
		return
	}
	writeJSON(w, adminObjectLoiterOffsetResponse{ID: string(out.ID), X: out.X, Y: out.Y})
}

// handleUmbilicalObjectSetEntryPolicy sets a placed object's entry policy — the
// operator counterpart to /admin/object/set-entry-policy. The handler validates
// the enum ("", "open", "owner-only", "closed") and the command guards it again.
// 400 missing object_id / unknown policy; 404 object not found; 200 with the
// applied policy.
func (s *Server) handleUmbilicalObjectSetEntryPolicy(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req adminObjectEntryPolicyRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.ObjectID == "" {
		writeError(w, http.StatusBadRequest, "object_id is required")
		return
	}
	if !validEntryPolicy(req.EntryPolicy) {
		writeError(w, http.StatusBadRequest, `entry_policy must be "", "open", "owner-only", or "closed"`)
		return
	}

	auditUmbilical(user.Username, "object.set-entry-policy", fmt.Sprintf("object=%s policy=%q", req.ObjectID, req.EntryPolicy))

	res, err := s.world.SendContext(r.Context(), sim.SetVillageObjectEntryPolicy(sim.VillageObjectID(req.ObjectID), sim.EntryPolicy(req.EntryPolicy)))
	if writeUmbilicalObjectError(w, err) {
		return
	}
	out, ok := res.(sim.SetEntryPolicyResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-entry-policy result")
		return
	}
	writeJSON(w, adminObjectEntryPolicyResponse{ID: string(out.ID), EntryPolicy: string(out.EntryPolicy)})
}

// handleUmbilicalObjectAddTag adds a per-instance tag to a placed object (the
// operator counterpart to /admin/object/add-tag). Idempotent — a tag already
// present is a no-op.
func (s *Server) handleUmbilicalObjectAddTag(w http.ResponseWriter, r *http.Request) {
	s.handleUmbilicalObjectTagMutation(w, r, true)
}

// handleUmbilicalObjectRemoveTag removes a per-instance tag from a placed object
// (the operator counterpart to /admin/object/remove-tag). Idempotent — removing
// an absent tag is a no-op.
func (s *Server) handleUmbilicalObjectRemoveTag(w http.ResponseWriter, r *http.Request) {
	s.handleUmbilicalObjectTagMutation(w, r, false)
}

// handleUmbilicalObjectTagMutation is the shared add/remove-tag handler — the two
// routes differ only in which sim command they dispatch, so the gate, decode,
// validation, status mapping, and response shape live here once. 400 missing
// object_id / invalid tag; 404 object not found; 200 with the full tag set (always
// an array, [] when the last tag was just removed).
func (s *Server) handleUmbilicalObjectTagMutation(w http.ResponseWriter, r *http.Request, add bool) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req adminObjectTagRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.ObjectID == "" {
		writeError(w, http.StatusBadRequest, "object_id is required")
		return
	}

	action := "object.remove-tag"
	cmd := sim.RemoveVillageObjectTag(sim.VillageObjectID(req.ObjectID), req.Tag)
	if add {
		action = "object.add-tag"
		cmd = sim.AddVillageObjectTag(sim.VillageObjectID(req.ObjectID), req.Tag)
	}
	auditUmbilical(user.Username, action, fmt.Sprintf("object=%s tag=%q", req.ObjectID, req.Tag))

	res, err := s.world.SendContext(r.Context(), cmd)
	if writeUmbilicalObjectError(w, err) {
		return
	}
	out, ok := res.(sim.SetTagsResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected tag-mutation result")
		return
	}
	// Coerce nil → [] so the body is a JSON array even when the last tag was just
	// removed (mirrors the WS frame's always-an-array contract).
	tags := out.Tags
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, adminObjectTagResponse{ID: string(out.ID), Tags: tags})
}

// handleUmbilicalObjectSetRefresh replaces a placed object's refresh-policy set —
// the operator counterpart to /admin/object/set-refresh, and the partner to
// set-display-name for fixing a broken gather/eat source live (a usable source
// needs both a name and a valid refresh policy). The set replaces the object's
// existing policies wholesale; an empty/omitted rows clears them. 400 missing
// object_id / invalid refresh row; 404 object not found; 200 with the applied set.
func (s *Server) handleUmbilicalObjectSetRefresh(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req adminObjectRefreshRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.ObjectID == "" {
		writeError(w, http.StatusBadRequest, "object_id is required")
		return
	}

	// Map the wire rows to sim.ObjectRefresh; the command deep-copies before
	// storing, so passing the request's pointers straight through is safe.
	rows := make([]*sim.ObjectRefresh, 0, len(req.Rows))
	for _, row := range req.Rows {
		rows = append(rows, &sim.ObjectRefresh{
			Attribute:          sim.NeedKey(row.Attribute),
			Amount:             row.Amount,
			AvailableQuantity:  row.AvailableQuantity,
			MaxQuantity:        row.MaxQuantity,
			RefreshMode:        sim.RefreshMode(row.RefreshMode),
			RefreshPeriodHours: row.RefreshPeriodHours,
			DwellDelta:         row.DwellDelta,
			DwellPeriodMinutes: row.DwellPeriodMinutes,
		})
	}

	auditUmbilical(user.Username, "object.set-refresh", fmt.Sprintf("object=%s rows=%d", req.ObjectID, len(rows)))

	res, err := s.world.SendContext(r.Context(), sim.SetVillageObjectRefreshes(sim.VillageObjectID(req.ObjectID), rows))
	if writeUmbilicalObjectError(w, err) {
		return
	}
	out, ok := res.(sim.SetRefreshesResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-refresh result")
		return
	}
	writeJSON(w, adminObjectRefreshResponse{ID: string(out.ID), Rows: refreshRowsToWire(out.Refreshes)})
}
