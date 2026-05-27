package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// OrdersRepo reads and writes Order rows against pay_ledger. Each
// pay_ledger row with state='accepted' AND fulfillment_status IN
// ('ready', 'pending') represents one in-flight Order in World.Orders.
// Terminal rows (delivered / expired) stay in pg as history but are
// not loaded back into memory at restart.
type OrdersRepo struct {
	pool Pool
}

// NewOrdersRepo constructs an OrdersRepo against the given pool.
// Mainly for callers that want to swap just the Orders sub-repo; the
// normal path is pg.NewRepository which wires this internally.
func NewOrdersRepo(pool Pool) *OrdersRepo {
	return &OrdersRepo{pool: pool}
}

// loadAllSQL selects the v2 in-flight set. The partial index
// ix_pay_ledger_v2_in_flight matches the WHERE clause, supporting
// the ordered scan over the in-flight subset. It is NOT a
// selective filter index — only `id` is indexed, so within the
// partial set there's no further pruning. That's fine for the
// expected workload (load-all-in-flight at restart over a small
// set); if future read patterns need selective filtering, add a
// columns-included or composite index.
//
// Today v2 emits only Ready; Pending support lands when the
// craft-lead-time slice ships and is forward-compatible with no
// repo change.
const loadAllSQL = `
SELECT
    id,
    buyer_id,
    seller_id,
    item_kind,
    qty,
    offered_amount,
    consumer_actor_ids,
    fulfillment_status,
    created_at,
    delivered_on,
    expires_at
FROM pay_ledger
WHERE state = 'accepted'
  AND fulfillment_status IN ('ready', 'pending')
ORDER BY id`

// upsertSQL writes one Order's pay_ledger row. v2-written rows always
// carry state='accepted'; haggle states (pending/declined/...) live
// in-memory pre-acceptance and don't persist. Columns v2 doesn't track
// stay at their defaults or NULL.
//
// ON CONFLICT (id) infers pay_ledger's PRIMARY KEY (id) — established
// by migration ZBBS-128 (`id bigserial PRIMARY KEY`). If a future
// migration changes the key shape, this conflict target needs to
// follow.
//
// resolved_at == created_at because v2 transitions to accepted in the
// same in-memory step that mints the Order — pay_ledger's
// CHECK ((state = 'pending') = (resolved_at IS NULL)) requires a
// non-NULL resolved_at on any non-pending row.
//
// ready_by mirrors v1's non-craft backfill: created_at::date. Future
// craft-lead-time orders write a future date; today every Order is
// non-craft.
//
// On conflict, only the fields the in-memory Order owns get updated.
// We deliberately don't UPDATE buyer_id, seller_id, item_kind, qty,
// offered_amount, consumer_actor_ids, created_at — those are
// immutable post-acceptance and re-asserting them in the UPDATE risks
// papering over a real corruption.
const upsertSQL = `
INSERT INTO pay_ledger (
    id, buyer_id, seller_id, item_kind, qty, offered_amount,
    consumer_actor_ids, state, fulfillment_status,
    ready_by, expires_at, created_at, resolved_at, delivered_on
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, 'accepted', $8,
    $9::date, $10, $11, $11, $12
)
ON CONFLICT (id) DO UPDATE SET
    fulfillment_status = EXCLUDED.fulfillment_status,
    expires_at         = EXCLUDED.expires_at,
    delivered_on       = EXCLUDED.delivered_on`

