package pg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// RecurringVisitorsRepo reads and writes the returning-traveler tables (LLM-372):
// recurring_visitor (durable persona + visit_count + next_return_at) and its child
// recurring_visitor_acquaintance (per-PC familiarity).
//
// UNLIKE VisitorsRepo — which mirrors the LIVE in-flight visitor set with a
// generation-marker sweep — these rows are DURABLE IDENTITY that OUTLIVES the
// visit, so SaveSnapshot is a plain per-row UPSERT with NO delete-stale step (the
// DiscoveredKind precedent). A returner is only ever added or updated in memory,
// never removed, so the DB stays in sync without a sweep. Written inside the
// caller's checkpoint Tx so a crash can't split a returner's persona from its
// familiarity or from the in-flight visitor's recurring_visitor_id link.
type RecurringVisitorsRepo struct {
	pool Pool
}

// NewRecurringVisitorsRepo constructs a RecurringVisitorsRepo against the given
// pool. Normal wiring is pg.NewRepository.
func NewRecurringVisitorsRepo(pool Pool) *RecurringVisitorsRepo {
	return &RecurringVisitorsRepo{pool: pool}
}

const loadRecurringSQL = `
SELECT id, name, archetype, origin, disposition, visit_count,
       first_seen_at, last_seen_at, next_return_at
  FROM recurring_visitor`

const loadRecurringAcqSQL = `
SELECT recurring_visitor_id, pc_actor_id, pc_display_name, first_met_at, last_met_at
  FROM recurring_visitor_acquaintance`

// upsertRecurringSQL writes one returner row. next_return_at is bound NULL when
// the returner is in-village / unscheduled (zero time).
const upsertRecurringSQL = `
INSERT INTO recurring_visitor (
    id, name, archetype, origin, disposition, visit_count,
    first_seen_at, last_seen_at, next_return_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (id) DO UPDATE SET
    name           = EXCLUDED.name,
    archetype      = EXCLUDED.archetype,
    origin         = EXCLUDED.origin,
    disposition    = EXCLUDED.disposition,
    visit_count    = EXCLUDED.visit_count,
    first_seen_at  = EXCLUDED.first_seen_at,
    last_seen_at   = EXCLUDED.last_seen_at,
    next_return_at = EXCLUDED.next_return_at`

const upsertRecurringAcqSQL = `
INSERT INTO recurring_visitor_acquaintance (
    recurring_visitor_id, pc_actor_id, pc_display_name, first_met_at, last_met_at
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (recurring_visitor_id, pc_actor_id) DO UPDATE SET
    pc_display_name = EXCLUDED.pc_display_name,
    first_met_at    = EXCLUDED.first_met_at,
    last_met_at     = EXCLUDED.last_met_at`

// deleteRecurringAcqNotInSQL reconciles a returner's acquaintance children to its
// current in-memory set: any child whose pc_actor_id is not in the passed array is
// removed. Keeps children faithful WITHOUT sweeping the durable parents — so if a
// future path ever drops an in-memory acquaintance, the DB row doesn't resurrect
// on restart. `<> ALL('{}')` is TRUE for every row, so an empty set clears all of a
// parent's children.
const deleteRecurringAcqNotInSQL = `
DELETE FROM recurring_visitor_acquaintance
 WHERE recurring_visitor_id = $1
   AND pc_actor_id <> ALL($2::text[])`

// advisoryLockRecurringSQL serializes concurrent checkpoints on this aggregate for
// the Tx duration — parity with VisitorsRepo, cheap insurance even though the
// checkpointer is single-threaded.
const advisoryLockRecurringSQL = `SELECT pg_advisory_xact_lock(hashtext('recurring_visitor_snapshot'), 0)`

