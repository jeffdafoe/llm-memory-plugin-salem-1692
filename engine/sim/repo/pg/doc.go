// Package pg implements sim.Repository sub-interfaces against a real
// Postgres database via jackc/pgx/v5. Slice 5 (Phase 4 prep) lands the
// OrdersRepo only — every other sub-repo is wired to a notImpl stub so
// tests that touch them fail fast with a clear message.
//
// # Why pay_ledger is the durable Order home
//
// v2's Order is the runtime projection of an accepted, in-flight
// pay_ledger row — both event and entity in v1's schema. Instead of
// introducing a parallel pay_order table, OrdersRepo reads + writes
// pay_ledger directly:
//
//   - LoadAll runs `WHERE state='accepted' AND fulfillment_status IN
//     ('ready', 'pending')` so it covers both today's Ready surface
//     and the future craft-lead-time Pending state.
//   - SaveSnapshot upserts each Order onto pay_ledger.id with
//     state='accepted' (v1's haggle states pending/declined/countered/
//     withdrawn/failed live in-memory pre-acceptance in v2 and never
//     persist as Order rows).
//
// pay_ledger columns v2 writes:
//
//	id, buyer_id, seller_id, item_kind, qty, offered_amount,
//	consumer_actor_ids, fulfillment_status, ready_by, expires_at,
//	delivered_on, created_at, resolved_at, state='accepted'
//
// pay_ledger columns v2 leaves NULL / default:
//
//	huddle_id, scene_id, quoted_unit_amount, message, counter_amount,
//	parent_id, depth (=0), consume_now (=false)
//
// v1 readers already tolerate NULL huddle_id (pre-MEM-121 rows) so
// the partial population is compatible.
//
// # Schema dependencies
//
// Migration ZBBS-WORK-236 prepares pay_ledger for v2:
//
//   - buyer_id, seller_id, consumer_actor_ids retyped UUID → TEXT
//     (v2 ActorIDs are heterogeneous strings: visitors, PCs, NPCs).
//   - expires_at column added for v2's Expired terminal state.
//   - fulfillment_status CHECK extended with 'expired'.
//   - Partial index on (id) WHERE state='accepted' AND
//     fulfillment_status IN ('ready','pending') for fast LoadAll.
//
// # Working set boundary
//
// In-memory World.Orders is the Ready/Pending cache; pg keeps the full
// history including terminal rows. Terminal write-through + prune from
// World.Orders lands in Slice 6 — Slice 5 is repo-shape only and is
// not yet wired into any substrate.
//
// # Tx wiring
//
// The Pool interface here is the surface *pgxpool.Pool naturally
// satisfies; pgxmock's PgxPoolIface satisfies it too. Tests inject a
// mock; production wires the real pool from main.go at cutover.
// SaveSnapshot runs inside the caller's Tx (passed in by World's
// checkpoint flow); LoadAll uses the Pool directly (no Tx needed).
package pg
