package main

// Serve tool — tavernkeeper hands food/drink from their stock to one
// or more co-located people. The seller-side counterpart to consume:
// decrements the server's inventory and (with consume_now=true, the
// default) drops each recipient's matching need. No coin transfer.
// Payment, if any, happens separately via the buyer-side `pay` tool.
//
// Why this exists: in the customer-driven model, tavern transactions
// fire when a buyer NPC calls pay(seller, amount, item). That works
// for NPC-to-NPC tavern flows. PCs (login_username set) have no LLM
// tool surface and no pay UI — so when a tavernkeeper serves stew to
// PCs Jefferey and Wendy, nothing decrements the kitchen stock and
// nothing drops the players' hunger. serve gives the tavernkeeper a
// canonical verb that does the seller-side state change directly,
// independent of payment.
//
// Co-location: server and every recipient must share a non-NULL
// current_huddle_id. Out of the open village (no huddle) the tool
// rejects — there is no scene to "serve into."
//
// Reverses cleanly on any leg failure: stock decrement, recipient
// inventory credits, and consumption side-effects are all wrapped in
// one transaction.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// serveResult captures the outcome of an attempted serve so the
// dispatcher can build the audit row + tool-result message without
// duplicating the switch logic.
type serveResult struct {
	Result string // "ok" | "rejected" | "failed"
	Err    string // human-readable, empty on ok
	// Per-recipient summary for the tool-result text. Empty on
	// rejected / failed.
	Summaries []string
	// Per-recipient post-consumption need values, used for the
	// npc_needs_changed broadcasts. nil on take-home.
	NeedUpdates []serveNeedUpdate
	// Display names of recipients that received take-home items
	// (for the inventory broadcast).
	TakeHomeRecipientIDs []string
	// item_kind that moved (server's stock decremented). Empty on
	// rejected / failed.
	Item string
}

type serveNeedUpdate struct {
	ActorID   string
	Hunger    int
	Thirst    int
	Tiredness int
}

// serveRequest groups the serve arguments. recipientNames are the
// display-name strings the model used; resolution happens inside.
type serveRequest struct {
	RecipientNames []string
	Item           string
	Qty            int  // per recipient; defaults to 1
	ConsumeNow     bool // tavern (true) vs take-home (false)
}

