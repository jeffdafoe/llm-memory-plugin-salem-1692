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

// NewRepository wires a sim.Repository where Orders is the pg impl and
// every other sub-repo is notImpl. Slice 5 ships Orders only; other
// aggregates land as their pg-impl slices arrive. Begin returns a
// pgx.Tx wrapped as sim.Tx.
//
// Future slices replace notImpl stubs in-place (or via a richer
// constructor that accepts opts) — the shape established here is the
// pattern other aggregates follow.
func NewRepository(pool Pool) sim.Repository {
	return sim.Repository{
		Actors:         notImplActors{},
		Structures:     &StructuresRepo{pool: pool},
		Huddles:        &HuddlesRepo{pool: pool},
		Scenes:         notImplScenes{},
		Orders:         &OrdersRepo{pool: pool},
		Environment:    notImplEnvironment{},
		Assets:         notImplAssets{},
		Recipes:        notImplRecipes{},
		ItemKinds:      notImplItemKinds{},
		Terrain:        notImplTerrain{},
		VillageObjects: &VillageObjectsRepo{pool: pool},
		ActionLog:      notImplActionLog{},
		TickTelemetry:  notImplTickTelemetry{},
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

type notImplActors struct{}

func (notImplActors) LoadAll(_ context.Context) (map[sim.ActorID]*sim.Actor, error) {
	return nil, errNotImpl
}
func (notImplActors) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.ActorID]*sim.Actor) error {
	return errNotImpl
}

type notImplScenes struct{}

func (notImplScenes) LoadAll(_ context.Context) (map[sim.SceneID]*sim.Scene, error) {
	return nil, errNotImpl
}
func (notImplScenes) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.SceneID]*sim.Scene) error {
	return errNotImpl
}

type notImplEnvironment struct{}

func (notImplEnvironment) Load(_ context.Context) (sim.WorldEnvironment, sim.Phase, sim.WorldSettings, error) {
	return sim.WorldEnvironment{}, sim.Phase(""), sim.WorldSettings{}, errNotImpl
}
func (notImplEnvironment) SaveSnapshot(_ context.Context, _ sim.Tx, _ sim.WorldEnvironment, _ sim.Phase) error {
	return errNotImpl
}

type notImplAssets struct{}

func (notImplAssets) LoadAll(_ context.Context) (map[sim.AssetID]*sim.Asset, error) {
	return nil, errNotImpl
}

type notImplRecipes struct{}

func (notImplRecipes) LoadAll(_ context.Context) (map[sim.ItemKind]*sim.ItemRecipe, error) {
	return nil, errNotImpl
}

type notImplItemKinds struct{}

func (notImplItemKinds) LoadAll(_ context.Context) (map[sim.ItemKind]*sim.ItemKindDef, error) {
	return nil, errNotImpl
}

type notImplTerrain struct{}

func (notImplTerrain) Load(_ context.Context) (*sim.Terrain, error) { return nil, errNotImpl }

type notImplActionLog struct{}

func (notImplActionLog) Append(_ context.Context, _ sim.ActionLogEntry) error { return errNotImpl }

type notImplTickTelemetry struct{}

// WriteTickTelemetry is fire-and-forget by contract — silently drop
// rather than return an error the writer must ignore.
func (notImplTickTelemetry) WriteTickTelemetry(_ sim.TickTelemetryRecord) {}
