package main

// Inventory and trade — Phase 1 (ZBBS-091).
//
// Two new agent actions sitting alongside pay(): buy() transfers coin
// + items between actors atomically; consume() decrements the buyer's
// inventory and applies the item's configured satisfaction to the
// linked actor need.
//
// pay() stays unchanged. It remains the "drink at the bar" verb —
// instant gratification with no inventory step. The buy/consume pair
// is for non-tavern flow: take-home goods, supply chain, eventually
// recipes (Phase 2).
//
// Wire convention: item_kind.satisfies_amount is positive in storage
// ("amount restored when consumed"); applyConsumption takes a negative
// delta to reduce the need. Negation happens at the consume site.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// itemKindCache holds the canonical item_kind names plus a compiled
// word-boundary regex matching any of them. Built lazily on first
// extractImplicitItemMentions call; lives on App.ItemKindCache and is
// invalidated only on engine restart.
type itemKindCache struct {
	names []string
	regex *regexp.Regexp
}

// normalizeMentions converts the raw mentions value from a speak tool
// call into a deduped, lowercased, trimmed []string. Tolerates the
// common LLM variants — []interface{} of strings, a bare string, or
// a JSON-encoded array string ("[\"cheese\",\"ale\"]"). Non-string
// elements are dropped silently. Empty input → nil. Phase C of
// sales-and-gifts.
func normalizeMentions(raw interface{}) []string {
	if raw == nil {
		return nil
	}
	var collected []string
	switch v := raw.(type) {
	case []interface{}:
		for _, x := range v {
			if s, ok := x.(string); ok {
				collected = append(collected, s)
			}
		}
	case []string:
		collected = append(collected, v...)
	case string:
		t := strings.TrimSpace(v)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			// JSON-array-as-string variant — same defensive parse the
			// serve dispatcher does for recipients.
			var parsed []string
			if err := json.Unmarshal([]byte(t), &parsed); err == nil {
				collected = parsed
			} else {
				collected = []string{t}
			}
		} else if t != "" {
			collected = []string{t}
		}
	}
	if len(collected) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(collected))
	out := make([]string, 0, len(collected))
	for _, s := range collected {
		k := strings.TrimSpace(strings.ToLower(s))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// actorHasAnyInventory returns true when the actor carries at least
// one actor_inventory row with qty > 0. Used by the speak tool's
// mention-validation gate (ZBBS-WORK-223): the validation only
// makes sense for actors who have stock to sell. Visitors and
// non-vendor PCs typically have no inventory rows at all — gating
// the validation on this avoids rejecting buyer-side speech like
// Jeremiah Soames saying "I'd like bread and ale" against his own
// (empty) inventory.
//
// A vendor whose stock has bottomed out (zero rows) effectively
// bypasses the strict check too — accepted trade-off, since the
// role-prompt directive ("you can only sell items in your inventory
// list") and the empty perception inventory line already discourage
// hallucinating sales of non-existent stock.
func (app *App) actorHasAnyInventory(ctx context.Context, actorID string) bool {
	var has bool
	err := app.DB.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM actor_inventory
		                 WHERE actor_id = $1 AND quantity > 0)`,
		actorID,
	).Scan(&has)
	if err != nil {
		// On error, default to "yes" so the strict validation still
		// runs — fail-safe toward the pre-WORK-223 behavior.
		log.Printf("actorHasAnyInventory(%s): %v", actorID, err)
		return true
	}
	return has
}

// validateMentionsAgainstInventory returns the subset of mentions that
// are NOT present in the speaker's actor_inventory (or don't exist in
// item_kind at all — both fail the same predicate). Empty result means
// every mention is valid. Used by the speak tool to reject speech
// referencing goods the speaker doesn't have, so the customer's
// pay-dropdown population is grounded in real stock.
func (app *App) validateMentionsAgainstInventory(ctx context.Context, actorID string, mentions []string) ([]string, error) {
	if len(mentions) == 0 {
		return nil, nil
	}
	rows, err := app.DB.Query(ctx, `
		SELECT m.name
		  FROM unnest($1::text[]) AS m(name)
		 WHERE NOT EXISTS (
		     SELECT 1 FROM actor_inventory ai
		      WHERE ai.actor_id = $2 AND ai.item_kind = m.name
		 )
	`, mentions, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bogus []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		bogus = append(bogus, name)
	}
	return bogus, rows.Err()
}

// actTransferVerbsRegex matches transfer-implying past-tense verbs that
// must not appear in an act verb_phrase alongside an item name. Item
// transfers route through serve (gift), pay + deliver_order (purchase),
// or give — never narrated via act, which is pure prose with no
// mechanical effect. ZBBS-HOME-265 follow-up to ZBBS-WORK-227: the
// original prose-mention gate caught items the speaker DIDN'T have, but
// a vendor with stock in hand could still narrate "handed Ezekiel a
// bowl of stew" via act without actually transferring the stew. The
// chat panel rendered the fabrication as a fait accompli; the
// recipient's `consume` then rejected with "you have no stew", because
// nothing had really moved.
//
// Verb list is intentionally conservative — clear transfer verbs only.
// Ambiguous candidates ("offered" can mean a verbal price offer;
// "poured" / "brought" can be intransitive movement) are excluded to
// avoid false rejections on flavor narration. False negatives get added
// here as they surface in play.
var actTransferVerbsRegex = regexp.MustCompile(
	`(?i)\b(handed|gave|served|delivered|dished|ladled|doled)\b`,
)

// extractActTransferVerb returns the first transfer-implying verb found
// in text (lowercased), or "" if none match. Used by the act handler to
// reject verb_phrases that imply real item transfers.
func extractActTransferVerb(text string) string {
	return strings.ToLower(actTransferVerbsRegex.FindString(text))
}

// knownItemKinds lazily loads the canonical item_kind names and a
// pre-compiled word-boundary regex that matches any of them in free-
// form prose. Built once per engine lifetime. Returns nil with no
// error when the catalog is empty.
func (app *App) knownItemKinds(ctx context.Context) (*itemKindCache, error) {
	app.ItemKindMu.RLock()
	if app.ItemKindCache != nil {
		c := app.ItemKindCache
		app.ItemKindMu.RUnlock()
		return c, nil
	}
	app.ItemKindMu.RUnlock()

	app.ItemKindMu.Lock()
	defer app.ItemKindMu.Unlock()
	// Double-check under the write lock.
	if app.ItemKindCache != nil {
		return app.ItemKindCache, nil
	}

	rows, err := app.DB.Query(ctx, `SELECT name FROM item_kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, nil
	}

	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, regexp.QuoteMeta(n))
	}
	// (?i) case-insensitive, \b word boundaries so "ale" doesn't match
	// "sale" or "scale". Alternation across all kinds compiles once.
	re, err := regexp.Compile(`(?i)\b(` + strings.Join(parts, "|") + `)\b`)
	if err != nil {
		return nil, err
	}
	app.ItemKindCache = &itemKindCache{names: names, regex: re}
	return app.ItemKindCache, nil
}