// writeTerminalSQL is Slice 6's single-row terminal-flip statement.
// Runs against the pool directly (no Tx) — terminal write-through
// is one row, one statement; atomicity is inherent in pg's row-level
// MVCC. Tracks the same UPDATE column set as upsertSQL's conflict
// path (fulfillment_status, expires_at, delivered_on) so a terminal
// reached via write-through and a terminal observed at the next
// SaveSnapshot upsert produce identical column values.
//
// state stays 'accepted' — pay-ledger acceptance is the gate that
// minted the Order; the macro state column doesn't move on
// fulfillment terminations (only fulfillment_status does).
//
// expires_at is restamped to NOW() on Expired transitions so admin
// reads can see when the safety-net sweep fired vs when the original
// TTL would have elapsed. For Delivered transitions the original
// expires_at is preserved (the field already records the planned
// boundary; restamping would obscure the by-when contract).
//
// WHERE guard scope (post-R1 code_review fix). pay_ledger is shared
// substrate state across v1 + v2; a bare `WHERE id = $1` could stamp
// fulfillment_status onto an arbitrary v1 row on stale-id / collision /
// caller-bug. The two-clause guard restricts the write to v2-owned
// fulfillment-tracking rows:
//
//   - `state = 'accepted'` — v2 only writes accepted rows. Pre-acceptance
//     haggle states (pending/declined/...) live in-memory in v2 and never
//     reach this path; a row with state != 'accepted' is by definition
//     not a v2 Order.
//   - `fulfillment_status IN ('ready', 'pending', 'delivered', 'expired')`
//     — matches the CHECK constraint set, excluding rows where
//     fulfillment_status is NULL (legacy v1 pre-fulfillment-column rows).
//     The terminal members are INCLUDED so a drift-correction write still
//     succeeds: if pg has 'delivered' and memory has 'expired' (or vice
//     versa), in-memory still wins.
//
// Discriminator-column posture for the v1+v2 coexistence concern is
// carried forward in the orders-pg codebase note; this WHERE clause is
// the best-available protection without a discriminator column.
//
// RowsAffected = 0 means either (a) the id is absent from pay_ledger or
// (b) the row exists but doesn't pass the two-clause guard (not accepted
// / non-fulfillment-tracking). Both surface as a substrate-level error.
const writeTerminalSQL = `
UPDATE pay_ledger
SET fulfillment_status = $2,
    delivered_on       = $3,
    expires_at         = CASE WHEN $2 = 'expired' THEN NOW() ELSE expires_at END
WHERE id = $1
  AND state = 'accepted'
  AND fulfillment_status IN ('ready', 'pending', 'delivered', 'expired')`

// expireAbsentSQL enforces SaveSnapshot's contract: the supplied map
// IS the complete in-flight set. Any pay_ledger row currently
// state='accepted' AND fulfillment_status IN ('ready','pending')
// whose id is NOT in the snapshot's ID list gets flipped to
// fulfillment_status='expired' with expires_at stamped (or
// preserved if non-null).
//
// Why this matters: without it, mem and pg SaveSnapshot diverge —
// mem replaces wholesale, pg only upserts. After Slice 6's terminal-
// pruning behavior lands, a SaveSnapshot containing only in-flight
// orders would leave previously-pruned terminal orders in pg as
// state='accepted' AND fulfillment_status='ready' (their LAST upserted
// state). Restart would resurrect them.
//
// Empty IDs slice intentionally expires all in-flight rows — the
// snapshot semantic is "this is everything in-flight"; empty means
// "no in-flight orders."
//
// Runs INSIDE the caller's checkpoint Tx so atomicity is preserved:
// if the upsert loop fails, the expire rolls back too.
// Empty-array case: pgx may bind an untyped empty []int64 to $1
// with ambiguous element type; explicit cast to bigint[] removes
// any ambiguity. pay_ledger.id is bigint, so the cast is exact
// (R2 finding).
//
// Domain-scope note: the UPDATE predicate matches the same
// (state='accepted' AND fulfillment_status IN ('ready','pending'))
// surface that LoadAll owns. If v1 and v2 ever coexist on the same
// pay_ledger table during a transition (no current plan; v2 fully
// replaces v1 at cutover), v2's SaveSnapshot would expire v1-
// written in-flight rows that v2 doesn't know about — a
// discriminator column would be needed. Documented here so the
// concern doesn't get rediscovered later.
const expireAbsentSQL = `
UPDATE pay_ledger
SET fulfillment_status = 'expired',
    expires_at         = COALESCE(expires_at, NOW())
WHERE state = 'accepted'
  AND fulfillment_status IN ('ready', 'pending')
  AND NOT (id = ANY($1::bigint[]))`

