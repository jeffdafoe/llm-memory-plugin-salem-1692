package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// SaveWorld tests use spy sub-repos that record the order their
// SaveSnapshot is called in (and can inject a failure at a chosen
// aggregate) plus a spy Tx that records Commit/Rollback. This layer
// tests orchestration — call order, atomic commit, and rollback-on-error.
// Per-repo SaveSnapshot SQL semantics are covered by each *_test.go.

// --- spies -----------------------------------------------------------------

// saveRecorder collects the labels of SaveSnapshot calls in order and, if
// failOn is set, returns failErr the first time that label is recorded.
type saveRecorder struct {
	order   []string
	failOn  string
	failErr error
}

func (r *saveRecorder) record(label string) error {
	r.order = append(r.order, label)
	if label == r.failOn {
		return r.failErr
	}
	return nil
}

// Each spy embeds the corresponding LoadWorld fake to inherit LoadAll (and
// LoadRecentPrices for Orders), then overrides SaveSnapshot to record.
type spyVO struct {
	fakeVillageObjects
	rec *saveRecorder
}

func (s spyVO) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.VillageObjectID]*sim.VillageObject) error {
	return s.rec.record("VillageObjects")
}

type spyStructures struct {
	fakeStructures
	rec *saveRecorder
}

func (s spyStructures) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.StructureID]*sim.Structure) error {
	return s.rec.record("Structures")
}

type spyHuddles struct {
	fakeHuddles
	rec *saveRecorder
}

func (s spyHuddles) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.HuddleID]*sim.Huddle) error {
	return s.rec.record("Huddles")
}

type spyScenes struct {
	fakeScenes
	rec *saveRecorder
}

func (s spyScenes) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.SceneID]*sim.Scene) error {
	return s.rec.record("Scenes")
}

type spyActors struct {
	fakeActors
	rec *saveRecorder
}

func (s spyActors) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.ActorID]*sim.Actor) error {
	return s.rec.record("Actors")
}

type spyOrders struct {
	fakeOrders
	rec *saveRecorder
}

func (s spyOrders) SaveSnapshot(_ context.Context, _ sim.Tx, _ map[sim.OrderID]*sim.Order) error {
	return s.rec.record("Orders")
}

type spyEnv struct {
	fakeEnvironment
	rec *saveRecorder
}

func (s spyEnv) SaveSnapshot(_ context.Context, _ sim.Tx, _ sim.WorldEnvironment, _ sim.Phase) error {
	return s.rec.record("Environment")
}

// spyTx records Commit/Rollback. commitCalled tracks the attempt; committed
// is set only on a SUCCESSFUL commit, so a failed Commit doesn't masquerade
// as a successful one in assertions. The query surface is never touched by
// SaveWorld (the spy repos ignore the tx), so Exec/Query/QueryRow panic to
// catch any accidental use.
type spyTx struct {
	commitCalled bool
	committed    bool
	rolledBack   bool
	commitErr    error
}

func (t *spyTx) Exec(context.Context, string, ...any) (sim.CommandTag, error) {
	panic("spyTx.Exec should not be called")
}
func (t *spyTx) Query(context.Context, string, ...any) (sim.Rows, error) {
	panic("spyTx.Query should not be called")
}
func (t *spyTx) QueryRow(context.Context, string, ...any) sim.Row {
	panic("spyTx.QueryRow should not be called")
}
func (t *spyTx) Commit(context.Context) error {
	t.commitCalled = true
	if t.commitErr != nil {
		return t.commitErr
	}
	t.committed = true
	return nil
}
func (t *spyTx) Rollback(context.Context) error { t.rolledBack = true; return nil }

// saveSpyRepo assembles a sim.Repository whose seven checkpoint writers are
// spies sharing rec, and whose Begin returns tx (or beginErr if non-nil).
func saveSpyRepo(rec *saveRecorder, tx *spyTx, beginErr error) sim.Repository {
	r := fakeRepoOpts{
		villageObjects: spyVO{rec: rec},
		structures:     spyStructures{rec: rec},
		huddles:        spyHuddles{rec: rec},
		scenes:         spyScenes{rec: rec},
		actors:         spyActors{rec: rec},
		orders:         spyOrders{rec: rec},
		environment:    spyEnv{rec: rec},
	}.build()
	r.Begin = func(context.Context) (sim.Tx, error) {
		if beginErr != nil {
			return nil, beginErr
		}
		return tx, nil
	}
	return r
}

