package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// asset_write_handlers.go — the asset-geometry editor write routes (LLM-263):
// PATCH /api/assets/{id}/door | /footprint | /stand. The Godot editor's
// draggable door / footprint / stand markers PATCH these on release
// (client/scripts/editor.gd); before this the v2 engine registered no handler,
// so the pins never persisted (the editor drew them optimistically off its local
// catalog and looked like it worked).
//
// These are player-ADMIN editor writes (not operator/umbilical): gated by
// requireAuth (valid salem session) at registration, then adminCommand (the
// caller's in-world actor must be IsAdmin, checked on the world goroutine). The
// asset id is a URL path segment; the geometry values are in the body.
//
// Flow per route (apply-then-persist, the editor-write family's posture — npc /
// object edits also broadcast before their persistence lands, just via the
// deferred checkpoint rather than a direct write):
//  1. 503 early if the durable writer isn't wired (mem-backed deploy) — so we
//     never broadcast a change we can't persist.
//  2. adminCommand → SetAsset* : gate on admin, validate, mutate World.Assets,
//     emit the Asset*Changed event the hub broadcasts as the asset_* WS frame.
//  3. durable UPDATE asset via the injected writer — assets are reference data
//     with no checkpoint path, so this direct write is the edit's durable half
//     (the source of truth the catalog rebuilds from on restart).
//
// A durable-write failure after the in-memory apply is a 500 that says so: the
// live catalog + connected editors already reflect the change, but it reverts on
// the next engine restart. Loud, rare (a pg outage mid-drag), recoverable by
// re-dragging.

// AssetGeometryWriter is the durable half of the asset-geometry writes, injected
// by cmd/engine (Server.SetAssetGeometryWriter) so httpapi doesn't import the pg
// package. nil on a mem-backed deploy → the routes answer 503. pg.AssetsRepo
// satisfies it.
type AssetGeometryWriter interface {
	UpdateAssetDoorOffset(ctx context.Context, id sim.AssetID, x, y *int) error
	UpdateAssetFootprint(ctx context.Context, id sim.AssetID, left, right, top, bottom int) error
	UpdateAssetStandOffset(ctx context.Context, id sim.AssetID, x, y *int) error
}

// assetOffsetRequest is the PATCH /door and /stand body: a tile offset pair from
// the asset anchor. Pointers so an explicit JSON null clears that side; a MISSING
// field is a 400, not a silent clear (decodeAssetOffset enforces presence) — the
// door/stand columns are nullable, so an absent key must never be read as "clear
// the door". The command rejects a half-set pair (exactly one null) as 400.
type assetOffsetRequest struct {
	X *int `json:"x"`
	Y *int `json:"y"`
}

// assetFootprintRequest is the PATCH /footprint body: the four per-side tile
// counts. Pointers so a MISSING (or typo'd) field is rejected rather than
// silently persisting a zero for that side — the handler requires all four
// present. The command rejects a negative side as 400.
type assetFootprintRequest struct {
	Left   *int `json:"left"`
	Right  *int `json:"right"`
	Top    *int `json:"top"`
	Bottom *int `json:"bottom"`
}

// assetDoorResponse / assetFootprintResponse / assetStandResponse echo the
// applied values. The Godot editor only checks the HTTP status (it already
// applied optimistically and learns the authoritative value from the WS frame),
// but the echo keeps the route testable and useful to other callers.
type assetDoorResponse struct {
	AssetID string `json:"asset_id"`
	X       *int   `json:"x"`
	Y       *int   `json:"y"`
}

type assetFootprintResponse struct {
	AssetID string `json:"asset_id"`
	Left    int    `json:"left"`
	Right   int    `json:"right"`
	Top     int    `json:"top"`
	Bottom  int    `json:"bottom"`
}

type assetStandResponse struct {
	AssetID string `json:"asset_id"`
	X       *int   `json:"x"`
	Y       *int   `json:"y"`
}

// writeAssetWriteError maps a SetAsset* command error to its HTTP status. The
// input sentinels are disjoint across the three commands, so one mapper serves
// all of them.
func writeAssetWriteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	case errors.Is(err, errAdminForbidden):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, sim.ErrAssetNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, sim.ErrInvalidDoorOffset),
		errors.Is(err, sim.ErrInvalidStandOffset),
		errors.Is(err, sim.ErrInvalidFootprint):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	}
}

// assetWriteAuth is the shared front half of every asset-geometry handler:
// require a valid session, require the durable writer be wired (503 before any
// mutation on a mem-backed deploy), and extract the asset id from the path.
// Returns the caller's username (for the adminCommand gate) and ok=false (after
// writing the error) on any failure. Body decoding is per-route (each shape has
// its own presence checks), so it is NOT done here.
func (s *Server) assetWriteAuth(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return "", "", false
	}
	if s.assetGeometryWriter == nil {
		writeError(w, http.StatusServiceUnavailable, "asset geometry editing is not wired on this deploy")
		return "", "", false
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "asset id is required")
		return "", "", false
	}
	return user.Username, id, true
}

