package main

// npc_errand_offer — small fetch/deliver tasks NPCs offer to PCs.
//
// ZBBS-159. Schema: migrations/ZBBS-159-npc-errand-offer_up.sql.
//
// State machine (forward-only):
//   offered → accepted → completed
//          ↓                 ↓
//     expired            expired (timeout)
//          ↓
//     rejected
//
// PC accepts via /pc/accept-errand, completes via /pc/complete-errand
// at the requester's location with the fetched item in inventory.
// Reward paid via direct coin transfer (no deliberation gate; this
// is a contracted handoff).
//
// v1 keeps detection explicit (PC calls the endpoints) rather than
// implicit-on-pay-and-arrive heuristics. Per work mail 32e8824c
// fallback recommendation: explicit is cleaner.

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

// pcErrandIDRequest is shared by /pc/accept-errand and
// /pc/complete-errand. The state is the differentiator.
type pcErrandIDRequest struct {
	ErrandID int64 `json:"errand_id"`
}

type pcErrandResponse struct {
	Result    string `json:"result"`
	Error     string `json:"error,omitempty"`
	ErrandID  int64  `json:"errand_id,omitempty"`
	Reward    int    `json:"reward,omitempty"`
	NewCoins  int    `json:"buyer_new_coins,omitempty"`
}

