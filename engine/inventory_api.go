package main

// HTTP handlers for the editor's Inventory panel (ZBBS-091).
//
//   GET /api/items
//       Lookup table → editor's item picker. Read by any authed user.
//
//   GET /api/village/npcs/{id}/inventory
//       Returns the row set for one NPC's inventory.
//
//   PUT /api/village/npcs/{id}/inventory
//       Replaces the full row set in one tx (delete-all + insert-all).
//       Admin only. Same whole-set replace pattern as object_refresh.

import (
	"encoding/json"
	"net/http"
)

// itemSatisfiesEntry is the wire shape for one (attribute, amount)
// effect on an item — mirrors the item_satisfies table (ZBBS-125).
// An item with no entries (a material) reports an empty array.
//
// Dwell fields are populated for items that deliver satiation in two
// stages: an immediate Amount on consumption, then DwellAmount per
// DwellPeriodMinutes for DwellTotalTicks ticks. All three are NULL
// in the DB for non-dwell items; we surface them as omitempty pointers
// so the JSON stays clean for the common case.
type itemSatisfiesEntry struct {
	Attribute          string `json:"attribute"`
	Amount             int    `json:"amount"`
	DwellAmount        *int   `json:"dwell_amount,omitempty"`
	DwellPeriodMinutes *int   `json:"dwell_period_minutes,omitempty"`
	DwellTotalTicks    *int   `json:"dwell_total_ticks,omitempty"`
}

// itemKindRow mirrors a row of item_kind for the picker / catalog.
// No price column post-ZBBS-092 — prices are negotiated in dialogue.
// Capabilities surfaced (ZBBS-114) for the config panel's read-only
// items view: shows which items are portable, etc., without a second
// round-trip. Satisfies array (ZBBS-125) replaces the legacy single
// satisfies_attribute / satisfies_amount columns; multi-effect items
// list every effect.
//
// TotalInWorld + HeldByActors (ZBBS-HOME-203) are aggregated from
// actor_inventory so the config panel can show how many of each item
// currently exist in the village without a separate query. Items with
// no inventory rows report 0 / 0 (LEFT JOIN, not INNER).
type itemKindRow struct {
	Name         string               `json:"name"`
	DisplayLabel string               `json:"display_label"`
	Category     string               `json:"category"`
	Satisfies    []itemSatisfiesEntry `json:"satisfies"`
	SortOrder    int                  `json:"sort_order"`
	Capabilities []string             `json:"capabilities"`
	TotalInWorld int                  `json:"total_in_world"`
	HeldByActors int                  `json:"held_by_actors"`
}