// LoadAll loads every returner + its familiarity rows into the in-memory model.
// Two reads (parents, then children) stitched by id — read-only restart path off
// the pool, same posture as the other LoadAll implementations. A child row whose
// parent id is absent (only possible from an out-of-band edit; a consistent
// checkpoint writes parent+child in one Tx) is skipped, never fatal.
func (r *RecurringVisitorsRepo) LoadAll(ctx context.Context) (map[sim.RecurringVisitorID]*sim.RecurringVisitor, error) {
	out := make(map[sim.RecurringVisitorID]*sim.RecurringVisitor)

	rows, err := r.pool.Query(ctx, loadRecurringSQL)
	if err != nil {
		return nil, fmt.Errorf("pg recurring_visitors LoadAll query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id           string
			name         string
			archetype    string
			origin       string
			disposition  string
			visitCount   int
			firstSeenAt  time.Time
			lastSeenAt   time.Time
			nextReturnAt *time.Time
		)
		if err := rows.Scan(&id, &name, &archetype, &origin, &disposition, &visitCount,
			&firstSeenAt, &lastSeenAt, &nextReturnAt); err != nil {
			return nil, fmt.Errorf("pg recurring_visitors LoadAll scan: %w", err)
		}
		rv := &sim.RecurringVisitor{
			ID:            sim.RecurringVisitorID(id),
			Name:          name,
			Archetype:     archetype,
			Origin:        origin,
			Disposition:   disposition,
			VisitCount:    visitCount,
			FirstSeenAt:   firstSeenAt,
			LastSeenAt:    lastSeenAt,
			Acquaintances: make(map[sim.ActorID]*sim.RecurringAcquaintance),
		}
		if nextReturnAt != nil {
			rv.NextReturnAt = *nextReturnAt
		}
		out[rv.ID] = rv
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg recurring_visitors LoadAll iter: %w", err)
	}

	acqRows, err := r.pool.Query(ctx, loadRecurringAcqSQL)
	if err != nil {
		return nil, fmt.Errorf("pg recurring_visitors LoadAll acq query: %w", err)
	}
	defer acqRows.Close()
	for acqRows.Next() {
		var (
			rvID       string
			pcActorID  string
			pcName     string
			firstMetAt time.Time
			lastMetAt  time.Time
		)
		if err := acqRows.Scan(&rvID, &pcActorID, &pcName, &firstMetAt, &lastMetAt); err != nil {
			return nil, fmt.Errorf("pg recurring_visitors LoadAll acq scan: %w", err)
		}
		rv := out[sim.RecurringVisitorID(rvID)]
		if rv == nil {
			continue // orphan child (out-of-band edit); the FK makes this unreachable from a consistent write
		}
		rv.Acquaintances[sim.ActorID(pcActorID)] = &sim.RecurringAcquaintance{
			PCActorID:     sim.ActorID(pcActorID),
			PCDisplayName: pcName,
			FirstMetAt:    firstMetAt,
			LastMetAt:     lastMetAt,
		}
	}
	if err := acqRows.Err(); err != nil {
		return nil, fmt.Errorf("pg recurring_visitors LoadAll acq iter: %w", err)
	}
	return out, nil
}

// SaveSnapshot upserts the in-memory returner set inside the checkpoint Tx — no
// delete-stale sweep (these rows outlive the visit; the in-memory set only grows).
// Parent recurring_visitor rows first, then their acquaintance children (parent
// must exist for the child FK). Substrate-boundary validation rejects an empty id
// / persona name so a promotion bug surfaces on the failing checkpoint rather than
// silently persisting junk.
func (r *RecurringVisitorsRepo) SaveSnapshot(ctx context.Context, tx sim.Tx, recurring map[sim.RecurringVisitorID]*sim.RecurringVisitor) error {
	if tx == nil {
		return fmt.Errorf("pg recurring_visitors SaveSnapshot: nil tx")
	}
	if len(recurring) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, advisoryLockRecurringSQL); err != nil {
		return fmt.Errorf("pg recurring_visitors SaveSnapshot: advisory lock: %w", err)
	}

	for id, rv := range recurring {
		if rv == nil {
			continue
		}
		if rv.ID != id {
			return fmt.Errorf("pg recurring_visitors SaveSnapshot: map key=%s does not match rv.ID=%s", id, rv.ID)
		}
		if strings.TrimSpace(string(rv.ID)) == "" {
			return fmt.Errorf("pg recurring_visitors SaveSnapshot: empty recurring visitor id")
		}
		if strings.TrimSpace(rv.Name) == "" {
			return fmt.Errorf("pg recurring_visitors SaveSnapshot: id=%s has empty persona name", rv.ID)
		}
		var nextReturnArg any
		if !rv.NextReturnAt.IsZero() {
			nextReturnArg = rv.NextReturnAt
		}
		if _, err := tx.Exec(ctx, upsertRecurringSQL,
			string(rv.ID),   // $1 id
			rv.Name,         // $2 name
			rv.Archetype,    // $3 archetype
			rv.Origin,       // $4 origin
			rv.Disposition,  // $5 disposition
			rv.VisitCount,   // $6 visit_count
			rv.FirstSeenAt,  // $7 first_seen_at
			rv.LastSeenAt,   // $8 last_seen_at
			nextReturnArg,   // $9 next_return_at (nullable)
		); err != nil {
			return fmt.Errorf("pg recurring_visitors SaveSnapshot: upsert id=%s: %w", rv.ID, err)
		}
		// Reconcile this returner's acquaintance children to the in-memory set first,
		// then upsert the current ones. Parents are never swept; children are, per
		// parent, so the child table stays a faithful mirror of the in-memory map.
		pcIDs := make([]string, 0, len(rv.Acquaintances))
		for pcID := range rv.Acquaintances {
			pcIDs = append(pcIDs, string(pcID))
		}
		if _, err := tx.Exec(ctx, deleteRecurringAcqNotInSQL, string(rv.ID), pcIDs); err != nil {
			return fmt.Errorf("pg recurring_visitors SaveSnapshot: reconcile acq id=%s: %w", rv.ID, err)
		}
		for pcID, acq := range rv.Acquaintances {
			if acq == nil {
				continue
			}
			if _, err := tx.Exec(ctx, upsertRecurringAcqSQL,
				string(rv.ID),     // $1 recurring_visitor_id
				string(pcID),      // $2 pc_actor_id
				acq.PCDisplayName, // $3 pc_display_name
				acq.FirstMetAt,    // $4 first_met_at
				acq.LastMetAt,     // $5 last_met_at
			); err != nil {
				return fmt.Errorf("pg recurring_visitors SaveSnapshot: upsert acq id=%s pc=%s: %w", rv.ID, pcID, err)
			}
		}
	}
	return nil
}
