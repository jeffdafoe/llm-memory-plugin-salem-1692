package main

// sealed_note — letter/messenger errand chain.
//
// ZBBS-158. Schema: migrations/ZBBS-158-sealed-note_up.sql.
//
// A short note from author NPC to recipient NPC, carried by a PC.
// PC POSTs /pc/deliver-note with the note_id. Engine validates that
// the PC is the registered courier and is co-located with the
// recipient, then flips sealed=false + stamps delivered_at. The
// recipient's next perception sees the note body in a
// "Notes delivered to you:" block; the recipient may react via
// speak.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// pcDeliverNoteRequest is the request body for /pc/deliver-note.
type pcDeliverNoteRequest struct {
	NoteID int64 `json:"note_id"`
}

// pcDeliverNoteResponse mirrors the other /pc/* response shapes.
type pcDeliverNoteResponse struct {
	Result        string `json:"result"`
	Error         string `json:"error,omitempty"`
	NoteID        int64  `json:"note_id,omitempty"`
	RecipientName string `json:"recipient_name,omitempty"`
}

// handlePCDeliverNote is the deliver endpoint. PC must be the
// registered courier_actor_id AND must be co-located with the
// recipient (same inside_structure_id OR same current_huddle_id).
//
// Validation rejects (no DB writes performed):
//   - Not authenticated.
//   - Note row missing.
//   - Note already delivered (sealed = false).
//   - Caller is not the registered courier.
//   - Caller is not co-located with the recipient.
func (app *App) handlePCDeliverNote(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}
	var req pcDeliverNoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.NoteID <= 0 {
		jsonError(w, "missing note_id", http.StatusBadRequest)
		return
	}

	var actorID string
	if err := app.DB.QueryRow(r.Context(),
		`SELECT id::text FROM actor WHERE login_username = $1`,
		user.Username,
	).Scan(&actorID); err != nil {
		if err == sql.ErrNoRows {
			jsonError(w, "No character", http.StatusBadRequest)
			return
		}
		log.Printf("pc/deliver-note actor lookup: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	app.touchPCInput(r.Context(), actorID)

	// Pull note + recipient state in one round trip; gate on courier
	// match and sealed-ness up front.
	var (
		recipientID         string
		recipientName       string
		recipientInside     sql.NullString
		recipientHuddle     sql.NullString
		callerInside        sql.NullString
		callerHuddle        sql.NullString
		bodyText            string
		courierActorID      sql.NullString
		sealed              bool
	)
	err := app.DB.QueryRow(r.Context(),
		`SELECT
		    sn.recipient_actor_id::text,
		    rcp.display_name,
		    rcp.inside_structure_id::text,
		    rcp.current_huddle_id::text,
		    me.inside_structure_id::text,
		    me.current_huddle_id::text,
		    sn.body_text,
		    sn.courier_actor_id::text,
		    sn.sealed
		   FROM sealed_note sn
		   JOIN actor rcp ON rcp.id = sn.recipient_actor_id
		   JOIN actor me ON me.id = $2::uuid
		  WHERE sn.id = $1`,
		req.NoteID, actorID,
	).Scan(
		&recipientID, &recipientName,
		&recipientInside, &recipientHuddle,
		&callerInside, &callerHuddle,
		&bodyText, &courierActorID, &sealed,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			jsonResponse(w, http.StatusOK, pcDeliverNoteResponse{
				Result: "rejected",
				Error:  fmt.Sprintf("no such note %d", req.NoteID),
			})
			return
		}
		log.Printf("pc/deliver-note lookup: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if !sealed {
		jsonResponse(w, http.StatusOK, pcDeliverNoteResponse{
			Result: "rejected",
			Error:  "note is already delivered",
			NoteID: req.NoteID,
		})
		return
	}
	if !courierActorID.Valid || courierActorID.String != actorID {
		jsonResponse(w, http.StatusOK, pcDeliverNoteResponse{
			Result: "rejected",
			Error:  "you are not carrying this note",
			NoteID: req.NoteID,
		})
		return
	}

	sameStructure := callerInside.Valid && recipientInside.Valid &&
		callerInside.String != "" && callerInside.String == recipientInside.String
	sameHuddle := callerHuddle.Valid && recipientHuddle.Valid &&
		callerHuddle.String != "" && callerHuddle.String == recipientHuddle.String
	if !sameStructure && !sameHuddle {
		jsonResponse(w, http.StatusOK, pcDeliverNoteResponse{
			Result: "rejected",
			Error:  fmt.Sprintf("%s isn't here — find them and try again", recipientName),
			NoteID: req.NoteID,
		})
		return
	}

	// Atomic flip: only succeeds if still sealed AND we're still the
	// courier — guards against a concurrent delivery attempt or a
	// retroactive admin reassignment.
	tag, err := app.DB.Exec(r.Context(),
		`UPDATE sealed_note
		    SET sealed = false,
		        delivered_at = NOW()
		  WHERE id = $1
		    AND sealed = true
		    AND courier_actor_id = $2::uuid`,
		req.NoteID, actorID,
	)
	if err != nil {
		log.Printf("pc/deliver-note flip %d: %v", req.NoteID, err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		// Race lost — concurrent flip won. Treat as already-delivered.
		jsonResponse(w, http.StatusOK, pcDeliverNoteResponse{
			Result: "rejected",
			Error:  "note was just delivered (race lost)",
			NoteID: req.NoteID,
		})
		return
	}

	// Broadcast for any open client UI. Recipient's next perception
	// will show the note body via visibleDeliveredNotes.
	app.Hub.Broadcast(WorldEvent{
		Type: "sealed_note_delivered",
		Data: map[string]any{
			"note_id":        req.NoteID,
			"courier_id":     actorID,
			"recipient_id":   recipientID,
			"recipient_name": recipientName,
			"at":             time.Now().UTC().Format(time.RFC3339),
		},
	})
	log.Printf("sealed_note: courier=%s delivered note=%d to %s", actorID, req.NoteID, recipientName)

	jsonResponse(w, http.StatusOK, pcDeliverNoteResponse{
		Result:        "ok",
		NoteID:        req.NoteID,
		RecipientName: recipientName,
	})
}

// visibleDeliveredNotes returns recent notes delivered to the
// recipient in the last `window`. Used by the perception builder to
// surface a "Notes delivered to you:" block. Caller renders.
//
// Best-effort: errors logged + nil returned. Empty result =
// section suppressed.
func (app *App) visibleDeliveredNotes(ctx context.Context, recipientID string, window time.Duration, limit int) []string {
	if limit <= 0 {
		return nil
	}
	rows, err := app.DB.Query(ctx,
		`SELECT a.display_name, sn.body_text
		   FROM sealed_note sn
		   JOIN actor a ON a.id = sn.author_actor_id
		  WHERE sn.recipient_actor_id = $1::uuid
		    AND sn.sealed = false
		    AND sn.delivered_at > NOW() - ($2::int * INTERVAL '1 second')
		  ORDER BY sn.delivered_at DESC
		  LIMIT $3`,
		recipientID, int(window.Seconds()), limit,
	)
	if err != nil {
		log.Printf("sealed_note: visibleDeliveredNotes for %s: %v", recipientID, err)
		return nil
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var author, body string
		if err := rows.Scan(&author, &body); err != nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("  From %s: \"%s\"", author, body))
	}
	return lines
}
