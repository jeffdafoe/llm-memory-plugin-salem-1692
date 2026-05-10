package main

// Buy-side candidate resolver (ZBBS-HOME-241).
//
// This file owns the "who should I try to buy this item from"
// algorithm. The walk + haggle state machine that ACTS on the
// resolver's output is intentionally NOT in this file — that
// integration is heavier (NPCBehaviors slot, take_break composition,
// arrival hooks for the no_stock fallback path) and lands in a
// follow-up commit so the foundation can ship and be reviewed in
// isolation.
//
// Algorithm (matches the ZBBS-HOME-203 design note resolutions):
//
//   1. Read settings: cycle_lookback_hours, buy_failure_backoff_minutes.
//   2. Read buyer's actor_buy_state for this item: skip if
//      last_buy_failed_at is within backoff.
//   3. Read buyer's current inventory for this item: skip if >= target.
//   4. Build candidate set:
//        * Primary: DISTINCT seller_id from pay_ledger where
//          item_kind = X and state IN ('accepted','no_stock',
//          'declined','countered'). All "approached as a seller"
//          states count for reputation, even unsuccessful ones.
//        * Fallback (when primary empty): UNION of (a) actors with
//          a `produce X` restock entry and (b) actors with X in
//          actor_inventory > 0. Bootstraps a fresh world without
//          synthetic seed rows.
//   5. Filter:
//        * Remove self.
//        * Remove cycles: actors who bought this item from buyer
//          within cycle_lookback_hours (per pay_ledger).
//   6. Score by Euclidean distance from buyer's current position to
//      seller's work_structure walk-target. Falls back to seller's
//      home_x/home_y if work_structure not set.
//   7. Tiebreak chain:
//        a. Prefer last_bought_from from buyer's actor_buy_state if
//           in the tied set.
//        b. Lowest recent quoted unit price (offered_amount / qty)
//           for that item with that seller in pay_ledger.
//        c. Random.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5"
)

// BuyCandidate is one ranked candidate seller.
type BuyCandidate struct {
	ActorID      string
	DisplayName  string
	WalkTargetX  float64
	WalkTargetY  float64
	DistanceSq   float64
	IsLastBought bool    // true when this candidate was the buyer's most recent successful supplier
	RecentPrice  float64 // average unit price from prior accepted rows; math.Inf(1) when no history
}

// BuyResolverDecision is the resolver's output: chosen candidate
// (nil means "no candidate, skip with backoff") plus a reason for
// audit / narration.
type BuyResolverDecision struct {
	Candidate *BuyCandidate
	Reason    string // "ok", "below-target-not-met", "in-backoff", "no-candidates", etc.
}

// resolveBuyCandidate runs the full algorithm. Returns a decision
// the caller (the to-be-implemented walk dispatcher) can act on.
// All reads are non-locking — locks happen later in the actual pay
// flow. This function is safe to call from any goroutine.
func (app *App) resolveBuyCandidate(
	ctx context.Context,
	buyerID string,
	entry RestockEntry,
	buyerX, buyerY float64,
	now time.Time,
) (BuyResolverDecision, error) {
	if entry.Source != RestockSourceBuy {
		return BuyResolverDecision{Reason: "wrong-source"}, nil
	}

	cycleLookbackHours := app.loadIntSetting(ctx, "restock.cycle_lookback_hours", 24)
	backoffMinutes := app.loadIntSetting(ctx, "restock.buy_failure_backoff_minutes", 60)

	// Backoff check.
	var lastFailedAt *time.Time
	var lastBoughtFrom *string
	err := app.DB.QueryRow(ctx,
		`SELECT last_buy_failed_at, last_bought_from::text
		   FROM actor_buy_state
		  WHERE actor_id = $1::uuid AND item_kind = $2`,
		buyerID, entry.Item,
	).Scan(&lastFailedAt, &lastBoughtFrom)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return BuyResolverDecision{}, fmt.Errorf("load buy_state: %w", err)
	}
	if lastFailedAt != nil {
		backoffEnds := lastFailedAt.Add(time.Duration(backoffMinutes) * time.Minute)
		if now.Before(backoffEnds) {
			return BuyResolverDecision{Reason: "in-backoff"}, nil
		}
	}

	// Target check — skip the buy if we're already at/above target.
	var currentQty int
	err = app.DB.QueryRow(ctx,
		`SELECT COALESCE(quantity, 0) FROM actor_inventory
		  WHERE actor_id = $1::uuid AND item_kind = $2`,
		buyerID, entry.Item,
	).Scan(&currentQty)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return BuyResolverDecision{}, fmt.Errorf("load inventory: %w", err)
	}
	if entry.Target > 0 && currentQty >= entry.Target {
		return BuyResolverDecision{Reason: "above-target"}, nil
	}

	candidates, err := app.discoverBuyCandidates(ctx, buyerID, entry.Item, cycleLookbackHours)
	if err != nil {
		return BuyResolverDecision{}, err
	}
	if len(candidates) == 0 {
		return BuyResolverDecision{Reason: "no-candidates"}, nil
	}

	// Mark the last_bought_from candidate (used in tiebreak).
	if lastBoughtFrom != nil {
		for i := range candidates {
			if candidates[i].ActorID == *lastBoughtFrom {
				candidates[i].IsLastBought = true
			}
		}
	}

	// Score distance, attach recent price, sort.
	for i := range candidates {
		dx := candidates[i].WalkTargetX - buyerX
		dy := candidates[i].WalkTargetY - buyerY
		candidates[i].DistanceSq = dx*dx + dy*dy
		price, err := app.loadRecentUnitPrice(ctx, candidates[i].ActorID, entry.Item)
		if err != nil {
			// Non-fatal — fall back to "no history" sentinel.
			candidates[i].RecentPrice = math.Inf(1)
		} else {
			candidates[i].RecentPrice = price
		}
	}

	chosen := pickBuyCandidate(candidates)
	return BuyResolverDecision{Candidate: chosen, Reason: "ok"}, nil
}

