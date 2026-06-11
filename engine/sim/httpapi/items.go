package httpapi

import (
	"net/http"
	"sort"
)

// items.go — ZBBS-HOME-423. GET /api/village/items serves the item-kind
// catalog so the play client can compose a pay offer for ANY good, not just
// ones a vendor has formally quoted. The Pay modal's item dropdown was
// mentions-only (scene-quote-derived broadcasts), which made a verbally
// offered good — "thou shalt have a room here for 4 coins" with no
// scene_quote behind it — impossible for a PC to buy: the server's slow path
// accepts any item offer, but the client had no way to express one.
//
// Reads world.ItemKinds directly: reference state loaded once at startup and
// never written by the engine loop (same lock-free posture as handleTerrain;
// pc_me already reads it in-handler for inventory labels).

// itemKindDTO is one catalog row. Lean on purpose — the Pay modal needs the
// wire name, a human label, and ordering hints; admin-side fields
// (capabilities, hours_per_unit, dwell narration) stay engine-internal.
type itemKindDTO struct {
	Name         string `json:"name"`
	DisplayLabel string `json:"display_label"`
	Category     string `json:"category"`
	SortOrder    int    `json:"sort_order"`
}

func (s *Server) handleItems(w http.ResponseWriter, _ *http.Request) {
	items := make([]itemKindDTO, 0, len(s.world.ItemKinds))
	for _, def := range s.world.ItemKinds {
		if def == nil {
			continue
		}
		items = append(items, itemKindDTO{
			Name:         string(def.Name),
			DisplayLabel: def.DisplayLabel,
			Category:     string(def.Category),
			SortOrder:    def.SortOrder,
		})
	}
	// sort_order then name — deterministic wire order, ready for the
	// dropdown without client-side sorting.
	sort.Slice(items, func(i, j int) bool {
		if items[i].SortOrder != items[j].SortOrder {
			return items[i].SortOrder < items[j].SortOrder
		}
		return items[i].Name < items[j].Name
	})
	writeJSON(w, items)
}
