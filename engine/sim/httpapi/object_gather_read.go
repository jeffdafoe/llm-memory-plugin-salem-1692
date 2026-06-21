package httpapi

import (
	"net/http"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// object_gather_read.go — LLM-52. The read the client hits ON HOVER to show a
// gatherable source's live count in the object tooltip ("7 berries" / "picked
// clean"). Deliberately pull-on-hover rather than pushing AvailableQuantity into
// ObjectDTO/WS: a finite source's count changes on every gather AND every regen
// tick, so streaming it would be chatty for a number only needed while the
// cursor sits on the bush.

// objectGatherResponse reports the gatherable state of one placed object.
//   - non-gatherable object: only Gatherable (false).
//   - finite gatherable (a berry bush): Available/Max carry current/cap stock.
//   - infinite gatherable (a well): Available/Max nil — no count to show.
type objectGatherResponse struct {
	Gatherable bool   `json:"gatherable"`
	Item       string `json:"item,omitempty"`
	Available  *int   `json:"available,omitempty"`
	Max        *int   `json:"max,omitempty"`
}

// handleObjectGather answers GET /api/village/object/gather?id=<objectID>. Reads
// the published snapshot (no command-channel round trip) and returns the first
// gatherable refresh row's stock — resolve-then-check mirrors the gather command
// (the first IsGatherable row owns the source). IsFinite guarantees the
// Available/Max derefs are safe.
func (s *Server) handleObjectGather(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}
	snap := s.world.Published()
	obj := snap.VillageObjects[sim.VillageObjectID(id)]
	if obj == nil {
		writeError(w, http.StatusNotFound, "object not found")
		return
	}
	for _, row := range obj.Refreshes {
		if row == nil || !row.IsGatherable() {
			continue
		}
		resp := objectGatherResponse{Gatherable: true, Item: strings.TrimSpace(string(row.GatherItem))}
		if row.IsFinite() {
			avail := *row.AvailableQuantity
			max := *row.MaxQuantity
			resp.Available = &avail
			resp.Max = &max
		}
		writeJSON(w, resp)
		return
	}
	writeJSON(w, objectGatherResponse{Gatherable: false})
}
