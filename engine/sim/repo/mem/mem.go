// Package mem provides in-memory fakes for the sim.Repository sub-
// interfaces, suitable for tests and local smoke runs without a Postgres
// dependency.
//
// Sub-interfaces not yet implemented in v1 (Structures, Scenes, Orders,
// Environment) return a "not implemented" error on use so tests touching
// them fail fast with a clear message rather than silently passing.
package mem

import (
	"context"
	"errors"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// NewRepository wires a full sim.Repository with mem fakes for every
// sub-interface. Tests typically use this directly and then Seed the
// typed sub-repos via the returned Handles to populate initial state.
func NewRepository() (sim.Repository, *Handles) {
	actors := NewActorsRepo()
	huddles := NewHuddlesRepo()
	env := NewEnvironmentRepo()
	assets := NewAssetsRepo()
	recipes := NewRecipesRepo()
	terrain := NewTerrainRepo()
	structures := NewStructuresRepo()
	villageObjects := NewVillageObjectsRepo()
	h := &Handles{
		Actors:         actors,
		Huddles:        huddles,
		Environment:    env,
		Assets:         assets,
		Recipes:        recipes,
		Terrain:        terrain,
		Structures:     structures,
		VillageObjects: villageObjects,
	}
	return sim.Repository{
		Actors:         actors,
		Huddles:        huddles,
		Environment:    env,
		Assets:         assets,
		Recipes:        recipes,
		Terrain:        terrain,
		Structures:     structures,
		VillageObjects: villageObjects,
		Scenes:         notImplScenes{},
		Orders:         notImplOrders{},
		PayLedger:      noopPayLedger{},
		ActionLog:      noopActionLog{},
		Begin: func(_ context.Context) (sim.Tx, error) {
			return noopTx{}, nil
		},
	}, h
}

// Handles holds typed references to the mem sub-repos so tests can Seed
// them directly. The repos behind these handles are the same objects that
// the returned sim.Repository delegates to — mutations via either path
// are visible to the other.
type Handles struct {
	Actors         *ActorsRepo
	Huddles        *HuddlesRepo
	Environment    *EnvironmentRepo
	Assets         *AssetsRepo
	Recipes        *RecipesRepo
	Terrain        *TerrainRepo
	Structures     *StructuresRepo
	VillageObjects *VillageObjectsRepo
}

// errNotImpl is returned by sub-repos that haven't been ported into the
// mem fake yet. Add the missing impl when the test requires it.
var errNotImpl = errors.New("mem fake not implemented for this sub-repo yet — add it when the test requires it")

type notImplScenes struct{}

func (notImplScenes) LoadAll(_ context.Context) (map[sim.SceneID]*sim.Scene, error) {
	return nil, errNotImpl
}
func (notImplScenes) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.SceneID]*sim.Scene) error {
	return errNotImpl
}

type notImplOrders struct{}

func (notImplOrders) LoadAll(_ context.Context) (map[sim.OrderID]*sim.Order, error) {
	return nil, errNotImpl
}
func (notImplOrders) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.OrderID]*sim.Order) error {
	return errNotImpl
}

// noopPayLedger / noopActionLog accept appends silently; tests don't need
// to assert ledger writes at the skeleton level.
type noopPayLedger struct{}

func (noopPayLedger) Append(_ context.Context, _ sim.PayLedgerEntry) error { return nil }

type noopActionLog struct{}

func (noopActionLog) Append(_ context.Context, _ sim.ActionLogEntry) error { return nil }

// noopTx satisfies sim.Tx without doing anything — fine for the skeleton
// since no sub-repo here actually issues SQL. Real Tx behavior comes with
// the pg implementation under engine/sim/repo/pg.
type noopTx struct{}

func (noopTx) Exec(_ context.Context, _ string, _ ...any) (sim.CommandTag, error) {
	return noopCommandTag{}, nil
}
func (noopTx) Query(_ context.Context, _ string, _ ...any) (sim.Rows, error) {
	return noopRows{}, nil
}
func (noopTx) QueryRow(_ context.Context, _ string, _ ...any) sim.Row { return noopRow{} }
func (noopTx) Commit(_ context.Context) error                         { return nil }
func (noopTx) Rollback(_ context.Context) error                       { return nil }

type noopCommandTag struct{}

func (noopCommandTag) RowsAffected() int64 { return 0 }

type noopRows struct{}

func (noopRows) Next() bool          { return false }
func (noopRows) Scan(_ ...any) error { return nil }
func (noopRows) Err() error          { return nil }
func (noopRows) Close()              {}

type noopRow struct{}

func (noopRow) Scan(_ ...any) error { return nil }