// handlePCAcceptErrand validates and flips offered → accepted.
// Caller must be the target_pc on the row OR the row's target_pc
// must be NULL (open-to-anyone offer).
func (app *App) handlePCAcceptErrand(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}
	var req pcErrandIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ErrandID <= 0 {
		jsonError(w, "missing errand_id", http.StatusBadRequest)
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
		log.Printf("pc/accept-errand actor lookup: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	app.touchPCInput(r.Context(), actorID)

	tag, err := app.DB.Exec(r.Context(),
		`UPDATE npc_errand_offer
		    SET state = 'accepted',
		        accepted_at = NOW(),
		        target_pc_actor_id = $2::uuid
		  WHERE id = $1
		    AND state = 'offered'
		    AND (target_pc_actor_id IS NULL OR target_pc_actor_id = $2::uuid)
		    AND (expires_at IS NULL OR expires_at > NOW())`,
		req.ErrandID, actorID,
	)
	if err != nil {
		log.Printf("pc/accept-errand %d: %v", req.ErrandID, err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		jsonResponse(w, http.StatusOK, pcErrandResponse{
			Result:   "rejected",
			Error:    "errand is not available — already taken, expired, or not offered to you",
			ErrandID: req.ErrandID,
		})
		return
	}
	jsonResponse(w, http.StatusOK, pcErrandResponse{Result: "ok", ErrandID: req.ErrandID})
}

// handlePCCompleteErrand validates the PC has the fetched item, is
// at the requester's location, then atomically:
//   - flips state → 'completed', stamps completed_at
//   - decrements actor_inventory by fetch_qty
//   - moves reward_coins from requester to PC
func (app *App) handlePCCompleteErrand(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}
	var req pcErrandIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ErrandID <= 0 {
		jsonError(w, "missing errand_id", http.StatusBadRequest)
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
		log.Printf("pc/complete-errand actor lookup: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	app.touchPCInput(r.Context(), actorID)

	tx, err := app.DB.Begin(r.Context())
	if err != nil {
		log.Printf("pc/complete-errand begin tx: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	// Lock + load the offer + caller + requester state.
	var (
		requesterID         string
		requesterInside     sql.NullString
		requesterHuddle     sql.NullString
		callerInside        sql.NullString
		callerHuddle        sql.NullString
		state               string
		fetchItem           string
		fetchQty            int
		rewardCoins         int
		requesterCoins      int
	)
	err = tx.QueryRow(r.Context(),
		`SELECT
		    eo.requester_actor_id::text,
		    rq.inside_structure_id::text,
		    rq.current_huddle_id::text,
		    me.inside_structure_id::text,
		    me.current_huddle_id::text,
		    eo.state, eo.fetch_item_kind, eo.fetch_qty, eo.reward_coins,
		    rq.coins
		   FROM npc_errand_offer eo
		   JOIN actor rq ON rq.id = eo.requester_actor_id
		   JOIN actor me ON me.id = $2::uuid
		  WHERE eo.id = $1
		  FOR UPDATE OF eo, rq, me`,
		req.ErrandID, actorID,
	).Scan(&requesterID, &requesterInside, &requesterHuddle,
		&callerInside, &callerHuddle, &state, &fetchItem, &fetchQty, &rewardCoins,
		&requesterCoins)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			jsonResponse(w, http.StatusOK, pcErrandResponse{Result: "rejected", Error: fmt.Sprintf("no such errand %d", req.ErrandID), ErrandID: req.ErrandID})
			return
		}
		log.Printf("pc/complete-errand lookup %d: %v", req.ErrandID, err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if state != "accepted" {
		jsonResponse(w, http.StatusOK, pcErrandResponse{Result: "rejected", Error: fmt.Sprintf("errand state is %q (need 'accepted')", state), ErrandID: req.ErrandID})
		return
	}

	// Co-location gate (same as ZBBS-142).
	sameStructure := callerInside.Valid && requesterInside.Valid &&
		callerInside.String != "" && callerInside.String == requesterInside.String
	sameHuddle := callerHuddle.Valid && requesterHuddle.Valid &&
		callerHuddle.String != "" && callerHuddle.String == requesterHuddle.String
	if !sameStructure && !sameHuddle {
		jsonResponse(w, http.StatusOK, pcErrandResponse{
			Result:   "rejected",
			Error:    "you must be with the requester to deliver",
			ErrandID: req.ErrandID,
		})
		return
	}

	// Inventory check + atomic decrement. ZBBS-HOME-258: split the
	// pre-fix `WHERE quantity >= $3` UPDATE into a DELETE for the
	// exact-match case (delivering the player's last unit) and an
	// UPDATE for the more-than-enough case. The pre-fix shape
	// allowed a quantity-becomes-0 UPDATE which trips
	// actor_inventory's CHECK (quantity > 0). DELETE-then-UPDATE
	// with mutually-exclusive WHERE clauses keeps the validation:
	// quantity < fetchQty → neither matches → rejected.
	delTag, err := tx.Exec(r.Context(),
		`DELETE FROM actor_inventory
		  WHERE actor_id = $1::uuid
		    AND item_kind = $2
		    AND quantity = $3`,
		actorID, fetchItem, fetchQty,
	)
	if err != nil {
		log.Printf("pc/complete-errand inventory %d delete: %v", req.ErrandID, err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	updTag, err := tx.Exec(r.Context(),
		`UPDATE actor_inventory
		    SET quantity = quantity - $3
		  WHERE actor_id = $1::uuid
		    AND item_kind = $2
		    AND quantity > $3`,
		actorID, fetchItem, fetchQty,
	)
	if err != nil {
		log.Printf("pc/complete-errand inventory %d update: %v", req.ErrandID, err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if delTag.RowsAffected()+updTag.RowsAffected() == 0 {
		jsonResponse(w, http.StatusOK, pcErrandResponse{
			Result:   "rejected",
			Error:    fmt.Sprintf("you don't have %d %s to deliver", fetchQty, fetchItem),
			ErrandID: req.ErrandID,
		})
		return
	}

	// Coin transfer requester → PC. Reward is sized at offer time; if
	// the requester is short on coins by completion, the errand
	// completes anyway and we deliver whatever they have. Avoids a
	// deadlock where the player is stuck with a pile of milk and an
	// NPC who can't pay.
	payable := rewardCoins
	if requesterCoins < payable {
		payable = requesterCoins
	}
	if payable > 0 {
		if _, err := tx.Exec(r.Context(),
			`UPDATE actor SET coins = coins - $1 WHERE id = $2::uuid`,
			payable, requesterID,
		); err != nil {
			log.Printf("pc/complete-errand debit requester: %v", err)
			jsonError(w, "Internal error", http.StatusInternalServerError)
			return
		}
		if _, err := tx.Exec(r.Context(),
			`UPDATE actor SET coins = coins + $1 WHERE id = $2::uuid`,
			payable, actorID,
		); err != nil {
			log.Printf("pc/complete-errand credit pc: %v", err)
			jsonError(w, "Internal error", http.StatusInternalServerError)
			return
		}
	}

	// Flip state.
	if _, err := tx.Exec(r.Context(),
		`UPDATE npc_errand_offer SET state = 'completed', completed_at = NOW() WHERE id = $1`,
		req.ErrandID,
	); err != nil {
		log.Printf("pc/complete-errand flip state %d: %v", req.ErrandID, err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		log.Printf("pc/complete-errand commit %d: %v", req.ErrandID, err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Read PC's new coin balance for the response.
	var newCoins int
	_ = app.DB.QueryRow(r.Context(),
		`SELECT coins FROM actor WHERE id = $1::uuid`,
		actorID,
	).Scan(&newCoins)

	app.Hub.Broadcast(WorldEvent{
		Type: "errand_completed",
		Data: map[string]any{
			"errand_id":  req.ErrandID,
			"pc_id":      actorID,
			"requester":  requesterID,
			"reward":     payable,
			"item":       fetchItem,
			"qty":        fetchQty,
			"at":         time.Now().UTC().Format(time.RFC3339),
		},
	})
	log.Printf("errand_completed: pc=%s errand=%d %s x%d reward=%d", actorID, req.ErrandID, fetchItem, fetchQty, payable)

	jsonResponse(w, http.StatusOK, pcErrandResponse{
		Result:   "ok",
		ErrandID: req.ErrandID,
		Reward:   payable,
		NewCoins: newCoins,
	})
}

// activeErrandsForPC returns the PC's offered + accepted errands for
// /pc/me surfacing. Caller renders.
func (app *App) activeErrandsForPC(ctx context.Context, actorID string) []map[string]any {
	rows, err := app.DB.Query(ctx,
		`SELECT eo.id, eo.state, eo.fetch_item_kind, eo.fetch_qty, eo.reward_coins,
		        rq.display_name AS requester_name,
		        COALESCE(src.display_name, '') AS source_name,
		        COALESCE(srcvo.display_name, '') AS source_structure_name
		   FROM npc_errand_offer eo
		   JOIN actor rq ON rq.id = eo.requester_actor_id
		   LEFT JOIN actor src ON src.id = eo.source_actor_id
		   LEFT JOIN village_object srcvo ON srcvo.id = eo.source_structure_id
		  WHERE (eo.target_pc_actor_id = $1::uuid OR (eo.target_pc_actor_id IS NULL AND eo.state = 'offered'))
		    AND eo.state IN ('offered','accepted')
		    AND (eo.expires_at IS NULL OR eo.expires_at > NOW())
		  ORDER BY eo.offered_at ASC`,
		actorID,
	)
	if err != nil {
		log.Printf("activeErrandsForPC %s: %v", actorID, err)
		return nil
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var (
			id, qty, reward                                    int64
			state, item, requester, source, structureName      string
		)
		if err := rows.Scan(&id, &state, &item, &qty, &reward, &requester, &source, &structureName); err != nil {
			continue
		}
		entry := map[string]any{
			"id":             id,
			"state":          state,
			"fetch_item":     item,
			"fetch_qty":      qty,
			"reward_coins":   reward,
			"requester_name": requester,
		}
		if source != "" {
			entry["source_name"] = source
		}
		if structureName != "" {
			entry["source_structure_name"] = structureName
		}
		out = append(out, entry)
	}
	return out
}
