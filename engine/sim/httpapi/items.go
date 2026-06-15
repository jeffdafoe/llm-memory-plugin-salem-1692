package httpapi

import (
	"context"
	"errors"
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
// Reads the catalog off the published snapshot (s.world.Published().ItemKinds):
// ZBBS-WORK-412 made ItemKinds runtime-mutable (discovered kinds are minted via
// copy-on-write on the world goroutine), so a direct off-goroutine read of the
// live field would race the reassignment. The snapshot aliases a stable map.
// Discovered (unknown-category) kinds are filtered out of this player route —
// they're unsourced and unbuyable; they surface only in the admin Village
// Config catalog below (handleItemCatalog).

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
	// Disposition: "choice" (buyer picks eat-here vs take-home), "eat_here"
	// (non-portable consumable — can't leave the premises), or "tonight"
	// (service kinds — no physical good, the engine forces the service
	// shape on settle; nights_stay).
	Disposition string `json:"disposition"`
}

// itemDispositionClass derives the buyer-facing disposition class for an
// item kind: "tonight" for service kinds, "eat_here" for consumables
// without the portable capability, "choice" for everything else.
//
// The eat_here rule (ZBBS-WORK-403) leans on `portable` being genuinely
// seeded in the live item data — Jeff confirmed it was set early on
// precisely so stew can't be carried off (the WORK-402 deferral assumed
// it was unseeded because no migration populates it; the live DB was
// seeded by hand). Non-consumables (tools — no Satisfies rows) stay
// "choice": eat-here is meaningless for them and carry-home is the only
// sane outcome, which the buyer toggle covers; an unseeded consumable
// also degrades to "choice" (permissive) rather than getting wrongly
// locked.
func itemDispositionClass(def *sim.ItemKindDef) string {
	if def.HasCapability("service") {
		return "tonight"
	}
	if def.Consumable() && !def.HasCapability("portable") {
		return "eat_here"
	}
	return "choice"
}

func (s *Server) handleItems(w http.ResponseWriter, _ *http.Request) {
	// Off the published snapshot, not s.world.ItemKinds directly — see the file
	// header: ZBBS-WORK-412 made the catalog runtime-mutable, so a live
	// off-goroutine field read would race the copy-on-write mint.
	snap := s.world.Published()
	if snap == nil {
		writeJSON(w, []itemKindDTO{})
		return
	}
	items := make([]itemKindDTO, 0, len(snap.ItemKinds))
	for _, def := range snap.ItemKinds {
		if def == nil {
			continue
		}
		// Discovered (engine-minted, qty-0, price-less) kinds aren't buyable —
		// keep them out of the player Pay-modal dropdown (they show only in the
		// admin Village Config catalog).
		if def.Category == sim.ItemCategoryUnknown {
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

// ---- admin Village Config items catalog (ZBBS-WORK-412) ----
//
// The lean /api/village/items above feeds the PC Pay modal + editor dropdown.
// The Village Config panel needs MORE: per-kind satisfies/capabilities AND a
// live "in world" stock rollup (how many of each item exist across all actor
// inventories, and how many actors hold it). That rollup scans every actor's
// inventory, so it's a separate, admin-gated route rather than bloating the hot
// Pay-modal path. This revives the v1 ZBBS-114 catalog the Godot config panel
// still expects — the v1 /api/items route didn't survive the v2 cutover.

// itemSatisfactionDTO is one per-need effect of consuming a unit. The dwell
// triple uses omitempty so a non-dwell satisfaction omits those keys entirely:
// the client only renders the "(+N over M min)" tail when all three are present,
// so emitting 0s would wrongly show "(+0 over 0 min)".
type itemSatisfactionDTO struct {
	Attribute          string `json:"attribute"`
	Amount             int    `json:"amount"`
	DwellAmount        int    `json:"dwell_amount,omitempty"`
	DwellPeriodMinutes int    `json:"dwell_period_minutes,omitempty"`
	DwellTotalTicks    int    `json:"dwell_total_ticks,omitempty"`
}

// itemCatalogRowDTO is one Village Config catalog row: the full item-kind
// definition plus the live in-world stock rollup. total_in_world / held_by_actors
// drive the panel's "In World" column — a discovered qty-0 kind reads "0", and a
// discovery also carries category "unknown" with no satisfies (ZBBS-WORK-412).
type itemCatalogRowDTO struct {
	Name         string                `json:"name"`
	DisplayLabel string                `json:"display_label"`
	Category     string                `json:"category"`
	SortOrder    int                   `json:"sort_order"`
	Capabilities []string              `json:"capabilities"`
	Satisfies    []itemSatisfactionDTO `json:"satisfies"`
	TotalInWorld int                   `json:"total_in_world"`
	HeldByActors int                   `json:"held_by_actors"`
}

// buildItemCatalog assembles the admin items catalog from live world state.
// Called inside the adminCommand Fn (world goroutine), so it reads world.Actors
// + world.ItemKinds directly with no race. Inventory is aggregated in a single
// pass over actors, then joined to each kind.
func buildItemCatalog(world *sim.World) []itemCatalogRowDTO {
	totalInWorld := make(map[sim.ItemKind]int)
	heldByActors := make(map[sim.ItemKind]int)
	for _, a := range world.Actors {
		for kind, qty := range a.Inventory {
			if qty <= 0 {
				continue
			}
			totalInWorld[kind] += qty
			heldByActors[kind]++
		}
	}

	rows := make([]itemCatalogRowDTO, 0, len(world.ItemKinds))
	for _, def := range world.ItemKinds {
		if def == nil {
			continue
		}
		sats := make([]itemSatisfactionDTO, 0, len(def.Satisfies))
		for _, sat := range def.Satisfies {
			sats = append(sats, itemSatisfactionDTO{
				Attribute:          string(sat.Attribute),
				Amount:             sat.Immediate,
				DwellAmount:        sat.DwellAmount,
				DwellPeriodMinutes: sat.DwellPeriodMinutes,
				DwellTotalTicks:    sat.DwellTotalTicks,
			})
		}
		// Emit [] rather than null for an empty capability set so the client's
		// Array typecheck is happy without a null guard.
		caps := def.Capabilities
		if caps == nil {
			caps = []string{}
		}
		rows = append(rows, itemCatalogRowDTO{
			Name:         string(def.Name),
			DisplayLabel: def.DisplayLabel,
			Category:     string(def.Category),
			SortOrder:    def.SortOrder,
			Capabilities: caps,
			Satisfies:    sats,
			TotalInWorld: totalInWorld[def.Name],
			HeldByActors: heldByActors[def.Name],
		})
	}
	// sort_order then name — deterministic wire order, mirrors handleItems.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].SortOrder != rows[j].SortOrder {
			return rows[i].SortOrder < rows[j].SortOrder
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}

// handleItemCatalog serves the admin Village Config items catalog (rich rows +
// in-world stock). Admin-gated through the command channel exactly like
// handleConfig: a non-admin (or no matching actor) gets errAdminForbidden → 403.
func (s *Server) handleItemCatalog(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	res, err := s.world.SendContext(r.Context(), adminCommand(user.Username, func(world *sim.World) (any, error) {
		return buildItemCatalog(world), nil
	}))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errAdminForbidden) {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to read item catalog")
		return
	}
	rows, ok := res.([]itemCatalogRowDTO)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected catalog result")
		return
	}
	writeJSON(w, rows)
}