// discoverBuyCandidates builds the candidate set. Primary = pay_ledger
// reputation; fallback = produce-policy + inventory-holders.
func (app *App) discoverBuyCandidates(
	ctx context.Context,
	buyerID, item string,
	cycleLookbackHours int,
) ([]BuyCandidate, error) {
	primary, err := app.candidatesFromPayLedger(ctx, item)
	if err != nil {
		return nil, err
	}
	if len(primary) == 0 {
		fallback, err := app.candidatesFromGameState(ctx, item)
		if err != nil {
			return nil, err
		}
		primary = fallback
	}
	if len(primary) == 0 {
		return nil, nil
	}

	// Self filter + cycle filter.
	excluded, err := app.cycleFilterSet(ctx, buyerID, item, cycleLookbackHours)
	if err != nil {
		return nil, err
	}
	excluded[buyerID] = true

	// Hydrate location + name for each non-excluded candidate.
	out := make([]BuyCandidate, 0, len(primary))
	for _, id := range primary {
		if excluded[id] {
			continue
		}
		cand, err := app.hydrateCandidate(ctx, id)
		if err != nil {
			// Skip candidates we can't locate; not fatal.
			continue
		}
		out = append(out, cand)
	}
	return out, nil
}

func (app *App) candidatesFromPayLedger(ctx context.Context, item string) ([]string, error) {
	rows, err := app.DB.Query(ctx,
		`SELECT DISTINCT seller_id::text FROM pay_ledger
		  WHERE item_kind = $1
		    AND state IN ('accepted','no_stock','declined','countered')`,
		item,
	)
	if err != nil {
		return nil, fmt.Errorf("pay_ledger candidates: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// candidatesFromGameState falls back when pay_ledger has no signal.
// Union of (actors with `produce X` in their restock policy) and
// (actors with X in inventory). Distance/cycle filtering happens in
// the caller.
func (app *App) candidatesFromGameState(ctx context.Context, item string) ([]string, error) {
	rows, err := app.DB.Query(ctx,
		`SELECT DISTINCT actor_id::text FROM (
		    SELECT actor_id FROM actor_attribute
		     WHERE jsonb_path_exists(params,
		           '$.restock[*] ? (@.source == "produce" && @.item == $i)',
		           jsonb_build_object('i', $1::text))
		    UNION
		    SELECT actor_id FROM actor_inventory
		     WHERE item_kind = $1 AND quantity > 0
		 ) c`,
		item,
	)
	if err != nil {
		return nil, fmt.Errorf("game_state candidates: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// cycleFilterSet returns the set of actors who have bought `item`
// from `buyerID` within the lookback window. Includes accepted,
// declined, countered, AND no_stock attempts — any approach counts
// as a customer relationship for cycle prevention.
func (app *App) cycleFilterSet(
	ctx context.Context,
	buyerID, item string,
	lookbackHours int,
) (map[string]bool, error) {
	rows, err := app.DB.Query(ctx,
		`SELECT DISTINCT buyer_id::text FROM pay_ledger
		  WHERE seller_id = $1::uuid
		    AND item_kind = $2
		    AND state IN ('accepted','no_stock','declined','countered')
		    AND created_at > $3`,
		buyerID, item, time.Now().UTC().Add(-time.Duration(lookbackHours)*time.Hour),
	)
	if err != nil {
		return nil, fmt.Errorf("cycle filter: %w", err)
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// hydrateCandidate fills name + walk-target for a candidate. Walk
// target prefers the seller's work_structure (which is a
// village_object row in Salem's data model) — uses the loiter
// offset when set, otherwise the object anchor. Falls back to the
// actor's home_x/home_y; final fallback to current_x/current_y.
//
// The walk-target math here is just for distance scoring; the
// actual walk dispatched later runs through pickWalkTarget which
// applies the same loiter / door logic.
func (app *App) hydrateCandidate(ctx context.Context, actorID string) (BuyCandidate, error) {
	cand := BuyCandidate{ActorID: actorID}
	var (
		displayName string
		homeX       *float64
		homeY       *float64
		currentX    float64
		currentY    float64
		objX        *float64
		objY        *float64
		loiterX     *float64
		loiterY     *float64
	)
	const tileSize = 32.0
	err := app.DB.QueryRow(ctx,
		`SELECT a.display_name, a.home_x, a.home_y, a.current_x, a.current_y,
		        vo.x, vo.y, vo.loiter_offset_x, vo.loiter_offset_y
		   FROM actor a
		   LEFT JOIN village_object vo ON vo.id = a.work_structure_id
		  WHERE a.id = $1::uuid`,
		actorID,
	).Scan(&displayName, &homeX, &homeY, &currentX, &currentY,
		&objX, &objY, &loiterX, &loiterY)
	if err != nil {
		return cand, err
	}
	cand.DisplayName = displayName
	switch {
	case objX != nil && objY != nil:
		cand.WalkTargetX = *objX
		cand.WalkTargetY = *objY
		if loiterX != nil {
			cand.WalkTargetX += *loiterX * tileSize
		}
		if loiterY != nil {
			cand.WalkTargetY += *loiterY * tileSize
		}
	case homeX != nil && homeY != nil:
		cand.WalkTargetX = *homeX
		cand.WalkTargetY = *homeY
	default:
		cand.WalkTargetX = currentX
		cand.WalkTargetY = currentY
	}
	return cand, nil
}

// loadRecentUnitPrice averages the most recent N accepted unit prices
// for (seller, item) from pay_ledger. Used as the second tiebreak
// when distance + last_bought_from don't disambiguate. Returns
// math.Inf(1) when no history (so it sorts last in min-tiebreak).
func (app *App) loadRecentUnitPrice(ctx context.Context, sellerID, item string) (float64, error) {
	const recencyLimit = 5
	rows, err := app.DB.Query(ctx,
		`SELECT offered_amount, qty FROM pay_ledger
		  WHERE seller_id = $1::uuid AND item_kind = $2 AND state = 'accepted'
		    AND qty IS NOT NULL AND qty > 0
		  ORDER BY created_at DESC LIMIT $3`,
		sellerID, item, recencyLimit,
	)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var (
		sum   float64
		count int
	)
	for rows.Next() {
		var offered int
		var qty int
		if err := rows.Scan(&offered, &qty); err != nil {
			return 0, err
		}
		if qty <= 0 {
			continue
		}
		sum += float64(offered) / float64(qty)
		count++
	}
	if count == 0 {
		return math.Inf(1), nil
	}
	return sum / float64(count), nil
}

// pickBuyCandidate runs the tiebreak chain on a candidate slice
// already populated with DistanceSq, IsLastBought, and RecentPrice.
// Returns nil when the slice is empty.
func pickBuyCandidate(cands []BuyCandidate) *BuyCandidate {
	if len(cands) == 0 {
		return nil
	}
	// Find min distance (tile epsilon: ~1 tile = 32px, sq = 1024).
	const tieEpsilonSq = 1024.0
	minDist := math.Inf(1)
	for _, c := range cands {
		if c.DistanceSq < minDist {
			minDist = c.DistanceSq
		}
	}
	tied := make([]BuyCandidate, 0, len(cands))
	for _, c := range cands {
		if c.DistanceSq <= minDist+tieEpsilonSq {
			tied = append(tied, c)
		}
	}
	if len(tied) == 1 {
		c := tied[0]
		return &c
	}

	// Tiebreak (a): last_bought_from preference.
	for _, c := range tied {
		if c.IsLastBought {
			cc := c
			return &cc
		}
	}

	// Tiebreak (b): lowest recent unit price.
	minPrice := math.Inf(1)
	priced := make([]BuyCandidate, 0, len(tied))
	for _, c := range tied {
		if c.RecentPrice < minPrice {
			minPrice = c.RecentPrice
		}
	}
	for _, c := range tied {
		if c.RecentPrice == minPrice {
			priced = append(priced, c)
		}
	}
	if len(priced) == 1 {
		c := priced[0]
		return &c
	}

	// Tiebreak (c): random across the still-tied set. Each call uses
	// a fresh local random source so we don't depend on global state.
	idx := rand.New(rand.NewSource(time.Now().UnixNano())).Intn(len(priced))
	c := priced[idx]
	return &c
}

// loadIntSetting lives in needs.go and is shared across handlers.
