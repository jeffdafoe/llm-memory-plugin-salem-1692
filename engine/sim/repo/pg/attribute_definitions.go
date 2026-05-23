package pg

import (
	"context"
	"fmt"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// AttributeDefinitionsRepo loads the actor-assignable attribute-definition
// catalog — attribute_definition rows scoped to actors — into
// sim.AttributeDefinition aggregates keyed by slug. Reference state:
// read-only, no checkpoint path. Admin edits write directly to the table
// through the editor port; the world rebuilds the map wholesale via LoadAll
// at startup (and on SIGHUP when that lands).
//
// Port of v1's handleListNPCBehaviors query (engine/npcs.go). Parallel to
// SpritesRepo / AssetsRepo — a distinct reference catalog with a single flat
// table and no child rows, so it's a one-query load.
type AttributeDefinitionsRepo struct {
	pool Pool
}

// NewAttributeDefinitionsRepo constructs an AttributeDefinitionsRepo against
// the given pool. Normal wiring path is pg.NewRepository.
func NewAttributeDefinitionsRepo(pool Pool) *AttributeDefinitionsRepo {
	return &AttributeDefinitionsRepo{pool: pool}
}

// loadAllAttributeDefinitionsSQL reads the actor-assignable subset — scope
// 'actor' or 'both', excluding object-only definitions — matching v1's
// handleListNPCBehaviors filter. The ORDER BY is not load-bearing for the map
// (it's keyed by slug) but keeps the query plan deterministic; the read
// handler re-sorts for the wire response anyway.
const loadAllAttributeDefinitionsSQL = `
SELECT slug, display_name
  FROM attribute_definition
 WHERE scope IN ('actor', 'both')
 ORDER BY display_name`

// LoadAll reads the actor-scoped attribute_definition rows into the keyed map.
// One query, no N+1. Runs against the pool directly (no Tx) — read-only
// restart path, same posture as the other reference-catalog repos.
//
// slug is the table PK so a duplicate is unreachable in valid data; the
// map-build guards loudly against schema drift rather than letting a later
// row silently win.
func (r *AttributeDefinitionsRepo) LoadAll(ctx context.Context) (map[string]*sim.AttributeDefinition, error) {
	rows, err := r.pool.Query(ctx, loadAllAttributeDefinitionsSQL)
	if err != nil {
		return nil, fmt.Errorf("pg attribute_definitions LoadAll: query: %w", err)
	}
	defer rows.Close()

	defs := make(map[string]*sim.AttributeDefinition)
	for rows.Next() {
		var d sim.AttributeDefinition
		if err := rows.Scan(&d.Slug, &d.DisplayName); err != nil {
			return nil, fmt.Errorf("pg attribute_definitions LoadAll: scan: %w", err)
		}
		if _, exists := defs[d.Slug]; exists {
			return nil, fmt.Errorf("pg attribute_definitions LoadAll: duplicate slug %q", d.Slug)
		}
		defs[d.Slug] = &d
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg attribute_definitions LoadAll: iter: %w", err)
	}
	return defs, nil
}
