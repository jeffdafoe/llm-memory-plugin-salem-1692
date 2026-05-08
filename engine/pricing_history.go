package main

// Pricing history blocks for the pay-deliberation prompt (ZBBS-171).
//
// Origin: 2026-05-08 sale-data audit found 7 counter chains in the
// corpus. Sellers were improvising "fair prices" at each deliberation
// with no awareness of:
//   - what they had charged for the same item in prior scenes
//   - what they had charged this specific buyer before
//   - whether the buyer had recently sold to them (reciprocity context)
//
// Without these anchors the LLM was producing inconsistent prices
// (#21 water countered to same as offered, #45 milk countered DOWN
// from a generous offer) and never doing what merchants actually do:
// remembering customers, charging-relative-to-history, reciprocating
// recent goodwill from regulars.
//
// This file holds the data fetch + render helpers used by
// runPayDeliberation. Phase 1 ships seller-side only (the deliberating
// actor is the seller). Phase 2 will add buyer-side analogues into the
// regular agent_tick perception so buyers also see their own purchase
// history when picking an offered_amount.
//
// Conventions:
//   - Per-unit prices everywhere. Cross-deal comparison only makes
//     sense if you normalize qty out — "1 stew at 2 coins" and
//     "5 stew at 10 coins" are the same unit-price.
//   - Suppress empty / trivial blocks. A seller with one sale of an
//     item gets a single-line "30d: 2 coins (1 sale to Ezekiel)" —
//     not a 5-line block with min == max == low == high.
//   - "Where" the sale happened comes from
//     pay_ledger.huddle_id → scene_huddle.structure_id →
//     village_object.display_name. Outdoor / NULL-huddle deals
//     render as no-structure (the counterparty name alone).
//   - Reciprocity depth = 3 most recent cross-role transactions
//     across all items, with the perceiver's per-item 30d range
//     appended so the LLM can read "I paid 3 — that was on the high
//     end of my horseshoe range."

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"
)

// historyEntry is one accepted-pay row, normalized to per-unit pricing
// and tagged with counterparty + structure for prose rendering.
type historyEntry struct {
	UnitPrice    int
	Qty          int
	OfferedTotal int
	ItemKind     string
	Counterparty string
	Structure    string // empty when huddle was outdoors / unresolved
	CreatedAt    time.Time
}

// historyStats summarizes a window-bucket of historyEntry rows. Low and
// High are the actual rows at the min and max unit prices (most-recent
// wins on tie) so the renderer can attach counterparty + structure
// context. Count is total accepted sales in the window.
type historyStats struct {
	Count   int
	MinUnit int
	MaxUnit int
	Low     historyEntry
	High    historyEntry
}

// fetchActorCoins returns the actor's current coin balance. Used by
// the deliberation prompt's coin-context block — the actor knows their
// own purse, even if they don't see the counterparty's.
func (app *App) fetchActorCoins(ctx context.Context, actorID string) (int, error) {
	var coins int
	err := app.DB.QueryRow(ctx, `SELECT coins FROM actor WHERE id = $1::uuid`, actorID).Scan(&coins)
	if err != nil {
		return 0, fmt.Errorf("fetch actor coins: %w", err)
	}
	return coins, nil
}