// extractImplicitItemMentions returns item_kind names that appear in
// free-form prose. Used by the speak / act handlers to back-fill the
// mention validation gate when an LLM emits item-naming text without
// declaring the items in the structured mentions[] field.
//
// ZBBS-WORK-227: pre-WORK-227 the LLM could bypass speak validation
// by emitting mentions: null while keeping item-naming text. Same
// shape on act.verb_phrase. This function plus the merge into the
// existing validateMentionsAgainstInventory path closes both bypasses.
//
// Returns deduped, lowercased names. Empty result means no item
// names found in the text (or catalog unavailable — fail-open since
// the structured-field validation remains in place).
func (app *App) extractImplicitItemMentions(ctx context.Context, text string) []string {
	if text == "" {
		return nil
	}
	cache, err := app.knownItemKinds(ctx)
	if err != nil {
		log.Printf("extractImplicitItemMentions: load item_kind catalog: %v", err)
		return nil
	}
	if cache == nil || cache.regex == nil {
		return nil
	}
	matches := cache.regex.FindAllString(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		lower := strings.ToLower(m)
		if _, ok := seen[lower]; !ok {
			seen[lower] = struct{}{}
			out = append(out, lower)
		}
	}
	return out
}

// mergeMentions combines declared mentions[] with implicit names
// extracted from prose, deduped on lowercased value. Declared order
// is preserved; implicit names are appended in scan order.
func mergeMentions(declared, implicit []string) []string {
	if len(implicit) == 0 {
		return declared
	}
	seen := make(map[string]struct{}, len(declared)+len(implicit))
	for _, m := range declared {
		seen[strings.ToLower(m)] = struct{}{}
	}
	out := append([]string(nil), declared...)
	for _, m := range implicit {
		key := strings.ToLower(m)
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			out = append(out, m)
		}
	}
	return out
}

