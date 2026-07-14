package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// LaborContractsRepo reads and writes the labor_contract table — the durable
// mirror of the accepted-but-unsettled subset of World.LaborLedger (LLM-259):
// the en_route + working LaborOffers. pending (unaccepted) and terminal
// (completed/declined/expired/failed) offers are never persisted; the checkpoint
// filters to the accepted subset at build time (BuildCheckpointSnapshot).
//
// SaveSnapshot uses the single-table generation-marker pattern (same shape as
// scene_huddle's parent write): advisory lock → nextval(gen) → per-row UPSERT
// stamping snapshot_gen → DELETE WHERE snapshot_gen < gen. A contract that left
// the accepted set between checkpoints (settled to completed, voided, or its
// pending precursor never accepted) is absent from the map, so the trailing
// delete sweeps its row — the table stays a true mirror of the live accepted
// set, crash-consistent with pay_ledger because it writes inside the same
// SaveWorld Tx.
//
// worker_id / employer_id are soft TEXT refs to actor(id) with NO FK — the v2
// cross-aggregate posture (integrity enforced Go-side at LoadWorld), same as
// pay_ledger.buyer_id / seller_id.
type LaborContractsRepo struct {
	pool Pool
}

// NewLaborContractsRepo constructs a LaborContractsRepo against the given pool.
// Normal wiring path is pg.NewRepository which wires this internally.
func NewLaborContractsRepo(pool Pool) *LaborContractsRepo {
	return &LaborContractsRepo{pool: pool}
}

// loadAllSQLLC selects every labor_contract row. snapshot_gen omitted — pure
// sync bookkeeping with no in-memory representation. No ORDER BY — the in-memory
// model is map-keyed.
const loadAllSQLLC = `
SELECT labor_id, worker_id, employer_id, state, reward, reward_items,
       duration_min, created_at, accepted_at, work_started_at, working_until,
       en_route_deadline, en_route_waiting
  FROM labor_contract`

// upsertSQLLC writes one labor_contract row. reward_items is bound as text and
// cast to jsonb (pgx encodes a Go string as text; the explicit ::jsonb cast is
// unambiguous). snapshot_gen carries the new checkpoint gen so the trailing
// DELETE can prune stale rows.
const upsertSQLLC = `
INSERT INTO labor_contract (
    labor_id, worker_id, employer_id, state, reward, reward_items,
    duration_min, created_at, accepted_at, work_started_at, working_until,
    en_route_deadline, en_route_waiting, snapshot_gen
) VALUES (
    $1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10, $11, $12, $13, $14
)
ON CONFLICT (labor_id) DO UPDATE SET
    worker_id         = EXCLUDED.worker_id,
    employer_id       = EXCLUDED.employer_id,
    state             = EXCLUDED.state,
    reward            = EXCLUDED.reward,
    reward_items      = EXCLUDED.reward_items,
    duration_min      = EXCLUDED.duration_min,
    created_at        = EXCLUDED.created_at,
    accepted_at       = EXCLUDED.accepted_at,
    work_started_at   = EXCLUDED.work_started_at,
    working_until     = EXCLUDED.working_until,
    en_route_deadline = EXCLUDED.en_route_deadline,
    en_route_waiting  = EXCLUDED.en_route_waiting,
    snapshot_gen      = EXCLUDED.snapshot_gen`

// deleteStaleSQLLC prunes labor_contract rows whose snapshot_gen is below the
// current checkpoint gen — the accepted contracts absent from this snapshot
// (settled, voided, or never accepted). Plain DELETE — no self-FK.
const deleteStaleSQLLC = `DELETE FROM labor_contract WHERE snapshot_gen < $1`

// nextGenSQLLC bumps the aggregate's gen sequence.
const nextGenSQLLC = `SELECT nextval('labor_contract_snapshot_gen_seq')`

// advisoryLockSQLLC is the single global lock for the labor-contract aggregate,
// held for the Tx duration to serialize concurrent SaveSnapshot calls. Multi-
// realm upgrade path: replace 0 with hashtext($realm_id) when realms land.
const advisoryLockSQLLC = `SELECT pg_advisory_xact_lock(hashtext('labor_contract_snapshot'), 0)`