// fetchSellerSales returns the seller's accepted sales of itemKind
// within the last 30 days, in created_at DESC order. The 7d bucket is
// derived in-Go by filtering this list. One query per deliberation —
// the corpus is small enough (low thousands of rows total) that an
// in-Go aggregate is cheaper than two FILTERed window queries.
func (app *App) fetchSellerSales(ctx context.Context, sellerID, itemKind string) ([]historyEntry, error) {
	rows, err := app.DB.Query(ctx, `
		SELECT pl.offered_amount, pl.qty, pl.created_at,
		       b.display_name AS counterparty,
		       COALESCE(vo.display_name, '') AS structure_name
		  FROM pay_ledger pl
		  JOIN actor b ON b.id = pl.buyer_id
		  LEFT JOIN scene_huddle sh ON sh.id = pl.huddle_id
		  LEFT JOIN village_object vo ON vo.id = sh.structure_id
		 WHERE pl.seller_id = $1::uuid
		   AND pl.item_kind = $2
		   AND pl.state = 'accepted'
		   AND pl.created_at > NOW() - INTERVAL '30 days'
		 ORDER BY pl.created_at DESC
	`, sellerID, itemKind)
	if err != nil {
		return nil, fmt.Errorf("fetch seller sales: %w", err)
	}
	defer rows.Close()

	var out []historyEntry
	for rows.Next() {
		var e historyEntry
		var qty sql.NullInt32
		if err := rows.Scan(&e.OfferedTotal, &qty, &e.CreatedAt, &e.Counterparty, &e.Structure); err != nil {
			return nil, fmt.Errorf("scan seller sale: %w", err)
		}
		e.Qty = 1
		if qty.Valid && qty.Int32 > 0 {
			e.Qty = int(qty.Int32)
		}
		// Per-unit, integer-truncated. A 5-coin offer for 2 stew is
		// "2 per unit" — fine for anchoring; the LLM doesn't need
		// fractional precision.
		e.UnitPrice = e.OfferedTotal / e.Qty
		e.ItemKind = itemKind
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate seller sales: %w", err)
	}
	return out, nil
}

// fetchPerceiverPurchases returns the actor's accepted purchases FROM
// the named counterparty across all items, last 30d, in created_at
// DESC. Used for the reciprocity block — the deliberating seller sees
// what they recently bought from the current buyer.
//
// limit caps the number of rows returned so the prompt doesn't bloat
// for chatty regulars.
func (app *App) fetchPerceiverPurchases(ctx context.Context, perceiverID, counterpartyID string, limit int) ([]historyEntry, error) {
	if limit <= 0 {
		limit = 3
	}
	rows, err := app.DB.Query(ctx, `
		SELECT pl.offered_amount, pl.qty, pl.item_kind, pl.created_at
		  FROM pay_ledger pl
		 WHERE pl.buyer_id = $1::uuid
		   AND pl.seller_id = $2::uuid
		   AND pl.state = 'accepted'
		   AND pl.item_kind IS NOT NULL
		   AND pl.created_at > NOW() - INTERVAL '30 days'
		 ORDER BY pl.created_at DESC
		 LIMIT $3
	`, perceiverID, counterpartyID, limit)
	if err != nil {
		return nil, fmt.Errorf("fetch perceiver purchases: %w", err)
	}
	defer rows.Close()

	var out []historyEntry
	for rows.Next() {
		var e historyEntry
		var qty sql.NullInt32
		if err := rows.Scan(&e.OfferedTotal, &qty, &e.ItemKind, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan perceiver purchase: %w", err)
		}
		e.Qty = 1
		if qty.Valid && qty.Int32 > 0 {
			e.Qty = int(qty.Int32)
		}
		e.UnitPrice = e.OfferedTotal / e.Qty
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate perceiver purchases: %w", err)
	}
	return out, nil
}