// inventoryLine builds the "ale x3, bread x1" comma-separated string
// surfaced in agent perception. Returns "" when the actor carries
// nothing — the caller suppresses the whole "Your inventory:" line in
// that case rather than rendering "nothing." Ordered by item_kind's
// configured sort_order so categories cluster (drinks before food
// before materials).
func (app *App) inventoryLine(ctx context.Context, actorID string) string {
	rows, err := app.DB.Query(ctx,
		`SELECT k.name, ai.quantity, COALESCE(k.capabilities, '{}'::text[])
		   FROM actor_inventory ai
		   JOIN item_kind k ON k.name = ai.item_kind
		  WHERE ai.actor_id = $1
		  ORDER BY k.sort_order, k.name`,
		actorID,
	)
	if err != nil {
		log.Printf("inventory: load %s: %v", actorID, err)
		return ""
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var (
			name         string
			qty          int
			capabilities []string
		)
		if err := rows.Scan(&name, &qty, &capabilities); err != nil {
			log.Printf("inventory: scan row for %s: %v", actorID, err)
			continue
		}
		// Service-capability items (e.g. nights_stay) carry a sentinel qty
		// in actor_inventory so the JOIN finds them, but the "xN" suffix
		// reads as a count when the row really represents a capacity-
		// based offering. Render service rows without the qty so the LLM
		// sees "nights_stay" instead of "nights_stay x1".
		if hasCapability(capabilities, "service") {
			parts = append(parts, name)
		} else {
			parts = append(parts, fmt.Sprintf("%s x%d", name, qty))
		}
	}
	return strings.Join(parts, ", ")
}

// consumeResult mirrors payResult's shape so the dispatcher in
// executeAgentCommit can build (result, errStr) pairs without
// duplicating switch logic.
type consumeResult struct {
	Result      string
	Err         string
	BuyerNewQty int               // post-consumption count in buyer's inventory
	NeedsAfter  consumptionResult // empty if item is non-consumable / consume_amount was zero
}