// executeServe carries out the serve. server is the actor calling the
// tool. Recipients are looked up by display name and required to share
// the server's current_huddle_id. On any failure mid-transaction, the
// whole thing rolls back and stock stays where it was.
func (app *App) executeServe(ctx context.Context, server *agentNPCRow, req serveRequest) serveResult {
	itemKind := strings.TrimSpace(strings.ToLower(req.Item))
	if itemKind == "" {
		return serveResult{Result: "rejected", Err: "missing item"}
	}
	if len(req.RecipientNames) == 0 {
		return serveResult{Result: "rejected", Err: "no recipients"}
	}
	qty := req.Qty
	if qty <= 0 {
		qty = 1
	}

	// Normalize recipient names: trim, drop empties, dedupe (case-
	// insensitive). A serve call that names the same person twice
	// would otherwise serve two helpings; the model meant one.
	seen := make(map[string]bool, len(req.RecipientNames))
	var cleanNames []string
	for _, n := range req.RecipientNames {
		t := strings.TrimSpace(n)
		if t == "" {
			continue
		}
		key := strings.ToLower(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		cleanNames = append(cleanNames, t)
	}
	if len(cleanNames) == 0 {
		return serveResult{Result: "rejected", Err: "no recipients"}
	}

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return serveResult{Result: "failed", Err: fmt.Sprintf("begin tx: %v", err)}
	}
	defer tx.Rollback(ctx)

	// Server's huddle. Required — outdoor serve has no scene to
	// serve into. Lock the actor row so a concurrent move-out
	// doesn't change the huddle mid-transaction.
	var serverHuddle sql.NullString
	if err := tx.QueryRow(ctx,
		`SELECT current_huddle_id FROM actor WHERE id = $1 FOR UPDATE`,
		server.ID,
	).Scan(&serverHuddle); err != nil {
		return serveResult{Result: "failed", Err: fmt.Sprintf("lock server: %v", err)}
	}
	if !serverHuddle.Valid {
		return serveResult{Result: "rejected", Err: "no one to serve — you must be inside a structure with the recipients"}
	}

	// Validate the item and pull capabilities + satisfies pair so
	// we can apply consumption in the consume_now path.
	var (
		itemSatisfiesAttr sql.NullString
		itemSatisfiesAmt  sql.NullInt32
		itemCapabilities  []string
	)
	err = tx.QueryRow(ctx,
		`SELECT satisfies_attribute, satisfies_amount, capabilities
		   FROM item_kind WHERE name = $1`,
		itemKind,
	).Scan(&itemSatisfiesAttr, &itemSatisfiesAmt, &itemCapabilities)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return serveResult{Result: "rejected", Err: fmt.Sprintf("no such item %q", itemKind)}
		}
		return serveResult{Result: "failed", Err: fmt.Sprintf("look up item: %v", err)}
	}

	// Take-home requires portable. Stew, water are non-portable;
	// reject with a clean error so the model can retry with
	// consume_now=true (the tavern flow).
	if !req.ConsumeNow && !hasCapability(itemCapabilities, "portable") {
		return serveResult{Result: "rejected", Err: fmt.Sprintf("%s cannot be carried; serve at-source with consume_now=true", itemKind)}
	}

	// Resolve recipients. Each must (a) exist, (b) not be the
	// server, (c) share the server's current_huddle_id. Lock the
	// rows so a concurrent leave doesn't slip a recipient out
	// mid-transaction.
	recipients := make([]serveRecipient, 0, len(cleanNames))
	for _, name := range cleanNames {
		var rid string
		var rdn string
		var rhuddle sql.NullString
		err := tx.QueryRow(ctx,
			`SELECT id, display_name, current_huddle_id
			   FROM actor
			  WHERE LOWER(display_name) = LOWER($1)
			  LIMIT 1
			  FOR UPDATE`,
			name,
		).Scan(&rid, &rdn, &rhuddle)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return serveResult{Result: "rejected", Err: fmt.Sprintf("no one named %q here", name)}
			}
			return serveResult{Result: "failed", Err: fmt.Sprintf("lock recipient %q: %v", name, err)}
		}
		if rid == server.ID {
			return serveResult{Result: "rejected", Err: "cannot serve yourself; use consume to eat your own stock"}
		}
		if !rhuddle.Valid || rhuddle.String != serverHuddle.String {
			return serveResult{Result: "rejected", Err: fmt.Sprintf("%s is not in the room with you", rdn)}
		}
		recipients = append(recipients, serveRecipient{ID: rid, DisplayName: rdn})
	}

	// Lock + validate server's stock.
	totalQty := qty * len(recipients)
	var stock int
	if err := tx.QueryRow(ctx,
		`SELECT quantity FROM actor_inventory
		  WHERE actor_id = $1 AND item_kind = $2
		  FOR UPDATE`,
		server.ID, itemKind,
	).Scan(&stock); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return serveResult{Result: "rejected", Err: fmt.Sprintf("you have no %s to serve", itemKind)}
		}
		return serveResult{Result: "failed", Err: fmt.Sprintf("lock server stock: %v", err)}
	}
	if stock < totalQty {
		return serveResult{Result: "rejected", Err: fmt.Sprintf("you have only %d %s (need %d for %d recipients)", stock, itemKind, totalQty, len(recipients))}
	}

	// Decrement the server's stock by the total.
	newStock := stock - totalQty
	if newStock == 0 {
		if _, err := tx.Exec(ctx,
			`DELETE FROM actor_inventory WHERE actor_id = $1 AND item_kind = $2`,
			server.ID, itemKind,
		); err != nil {
			return serveResult{Result: "failed", Err: fmt.Sprintf("delete server stock: %v", err)}
		}
	} else {
		if _, err := tx.Exec(ctx,
			`UPDATE actor_inventory SET quantity = $1
			  WHERE actor_id = $2 AND item_kind = $3`,
			newStock, server.ID, itemKind,
		); err != nil {
			return serveResult{Result: "failed", Err: fmt.Sprintf("decrement server stock: %v", err)}
		}
	}

	// Per-recipient effect. Either eat-immediately (drop need) or
	// take-home (credit recipient inventory by qty). Item-only — no
	// coin movement, no audit row here (the dispatcher writes the
	// agent_action_log row from the tool-call payload).
	var summaries []string
	var needUpdates []serveNeedUpdate
	var takeHomeIDs []string

	for _, rcp := range recipients {
		if req.ConsumeNow {
			delta := consumptionDelta{}
			if itemSatisfiesAttr.Valid && itemSatisfiesAmt.Valid {
				totalAmount := int(itemSatisfiesAmt.Int32) * qty
				switch itemSatisfiesAttr.String {
				case "hunger":
					delta.Hunger = -totalAmount
				case "thirst":
					delta.Thirst = -totalAmount
				case "tiredness":
					delta.Tiredness = -totalAmount
				}
			}
			if delta.Hunger != 0 || delta.Thirst != 0 || delta.Tiredness != 0 {
				res, err := app.applyConsumption(ctx, tx, rcp.ID, delta, "serve-consume")
				if err != nil {
					return serveResult{Result: "failed", Err: fmt.Sprintf("apply consumption for %s: %v", rcp.DisplayName, err)}
				}
				needUpdates = append(needUpdates, serveNeedUpdate{
					ActorID:   rcp.ID,
					Hunger:    res.Hunger,
					Thirst:    res.Thirst,
					Tiredness: res.Tiredness,
				})
				summaries = append(summaries, fmt.Sprintf("%s ate/drank %d %s", rcp.DisplayName, qty, itemKind))
			} else {
				// Item isn't a recognized food/drink (e.g. wheat, iron)
				// but the call passed validation above. Reject — this
				// is the LLM trying to "serve" raw materials.
				return serveResult{Result: "rejected", Err: fmt.Sprintf("%s isn't food or drink; nothing to consume", itemKind)}
			}
		} else {
			// Take-home: credit recipient inventory.
			if _, err := tx.Exec(ctx,
				`INSERT INTO actor_inventory (actor_id, item_kind, quantity)
				 VALUES ($1, $2, $3)
				 ON CONFLICT (actor_id, item_kind)
				 DO UPDATE SET quantity = actor_inventory.quantity + EXCLUDED.quantity`,
				rcp.ID, itemKind, qty,
			); err != nil {
				return serveResult{Result: "failed", Err: fmt.Sprintf("credit %s's stock: %v", rcp.DisplayName, err)}
			}
			takeHomeIDs = append(takeHomeIDs, rcp.ID)
			summaries = append(summaries, fmt.Sprintf("%s took home %d %s", rcp.DisplayName, qty, itemKind))
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return serveResult{Result: "failed", Err: fmt.Sprintf("commit tx: %v", err)}
	}

	// Broadcasts. Single npc_served event so the editor / talk panel
	// can render a narration line. Plus inventory broadcasts (server +
	// any take-home recipients) and per-recipient needs broadcasts so
	// admin views update.
	app.Hub.Broadcast(WorldEvent{
		Type: "npc_served",
		Data: map[string]interface{}{
			"server_id":   server.ID,
			"server":      server.DisplayName,
			"item":        itemKind,
			"qty":         qty,
			"recipients":  recipientDisplayNames(recipients),
			"consume_now": req.ConsumeNow,
			"at":          time.Now().UTC().Format(time.RFC3339),
		},
	})
	app.Hub.Broadcast(WorldEvent{
		Type: "actor_inventory_changed",
		Data: map[string]any{
			"actor_id":  server.ID,
			"item_kind": itemKind,
		},
	})
	for _, rid := range takeHomeIDs {
		app.Hub.Broadcast(WorldEvent{
			Type: "actor_inventory_changed",
			Data: map[string]any{
				"actor_id":  rid,
				"item_kind": itemKind,
			},
		})
	}
	for _, nu := range needUpdates {
		app.Hub.Broadcast(WorldEvent{
			Type: "npc_needs_changed",
			Data: map[string]interface{}{
				"id":        nu.ActorID,
				"hunger":    nu.Hunger,
				"thirst":    nu.Thirst,
				"tiredness": nu.Tiredness,
			},
		})
	}

	return serveResult{
		Result:               "ok",
		Item:                 itemKind,
		Summaries:            summaries,
		NeedUpdates:          needUpdates,
		TakeHomeRecipientIDs: takeHomeIDs,
	}
}

// serveRecipient is the resolved actor row used during a serve. ID is
// authoritative; DisplayName is what we surface in narration and
// broadcasts (recipients deserve their canonical display name even if
// the server typed a slight variant).
type serveRecipient struct {
	ID          string
	DisplayName string
}

// recipientDisplayNames extracts display names for the broadcast payload.
func recipientDisplayNames(rcps []serveRecipient) []string {
	names := make([]string, len(rcps))
	for i, r := range rcps {
		names[i] = r.DisplayName
	}
	return names
}
