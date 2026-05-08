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
