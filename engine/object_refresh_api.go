package main

// HTTP handlers for the editor's Refreshes panel (ZBBS-090).
//
// Three endpoints:
//
//   GET /api/refresh-attributes
//       Lookup table → editor's attribute dropdown. Read by any authed user.
//
//   GET /api/village/objects/{id}/refresh
//       Returns the row set for one placed object. Read by any authed user.
//
//   PUT /api/village/objects/{id}/refresh
//       Replaces the full row set in one tx (delete-all + insert-all). Admin
//       only. The whole-set replace is intentional: the editor's panel always
//       shows every row at once, so the simplest contract is "save everything
//       I'm showing you." last_refresh_at is preserved per-(object,attribute)
//       across replaces — see commentary on handlePutObjectRefresh below.
//
// Wire convention: the editor sends `amount` as a positive "amount restored
// per use" because that's the operator's mental model. Storage stays signed
// (CHECK amount < 0 from ZBBS-085) so the engine's arrival-side delta math
// stays unchanged. Negation happens at this API boundary in both directions.

import (
	"encoding/json"
	"net/http"
	"time"
)

// refreshAttributeRow mirrors one row of refresh_attribute for the picker.
type refreshAttributeRow struct {
	Name         string `json:"name"`
	DisplayLabel string `json:"display_label"`
	SortOrder    int    `json:"sort_order"`
}

