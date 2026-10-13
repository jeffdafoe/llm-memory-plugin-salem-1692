package httpapi

import (
	"net/http"
	"sort"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_stall.go — LLM-753. GET /api/village/pc/stall?structure_id=… lists the
// goods on a business's shelf for the player's stall window: what its keepers
// sell (sim.StallWares) and how full each shelf reads. Pure read over
// s.world.Published(), the same posture as pc/quotes.
//
// Stock goes out as a tier, never a count — the window draws one of a good, a
// few, or a pile, so the player reads the shelf the way a customer would.

// Stall stock tiers on the wire.
const (
	stallStockOne    = "one"
	stallStockFew    = "few"
	stallStockPlenty = "plenty"
)

// stallStockFewMax is the most units that still read as "a few".
const stallStockFewMax = 5

type pcStallWareDTO struct {
	Item         string `json:"item"`
	DisplayLabel string `json:"display_label"`
	Stock        string `json:"stock"`
}

type pcStallResponse struct {
	StructureID string           `json:"structure_id"`
	Wares       []pcStallWareDTO `json:"wares"`
}

// stallStockTier maps a spare count (>= 1) to its shelf tier.
func stallStockTier(spare int) string {
	switch {
	case spare <= 1:
		return stallStockOne
	case spare <= stallStockFewMax:
		return stallStockFew
	default:
		return stallStockPlenty
	}
}

func (s *Server) handlePCStall(w http.ResponseWriter, r *http.Request) {
	structureID := sim.StructureID(r.URL.Query().Get("structure_id"))
	if structureID == "" {
		writeError(w, http.StatusBadRequest, "structure_id is required")
		return
	}
	snap := s.world.Published()
	if snap.Structures[structureID] == nil {
		writeError(w, http.StatusNotFound, "structure not found")
		return
	}
	resp := pcStallResponse{StructureID: string(structureID), Wares: []pcStallWareDTO{}}
	for _, ware := range sim.StallWares(snap, structureID) {
		label := string(ware.Kind)
		if def := snap.ItemKinds[ware.Kind]; def != nil && def.DisplayLabel != "" {
			label = def.DisplayLabel
		}
		resp.Wares = append(resp.Wares, pcStallWareDTO{
			Item:         string(ware.Kind),
			DisplayLabel: label,
			Stock:        stallStockTier(ware.Spare),
		})
	}
	sort.SliceStable(resp.Wares, func(i, j int) bool {
		return resp.Wares[i].DisplayLabel < resp.Wares[j].DisplayLabel
	})
	writeJSON(w, resp)
}
