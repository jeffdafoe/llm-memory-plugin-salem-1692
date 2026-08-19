package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// Fence-run admin routes (LLM-637) — the HTTP face of sim.PlaceFenceRun /
// sim.DeleteFenceRun behind the editor's drag-to-fence tool. Both are
// requireAuth + adminCommand like the other object writes.

// adminFencePlaceRequest is the POST /api/village/admin/fence/place body: the
// fence asset and the two corners of the drag in world pixels (any corner
// order). The engine floors them to tiles and lays the run over the inclusive
// rectangle — a 1x1 drag is a post, a 1-wide drag a line, anything else a ring.
//
// The coordinates are pointers so a missing or null corner is a 400 rather
// than a run silently anchored at (0,0).
type adminFencePlaceRequest struct {
	AssetID string   `json:"asset_id"`
	X1      *float64 `json:"x1"`
	Y1      *float64 `json:"y1"`
	X2      *float64 `json:"x2"`
	Y2      *float64 `json:"y2"`
}

// adminFencePlaceResponse reports the run id (the value after "fence-run:" on
// every segment's tags) and the minted segment ids in row-major order. The
// segments themselves reach every client, the placing one included, through the
// ordinary object_created + village_object_tags_updated broadcasts — the editor
// renders nothing optimistically for a run.
type adminFencePlaceResponse struct {
	RunID     string   `json:"run_id"`
	ObjectIDs []string `json:"object_ids"`
}

// adminFenceBlockedResponse is the 409 body when a ring tile is not fenceable:
// the blocked tile's world-pixel top-left, so the editor can point at it.
type adminFenceBlockedResponse struct {
	Error string  `json:"error"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
}

// handleAdminFencePlace lays a fence run. 400 malformed body / missing asset_id
// or coordinate / non-finite coords / unknown asset / asset without fence piece states / run
// over the size cap; 403 not admin; 409 a tile on the run is blocked (body
// names it; nothing was placed); 422 corner outside the map; 200 ok.
func (s *Server) handleAdminFencePlace(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req adminFencePlaceRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.AssetID == "" {
		writeError(w, http.StatusBadRequest, "asset_id is required")
		return
	}
	if req.X1 == nil || req.Y1 == nil || req.X2 == nil || req.Y2 == nil {
		writeError(w, http.StatusBadRequest, "x1, y1, x2 and y2 are required")
		return
	}
	x1, y1, x2, y2 := *req.X1, *req.Y1, *req.X2, *req.Y2
	for _, c := range [][2]float64{{x1, y1}, {x2, y2}} {
		if status, msg := validateObjectPosition(c[0], c[1]); msg != "" {
			writeError(w, status, msg)
			return
		}
	}

	res, err := s.world.SendContext(r.Context(), adminCommand(user.Username, func(world *sim.World) (any, error) {
		return sim.PlaceFenceRun(sim.AssetID(req.AssetID), x1, y1, x2, y2, user.Username).Fn(world)
	}))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errAdminForbidden) {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		var blocked *sim.FenceTileBlocked
		if errors.As(err, &blocked) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(adminFenceBlockedResponse{Error: err.Error(), X: blocked.X, Y: blocked.Y})
			return
		}
		if errors.Is(err, sim.ErrUnknownAsset) || errors.Is(err, sim.ErrInvalidObjectPosition) ||
			errors.Is(err, sim.ErrFenceAssetUnsupported) || errors.Is(err, sim.ErrFenceRunTooLarge) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.PlaceFenceRunResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected fence result")
		return
	}
	ids := make([]string, len(out.Objects))
	for i, obj := range out.Objects {
		ids[i] = string(obj.ID)
	}
	writeJSON(w, adminFencePlaceResponse{RunID: out.RunID, ObjectIDs: ids})
}

// adminFenceDeleteRequest is the POST /api/village/admin/fence/delete body.
type adminFenceDeleteRequest struct {
	RunID string `json:"run_id"`
}

// adminFenceDeleteResponse lists every removed id (segments plus anything
// attached to them, children first). Each also reaches all clients as its own
// object_deleted broadcast.
type adminFenceDeleteResponse struct {
	DeletedIDs []string `json:"deleted_ids"`
}

// handleAdminFenceDelete removes every segment of a run. 400 malformed /
// missing run_id; 403 not admin; 404 no object carries the run; 422 a segment
// backs a structure (refused, nothing removed); 200 ok.
func (s *Server) handleAdminFenceDelete(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req adminFenceDeleteRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.RunID == "" {
		writeError(w, http.StatusBadRequest, "run_id is required")
		return
	}

	res, err := s.world.SendContext(r.Context(), adminCommand(user.Username, func(world *sim.World) (any, error) {
		return sim.DeleteFenceRun(req.RunID).Fn(world)
	}))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errAdminForbidden) {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		if errors.Is(err, sim.ErrFenceRunNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.DeleteFenceRunResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected delete result")
		return
	}
	ids := make([]string, len(out.DeletedIDs))
	for i, id := range out.DeletedIDs {
		ids[i] = string(id)
	}
	writeJSON(w, adminFenceDeleteResponse{DeletedIDs: ids})
}