// decodeAssetOffset decodes a door/stand offset body, REQUIRING both x and y to
// be present. A missing key is a 400 (not a silent clear): the columns are
// nullable, so an absent field must not be read as "clear this offset". An
// explicit JSON null is allowed and clears that side (the command then rejects a
// half-set pair). Presence is checked via a raw-message pre-decode because a
// plain struct decode can't tell an absent key from a null one.
func decodeAssetOffset(w http.ResponseWriter, r *http.Request, dst *assetOffsetRequest) bool {
	var raw map[string]json.RawMessage
	if !decodeAdminBody(w, r, &raw) {
		return false
	}
	xr, hasX := raw["x"]
	yr, hasY := raw["y"]
	if !hasX || !hasY {
		writeError(w, http.StatusBadRequest, "x and y are required")
		return false
	}
	if err := json.Unmarshal(xr, &dst.X); err != nil {
		writeError(w, http.StatusBadRequest, "invalid x")
		return false
	}
	if err := json.Unmarshal(yr, &dst.Y); err != nil {
		writeError(w, http.StatusBadRequest, "invalid y")
		return false
	}
	return true
}

func (s *Server) handleAssetSetDoor(w http.ResponseWriter, r *http.Request) {
	username, id, ok := s.assetWriteAuth(w, r)
	if !ok {
		return
	}
	var req assetOffsetRequest
	if !decodeAssetOffset(w, r, &req) {
		return
	}
	res, err := s.world.SendContext(r.Context(), adminCommand(username, func(world *sim.World) (any, error) {
		return sim.SetAssetDoorOffset(sim.AssetID(id), req.X, req.Y).Fn(world)
	}))
	if err != nil {
		writeAssetWriteError(w, err)
		return
	}
	out, ok := res.(sim.AssetDoorOffsetResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-door result")
		return
	}
	if err := s.assetGeometryWriter.UpdateAssetDoorOffset(r.Context(), out.ID, out.X, out.Y); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		// The in-memory apply + WS broadcast already landed; the durable write is
		// the source of truth on restart, so be explicit that live is ahead of it.
		log.Printf("asset door write: id=%s applied live but durable write failed: %v", out.ID, err)
		writeError(w, http.StatusInternalServerError, "door applied live but durable write failed; reverts on restart")
		return
	}
	writeJSON(w, assetDoorResponse{AssetID: string(out.ID), X: out.X, Y: out.Y})
}

func (s *Server) handleAssetSetFootprint(w http.ResponseWriter, r *http.Request) {
	username, id, ok := s.assetWriteAuth(w, r)
	if !ok {
		return
	}
	var req assetFootprintRequest
	if !decodeAdminBody(w, r, &req) {
		return
	}
	// Require all four sides — a missing (or typo'd) field must not silently
	// persist a zero for that side.
	if req.Left == nil || req.Right == nil || req.Top == nil || req.Bottom == nil {
		writeError(w, http.StatusBadRequest, "left, right, top, and bottom are required")
		return
	}
	res, err := s.world.SendContext(r.Context(), adminCommand(username, func(world *sim.World) (any, error) {
		return sim.SetAssetFootprint(sim.AssetID(id), *req.Left, *req.Right, *req.Top, *req.Bottom).Fn(world)
	}))
	if err != nil {
		writeAssetWriteError(w, err)
		return
	}
	out, ok := res.(sim.AssetFootprintResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-footprint result")
		return
	}
	if err := s.assetGeometryWriter.UpdateAssetFootprint(r.Context(), out.ID, out.Left, out.Right, out.Top, out.Bottom); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		log.Printf("asset footprint write: id=%s applied live but durable write failed: %v", out.ID, err)
		writeError(w, http.StatusInternalServerError, "footprint applied live but durable write failed; reverts on restart")
		return
	}
	writeJSON(w, assetFootprintResponse{
		AssetID: string(out.ID),
		Left:    out.Left,
		Right:   out.Right,
		Top:     out.Top,
		Bottom:  out.Bottom,
	})
}

func (s *Server) handleAssetSetStand(w http.ResponseWriter, r *http.Request) {
	username, id, ok := s.assetWriteAuth(w, r)
	if !ok {
		return
	}
	var req assetOffsetRequest
	if !decodeAssetOffset(w, r, &req) {
		return
	}
	res, err := s.world.SendContext(r.Context(), adminCommand(username, func(world *sim.World) (any, error) {
		return sim.SetAssetStandOffset(sim.AssetID(id), req.X, req.Y).Fn(world)
	}))
	if err != nil {
		writeAssetWriteError(w, err)
		return
	}
	out, ok := res.(sim.AssetStandOffsetResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-stand result")
		return
	}
	if err := s.assetGeometryWriter.UpdateAssetStandOffset(r.Context(), out.ID, out.X, out.Y); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		log.Printf("asset stand write: id=%s applied live but durable write failed: %v", out.ID, err)
		writeError(w, http.StatusInternalServerError, "stand applied live but durable write failed; reverts on restart")
		return
	}
	writeJSON(w, assetStandResponse{AssetID: string(out.ID), X: out.X, Y: out.Y})
}