// fetchPerceiverItemRanges returns the actor's per-item purchase range
// over the last 30 days as buyer (across all sellers). Key is item_kind.
// Used by the reciprocity block to attach a delta-context phrase to
// each recent cross-role purchase ("you paid 3 — your horseshoe range
// is 2-4 over 30d, 5 buys").
func (app *App) fetchPerceiverItemRanges(ctx context.Context, perceiverID string) (map[string]historyStats, error) {
	rows, err := app.DB.Query(ctx, `
		SELECT pl.item_kind,
		       MIN(pl.offered_amount / GREATEST(COALESCE(pl.qty, 1), 1)) AS min_unit,
		       MAX(pl.offered_amount / GREATEST(COALESCE(pl.qty, 1), 1)) AS max_unit,
		       COUNT(*) AS sale_count
		  FROM pay_ledger pl
		 WHERE pl.buyer_id = $1::uuid
		   AND pl.state = 'accepted'
		   AND pl.item_kind IS NOT NULL
		   AND pl.created_at > NOW() - INTERVAL '30 days'
		 GROUP BY pl.item_kind
	`, perceiverID)
	if err != nil {
		return nil, fmt.Errorf("fetch perceiver item ranges: %w", err)
	}
	defer rows.Close()

	out := map[string]historyStats{}
	for rows.Next() {
		var item string
		var s historyStats
		if err := rows.Scan(&item, &s.MinUnit, &s.MaxUnit, &s.Count); err != nil {
			return nil, fmt.Errorf("scan perceiver item range: %w", err)
		}
		out[item] = s
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate perceiver item ranges: %w", err)
	}
	return out, nil
}

// summarizeWindow buckets entries created within the given duration of
// `now` and returns the min/max/count + the rows at min and max. Tie-
// breaking on Low/High: most-recent entry wins (so the prose anchors
// on the freshest extreme rather than a stale outlier).
func summarizeWindow(entries []historyEntry, now time.Time, window time.Duration) historyStats {
	cutoff := now.Add(-window)
	var stats historyStats
	stats.MinUnit = math.MaxInt
	stats.MaxUnit = math.MinInt
	for _, e := range entries {
		if e.CreatedAt.Before(cutoff) {
			continue
		}
		stats.Count++
		if e.UnitPrice < stats.MinUnit ||
			(e.UnitPrice == stats.MinUnit && e.CreatedAt.After(stats.Low.CreatedAt)) {
			stats.MinUnit = e.UnitPrice
			stats.Low = e
		}
		if e.UnitPrice > stats.MaxUnit ||
			(e.UnitPrice == stats.MaxUnit && e.CreatedAt.After(stats.High.CreatedAt)) {
			stats.MaxUnit = e.UnitPrice
			stats.High = e
		}
	}
	if stats.Count == 0 {
		return historyStats{} // zero-value: Count==0 signals "empty"
	}
	return stats
}

// summarizeFiltered is summarizeWindow with an additional counterparty
// filter — used for the "From <buyer>" line scoped to a specific
// counterparty.
func summarizeFiltered(entries []historyEntry, now time.Time, window time.Duration, counterparty string) historyStats {
	cutoff := now.Add(-window)
	var stats historyStats
	stats.MinUnit = math.MaxInt
	stats.MaxUnit = math.MinInt
	for _, e := range entries {
		if e.CreatedAt.Before(cutoff) {
			continue
		}
		if !strings.EqualFold(e.Counterparty, counterparty) {
			continue
		}
		stats.Count++
		if e.UnitPrice < stats.MinUnit ||
			(e.UnitPrice == stats.MinUnit && e.CreatedAt.After(stats.Low.CreatedAt)) {
			stats.MinUnit = e.UnitPrice
			stats.Low = e
		}
		if e.UnitPrice > stats.MaxUnit ||
			(e.UnitPrice == stats.MaxUnit && e.CreatedAt.After(stats.High.CreatedAt)) {
			stats.MaxUnit = e.UnitPrice
			stats.High = e
		}
	}
	if stats.Count == 0 {
		return historyStats{}
	}
	return stats
}

