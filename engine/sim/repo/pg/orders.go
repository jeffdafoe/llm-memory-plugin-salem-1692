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
const expireAbsentSQL = `
UPDATE pay_ledger
SET fulfillment_status = 'expired',
    expires_at         = COALESCE(expires_at, NOW())
WHERE state = 'accepted'
  AND fulfillment_status IN ('ready', 'pending')
  AND NOT (id = ANY($1))`

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
