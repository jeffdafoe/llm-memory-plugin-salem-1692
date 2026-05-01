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

// itemKindRow mirrors a row of item_kind for the picker / catalog.
// No price column post-ZBBS-092 — prices are negotiated in dialogue.
type itemKindRow struct {
	Name               string  `json:"name"`
	DisplayLabel       string  `json:"display_label"`
	Category           string  `json:"category"`
	SatisfiesAttribute *string `json:"satisfies_attribute"`
	SatisfiesAmount    *int    `json:"satisfies_amount"`
	SortOrder          int     `json:"sort_order"`
}

func (app *App) handleListItems(w http.ResponseWriter, r *http.Request) {
	rows, err := app.DB.Query(r.Context(),
		`SELECT name, display_label, category,
		        satisfies_attribute, satisfies_amount, sort_order
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
			&rec.SatisfiesAttribute, &rec.SatisfiesAmount, &rec.SortOrder); err != nil {
			jsonError(w, "Failed to scan item", http.StatusInternalServerError)
			return
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		jsonError(w, "Failed to iterate items", http.StatusInternalServerError)
		return
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