func (app *App) handleListItems(w http.ResponseWriter, r *http.Request) {
	rows, err := app.DB.Query(r.Context(),
		`SELECT name, display_label, category, sort_order, capabilities
		   FROM item_kind
		  ORDER BY sort_order, name`,
	)
	if err != nil {
		jsonError(w, "Failed to load items", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []itemKindRow{}
	for rows.Next() {
		var rec itemKindRow
		if err := rows.Scan(&rec.Name, &rec.DisplayLabel, &rec.Category,
			&rec.SortOrder, &rec.Capabilities); err != nil {
			jsonError(w, "Failed to scan item", http.StatusInternalServerError)
			return
		}
		if rec.Capabilities == nil {
			rec.Capabilities = []string{}
		}
		rec.Satisfies = []itemSatisfiesEntry{}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		jsonError(w, "Failed to iterate items", http.StatusInternalServerError)
		return
	}

	// Second pass: load all item_satisfies rows in one query and
	// attach to the matching catalog entry. Avoids N+1 by indexing
	// the slice by name. Empty result = nothing to attach (every
	// item already has Satisfies = []), so the call still serializes
	// cleanly as `"satisfies": []`.
	if len(out) > 0 {
		index := make(map[string]int, len(out))
		for i, rec := range out {
			index[rec.Name] = i
		}
		sRows, err := app.DB.Query(r.Context(),
			`SELECT item_kind, attribute, amount,
			        dwell_amount, dwell_period_minutes, dwell_total_ticks
			   FROM item_satisfies
			  ORDER BY item_kind, amount DESC, attribute`,
		)
		if err != nil {
			jsonError(w, "Failed to load item satisfies", http.StatusInternalServerError)
			return
		}
		defer sRows.Close()
		for sRows.Next() {
			var name, attr string
			var amount int
			var dwellAmount, dwellPeriod, dwellTicks *int
			if err := sRows.Scan(&name, &attr, &amount,
				&dwellAmount, &dwellPeriod, &dwellTicks); err != nil {
				jsonError(w, "Failed to scan item satisfies", http.StatusInternalServerError)
				return
			}
			if i, ok := index[name]; ok {
				out[i].Satisfies = append(out[i].Satisfies, itemSatisfiesEntry{
					Attribute:          attr,
					Amount:             amount,
					DwellAmount:        dwellAmount,
					DwellPeriodMinutes: dwellPeriod,
					DwellTotalTicks:    dwellTicks,
				})
			}
		}
		if err := sRows.Err(); err != nil {
			jsonError(w, "Failed to iterate item satisfies", http.StatusInternalServerError)
			return
		}

		// Third pass: aggregate actor_inventory by item_kind so the
		// config panel can render "how many bread loaves exist in the
		// village" without a separate round-trip. LEFT JOIN so items
		// held by no one (e.g. iron, currently uncrafted) report as 0
		// rather than disappearing from the rollup.
		invRows, err := app.DB.Query(r.Context(),
			`SELECT ik.name,
			        COALESCE(SUM(ai.quantity), 0)::int AS total,
			        COUNT(DISTINCT ai.actor_id)::int AS holders
			   FROM item_kind ik
			   LEFT JOIN actor_inventory ai ON ai.item_kind = ik.name
			  GROUP BY ik.name`,
		)
		if err != nil {
			jsonError(w, "Failed to load item stock", http.StatusInternalServerError)
			return
		}
		defer invRows.Close()
		for invRows.Next() {
			var name string
			var total, holders int
			if err := invRows.Scan(&name, &total, &holders); err != nil {
				jsonError(w, "Failed to scan item stock", http.StatusInternalServerError)
				return
			}
			if i, ok := index[name]; ok {
				out[i].TotalInWorld = total
				out[i].HeldByActors = holders
			}
		}
		if err := invRows.Err(); err != nil {
			jsonError(w, "Failed to iterate item stock", http.StatusInternalServerError)
			return
		}
	}

	jsonResponse(w, http.StatusOK, out)
}

// inventoryRow is the wire shape for one inventory entry.
type inventoryRow struct {
	ItemKind string `json:"item_kind"`
	Quantity int    `json:"quantity"`
}

func (app *App) handleGetActorInventory(w http.ResponseWriter, r *http.Request) {
	actorID := r.PathValue("id")
	if actorID == "" {
		jsonError(w, "Missing actor id", http.StatusBadRequest)
		return
	}

	rows, err := app.DB.Query(r.Context(),
		`SELECT ai.item_kind, ai.quantity
		   FROM actor_inventory ai
		   JOIN item_kind k ON k.name = ai.item_kind
		  WHERE ai.actor_id = $1
		  ORDER BY k.sort_order, k.name`,
		actorID,
	)
	if err != nil {
		jsonError(w, "Failed to load inventory", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []inventoryRow{}
	for rows.Next() {
		var rec inventoryRow
		if err := rows.Scan(&rec.ItemKind, &rec.Quantity); err != nil {
			jsonError(w, "Failed to scan inventory row", http.StatusInternalServerError)
			return
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		jsonError(w, "Failed to iterate inventory rows", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, out)
}

// handlePutActorInventory replaces the entire inventory for one actor
// atomically. Admin only. Same whole-set pattern as object_refresh:
// delete + insert in one tx, validates each row against item_kind.
//
// Quantity = 0 rows are dropped silently (the operator clearing a slot
// is the same as "no row" — keeps perception clean).
func (app *App) handlePutActorInventory(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin access required", http.StatusForbidden)
		return
	}

	actorID := r.PathValue("id")
	if actorID == "" {
		jsonError(w, "Missing actor id", http.StatusBadRequest)
		return
	}

	var req struct {
		Rows []inventoryRow `json:"rows"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Per-row validation. Mirror DB CHECKs so the editor gets clean 400s
	// instead of generic 500s. Drop quantity=0 rows now so they don't
	// trip the CHECK (quantity > 0).
	seen := make(map[string]bool, len(req.Rows))
	cleaned := make([]inventoryRow, 0, len(req.Rows))
	for _, row := range req.Rows {
		if row.ItemKind == "" {
			jsonError(w, "Missing item_kind on a row", http.StatusBadRequest)
			return
		}
		if seen[row.ItemKind] {
			jsonError(w, "Duplicate item_kind on rows: "+row.ItemKind, http.StatusBadRequest)
			return
		}
		seen[row.ItemKind] = true
		if row.Quantity < 0 {
			jsonError(w, "Quantity cannot be negative ("+row.ItemKind+")", http.StatusBadRequest)
			return
		}
		if row.Quantity == 0 {
			// Operator cleared the slot. Same effect as no row.
			continue
		}
		cleaned = append(cleaned, row)
	}

	tx, err := app.DB.Begin(r.Context())
	if err != nil {
		jsonError(w, "Failed to begin tx", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	// Actor existence guard — clean 404 instead of FK violation.
	var exists bool
	if err := tx.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM actor WHERE id = $1)`,
		actorID,
	).Scan(&exists); err != nil || !exists {
		jsonError(w, "Actor not found", http.StatusNotFound)
		return
	}

	// Validate every incoming item_kind against the lookup table inside
	// the tx — same pattern as object_refresh PUT.
	if len(cleaned) > 0 {
		incoming := make([]string, 0, len(cleaned))
		for _, row := range cleaned {
			incoming = append(incoming, row.ItemKind)
		}
		known := map[string]bool{}
		itemRows, err := tx.Query(r.Context(),
			`SELECT name FROM item_kind WHERE name = ANY($1)`,
			incoming,
		)
		if err != nil {
			jsonError(w, "Failed to validate item kinds", http.StatusInternalServerError)
			return
		}
		for itemRows.Next() {
			var name string
			if err := itemRows.Scan(&name); err != nil {
				itemRows.Close()
				jsonError(w, "Failed to scan item kind", http.StatusInternalServerError)
				return
			}
			known[name] = true
		}
		itemRows.Close()
		for _, row := range cleaned {
			if !known[row.ItemKind] {
				jsonError(w, "Unknown item: "+row.ItemKind, http.StatusBadRequest)
				return
			}
		}
	}

	if _, err := tx.Exec(r.Context(),
		`DELETE FROM actor_inventory WHERE actor_id = $1`,
		actorID,
	); err != nil {
		jsonError(w, "Failed to delete existing inventory", http.StatusInternalServerError)
		return
	}

	for _, row := range cleaned {
		if _, err := tx.Exec(r.Context(),
			`INSERT INTO actor_inventory (actor_id, item_kind, quantity)
			 VALUES ($1, $2, $3)`,
			actorID, row.ItemKind, row.Quantity,
		); err != nil {
			jsonError(w, "Failed to insert inventory row", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		jsonError(w, "Failed to commit inventory replace", http.StatusInternalServerError)
		return
	}

	app.Hub.Broadcast(WorldEvent{
		Type: "actor_inventory_replaced",
		Data: map[string]any{
			"actor_id": actorID,
		},
	})

	w.WriteHeader(http.StatusNoContent)
}
