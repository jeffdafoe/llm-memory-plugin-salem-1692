package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// asset_refresh_default_handlers.go — the admin editor write for the asset-level
// refresh-default TEMPLATE (LLM-363): POST /api/village/admin/asset/set-refresh-default.
// The asset-level sibling of POST /api/village/admin/object/set-refresh — same
// {..., rows} body shape and the same adminObjectRefreshRow wire rows — but it sets
// the DEFAULT that CreateVillageObject copies onto every NEW placement of the asset,
// so a forageable drops in working instead of inert. The editor's REFRESHES panel
// offers a "Save as default for this asset" action that posts the current object's
// rows here.
//
// Gated like the geometry writes: requireAuth at registration + adminCommand on the
// world goroutine. Apply-then-persist (asset_write_handlers.go's posture): the
// in-memory SetAssetRefreshDefaults command mutates World.Assets, then the injected
// durable writer persists to asset_refresh_default (reference data, no checkpoint
// path). 503 early if the writer isn't wired so we never apply a change we can't
// persist.

// AssetRefreshDefaultWriter is the durable half of the set-refresh-default write,
// injected by cmd/engine (Server.SetAssetRefreshDefaultWriter) so httpapi doesn't
// import the pg package. nil on a mem-backed deploy → the route answers 503.
// pg.AssetsRepo satisfies it (the same concrete writer as the geometry routes).
type AssetRefreshDefaultWriter interface {
	UpdateAssetRefreshDefaults(ctx context.Context, id sim.AssetID, rows []*sim.ObjectRefresh) error
}

// adminAssetRefreshDefaultRequest is the POST body: the target asset + the full
// default set to apply. Reuses adminObjectRefreshRow (write_handlers.go) — the wire
// shape is identical to the per-object route. An empty/omitted rows clears the
// asset's defaults.
type adminAssetRefreshDefaultRequest struct {
	AssetID string                  `json:"asset_id"`
	Rows    []adminObjectRefreshRow `json:"rows"`
}

// handleAdminAssetSetRefreshDefault replaces an asset's default refresh template.
// 400 malformed / missing asset_id / invalid refresh row; 403 not admin; 404 asset
// not found; 503 durable writer unwired; 500 applied-live-but-durable-write-failed;
// 200 with the applied set. Emits no event — refresh defaults are not client-visible
// (the response is the editor's read-back).
func (s *Server) handleAdminAssetSetRefreshDefault(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	if s.assetRefreshDefaultWriter == nil {
		writeError(w, http.StatusServiceUnavailable, "asset refresh-default editing is not wired on this deploy")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req adminAssetRefreshDefaultRequest
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

	// Map the wire rows to sim.ObjectRefresh (the command deep-copies before
	// storing, so passing the request's pointers straight through is safe).
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
			GatherItem:         sim.ItemKind(row.GatherItem),
		})
	}

	res, err := s.world.SendContext(r.Context(), adminCommand(user.Username, func(world *sim.World) (any, error) {
		return sim.SetAssetRefreshDefaults(sim.AssetID(req.AssetID), rows).Fn(world)
	}))
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return
		case errors.Is(err, errAdminForbidden):
			writeError(w, http.StatusForbidden, err.Error())
		case errors.Is(err, sim.ErrAssetNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, sim.ErrInvalidRefresh):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		}
		return
	}

	out, ok := res.(sim.AssetRefreshDefaultsResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected set-refresh-default result")
		return
	}
	if err := s.assetRefreshDefaultWriter.UpdateAssetRefreshDefaults(r.Context(), out.ID, out.Rows); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		// The in-memory apply already landed; the durable write is the source of
		// truth on restart, so be explicit that live is ahead of it.
		log.Printf("asset refresh-default write: id=%s applied live but durable write failed: %v", out.ID, err)
		writeError(w, http.StatusInternalServerError, "refresh default applied live but durable write failed; reverts on restart")
		return
	}
	writeJSON(w, adminObjectRefreshResponse{ID: string(out.ID), Rows: refreshRowsToWire(out.Rows)})
}