func (app *App) handleListRefreshAttributes(w http.ResponseWriter, r *http.Request) {
	rows, err := app.DB.Query(r.Context(),
		`SELECT name, display_label, sort_order
		   FROM refresh_attribute
		  ORDER BY sort_order, name`,
	)
	if err != nil {
		jsonError(w, "Failed to load refresh attributes", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []refreshAttributeRow{}
	for rows.Next() {
		var rec refreshAttributeRow
		if err := rows.Scan(&rec.Name, &rec.DisplayLabel, &rec.SortOrder); err != nil {
			jsonError(w, "Failed to scan refresh attribute", http.StatusInternalServerError)
			return
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		jsonError(w, "Failed to iterate refresh attributes", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, out)
}

// objectRefreshRow is the wire shape for one refresh row.
type objectRefreshRow struct {
	Attribute          string `json:"attribute"`
	Amount             int    `json:"amount"`               // positive on the wire; negated at the DB boundary
	AvailableQuantity  *int   `json:"available_quantity"`   // null = infinite
	MaxQuantity        *int   `json:"max_quantity"`         // null when available is null
	RefreshMode        string `json:"refresh_mode"`         // 'continuous' | 'periodic'
	RefreshPeriodHours *int   `json:"refresh_period_hours"` // null = no auto-regen
}

func (app *App) handleGetObjectRefresh(w http.ResponseWriter, r *http.Request) {
	objectID := r.PathValue("id")
	if objectID == "" {
		jsonError(w, "Missing object id", http.StatusBadRequest)
		return
	}

	rows, err := app.DB.Query(r.Context(),
		`SELECT attribute, amount, available_quantity, max_quantity,
		        refresh_mode, refresh_period_hours
		   FROM object_refresh
		  WHERE object_id = $1
		  ORDER BY attribute`,
		objectID,
	)
	if err != nil {
		jsonError(w, "Failed to load refresh rows", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []objectRefreshRow{}
	for rows.Next() {
		var (
			row       objectRefreshRow
			storedAmt int
			avail     *int
			maxQ      *int
			period    *int
		)
		if err := rows.Scan(&row.Attribute, &storedAmt, &avail, &maxQ, &row.RefreshMode, &period); err != nil {
			jsonError(w, "Failed to scan refresh row", http.StatusInternalServerError)
			return
		}
		row.Amount = -storedAmt
		row.AvailableQuantity = avail
		row.MaxQuantity = maxQ
		row.RefreshPeriodHours = period
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		jsonError(w, "Failed to iterate refresh rows", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, out)
}

// handlePutObjectRefresh replaces the entire refresh row set for one object
// atomically. Admin only.
//
// last_refresh_at handling across the replace:
//
//   - Existing row whose refresh_mode + refresh_period_hours are unchanged:
//     last_refresh_at is preserved. Operator tweaked amount or max — the
//     regen schedule shouldn't restart.
//   - Existing row whose mode or period changed: last_refresh_at cleared so
//     the next regen tick re-anchors. Avoids confusing mid-period state when
//     the period was just shortened or the mode flipped.
//   - New row: inserts with last_refresh_at = NULL; the regen tick stamps it.
//
// available_quantity follows the editor verbatim — if the operator set it
// to 5 and the engine had ticked it down to 2, the edit lifts it to 5. The
// editor is the operator's override channel; partial-state surprise during
// editing would be worse than this explicit reset.
func (app *App) handlePutObjectRefresh(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin access required", http.StatusForbidden)
		return
	}

	objectID := r.PathValue("id")
	if objectID == "" {
		jsonError(w, "Missing object id", http.StatusBadRequest)
		return
	}

	var req struct {
		Rows []objectRefreshRow `json:"rows"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Per-row validation. Mirrors the DB CHECK constraints so the editor
	// gets clean 400s instead of generic 500s from PG. Attribute name is
	// re-validated against the lookup table inside the tx.
	seen := make(map[string]bool, len(req.Rows))
	for _, row := range req.Rows {
		if row.Attribute == "" {
			jsonError(w, "Missing attribute on a row", http.StatusBadRequest)
			return
		}
		if seen[row.Attribute] {
			jsonError(w, "Duplicate attribute on rows: "+row.Attribute, http.StatusBadRequest)
			return
		}
		seen[row.Attribute] = true
		if row.Amount <= 0 {
			jsonError(w, "Amount must be positive (restored per use)", http.StatusBadRequest)
			return
		}
		switch row.RefreshMode {
		case "continuous", "periodic":
		default:
			jsonError(w, "refresh_mode must be 'continuous' or 'periodic'", http.StatusBadRequest)
			return
		}
		if (row.AvailableQuantity == nil) != (row.MaxQuantity == nil) {
			jsonError(w, "available_quantity and max_quantity must both be set or both null", http.StatusBadRequest)
			return
		}
		if row.AvailableQuantity != nil {
			if *row.AvailableQuantity < 0 {
				jsonError(w, "available_quantity must be >= 0", http.StatusBadRequest)
				return
			}
			if *row.MaxQuantity <= 0 {
				jsonError(w, "max_quantity must be > 0", http.StatusBadRequest)
				return
			}
			if *row.AvailableQuantity > *row.MaxQuantity {
				jsonError(w, "available_quantity cannot exceed max_quantity", http.StatusBadRequest)
				return
			}
		}
		if row.RefreshPeriodHours != nil && *row.RefreshPeriodHours <= 0 {
			jsonError(w, "refresh_period_hours must be > 0", http.StatusBadRequest)
			return
		}
	}

	tx, err := app.DB.Begin(r.Context())
	if err != nil {
		jsonError(w, "Failed to begin tx", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	// Object existence guard — clean 404 instead of generic FK violation.
	var exists bool
	if err := tx.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM village_object WHERE id = $1)`,
		objectID,
	).Scan(&exists); err != nil || !exists {
		jsonError(w, "Object not found", http.StatusNotFound)
		return
	}

	// Validate every incoming attribute against the lookup table inside the
	// tx so a concurrent attribute delete (vanishingly rare but possible)
	// can't slip through between validation and INSERT.
	if len(req.Rows) > 0 {
		incoming := make([]string, 0, len(req.Rows))
		for _, row := range req.Rows {
			incoming = append(incoming, row.Attribute)
		}
		known := map[string]bool{}
		attrRows, err := tx.Query(r.Context(),
			`SELECT name FROM refresh_attribute WHERE name = ANY($1)`,
			incoming,
		)
		if err != nil {
			jsonError(w, "Failed to validate attributes", http.StatusInternalServerError)
			return
		}
		for attrRows.Next() {
			var name string
			if err := attrRows.Scan(&name); err != nil {
				attrRows.Close()
				jsonError(w, "Failed to scan attribute name", http.StatusInternalServerError)
				return
			}
			known[name] = true
		}
		attrRows.Close()
		for _, row := range req.Rows {
			if !known[row.Attribute] {
				jsonError(w, "Unknown attribute: "+row.Attribute, http.StatusBadRequest)
				return
			}
		}
	}

	// Pull existing rows so we can preserve last_refresh_at where the regen
	// schedule didn't change.
	type existingRow struct {
		mode          string
		periodHours   *int
		lastRefreshAt *time.Time
	}
	existingRows := make(map[string]existingRow)
	exRows, err := tx.Query(r.Context(),
		`SELECT attribute, refresh_mode, refresh_period_hours, last_refresh_at
		   FROM object_refresh
		  WHERE object_id = $1`,
		objectID,
	)
	if err != nil {
		jsonError(w, "Failed to load existing refresh rows", http.StatusInternalServerError)
		return
	}
	for exRows.Next() {
		var (
			attr   string
			rec    existingRow
			period *int
			lastAt *time.Time
		)
		if err := exRows.Scan(&attr, &rec.mode, &period, &lastAt); err != nil {
			exRows.Close()
			jsonError(w, "Failed to scan existing refresh row", http.StatusInternalServerError)
			return
		}
		rec.periodHours = period
		rec.lastRefreshAt = lastAt
		existingRows[attr] = rec
	}
	exRows.Close()
	if err := exRows.Err(); err != nil {
		jsonError(w, "Failed to iterate existing refresh rows", http.StatusInternalServerError)
		return
	}

	// Whole-set replace: delete then insert. Simpler than a merge and the
	// row count is tiny (one to a few per object).
	if _, err := tx.Exec(r.Context(),
		`DELETE FROM object_refresh WHERE object_id = $1`,
		objectID,
	); err != nil {
		jsonError(w, "Failed to delete existing refresh rows", http.StatusInternalServerError)
		return
	}

	for _, row := range req.Rows {
		var lastAt *time.Time
		if prev, ok := existingRows[row.Attribute]; ok {
			samePeriod := (prev.periodHours == nil && row.RefreshPeriodHours == nil) ||
				(prev.periodHours != nil && row.RefreshPeriodHours != nil &&
					*prev.periodHours == *row.RefreshPeriodHours)
			if prev.mode == row.RefreshMode && samePeriod {
				lastAt = prev.lastRefreshAt
			}
		}

		storedAmt := -row.Amount

		if _, err := tx.Exec(r.Context(),
			`INSERT INTO object_refresh
			    (object_id, attribute, amount, available_quantity, max_quantity,
			     refresh_mode, refresh_period_hours, last_refresh_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			objectID, row.Attribute, storedAmt,
			row.AvailableQuantity, row.MaxQuantity,
			row.RefreshMode, row.RefreshPeriodHours, lastAt,
		); err != nil {
			jsonError(w, "Failed to insert refresh row", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		jsonError(w, "Failed to commit refresh row replace", http.StatusInternalServerError)
		return
	}

	// Broadcast so any open editor panels mirror the new state. Mirrors the
	// village_object_tags_updated fan-out pattern.
	app.Hub.Broadcast(WorldEvent{
		Type: "object_refresh_updated",
		Data: map[string]any{
			"object_id": objectID,
		},
	})

	w.WriteHeader(http.StatusNoContent)
}
