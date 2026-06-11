package httpapi

import (
	"net/http"
	"sort"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
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
// wire name, a human label, ordering hints, and the derived disposition
// class; raw admin-side fields (capabilities, hours_per_unit, dwell
// narration) stay engine-internal. Disposition ships DERIVED rather than
// exposing capabilities: the client needs "who picks eat-here vs
// take-home", not the capability vocabulary (ZBBS-WORK-402).
type itemKindDTO struct {
	Name         string `json:"name"`
	DisplayLabel string `json:"display_label"`
	Category     string `json:"category"`
	SortOrder    int    `json:"sort_order"`
	// Disposition: "choice" (buyer picks eat-here vs take-home) or
	// "tonight" (service kinds — no physical good, the engine forces the
	// service shape on settle; nights_stay).
	Disposition string `json:"disposition"`
}

// itemDispositionClass derives the buyer-facing disposition class for an
// item kind (ZBBS-WORK-402): "tonight" for service kinds, "choice" for
// everything else. A future "eat_here" class (stew-in-a-bowl, poured ale —
// forced immediate consumption) deliberately waits on the `portable`
// capability actually being seeded: it's a v1 token no v2 data populates
// yet (no migration sets it; the column defaults empty), so deriving
// non-portability from its absence today would misclassify every ordinary
// good as eat-here-only.
func itemDispositionClass(def *sim.ItemKindDef) string {
	if def.HasCapability("service") {
		return "tonight"
	}
	return "choice"
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
			Disposition:  itemDispositionClass(def),
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
