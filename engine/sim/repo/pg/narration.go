package pg

import (
	"context"
	"fmt"
)

// narration.go — narration_pool_expansion persistence (ZBBS-WORK-399).
//
// Two halves, both small:
//
//   - LoadAll: boot-time read of every persisted expansion phrase, fed to
//     World.MergeNarrationExpansions between LoadWorld and Run (main.go).
//     Not part of sim.Repository / LoadWorld — the pool registry seeds
//     itself from compile-time tables and works without this read, so
//     the merge stays a main.go concern with main.go's error posture
//     (log + continue with seed-only pools).
//   - Append: the sim.NarrationExpansionSink impl the expansion cascade
//     writes through at expansion time. SYNCHRONOUS by contract (the
//     cascade applies phrases in memory only after the write lands) and
//     called from the cascade goroutine, never the world goroutine, so
//     blocking on PG here is fine.
//
// Append runs all of one expansion's rows in a single Tx: an expansion
// is all-or-nothing durably, so a crash can't land half a batch that a
// restart would then merge as a smaller pool than the live one had.
// ON CONFLICT DO NOTHING on the (pool_key, phrase) PK makes a retried
// expansion (or a phrase the model re-emitted across expansions on two
// different days) idempotent rather than an error.

const loadNarrationExpansionsSQL = `
SELECT pool_key, phrase
  FROM narration_pool_expansion
 ORDER BY pool_key, created_at, phrase`

const insertNarrationExpansionSQL = `
INSERT INTO narration_pool_expansion (pool_key, phrase, generated_by)
VALUES ($1, $2, $3)
ON CONFLICT (pool_key, phrase) DO NOTHING`

// NarrationExpansionRepo persists LLM-expanded narration pool phrases.
// Implements sim.NarrationExpansionSink (Append). Construct with
// NewNarrationExpansionRepo; install on the World via
// SetNarrationExpansionSink.
type NarrationExpansionRepo struct {
	pool Pool
}

// NewNarrationExpansionRepo wires the repo. Panics on nil pool — a
// wiring bug should fail at startup, not on the first expansion.
func NewNarrationExpansionRepo(pool Pool) *NarrationExpansionRepo {
	if pool == nil {
		panic("pg: NewNarrationExpansionRepo requires a non-nil pool")
	}
	return &NarrationExpansionRepo{pool: pool}
}

// LoadAll returns every persisted expansion phrase grouped by pool key,
// in insertion order per pool (created_at, then phrase for same-instant
// determinism). Keys with no rows are simply absent.
func (r *NarrationExpansionRepo) LoadAll(ctx context.Context) (map[string][]string, error) {
	rows, err := r.pool.Query(ctx, loadNarrationExpansionsSQL)
	if err != nil {
		return nil, fmt.Errorf("pg: load narration_pool_expansion: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]string)
	for rows.Next() {
		var poolKey, phrase string
		if err := rows.Scan(&poolKey, &phrase); err != nil {
			return nil, fmt.Errorf("pg: scan narration_pool_expansion row: %w", err)
		}
		out[poolKey] = append(out[poolKey], phrase)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg: iterate narration_pool_expansion: %w", err)
	}
	return out, nil
}

// Append durably writes one expansion's phrases for poolKey inside a
// single Tx (see file header for the all-or-nothing rationale).
// Satisfies sim.NarrationExpansionSink.
func (r *NarrationExpansionRepo) Append(ctx context.Context, poolKey string, phrases []string, generatedBy string) error {
	if len(phrases) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg: begin narration_pool_expansion append: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, phrase := range phrases {
		if _, err := tx.Exec(ctx, insertNarrationExpansionSQL, poolKey, phrase, generatedBy); err != nil {
			return fmt.Errorf("pg: insert narration_pool_expansion (%s): %w", poolKey, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg: commit narration_pool_expansion append: %w", err)
	}
	return nil
}