// rewardItemRow is the JSONB element shape for reward_items — the in-kind goods
// leg of a contract's reward. Repo-local DTO with explicit tags so the on-disk
// shape is stable and independent of the sim.ItemKindQty field names.
type rewardItemRow struct {
	Kind string `json:"kind"`
	Qty  int    `json:"qty"`
}

// LoadAll loads every labor_contract row into a LaborLedger-shaped map.
// Runs against the pool directly (no Tx) — read-only restart path, same posture
// as the other LoadAll implementations (relies on LoadWorld running before the
// world goroutine and any checkpoint writer).
func (r *LaborContractsRepo) LoadAll(ctx context.Context) (map[sim.LaborID]*sim.LaborOffer, error) {
	rows, err := r.pool.Query(ctx, loadAllSQLLC)
	if err != nil {
		return nil, fmt.Errorf("pg labor_contracts LoadAll query: %w", err)
	}
	defer rows.Close()

	out := make(map[sim.LaborID]*sim.LaborOffer)
	for rows.Next() {
		var (
			laborID         int64
			workerID        string
			employerID      string
			state           string
			reward          int
			rewardItemsJSON []byte
			durationMin     int
			createdAt       time.Time
			acceptedAt      *time.Time
			workStartedAt   *time.Time
			workingUntil    *time.Time
			enRouteDeadline *time.Time
			enRouteWaiting  bool
		)
		if err := rows.Scan(&laborID, &workerID, &employerID, &state, &reward,
			&rewardItemsJSON, &durationMin, &createdAt, &acceptedAt, &workStartedAt,
			&workingUntil, &enRouteDeadline, &enRouteWaiting); err != nil {
			return nil, fmt.Errorf("pg labor_contracts LoadAll scan: %w", err)
		}
		// A non-positive labor_id can't come from the positive-only sequence — it's
		// a corrupt/tampered row, and the sign is LOST once cast to the uint64
		// LaborID map key (a negative becomes a huge id that would corrupt the
		// LaborID safety-floor), so it must be caught here while still signed. Skip
		// with a log rather than fail the whole load: a live village must boot and a
		// dropped labor row is data-clean. Value/ref/state validation of the rest is
		// the rehydrate pass's warn-and-drop (unusableLaborContract).
		if laborID <= 0 {
			log.Printf("pg labor_contracts LoadAll: skipping row with non-positive labor_id %d (corrupt)", laborID)
			continue
		}
		rewardItems, err := decodeRewardItems(rewardItemsJSON)
		if err != nil {
			log.Printf("pg labor_contracts LoadAll: skipping labor_id=%d — reward_items parse: %v", laborID, err)
			continue
		}
		var deadline time.Time
		if enRouteDeadline != nil {
			deadline = *enRouteDeadline
		}
		out[sim.LaborID(laborID)] = &sim.LaborOffer{
			ID:              sim.LaborID(laborID),
			WorkerID:        sim.ActorID(workerID),
			EmployerID:      sim.ActorID(employerID),
			State:           sim.LaborLedgerState(state),
			Reward:          reward,
			RewardItems:     rewardItems,
			DurationMin:     durationMin,
			CreatedAt:       createdAt,
			AcceptedAt:      acceptedAt,
			WorkStartedAt:   workStartedAt,
			WorkingUntil:    workingUntil,
			EnRouteDeadline: deadline,
			EnRouteWaiting:  enRouteWaiting,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg labor_contracts LoadAll iter: %w", err)
	}
	return out, nil
}

// SaveSnapshot writes the accepted-contract set durably via the generation-marker
// pattern, inside the caller's checkpoint Tx:
//
//  0. Advisory lock.
//  1. nextval(labor_contract_snapshot_gen_seq) → $gen.
//  2. Per-row UPSERT, stamping snapshot_gen = $gen. Substrate-boundary
//     validation: reject nil, empty worker/employer id, map-key ↔ o.ID
//     mismatch, and any state other than en_route/working (Go owns that
//     allowlist — a bad state is a build-filter bug worth surfacing on the
//     failing checkpoint, not a silent skip).
//  3. DELETE labor_contract WHERE snapshot_gen < $gen — sweep the contracts
//     absent from the snapshot (settled/voided/never-accepted).
//
// Empty contracts map: the gen still bumps, no UPSERTs run, the DELETE sweeps
// the whole table. nil entries are rejected (a nil in the accepted set is a
// build bug).
func (r *LaborContractsRepo) SaveSnapshot(ctx context.Context, tx sim.Tx, contracts map[sim.LaborID]*sim.LaborOffer) error {
	if tx == nil {
		return fmt.Errorf("pg labor_contracts SaveSnapshot: nil tx")
	}

	if _, err := tx.Exec(ctx, advisoryLockSQLLC); err != nil {
		return fmt.Errorf("pg labor_contracts SaveSnapshot: advisory lock: %w", err)
	}

	var gen int64
	if err := tx.QueryRow(ctx, nextGenSQLLC).Scan(&gen); err != nil {
		return fmt.Errorf("pg labor_contracts SaveSnapshot: nextval: %w", err)
	}

	// LLM-392: a malformed contract is quarantined, not fatal.
	q := quarantineOf(tx)
	for key, o := range contracts {
		if o == nil {
			q.Drop("labor_contract", fmt.Sprintf("%d", key), "nil offer")
			continue
		}
		id := fmt.Sprintf("%d", key)
		if o.ID != key {
			// Keyed on the MAP KEY: o.ID is the field we do not trust here, and it
			// may name a DIFFERENT, healthy contract.
			q.Drop("labor_contract", id, fmt.Sprintf("map key=%d does not match o.ID=%d", key, o.ID))
			continue
		}
		if o.State != sim.LaborStateEnRoute && o.State != sim.LaborStateWorking {
			q.Drop("labor_contract", id, fmt.Sprintf("non-accepted state %q (only en_route/working are persisted)", o.State))
			continue
		}
		if o.WorkerID == "" || o.EmployerID == "" {
			q.Drop("labor_contract", id, "empty worker/employer id")
			continue
		}
		rewardItemsJSON, err := encodeRewardItems(o.RewardItems)
		if err != nil {
			q.Drop("labor_contract", id, fmt.Sprintf("reward_items will not encode: %v", err))
			continue
		}
		// EnRouteDeadline is a value time.Time (zero for an on-site working hire);
		// bind zero as SQL NULL so the column round-trips nil-or-value cleanly.
		var deadlineArg any
		if !o.EnRouteDeadline.IsZero() {
			deadlineArg = o.EnRouteDeadline
		}
		if _, err := tx.Exec(ctx, upsertSQLLC,
			int64(o.ID),          // $1  labor_id
			string(o.WorkerID),   // $2  worker_id
			string(o.EmployerID), // $3 employer_id
			string(o.State),      // $4  state
			o.Reward,             // $5  reward
			rewardItemsJSON,      // $6  reward_items (::jsonb)
			o.DurationMin,        // $7  duration_min
			o.CreatedAt,          // $8  created_at
			o.AcceptedAt,         // $9  accepted_at (nullable)
			o.WorkStartedAt,      // $10 work_started_at (nullable)
			o.WorkingUntil,       // $11 working_until (nullable)
			deadlineArg,          // $12 en_route_deadline (nullable)
			o.EnRouteWaiting,     // $13 en_route_waiting
			gen,                  // $14 snapshot_gen
		); err != nil {
			return fmt.Errorf("pg labor_contracts SaveSnapshot: upsert labor_id=%d: %w", o.ID, err)
		}
	}

	if err := execSweep(ctx, tx, "labor_contract", deleteStaleSQLLC, gen); err != nil {
		return fmt.Errorf("pg labor_contracts SaveSnapshot: delete stale: %w", err)
	}
	return nil
}

// encodeRewardItems marshals the in-kind reward leg to the on-disk JSONB shape.
// A nil/empty slice encodes as "[]" so the column is never NULL (matches the
// table default and keeps decode simple).
func encodeRewardItems(items []sim.ItemKindQty) (string, error) {
	rows := make([]rewardItemRow, 0, len(items))
	for _, it := range items {
		rows = append(rows, rewardItemRow{Kind: string(it.Kind), Qty: it.Qty})
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeRewardItems parses the reward_items JSONB back into the sim slice.
// Empty array (or empty bytes, defensively) yields a nil slice — the canonical
// "coin-only reward" representation.
func decodeRewardItems(raw []byte) ([]sim.ItemKindQty, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var rows []rewardItemRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	out := make([]sim.ItemKindQty, 0, len(rows))
	for _, r := range rows {
		out = append(out, sim.ItemKindQty{Kind: sim.ItemKind(r.Kind), Qty: r.Qty})
	}
	return out, nil
}