// expectedSaveOrder is the dependency order SaveWorld drives. Roots first,
// mirroring LoadWorld's narrative (not FK-load-bearing — see save_world.go).
var expectedSaveOrder = []string{
	"VillageObjects", "Structures", "Huddles", "Scenes", "Actors", "Orders", "Environment",
}

func sameOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- tests -----------------------------------------------------------------

// TestSaveWorld_HappyPath — all seven aggregates checkpoint in order, the
// Tx commits, and no rollback fires.
func TestSaveWorld_HappyPath(t *testing.T) {
	rec := &saveRecorder{}
	tx := &spyTx{}
	repo := saveSpyRepo(rec, tx, nil)

	if err := SaveWorld(context.Background(), repo, sim.NewWorld(repo)); err != nil {
		t.Fatalf("SaveWorld: %v", err)
	}
	if !sameOrder(rec.order, expectedSaveOrder) {
		t.Errorf("save order = %v, want %v", rec.order, expectedSaveOrder)
	}
	if !tx.committed {
		t.Error("expected Commit")
	}
	if tx.rolledBack {
		t.Error("unexpected Rollback after successful commit")
	}
}

// TestSaveWorld_AbortsAndRollsBackMidCheckpoint — a SaveSnapshot failure
// stops the run at that aggregate, leaves later ones untouched, rolls the
// Tx back, never commits, and surfaces a wrapped error naming the aggregate.
func TestSaveWorld_AbortsAndRollsBackMidCheckpoint(t *testing.T) {
	sentinel := errors.New("scenes boom")
	rec := &saveRecorder{failOn: "Scenes", failErr: sentinel}
	tx := &spyTx{}
	repo := saveSpyRepo(rec, tx, nil)

	err := SaveWorld(context.Background(), repo, sim.NewWorld(repo))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v does not wrap sentinel", err)
	}
	wantOrder := []string{"VillageObjects", "Structures", "Huddles", "Scenes"}
	if !sameOrder(rec.order, wantOrder) {
		t.Errorf("save order = %v, want %v (aborts at Scenes, Actors/Orders/Environment untouched)", rec.order, wantOrder)
	}
	if tx.committed {
		t.Error("must not commit after a SaveSnapshot failure")
	}
	if !tx.rolledBack {
		t.Error("expected Rollback after SaveSnapshot failure")
	}
}

// TestSaveWorld_BeginError — a Begin failure surfaces wrapped, before any
// SaveSnapshot runs.
func TestSaveWorld_BeginError(t *testing.T) {
	sentinel := errors.New("begin boom")
	rec := &saveRecorder{}
	repo := saveSpyRepo(rec, nil, sentinel)

	err := SaveWorld(context.Background(), repo, sim.NewWorld(repo))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v does not wrap sentinel", err)
	}
	if len(rec.order) != 0 {
		t.Errorf("no SaveSnapshot should run on Begin failure, got %v", rec.order)
	}
}

// TestSaveWorld_CommitError — a Commit failure surfaces wrapped after all
// seven aggregates wrote, and the deferred Rollback still fires (committed
// flag never flips on a failed commit).
func TestSaveWorld_CommitError(t *testing.T) {
	sentinel := errors.New("commit boom")
	rec := &saveRecorder{}
	tx := &spyTx{commitErr: sentinel}
	repo := saveSpyRepo(rec, tx, nil)

	err := SaveWorld(context.Background(), repo, sim.NewWorld(repo))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v does not wrap sentinel", err)
	}
	if !sameOrder(rec.order, expectedSaveOrder) {
		t.Errorf("save order = %v, want %v", rec.order, expectedSaveOrder)
	}
	if !tx.commitCalled {
		t.Error("expected Commit attempt")
	}
	if tx.committed {
		t.Error("failed Commit must not count as committed")
	}
	if !tx.rolledBack {
		t.Error("expected Rollback after Commit failure")
	}
}

// TestSaveWorld_NilWorld — guard returns before Begin.
func TestSaveWorld_NilWorld(t *testing.T) {
	rec := &saveRecorder{}
	tx := &spyTx{}
	repo := saveSpyRepo(rec, tx, nil)

	if err := SaveWorld(context.Background(), repo, nil); err == nil {
		t.Fatal("expected error on nil world")
	}
	if tx.committed || tx.rolledBack {
		t.Error("nil-world guard must return before touching the Tx")
	}
}