// renderSaleHistoryBlock builds the seller-side sale-history prose for
// the deliberation prompt. Returns "" when the seller has no accepted
// sales of this item in the last 30d (suppress trivial / empty).
//
// Format:
//
//   Your <item> sale history (accepted, per unit):
//     7d:  1-3 coins (12 sales). Low: 1 to Ezekiel @ General Store. High: 3 to Goody @ General Store.
//     30d: 1-5 coins (40 sales). Low: 1 to Ezekiel. High: 5 to Mrs Hawkins @ General Store.
//     From <buyer>: 7d 1-2 (3 sales), 30d 1-3 (8 sales).
//
// When min == max for a window (one price across all sales), we collapse
// to a single line without the Low/High suffix. When count == 1, we
// elide the range and just show the single sale.
func renderSaleHistoryBlock(entries []historyEntry, itemKind, buyerName string, now time.Time) string {
	if len(entries) == 0 {
		return ""
	}
	overall7d := summarizeWindow(entries, now, 7*24*time.Hour)
	overall30d := summarizeWindow(entries, now, 30*24*time.Hour)
	from7d := summarizeFiltered(entries, now, 7*24*time.Hour, buyerName)
	from30d := summarizeFiltered(entries, now, 30*24*time.Hour, buyerName)

	var b strings.Builder
	fmt.Fprintf(&b, "Your %s sale history (accepted, per unit):\n", itemKind)
	if overall7d.Count > 0 {
		fmt.Fprintf(&b, "  7d:  %s\n", formatStatsLine(overall7d))
	}
	if overall30d.Count > 0 {
		fmt.Fprintf(&b, "  30d: %s\n", formatStatsLine(overall30d))
	}
	if from30d.Count > 0 {
		fmt.Fprintf(&b, "  From %s: %s\n", buyerName, formatPairCompact(from7d, from30d))
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatStatsLine renders the long form: range + count + low/high
// counterparty hints. Collapses to "X coins (1 sale to Y @ Z)" for
// single-row windows, "X coins (N sales)" when min == max.
func formatStatsLine(s historyStats) string {
	if s.Count == 0 {
		return ""
	}
	if s.Count == 1 {
		return fmt.Sprintf("%d coins (1 sale to %s%s)",
			s.Low.UnitPrice, s.Low.Counterparty, formatStructure(s.Low.Structure))
	}
	if s.MinUnit == s.MaxUnit {
		return fmt.Sprintf("%d coins (%d sales)", s.MinUnit, s.Count)
	}
	return fmt.Sprintf("%d-%d coins (%d sales). Low: %d to %s%s. High: %d to %s%s.",
		s.MinUnit, s.MaxUnit, s.Count,
		s.MinUnit, s.Low.Counterparty, formatStructure(s.Low.Structure),
		s.MaxUnit, s.High.Counterparty, formatStructure(s.High.Structure))
}

// formatPairCompact renders two windows on one line for the
// "From <buyer>" sub-line. Only the range + count, no low/high since
// the counterparty is fixed.
func formatPairCompact(s7d, s30d historyStats) string {
	parts := []string{}
	if s7d.Count > 0 {
		parts = append(parts, "7d "+formatRangeCompact(s7d))
	}
	if s30d.Count > 0 {
		parts = append(parts, "30d "+formatRangeCompact(s30d))
	}
	return strings.Join(parts, ", ")
}

// formatRangeCompact renders just the price range + sale count.
// "2-3 (4 sales)" or "2 (1 sale)" for collapsed cases.
func formatRangeCompact(s historyStats) string {
	if s.Count == 0 {
		return ""
	}
	saleWord := "sales"
	if s.Count == 1 {
		saleWord = "sale"
	}
	if s.MinUnit == s.MaxUnit {
		return fmt.Sprintf("%d (%d %s)", s.MinUnit, s.Count, saleWord)
	}
	return fmt.Sprintf("%d-%d (%d %s)", s.MinUnit, s.MaxUnit, s.Count, saleWord)
}

// formatStructure returns a " @ X" suffix when the structure is
// non-empty, "" otherwise. Outdoor / NULL-huddle sales render with
// just the counterparty name.
func formatStructure(structure string) string {
	if structure == "" {
		return ""
	}
	return " @ " + structure
}

// renderReciprocityBlock builds the cross-role prose for the
// deliberation prompt — recent purchases the seller made FROM the
// current buyer, with each row's per-item range appended for delta
// framing. Returns "" when the seller has no recent purchases from
// this buyer.
//
// Format:
//
//   Reciprocity — your purchases from <buyer>:
//     3 days ago: 1 horseshoe at 3 coins each. (Your horseshoe range overall: 30d 2-4, 5 buys.)
//     12 days ago: 2 nails at 2 coins each. (Your nail range overall: 30d 1-3, 8 buys.)
//
// When the perceiver has only the one purchase of an item (range
// would be a single value), suppress the range parenthetical — no
// useful delta context if there's nothing to compare against.
func renderReciprocityBlock(purchases []historyEntry, ranges map[string]historyStats, counterpartyName string, now time.Time) string {
	if len(purchases) == 0 {
		return ""
	}
	// Determinism: the fetch already returned in created_at DESC, but
	// guard against caller drift.
	sorted := make([]historyEntry, len(purchases))
	copy(sorted, purchases)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
	})

	var b strings.Builder
	fmt.Fprintf(&b, "Reciprocity — your purchases from %s:\n", counterpartyName)
	for _, p := range sorted {
		ago := relativeAge(now.Sub(p.CreatedAt))
		coinWord := "coins"
		if p.UnitPrice == 1 {
			coinWord = "coin"
		}
		line := fmt.Sprintf("  %s: %d %s at %d %s each", ago, p.Qty, p.ItemKind, p.UnitPrice, coinWord)

		if r, ok := ranges[p.ItemKind]; ok && r.Count > 1 {
			if r.MinUnit == r.MaxUnit {
				line += fmt.Sprintf(". (Your %s purchases overall: 30d %d, %d buys.)",
					p.ItemKind, r.MinUnit, r.Count)
			} else {
				line += fmt.Sprintf(". (Your %s purchases overall: 30d %d-%d, %d buys.)",
					p.ItemKind, r.MinUnit, r.MaxUnit, r.Count)
			}
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// fetchTopBuyerItems returns the actor's top-N most-purchased item kinds
// over the last 30 days, with all per-item entries bundled. Used by the
// regular agent_tick perception (ZBBS-171 Phase 2) so buyers anchor on
// what they've actually paid for things before picking offered_amount on
// their next pay() call. Phase 1 covered the seller side at deliberation
// time; this surfaces the data to buyers continuously so the first offer
// lands in-range instead of triggering a counter-offer round-trip.
//
// Returns map keyed by item_kind. The renderer sorts before display.
// Empty map means no purchase history in the window — caller suppresses.
func (app *App) fetchTopBuyerItems(ctx context.Context, buyerID string, limit int) (map[string][]historyEntry, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := app.DB.Query(ctx, `
		SELECT pl.offered_amount, pl.qty, pl.item_kind, pl.created_at,
		       s.display_name AS counterparty,
		       COALESCE(vo.display_name, '') AS structure_name
		  FROM pay_ledger pl
		  JOIN actor s ON s.id = pl.seller_id
		  LEFT JOIN scene_huddle sh ON sh.id = pl.huddle_id
		  LEFT JOIN village_object vo ON vo.id = sh.structure_id
		 WHERE pl.buyer_id = $1::uuid
		   AND pl.state = 'accepted'
		   AND pl.item_kind IS NOT NULL
		   AND pl.created_at > NOW() - INTERVAL '30 days'
		 ORDER BY pl.created_at DESC
	`, buyerID)
	if err != nil {
		return nil, fmt.Errorf("fetch top buyer items: %w", err)
	}
	defer rows.Close()

	perItem := map[string][]historyEntry{}
	for rows.Next() {
		var e historyEntry
		var qty sql.NullInt32
		if err := rows.Scan(&e.OfferedTotal, &qty, &e.ItemKind, &e.CreatedAt, &e.Counterparty, &e.Structure); err != nil {
			return nil, fmt.Errorf("scan top buyer item: %w", err)
		}
		e.Qty = 1
		if qty.Valid && qty.Int32 > 0 {
			e.Qty = int(qty.Int32)
		}
		e.UnitPrice = e.OfferedTotal / e.Qty
		perItem[e.ItemKind] = append(perItem[e.ItemKind], e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate top buyer items: %w", err)
	}
	return truncateTopByCount(perItem, limit), nil
}

// fetchTopSellerItems mirrors fetchTopBuyerItems on the seller side so
// the regular agent_tick perception anchors quoting (speak.price outside
// pay-deliberation) on the seller's recent sale ranges. Phase 1 already
// shows this at deliberation time via fetchSellerSales; this block is
// for the casual "merchant says a price in conversation" path.
func (app *App) fetchTopSellerItems(ctx context.Context, sellerID string, limit int) (map[string][]historyEntry, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := app.DB.Query(ctx, `
		SELECT pl.offered_amount, pl.qty, pl.item_kind, pl.created_at,
		       b.display_name AS counterparty,
		       COALESCE(vo.display_name, '') AS structure_name
		  FROM pay_ledger pl
		  JOIN actor b ON b.id = pl.buyer_id
		  LEFT JOIN scene_huddle sh ON sh.id = pl.huddle_id
		  LEFT JOIN village_object vo ON vo.id = sh.structure_id
		 WHERE pl.seller_id = $1::uuid
		   AND pl.state = 'accepted'
		   AND pl.item_kind IS NOT NULL
		   AND pl.created_at > NOW() - INTERVAL '30 days'
		 ORDER BY pl.created_at DESC
	`, sellerID)
	if err != nil {
		return nil, fmt.Errorf("fetch top seller items: %w", err)
	}
	defer rows.Close()

	perItem := map[string][]historyEntry{}
	for rows.Next() {
		var e historyEntry
		var qty sql.NullInt32
		if err := rows.Scan(&e.OfferedTotal, &qty, &e.ItemKind, &e.CreatedAt, &e.Counterparty, &e.Structure); err != nil {
			return nil, fmt.Errorf("scan top seller item: %w", err)
		}
		e.Qty = 1
		if qty.Valid && qty.Int32 > 0 {
			e.Qty = int(qty.Int32)
		}
		e.UnitPrice = e.OfferedTotal / e.Qty
		perItem[e.ItemKind] = append(perItem[e.ItemKind], e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate top seller items: %w", err)
	}
	return truncateTopByCount(perItem, limit), nil
}

// truncateTopByCount keeps the top-N items by entry count, ties broken
// by most-recent transaction. Returns the input unchanged when already
// at or below the cap. Deterministic ordering across re-renders is
// handled at render time, not here — this only enforces the cap.
func truncateTopByCount(perItem map[string][]historyEntry, limit int) map[string][]historyEntry {
	if len(perItem) <= limit {
		return perItem
	}
	type kindRank struct {
		kind   string
		count  int
		recent time.Time
	}
	ranked := make([]kindRank, 0, len(perItem))
	for k, entries := range perItem {
		var mostRecent time.Time
		for _, e := range entries {
			if e.CreatedAt.After(mostRecent) {
				mostRecent = e.CreatedAt
			}
		}
		ranked = append(ranked, kindRank{kind: k, count: len(entries), recent: mostRecent})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].count != ranked[j].count {
			return ranked[i].count > ranked[j].count
		}
		return ranked[i].recent.After(ranked[j].recent)
	})
	out := make(map[string][]historyEntry, limit)
	for i := 0; i < limit; i++ {
		out[ranked[i].kind] = perItem[ranked[i].kind]
	}
	return out
}

// renderRecentPurchasesPerception builds the buyer-side perception block.
// Returns "" when the input is empty so callers can append unconditionally
// without producing a header line above no data.
//
// Format:
//
//   Your recent purchase history (per unit, last 30d):
//     water: 1-3 coins (8 buys). Recent: 2 from John Ellis @ Tavern.
//       From John Ellis: 2 coins (5 buys).
//     stew: 2-5 coins (4 buys). Recent: 5 from John Ellis @ Tavern.
//     bread: 2 coins (1 buy from John Ellis @ Tavern).
//
// Single-buy items collapse to one line with the counterparty inline.
// min == max collapses the range to a single price but still shows a
// Recent: line for counterparty/structure context. The indented "From
// <peer>:" sub-line (Phase 2b) appears for items where the perceiver
// has accepted-purchase history with the specified counterparty AND
// that counterparty's count is < the overall count (no point repeating
// the parent line). Pass "" for counterparty to suppress the sub-line
// across the whole block.
func renderRecentPurchasesPerception(perItem map[string][]historyEntry, now time.Time, counterparty string) string {
	return renderRecentTransactionPerception(perItem, now, "purchase", "buy", "buys", "from", counterparty)
}

// renderRecentSalesPerception is the seller-side mirror of
// renderRecentPurchasesPerception. Same shape, sale vocabulary.
func renderRecentSalesPerception(perItem map[string][]historyEntry, now time.Time, counterparty string) string {
	return renderRecentTransactionPerception(perItem, now, "sale", "sale", "sales", "to", counterparty)
}

// renderRecentTransactionPerception is the shared body used by the buyer
// and seller perception renderers. headingNoun goes into the header
// ("purchase" / "sale"), singular/plural noun the parenthetical count
// ("buy"/"buys", "sale"/"sales"), and prep the "from"/"to" preposition
// for the counterparty. counterparty (if non-empty) drives the per-item
// drill-down sub-line — see renderRecentPurchasesPerception's comment.
//
// Items render in count-DESC order, ties broken by item_kind alpha
// (deterministic — recency would re-shuffle the block on every tick and
// the LLM treats list order as somewhat meaningful).
func renderRecentTransactionPerception(perItem map[string][]historyEntry, now time.Time, headingNoun, singular, plural, prep, counterparty string) string {
	if len(perItem) == 0 {
		return ""
	}
	type itemLine struct {
		kind  string
		count int
		line  string
	}
	var lines []itemLine
	for kind, entries := range perItem {
		if len(entries) == 0 {
			continue
		}
		stats := summarizeWindow(entries, now, 30*24*time.Hour)
		if stats.Count == 0 {
			continue
		}
		recentEntry := entries[0]
		for _, e := range entries[1:] {
			if e.CreatedAt.After(recentEntry.CreatedAt) {
				recentEntry = e
			}
		}
		var line string
		switch {
		case stats.Count == 1:
			line = fmt.Sprintf("  %s: %d coins (1 %s %s %s%s).",
				kind, stats.Low.UnitPrice, singular, prep,
				recentEntry.Counterparty, formatStructure(recentEntry.Structure))
		case stats.MinUnit == stats.MaxUnit:
			line = fmt.Sprintf("  %s: %d coins (%d %s). Recent: %d %s %s%s.",
				kind, stats.MinUnit, stats.Count, plural,
				recentEntry.UnitPrice, prep,
				recentEntry.Counterparty, formatStructure(recentEntry.Structure))
		default:
			line = fmt.Sprintf("  %s: %d-%d coins (%d %s). Recent: %d %s %s%s.",
				kind, stats.MinUnit, stats.MaxUnit, stats.Count, plural,
				recentEntry.UnitPrice, prep,
				recentEntry.Counterparty, formatStructure(recentEntry.Structure))
		}
		// Phase 2b drill-down: per-counterparty range when the perceiver
		// is currently engaged with a peer they've transacted on this item
		// with. Only renders when:
		//   - counterparty was supplied (caller has a relevant peer)
		//   - peer has at least one entry for this item in the 30d window
		//   - peer's count is strictly less than the overall count (no
		//     point repeating the parent line when this is the only peer)
		if counterparty != "" {
			cp := summarizeFiltered(entries, now, 30*24*time.Hour, counterparty)
			if cp.Count > 0 && cp.Count < stats.Count {
				countWord := plural
				if cp.Count == 1 {
					countWord = singular
				}
				var rng string
				if cp.MinUnit == cp.MaxUnit {
					rng = fmt.Sprintf("%d coins", cp.MinUnit)
				} else {
					rng = fmt.Sprintf("%d-%d coins", cp.MinUnit, cp.MaxUnit)
				}
				line += fmt.Sprintf("\n    %s %s: %s (%d %s).",
					capitalizePrep(prep), counterparty, rng, cp.Count, countWord)
			}
		}
		lines = append(lines, itemLine{kind: kind, count: stats.Count, line: line})
	}
	if len(lines) == 0 {
		return ""
	}
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].count != lines[j].count {
			return lines[i].count > lines[j].count
		}
		return lines[i].kind < lines[j].kind
	})
	var b strings.Builder
	fmt.Fprintf(&b, "Your recent %s history (per unit, last 30d):\n", headingNoun)
	for _, l := range lines {
		b.WriteString(l.line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// capitalizePrep title-cases a single-word preposition for the start of
// a sentence-like sub-line. "from" → "From", "to" → "To". The block
// puts the drill-down on its own line, so a leading capital reads
// naturally even though it's syntactically a continuation.
func capitalizePrep(prep string) string {
	if prep == "" {
		return ""
	}
	r := []rune(prep)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}

// pickPerceptionCounterparty selects the perceiver's most-relevant
// huddle peer by transaction overlap with the supplied history map.
// Used to pick the counterparty for the buyer/seller perception block's
// drill-down sub-line: the peer with the most matching entries wins,
// alpha tie-breaker on equal counts.
//
// Returns "" when the perceiver has no current huddle, no non-self
// agent/PC peers, or no peer overlaps the supplied history. The
// renderer suppresses the sub-line on empty.
//
// Direction-aware via perItem: pass the buyer-side history and the
// returned peer scores high if they've sold to the perceiver; pass the
// seller-side history and the peer scores high if they've bought. So
// in a vendor↔customer huddle the buyer block surfaces the vendor and
// the seller block surfaces "" (the customer doesn't typically sell
// back), which is the right behavior.
func (app *App) pickPerceptionCounterparty(ctx context.Context, actorID string, perItem map[string][]historyEntry) string {
	if len(perItem) == 0 || actorID == "" {
		return ""
	}
	rows, err := app.DB.Query(ctx, `
		SELECT a.display_name
		  FROM actor a
		 WHERE a.current_huddle_id IS NOT NULL
		   AND a.current_huddle_id = (
		       SELECT current_huddle_id FROM actor WHERE id::text = $1
		   )
		   AND a.id::text != $1
		   AND (a.llm_memory_agent IS NOT NULL OR a.login_username IS NOT NULL)
	`, actorID)
	if err != nil {
		log.Printf("pickPerceptionCounterparty %s: %v", actorID, err)
		return ""
	}
	defer rows.Close()
	var peers []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		if name == "" {
			continue
		}
		peers = append(peers, name)
	}
	if len(peers) == 0 {
		return ""
	}
	type score struct {
		name  string
		count int
	}
	scores := make([]score, 0, len(peers))
	for _, p := range peers {
		c := 0
		for _, entries := range perItem {
			for _, e := range entries {
				if strings.EqualFold(e.Counterparty, p) {
					c++
				}
			}
		}
		if c > 0 {
			scores = append(scores, score{name: p, count: c})
		}
	}
	if len(scores) == 0 {
		return ""
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].count != scores[j].count {
			return scores[i].count > scores[j].count
		}
		return scores[i].name < scores[j].name
	})
	return scores[0].name
}

// relativeAge renders a duration as "today" / "yesterday" / "N days ago".
// Anchors on day boundaries the way an LLM would actually read the data
// rather than precise seconds — "5 days ago" lands more useful than
// "120 hours ago."
func relativeAge(d time.Duration) string {
	if d < 0 {
		return "today"
	}
	days := int(d.Hours() / 24)
	switch days {
	case 0:
		return "today"
	case 1:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", days)
	}
}
