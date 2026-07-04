// Package mem provides in-memory fakes for the sim.Repository sub-
// interfaces, suitable for tests and local smoke runs without a Postgres
// dependency.
package mem

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// NewRepository wires a full sim.Repository with mem fakes for every
// sub-interface. Tests typically use this directly and then Seed the
// typed sub-repos via the returned Handles to populate initial state.
func NewRepository() (sim.Repository, *Handles) {
	actors := NewActorsRepo()
	huddles := NewHuddlesRepo()
	scenes := NewScenesRepo()
	orders := NewOrdersRepo()
	env := NewEnvironmentRepo()
	assets := NewAssetsRepo()
	sprites := NewSpritesRepo()
	attributeDefinitions := NewAttributeDefinitionsRepo()
	recipes := NewRecipesRepo()
	itemKinds := NewItemKindsRepo()
	terrain := NewTerrainRepo()
	structures := NewStructuresRepo()
	villageObjects := NewVillageObjectsRepo()
	laborContracts := NewLaborContractsRepo()
	h := &Handles{
		Actors:               actors,
		Huddles:              huddles,
		Scenes:               scenes,
		Orders:               orders,
		Environment:          env,
		Assets:               assets,
		Sprites:              sprites,
		AttributeDefinitions: attributeDefinitions,
		Recipes:              recipes,
		ItemKinds:            itemKinds,
		Terrain:              terrain,
		Structures:           structures,
		VillageObjects:       villageObjects,
		LaborContracts:       laborContracts,
	}
	return sim.Repository{
		Actors:               actors,
		Huddles:              huddles,
		Scenes:               scenes,
		Orders:               orders,
		Environment:          env,
		Assets:               assets,
		Sprites:              sprites,
		AttributeDefinitions: attributeDefinitions,
		Recipes:              recipes,
		ItemKinds:            itemKinds,
		Terrain:              terrain,
		Structures:           structures,
		VillageObjects:       villageObjects,
		LaborContracts:       laborContracts,
		ActionLog:            noopActionLog{},
		TickTelemetry:        noopTickTelemetry{},
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
	Actors               *ActorsRepo
	Huddles              *HuddlesRepo
	Scenes               *ScenesRepo
	Orders               *OrdersRepo
	Environment          *EnvironmentRepo
	Assets               *AssetsRepo
	Sprites              *SpritesRepo
	AttributeDefinitions *AttributeDefinitionsRepo
	Recipes              *RecipesRepo
	ItemKinds            *ItemKindsRepo
	Terrain              *TerrainRepo
	Structures           *StructuresRepo
	VillageObjects       *VillageObjectsRepo
	LaborContracts       *LaborContractsRepo
}

// noopActionLog accepts appends silently; tests don't need to assert
// action-log writes at the skeleton level. (Pay-ledger no longer goes
// through the repo — it's substrate state with no durable backing;
// pending entries are intentionally restart-lossy.)
type noopActionLog struct{}

func (noopActionLog) Append(_ context.Context, _ sim.DurableActionLogRow) error { return nil }

// noopTickTelemetry discards tick telemetry records — tests that care
// about telemetry use a recording fake instead; the skeleton just needs
// the world to have a non-nil sink to write through.
type noopTickTelemetry struct{}

func (noopTickTelemetry) WriteTickTelemetry(_ sim.TickTelemetryRecord) {}

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
