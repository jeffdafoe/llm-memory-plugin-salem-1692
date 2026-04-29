package main

// Pay tool — coin transfer between villagers, with optional consumption
// side-effect (drop hunger or thirst when the payment is for food / drink).
//
// Buyer calls pay(recipient, amount, for) to hand coins to another villager.
// The engine treats the buyer's free-text `for` argument as a hint about the
// social context of the payment — keyword-matched against canned food/drink
// vocabularies to decide whether to also drop the buyer's hunger/thirst.
// Bartering and negotiation happen entirely in `speak` turns; this tool just
// executes the agreed-upon transfer.
//
// Failure modes return a structured (result, errStr) pair so the dispatcher
// in executeAgentCommit can route the audit row consistently with how
// move_to / chore failures are recorded today. The buyer's coins are never
// partially deducted: the transfer runs in a single transaction and either
// fully commits (coins moved + attribute dropped if applicable) or fully
// rolls back.
//
// Recipient resolution: matches village_agent.name. Display names with
// whitespace get the same hyphenation the rest of the engine uses. Future
// work might broaden this to "tavern" or other place-tier recipients;
// today it's strictly NPC-to-NPC.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Canned vocab for the for-keyword heuristic. Lowercase substring matches
// against the for argument decide which need (if any) drops. Both lists
// can move to setting rows later if operators want to tune them; for now
// they're constant.
var (
	foodKeywords  = []string{"meal", "food", "stew", "bread", "supper", "dinner", "breakfast", "lunch", "porridge", "cheese", "pie"}
	drinkKeywords = []string{"ale", "beer", "cider", "mead", "wine", "drink", "water", "milk"}
)

// payResult captures the outcome of an attempted pay so the dispatcher can
// build the audit-row (result, errStr) pair without duplicating switch logic.
type payResult struct {
	Result          string // "ok" | "rejected" | "failed"
	Err             string // human-readable, empty when Result == "ok"
	BuyerNewCoins   int    // post-transfer balance for log/broadcast
	HungerReduction int    // 0 if for didn't match food
	ThirstReduction int    // 0 if for didn't match drink
}