// LoadAll loads in-flight Orders from pay_ledger.
//
// Runs against the pool directly (no Tx) — this is the restart path
// and doesn't need to read inside the checkpoint Tx.
func (r *OrdersRepo) LoadAll(ctx context.Context) (map[sim.OrderID]*sim.Order, error) {
	rows, err := r.pool.Query(ctx, loadAllSQL)
	if err != nil {
		return nil, fmt.Errorf("pg orders LoadAll query: %w", err)
	}
	defer rows.Close()

	out := make(map[sim.OrderID]*sim.Order)
	for rows.Next() {
		var (
			id          int64
			buyerID     string
			sellerID    string
			itemKind    string
			qty         int
			offeredAmt  int
			consumerIDs []string
			status      string
			createdAt   time.Time
			deliveredOn *time.Time
			expiresAt   *time.Time
		)
		if err := rows.Scan(
			&id, &buyerID, &sellerID, &itemKind, &qty, &offeredAmt,
			&consumerIDs, &status, &createdAt, &deliveredOn, &expiresAt,
		); err != nil {
			return nil, fmt.Errorf("pg orders LoadAll scan: %w", err)
		}
		state, err := fulfillmentToOrderState(status)
		if err != nil {
			// loadAllSQL filters fulfillment_status IN
			// ('ready','pending') and the column has a CHECK
			// constraint to the same set + delivered/expired — an
			// unknown value here means the DB is in an unexpected
			// shape (schema drift, manual SQL mutation). Surface
			// it loudly rather than silently dropping data.
			return nil, fmt.Errorf("pg orders LoadAll: row id=%d unknown fulfillment_status %q", id, status)
		}
		oid := sim.OrderID(id)
		consumers := make([]sim.ActorID, len(consumerIDs))
		for i, s := range consumerIDs {
			consumers[i] = sim.ActorID(s)
		}
		// expires_at is nullable in v1 (pre-ZBBS-WORK-236 rows have
		// NULL). Treat NULL as far-future so the TTL sweep doesn't
		// auto-expire historical Ready rows that pre-date the v2
		// schema. New v2 writes always populate it.
		var expires time.Time
		if expiresAt != nil {
			expires = *expiresAt
		} else {
			expires = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
		}
		out[oid] = &sim.Order{
			ID:          oid,
			State:       state,
			BuyerID:     sim.ActorID(buyerID),
			SellerID:    sim.ActorID(sellerID),
			Item:        sim.ItemKind(itemKind),
			Qty:         qty,
			Amount:      offeredAmt,
			ConsumerIDs: consumers,
			LedgerID:    sim.LedgerID(id),
			CreatedAt:   createdAt,
			DeliveredAt: deliveredOn,
			ExpiresAt:   expires,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg orders LoadAll iter: %w", err)
	}
	return out, nil
}

// SaveSnapshot writes the in-flight Order set durably. The supplied
// map IS the complete in-flight set — any pay_ledger row currently
// in-flight whose id is absent from the map gets flipped to
// fulfillment_status='expired' so that the next LoadAll returns the
// caller's authoritative set, not a superset including stale rows.
//
// Two-step inside one Tx:
//
//  1. Expire absent rows (UPDATE ... WHERE NOT (id = ANY(supplied_ids))).
//  2. Upsert each Order in the supplied map.
//
// Runs inside the caller's checkpoint Tx — the world's checkpoint
// flow calls repo.Begin once and passes the Tx to each sub-repo's
// SaveSnapshot in turn. If either step fails, the whole Tx rolls
// back; pg stays consistent with the prior checkpoint.
//
// Slice 5 has no caller wired (pg isn't in main.go yet); Slice 6
// wires the substrate terminal write-through + prune, and the
// caller invariant becomes load-bearing then.
func (r *OrdersRepo) SaveSnapshot(ctx context.Context, tx sim.Tx, orders map[sim.OrderID]*sim.Order) error {
	if tx == nil {
		return fmt.Errorf("pg orders SaveSnapshot: nil tx")
	}

	// Step 1: expire any in-flight row whose id is not in the snapshot.
	// Build the id slice first (skipping nil entries) so the UPDATE
	// argument and the upsert loop agree on what IS in the snapshot.
	ids := make([]int64, 0, len(orders))
	for id, o := range orders {
		if o == nil {
			continue
		}
		ids = append(ids, int64(id))
	}
	if _, err := tx.Exec(ctx, expireAbsentSQL, ids); err != nil {
		return fmt.Errorf("pg orders SaveSnapshot: expire absent: %w", err)
	}

	// Step 2: upsert each Order in the snapshot.
	for _, o := range orders {
		if o == nil {
			continue
		}
		// Order.ID and Order.LedgerID are the same value by domain
		// invariant — Order.LedgerID is a back-reference to the
		// originating pay_ledger row, which IS the same row now that
		// pay_ledger is Order's durable home. Catch the mismatch
		// rather than silently writing $1=Order.ID and ignoring
		// LedgerID.
		//
		// Both sim.OrderID and sim.LedgerID are uint64 today, so the
		// cast is exact. If either type ever widens (rare for ID
		// types but worth noting), revisit this comparison to add an
		// explicit range check before conversion.
		if o.LedgerID != 0 && sim.OrderID(o.LedgerID) != o.ID {
			return fmt.Errorf("pg orders SaveSnapshot: order %d LedgerID %d mismatch", o.ID, o.LedgerID)
		}
		status, err := orderStateToFulfillment(o.State)
		if err != nil {
			return fmt.Errorf("pg orders SaveSnapshot: order %d: %w", o.ID, err)
		}
		consumerIDs := make([]string, len(o.ConsumerIDs))
		for i, a := range o.ConsumerIDs {
			consumerIDs[i] = string(a)
		}
		if _, err := tx.Exec(ctx, upsertSQL,
			int64(o.ID),        // $1 id
			string(o.BuyerID),  // $2 buyer_id
			string(o.SellerID), // $3 seller_id
			string(o.Item),     // $4 item_kind
			o.Qty,              // $5 qty
			o.Amount,           // $6 offered_amount
			consumerIDs,        // $7 consumer_actor_ids
			status,             // $8 fulfillment_status
			o.CreatedAt,        // $9 ready_by (DATE-cast in SQL)
			o.ExpiresAt,        // $10 expires_at
			o.CreatedAt,        // $11 created_at + resolved_at
			o.DeliveredAt,      // $12 delivered_on
		); err != nil {
			return fmt.Errorf("pg orders SaveSnapshot: upsert id=%d: %w", o.ID, err)
		}
	}
	return nil
}

// WriteTerminal stamps a single Order's terminal state durably (Slice
// 6 write-through-then-prune). Called from inside finalizeOrderTerminal
// on the world goroutine, synchronously, between the in-memory state
// flip and the w.Orders prune. Blocks until pg returns; the world
// goroutine waits.
//
// Runs against the pool directly (no Tx) — single-row UPDATE on the
// pay_ledger primary key, atomicity inherent in row-level MVCC. No
// checkpoint coupling.
//
// Rejects non-terminal states (caller bug — finalizeOrderTerminal
// guarantees terminal-only). Returns an error on RowsAffected = 0
// (id absent from pay_ledger, or row present but not a writable v2
// accepted order row — see writeTerminalSQL's WHERE guard).
//
// On error, finalizeOrderTerminal logs and skips the prune, leaving
// the in-memory terminal entry in w.Orders for the next checkpoint
// SaveSnapshot to reconcile.
func (r *OrdersRepo) WriteTerminal(ctx context.Context, o *sim.Order) error {
	if o == nil {
		return fmt.Errorf("pg orders WriteTerminal: nil order")
	}
	if !o.State.IsTerminal() {
		return fmt.Errorf("pg orders WriteTerminal: order %d state %q is not terminal", o.ID, o.State)
	}
	status, err := orderStateToFulfillment(o.State)
	if err != nil {
		return fmt.Errorf("pg orders WriteTerminal: order %d: %w", o.ID, err)
	}
	tag, err := r.pool.Exec(ctx, writeTerminalSQL,
		int64(o.ID),   // $1 id
		status,        // $2 fulfillment_status
		o.DeliveredAt, // $3 delivered_on (nil for Expired)
	)
	if err != nil {
		return fmt.Errorf("pg orders WriteTerminal: order %d exec: %w", o.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("pg orders WriteTerminal: order %d not found or not a writable v2 accepted order row", o.ID)
	}
	return nil
}

// loadRecentPricesSQL pulls the top-N most recent accepted rows per
// (seller_id, item_kind) tuple, bounded by a `since` floor on
// created_at. ROW_NUMBER() OVER (PARTITION BY ... ORDER BY created_at
// DESC) ranks within each tuple; the outer WHERE keeps only ranks ≤
// perKeyCap. Final ORDER BY guarantees chronological (oldest-first)
// per-key ordering so World.SeedPriceBook's ring buffer pushes land
// in the right slots (RingBuffer.Push at capacity drops oldest).
//
// state='accepted' filter is the v1-parity rule: knowledge of price
// lands at acceptance. fulfillment_status is NOT in the filter —
// terminal-delivered rows count (the buyer paid; that knowledge stays).
// expired rows: filtered out via state='accepted' only if the schema
// has them at non-accepted; in practice pay_ledger.state ENUMs
// pre-acceptance and acceptance separately from fulfillment, and the
// rejected pre-acceptance terminals (declined/withdrawn/expired/
// failed_*) all have state != 'accepted'.
//
// Index opportunity: a partial index
//
//	(seller_id, item_kind, created_at DESC)
//	WHERE state = 'accepted'
//
// would cover this query's PARTITION BY / ORDER BY exactly. Not yet
// added; LoadWorld runs once at boot so seq-scan cost is paid once
// per restart. Add the index if seed time becomes noticeable.
const loadRecentPricesSQL = `
SELECT seller_id, item_kind, buyer_id, offered_amount, qty,
       cardinality(consumer_actor_ids), created_at
  FROM (
        SELECT seller_id, item_kind, buyer_id, offered_amount, qty,
               consumer_actor_ids, created_at,
               ROW_NUMBER() OVER (
                   PARTITION BY seller_id, item_kind
                   ORDER BY created_at DESC
               ) AS rn
          FROM pay_ledger
         WHERE state = 'accepted'
           AND created_at >= $1
           AND item_kind IS NOT NULL
       ) t
 WHERE rn <= $2
 ORDER BY seller_id, item_kind, created_at ASC`

// LoadRecentPrices returns top-perKeyCap most-recent accepted-price
// observations per (seller, item) tuple, within the `since` window,
// for World.SeedPriceBook at LoadWorld time. See loadRecentPricesSQL
// for the query shape and rationale.
//
// Returns observations in chronological (oldest-first) order per key
// so SeedPriceBook's ring-buffer push contract is satisfied directly.
// Cross-key ordering is by (seller_id, item_kind) as a side effect
// of the partition's ORDER BY in SQL; that ordering is not load-
// bearing — SeedPriceBook treats records as independent.
//
// Runs against the pool directly (no Tx) — read-only seed path,
// same posture as LoadAll.
//
// cardinality(consumer_actor_ids) is computed in SQL so we don't pull
// the consumer_actor_ids[] payload across the wire just to length-check
// it. NULL consumer_actor_ids (legacy v1 rows pre-multi-consumer)
// yields cardinality=NULL, which Go scans into 0; the cascade-side
// `consumers < 1 ? 1` normalization in SeedPriceBook's caller floors
// it back to 1. Solo orders therefore round-trip cleanly.
//
// item_kind IS NOT NULL is enforced in the query: a legacy pay_ledger
// row with NULL item_kind can't form a valid PriceBookKey (Item is the
// partition key), and itemKind below scans into a non-nullable string,
// so a NULL would fail the whole scan and abort the price-book seed at
// LoadWorld — leaving every merchant with an empty price history. The
// row is meaningless to the price book, so we exclude it at the source
// rather than scanning into a sql.NullString and discarding it in Go.
func (r *OrdersRepo) LoadRecentPrices(ctx context.Context, since time.Time, perKeyCap int) ([]sim.PriceBookSeedRecord, error) {
	if perKeyCap <= 0 {
		return nil, fmt.Errorf("pg orders LoadRecentPrices: perKeyCap must be > 0, got %d", perKeyCap)
	}
	rows, err := r.pool.Query(ctx, loadRecentPricesSQL, since, perKeyCap)
	if err != nil {
		return nil, fmt.Errorf("pg orders LoadRecentPrices query: %w", err)
	}
	defer rows.Close()

	out := make([]sim.PriceBookSeedRecord, 0)
	for rows.Next() {
		var (
			sellerID    string
			itemKind    string
			buyerID     string
			amount      int
			qty         int
			consumerCnt *int
			at          time.Time
		)
		if err := rows.Scan(&sellerID, &itemKind, &buyerID, &amount, &qty, &consumerCnt, &at); err != nil {
			return nil, fmt.Errorf("pg orders LoadRecentPrices scan: %w", err)
		}
		consumers := 1
		if consumerCnt != nil && *consumerCnt > 1 {
			consumers = *consumerCnt
		}
		out = append(out, sim.PriceBookSeedRecord{
			Key: sim.PriceBookKey{
				SellerID: sim.ActorID(sellerID),
				Item:     sim.ItemKind(itemKind),
			},
			Observation: sim.PriceObservation{
				BuyerID:   sim.ActorID(buyerID),
				Amount:    amount,
				Qty:       qty,
				Consumers: consumers,
				At:        at,
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg orders LoadRecentPrices iter: %w", err)
	}
	return out, nil
}

// fulfillmentToOrderState maps a pay_ledger.fulfillment_status string
// to sim.OrderState. Returns an error on unknown values so the caller
// can skip + log instead of silently materializing an Order with an
// invalid state. 'pending' maps to OrderStateReady today since v2
// doesn't have a Pending state yet — when crafts come back, add a
// sim.OrderStatePending and update this mapping.
func fulfillmentToOrderState(s string) (sim.OrderState, error) {
	switch s {
	case "ready", "pending":
		// Today v2 has no Pending — surface both as Ready so admin
		// investigation can spot the migrated row, and the in-memory
		// Order is still validatable for deliver_order.
		return sim.OrderStateReady, nil
	case "delivered":
		return sim.OrderStateDelivered, nil
	case "expired":
		return sim.OrderStateExpired, nil
	default:
		return "", fmt.Errorf("unknown fulfillment_status %q", s)
	}
}

// orderStateToFulfillment maps sim.OrderState to a pay_ledger
// fulfillment_status value. Slice 5 emits only ready / delivered /
// expired since OrderStatePending isn't yet a v2 state — the
// mapping panics on unknown values rather than silently writing a
// blank.
func orderStateToFulfillment(s sim.OrderState) (string, error) {
	switch s {
	case sim.OrderStateReady:
		return "ready", nil
	case sim.OrderStateDelivered:
		return "delivered", nil
	case sim.OrderStateExpired:
		return "expired", nil
	default:
		return "", fmt.Errorf("unknown OrderState %q", s)
	}
}
