package pg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// Pool is the surface OrdersRepo needs from the database. *pgxpool.Pool
// satisfies this naturally; pgxmock.PgxPoolIface satisfies it too. The
// interface stays minimal so the test mock surface is small.
//
// LoadAll uses Pool.Query directly (no Tx — read-only restart path).
// SaveSnapshot runs inside the caller-supplied Tx (the checkpoint flow
// passes one in).
type Pool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// NewRepository wires a sim.Repository with the pg implementation of every
// load/checkpoint sub-repo (all aggregate slices have landed). Begin returns a
// pgx.Tx wrapped as sim.Tx. The only remaining stubs are the write-only event
// sinks ActionLog + TickTelemetry — notImpl until the cutover wires their
// durable projections; LoadWorld never reads them, so the requireAllImpl gate
// passes. The notImplXxx types kept in this file back load_world_test.go's
// notImpl-tolerance tests, not production wiring.
func NewRepository(pool Pool) sim.Repository {
	return sim.Repository{
		Actors:               &ActorsRepo{pool: pool},
		Structures:           &StructuresRepo{pool: pool},
		Huddles:              &HuddlesRepo{pool: pool},
		Scenes:               &ScenesRepo{pool: pool},
		Orders:               &OrdersRepo{pool: pool},
		Environment:          &EnvironmentRepo{pool: pool},
		Assets:               &AssetsRepo{pool: pool},
		Sprites:              &SpritesRepo{pool: pool},
		AttributeDefinitions: &AttributeDefinitionsRepo{pool: pool},
		Recipes:              &RecipesRepo{pool: pool},
		ItemKinds:            &ItemKindsRepo{pool: pool},
		Terrain:              &TerrainRepo{pool: pool},
		VillageObjects:       &VillageObjectsRepo{pool: pool},
		LaborContracts:       &LaborContractsRepo{pool: pool},
		Visitors:             &VisitorsRepo{pool: pool},
		ActionLog:            notImplActionLog{},
		TickTelemetry:        notImplTickTelemetry{},
		Begin: func(ctx context.Context) (sim.Tx, error) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				return nil, err
			}
			return &txAdapter{tx: tx}, nil
		},
	}
}

// errNotImpl mirrors the mem package's signaling — a sub-repo whose pg
// impl hasn't landed yet returns this rather than nil, so any caller
// touching it fails loudly. Wires the future pg-impl slice as a
// drop-in replacement, no caller change.
var errNotImpl = errors.New("pg sub-repo not implemented yet — lands in a later slice")

// notImplActors is no longer wired by NewRepository (Slice 1 ships
// ActorsRepo), but load_world_test.go retains a test that exercises
// LoadWorld's notImpl-tolerance with an Actors stub. Kept here so the
// test continues to compile; remove when all aggregates are pg-impl.
type notImplActors struct{}

func (notImplActors) LoadAll(_ context.Context) (map[sim.ActorID]*sim.Actor, error) {
	return nil, errNotImpl
}
func (notImplActors) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.ActorID]*sim.Actor) error {
	return errNotImpl
}

// notImplAssets is no longer wired by NewRepository (ZBBS-WORK-247 ships
// AssetsRepo), but load_world_test.go retains a test that exercises
// LoadWorld's notImpl-tolerance with an Assets stub. Kept here so the test
// continues to compile; remove when the notImpl-tolerance test is retired.
type notImplAssets struct{}

func (notImplAssets) LoadAll(_ context.Context) (map[sim.AssetID]*sim.Asset, error) {
	return nil, errNotImpl
}

// notImplSprites is not wired by NewRepository (ZBBS-WORK-256 ships
// SpritesRepo), but load_world_test.go's notImpl-tolerance test exercises
// LoadWorld with this stub. Kept here so the test compiles; remove when the
// notImpl-tolerance test is retired.
type notImplSprites struct{}

func (notImplSprites) LoadAll(_ context.Context) (map[sim.SpriteID]*sim.Sprite, error) {
	return nil, errNotImpl
}

type notImplRecipes struct{}

func (notImplRecipes) LoadAll(_ context.Context) (map[sim.ItemKind]*sim.ItemRecipe, error) {
	return nil, errNotImpl
}

// notImplAttributeDefinitions is not wired by NewRepository (ZBBS-HOME-292
// ships AttributeDefinitionsRepo), but load_world_test.go's notImpl-tolerance
// test exercises LoadWorld with this stub. Kept here so the test compiles;
// remove when the notImpl-tolerance test is retired.
type notImplAttributeDefinitions struct{}

func (notImplAttributeDefinitions) LoadAll(_ context.Context) (map[string]*sim.AttributeDefinition, error) {
	return nil, errNotImpl
}

type notImplItemKinds struct{}

func (notImplItemKinds) LoadAll(_ context.Context) (map[sim.ItemKind]*sim.ItemKindDef, error) {
	return nil, errNotImpl
}

type notImplTerrain struct{}

func (notImplTerrain) Load(_ context.Context) (*sim.Terrain, error) { return nil, errNotImpl }

type notImplActionLog struct{}

func (notImplActionLog) Append(_ context.Context, _ sim.DurableActionLogRow) error { return errNotImpl }

type notImplTickTelemetry struct{}

// WriteTickTelemetry is fire-and-forget by contract — silently drop
// rather than return an error the writer must ignore.
func (notImplTickTelemetry) WriteTickTelemetry(_ sim.TickTelemetryRecord) {}