// executePay carries out the transfer and any consumption side-effect.
// Returns a payResult describing what happened. Never partial: if any leg
// fails, the transaction rolls back and the buyer keeps their coins.
//
// The attribute drops use the meal_drop / drink_drop settings (default 24
// = full reset). A payment whose `forText` matches BOTH a food and a drink
// keyword (e.g., "a meal and ale") drops both — a stew-and-pint counts
// against both needs, which matches how an NPC would actually be eating.
func (app *App) executePay(ctx context.Context, buyer *agentNPCRow, recipientName string, amount int, forText string) payResult {
	if amount <= 0 {
		return payResult{Result: "rejected", Err: "amount must be positive"}
	}
	recipientName = strings.TrimSpace(recipientName)
	if recipientName == "" {
		return payResult{Result: "rejected", Err: "missing recipient"}
	}

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return payResult{Result: "failed", Err: fmt.Sprintf("begin tx: %v", err)}
	}
	defer tx.Rollback(ctx) // safe to call after commit (no-op)

	// Lock the buyer row so a concurrent pay from the same NPC can't race
	// us into a negative balance. Recipient is locked too so a concurrent
	// pay TO the same recipient serializes its credit.
	var buyerCoins int
	err = tx.QueryRow(ctx,
		`SELECT coins FROM village_agent WHERE id = $1 FOR UPDATE`,
		buyer.ID,
	).Scan(&buyerCoins)
	if err != nil {
		return payResult{Result: "failed", Err: fmt.Sprintf("lock buyer: %v", err)}
	}

	if buyerCoins < amount {
		return payResult{Result: "rejected", Err: fmt.Sprintf("insufficient coins (have %d, need %d)", buyerCoins, amount)}
	}

	// Recipient lookup-and-lock. Try the period-appropriate display name
	// first (npc.display_name = "Ezekiel Crane"), then fall back to the
	// llm-memory slug (village_agent.name = "ezekiel-crane"). Display-name
	// match is what the LLM will naturally produce after dialogue ("I'll
	// pay John Ellis"); the slug fallback covers cases where the model
	// already speaks in slugs. Both matches are case-insensitive and
	// trimmed.
	//
	// Future: when PCs become payable parties, add a third UNION arm
	// against pc_position.character_name. Out of scope today since coins
	// only live on village_agent and PCs aren't reliably represented
	// there yet.
	var recipientID string
	err = tx.QueryRow(ctx, `
		SELECT va.id FROM village_agent va
		LEFT JOIN npc n ON n.llm_memory_agent = va.llm_memory_agent
		WHERE LOWER(n.display_name) = LOWER($1)
		   OR LOWER(va.name) = LOWER($1)
		ORDER BY (LOWER(n.display_name) = LOWER($1)) DESC NULLS LAST
		LIMIT 1
		FOR UPDATE OF va
	`, recipientName).Scan(&recipientID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return payResult{Result: "rejected", Err: fmt.Sprintf("no villager named %q", recipientName)}
		}
		return payResult{Result: "failed", Err: fmt.Sprintf("lock recipient: %v", err)}
	}

	// Block self-payment. Without this, an NPC could "pay themselves for
	// ale" — coins net to zero (debit and credit on the same row) but the
	// hunger/thirst drop still applies, effectively giving free meals.
	if recipientID == buyer.ID {
		return payResult{Result: "rejected", Err: "cannot pay yourself"}
	}

	// Deduct + credit. Two separate UPDATEs is fine — the SELECT FOR UPDATE
	// above ensures both rows are held by this txn so concurrent pays
	// involving either party serialize correctly.
	if _, err := tx.Exec(ctx, `UPDATE village_agent SET coins = coins - $1 WHERE id = $2`, amount, buyer.ID); err != nil {
		return payResult{Result: "failed", Err: fmt.Sprintf("debit buyer: %v", err)}
	}
	if _, err := tx.Exec(ctx, `UPDATE village_agent SET coins = coins + $1 WHERE id = $2`, amount, recipientID); err != nil {
		return payResult{Result: "failed", Err: fmt.Sprintf("credit recipient: %v", err)}
	}

	// Consumption side-effect — drop hunger/thirst on the buyer based on
	// `for` keyword match. Both, one, or neither may match; the UPDATE
	// only fires for matching needs. GREATEST clamps at 0 so an over-
	// configured drop magnitude can't push the value negative.
	hungerDrop := 0
	thirstDrop := 0
	forLower := strings.ToLower(forText)
	if matchesAny(forLower, foodKeywords) {
		hungerDrop = app.loadAttributeMagnitude(ctx, "meal_drop")
	}
	if matchesAny(forLower, drinkKeywords) {
		thirstDrop = app.loadAttributeMagnitude(ctx, "drink_drop")
	}
	if hungerDrop > 0 || thirstDrop > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE village_agent SET
				hunger = GREATEST(0, hunger - $1::int),
				thirst = GREATEST(0, thirst - $2::int)
			WHERE id = $3
		`, hungerDrop, thirstDrop, buyer.ID); err != nil {
			return payResult{Result: "failed", Err: fmt.Sprintf("apply consumption: %v", err)}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return payResult{Result: "failed", Err: fmt.Sprintf("commit tx: %v", err)}
	}

	// Post-commit: broadcast a Hub event so listening clients (Godot,
	// admin dashboard) can render the transaction. Non-fatal if broadcast
	// fails — the transfer already happened. WS broadcast happens outside
	// the txn so a slow client never blocks the DB.
	app.Hub.Broadcast(WorldEvent{
		Type: "npc_paid",
		Data: map[string]interface{}{
			"buyer":            buyer.DisplayName,
			"buyer_id":         buyer.ID,
			"recipient":        recipientName,
			"recipient_id":     recipientID,
			"amount":           amount,
			"for":              forText,
			"hunger_reduction": hungerDrop,
			"thirst_reduction": thirstDrop,
			"at":               time.Now().UTC().Format(time.RFC3339),
		},
	})

	return payResult{
		Result:          "ok",
		BuyerNewCoins:   buyerCoins - amount,
		HungerReduction: hungerDrop,
		ThirstReduction: thirstDrop,
	}
}

// matchesAny returns true if any keyword appears as a whole word in the
// input. Tokenized rather than substring so "alehouse repairs" doesn't
// false-match "ale" and "breadwinner" doesn't false-match "bread". Splits
// on any non-letter rune; case is normalized by the caller before passing
// in. Plural forms must be added as separate keywords if needed (e.g.
// "ales", "meals") — the matcher does not stem.
func matchesAny(haystack string, keywords []string) bool {
	if haystack == "" {
		return false
	}
	words := strings.FieldsFunc(haystack, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))
	})
	for _, w := range words {
		for _, kw := range keywords {
			if w == kw {
				return true
			}
		}
	}
	return false
}