// executeConsume decrements the buyer's stock of `itemKind` by `qty`
// and applies the configured satisfaction to the linked need via
// applyConsumption. Items with NULL satisfies_attribute (materials)
// are rejected with a clear message — you can't eat raw wheat.
func (app *App) executeConsume(ctx context.Context, buyer *agentNPCRow, itemKind string, qty int) consumeResult {
	if qty <= 0 {
		return consumeResult{Result: "rejected", Err: "qty must be positive"}
	}
	itemKind = strings.TrimSpace(strings.ToLower(itemKind))
	if itemKind == "" {
		return consumeResult{Result: "rejected", Err: "missing item"}
	}

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return consumeResult{Result: "failed", Err: fmt.Sprintf("begin tx: %v", err)}
	}
	defer tx.Rollback(ctx)

	// Validate the item exists (item_kind row required) so we fail
	// fast on typos before locking inventory. The actual satisfactions
	// are loaded from item_satisfies (ZBBS-125) — a row in item_kind
	// with no item_satisfies rows is a material (wheat/iron) which
	// rejects "isn't a consumable" the same as before.
	var itemExists bool
	if err := tx.QueryRow(ctx,
		`SELECT TRUE FROM item_kind WHERE name = $1`,
		itemKind,
	).Scan(&itemExists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return consumeResult{Result: "rejected", Err: fmt.Sprintf("no such item %q", itemKind)}
		}
		return consumeResult{Result: "failed", Err: fmt.Sprintf("look up item: %v", err)}
	}
	satisfactions, err := loadItemSatisfactions(ctx, tx, itemKind)
	if err != nil {
		return consumeResult{Result: "failed", Err: fmt.Sprintf("load satisfactions: %v", err)}
	}
	if len(satisfactions) == 0 {
		return consumeResult{Result: "rejected", Err: fmt.Sprintf("%s isn't a consumable", itemKind)}
	}

	// Lock buyer's inventory row.
	var qtyHave int
	if err := tx.QueryRow(ctx,
		`SELECT quantity FROM actor_inventory
		  WHERE actor_id = $1 AND item_kind = $2
		  FOR UPDATE`,
		buyer.ID, itemKind,
	).Scan(&qtyHave); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return consumeResult{Result: "rejected", Err: fmt.Sprintf("you have no %s", itemKind)}
		}
		return consumeResult{Result: "failed", Err: fmt.Sprintf("lock inventory: %v", err)}
	}
	if qtyHave < qty {
		return consumeResult{Result: "rejected", Err: fmt.Sprintf("you have only %d %s (tried to consume %d)", qtyHave, itemKind, qty)}
	}

	newQty := qtyHave - qty
	if newQty == 0 {
		if _, err := tx.Exec(ctx,
			`DELETE FROM actor_inventory WHERE actor_id = $1 AND item_kind = $2`,
			buyer.ID, itemKind,
		); err != nil {
			return consumeResult{Result: "failed", Err: fmt.Sprintf("delete row: %v", err)}
		}
	} else {
		if _, err := tx.Exec(ctx,
			`UPDATE actor_inventory SET quantity = $1
			  WHERE actor_id = $2 AND item_kind = $3`,
			newQty, buyer.ID, itemKind,
		); err != nil {
			return consumeResult{Result: "failed", Err: fmt.Sprintf("decrement row: %v", err)}
		}
	}

	// Build the consumption delta from every (attribute, amount) row
	// in item_satisfies (ZBBS-125). Multi-effect items like ale
	// (thirst -4, hunger -2 per unit) drop both needs in one consume.
	// Unknown attributes are silently skipped — defense in depth, see
	// applySatisfactionsToDelta's switch.
	delta := applySatisfactionsToDelta(consumptionDelta{}, satisfactions, qty)

	var needsAfter consumptionResult
	if delta.Hunger != 0 || delta.Thirst != 0 || delta.Tiredness != 0 {
		needsAfter, err = app.applyConsumption(ctx, tx, buyer.ID, delta)
		if err != nil {
			return consumeResult{Result: "failed", Err: fmt.Sprintf("apply consumption: %v", err)}
		}
	}

	// Item dwell (ZBBS-172). Stamp dwell credits for satisfactions
	// that carry a dwell triple. The buyer must be standing at a
	// named structure for dwell to take effect — eating on the road
	// gets only the immediate hit, no per-tick payoff. Position is
	// resolved via the LOCKED actor row so a parallel move can't
	// pin dwell to a stale structure.
	dwellStructureID, err := app.resolveLoiterStructureLocked(ctx, tx, buyer.ID)
	if err != nil {
		return consumeResult{Result: "failed", Err: fmt.Sprintf("resolve dwell structure: %v", err)}
	}
	if err := app.upsertItemDwellCredits(ctx, tx, buyer.ID, satisfactions, dwellStructureID); err != nil {
		return consumeResult{Result: "failed", Err: fmt.Sprintf("upsert dwell credits: %v", err)}
	}

	if err := tx.Commit(ctx); err != nil {
		return consumeResult{Result: "failed", Err: fmt.Sprintf("commit: %v", err)}
	}

	app.Hub.Broadcast(WorldEvent{
		Type: "actor_inventory_changed",
		Data: map[string]any{
			"actor_id":  buyer.ID,
			"item_kind": itemKind,
			"quantity":  newQty,
		},
	})
	if delta.Hunger != 0 || delta.Thirst != 0 || delta.Tiredness != 0 {
		app.Hub.Broadcast(WorldEvent{
			Type: "npc_needs_changed",
			Data: map[string]any{
				"id":        buyer.ID,
				"hunger":    needsAfter.Hunger,
				"thirst":    needsAfter.Thirst,
				"tiredness": needsAfter.Tiredness,
			},
		})
	}

	return consumeResult{
		Result:      "ok",
		BuyerNewQty: newQty,
		NeedsAfter:  needsAfter,
	}
}
