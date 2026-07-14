package pg

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// save_world_quarantine_integration_test.go — real-pg proof of the LLM-392
// contract: BAD ROW CONTENT MUST NOT ABORT THE CHECKPOINT.
//
// These run against embedded Postgres with the real schema (so the real
// constraints are live) and are the regression tests for the 2026-07-12
// outage. The unit tests prove the quarantine POLICY; these prove the thing
// that actually failed — that a checkpoint carrying an unpersistable row still
// commits everything else, and that a reload sees it.

// TestIntegration_SaveWorld_LodgingDoubleBook_QuarantinedNotFatal is THE
// regression test for the incident.
//
// The setup reproduces it exactly: two delivered nights_stay ledger rows for
// the same (buyer, seller, ready_by), which is what
// pay_ledger_lodging_active_once forbids. Before LLM-392, the second row's
// UPSERT raised a duplicate-key error, SaveWorld rolled back the entire
// transaction, and — because the in-memory snapshot never changed — every
// checkpoint for the next 17.5 hours did the same. The village ran that whole
// time with zero durability and lost all of it on restart.
//
// The assertion that matters is not "the poison row is reported" — it is that
// THE ACTOR STILL PERSISTED. That is what 17.5 hours of the village was.
func TestIntegration_SaveWorld_LodgingDoubleBook_QuarantinedNotFatal(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	seedQuarantineItemKinds(t, f, ctx)

	w := checkpointableWorld(repo)
	now := time.Date(2026, 7, 12, 23, 0, 0, 0, time.UTC)
	readyBy := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)

	// An ordinary actor, with coins and inventory — the state the outage threw
	// away. Nothing about this actor is wrong.
	w.Actors = map[sim.ActorID]*sim.Actor{
		ezekielID: {
			ID: ezekielID, Kind: sim.KindNPCShared,
			DisplayName: "Ezekiel", State: "idle",
			Coins:     42,
			Inventory: map[sim.ItemKind]int{"bread": 3},
			Needs:     map[sim.NeedKey]int{"hunger": 5},
		},
	}
	// The poison: two delivered lodging ledger rows for one booking.
	lodging := func(id sim.OrderID) *sim.Order {
		return &sim.Order{
			ID: id, LedgerID: sim.LedgerID(id),
			State:    sim.OrderStateDelivered,
			BuyerID:  ezekielID,
			SellerID: hannahID,
			Item:     lodgingItemKind,
			Qty:      1, Amount: 3,
			ReadyBy:   readyBy,
			CreatedAt: now,
			ExpiresAt: now.Add(time.Hour),
		}
	}
	w.Orders = map[sim.OrderID]*sim.Order{
		1447: lodging(1447),
		1448: lodging(1448), // the double-book — unpersistable
	}

	q, err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot())
	if err != nil {
		t.Fatalf("SaveWorld = %v — a single unpersistable ledger row must NOT fail the checkpoint (this is the 17.5-hour outage)", err)
	}
	if q.Clean() {
		t.Fatal("quarantine reports clean, but a double-booked lodging row cannot be persisted — the guard did not fire")
	}
	if !q.Dropped("pay_ledger", "1448") {
		t.Errorf("expected the later duplicate (1448) to be quarantined; rows = %+v", q.Rows())
	}

	// The whole point: everything else is durable.
	loaded, err := LoadWorld(ctx, repo, true)
	if err != nil {
		t.Fatalf("LoadWorld after quarantined checkpoint: %v", err)
	}
	a := loaded.Actors[ezekielID]
	if a == nil {
		t.Fatal("actor ezekiel was NOT persisted — the poison ledger row took the village down with it, which is exactly the bug")
	}
	if a.Coins != 42 {
		t.Errorf("ezekiel.Coins = %d, want 42 (actor state must survive a quarantined checkpoint)", a.Coins)
	}
	if a.Inventory["bread"] != 3 {
		t.Errorf("ezekiel.Inventory[bread] = %d, want 3", a.Inventory["bread"])
	}
	// The surviving booking is durable; the dropped one is not.
	if _, ok := loaded.Orders[1447]; !ok {
		// 1447 is delivered, so it is terminal and not reloaded as in-flight —
		// assert on the ledger row itself instead.
		var count int
		row := f.Pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM pay_ledger WHERE id = 1447`)
		if err := row.Scan(&count); err != nil {
			t.Fatalf("count ledger 1447: %v", err)
		}
		if count != 1 {
			t.Errorf("ledger row 1447 (the surviving booking) count = %d, want 1", count)
		}
	}
	var poisonCount int
	row := f.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM pay_ledger WHERE id = 1448`)
	if err := row.Scan(&poisonCount); err != nil {
		t.Fatalf("count ledger 1448: %v", err)
	}
	if poisonCount != 0 {
		t.Errorf("quarantined ledger row 1448 was written anyway (count=%d)", poisonCount)
	}
}

// TestIntegration_SaveWorld_DroppedActor_KeepsPreviousRowAndChildren proves the
// sweep guard, which is the subtlest part of the design.
//
// A quarantined row is NOT re-upserted, so its durable row still carries the
// OLD snapshot_gen — precisely what `DELETE ... WHERE snapshot_gen < $1` is
// aimed at. If the sweep ran, quarantining a row would DELETE it, turning "this
// actor is one checkpoint stale" into "this actor no longer exists". So a table
// with a drop stops sweeping, and a dropped PARENT also stops its child tables
// from sweeping — otherwise the actor's row would survive while its needs and
// inventory were swept out from under it.
func TestIntegration_SaveWorld_DroppedActor_KeepsPreviousRowAndChildren(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	seedQuarantineItemKinds(t, f, ctx)

	w := checkpointableWorld(repo)
	w.Actors = map[sim.ActorID]*sim.Actor{
		hannahID: {
			ID: hannahID, Kind: sim.KindNPCShared,
			DisplayName: "Hannah", State: "idle",
			Coins:     10,
			Inventory: map[sim.ItemKind]int{"porridge": 4},
			Needs:     map[sim.NeedKey]int{"hunger": 6},
		},
	}

	// Checkpoint 1: clean. Hannah is durable, with children.
	if _, err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("clean SaveWorld: %v", err)
	}

	// Checkpoint 2: Hannah's State is corrupted to empty — an unwritable actor
	// row (the FSM invariant). She must be quarantined, NOT deleted.
	w.Actors[hannahID].State = ""
	w.Actors[hannahID].Coins = 999 // this update must NOT land — she was dropped
	q, err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot())
	if err != nil {
		t.Fatalf("SaveWorld with a corrupt actor = %v, want nil (quarantine, not abort)", err)
	}
	if !q.Dropped("actor", string(hannahID)) {
		t.Fatalf("expected hannah to be quarantined; rows = %+v", q.Rows())
	}

	// She still exists, at her LAST GOOD values — not deleted, not updated.
	loaded, err := LoadWorld(ctx, repo, true)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	h := loaded.Actors[hannahID]
	if h == nil {
		t.Fatal("hannah was DELETED by the stale-row sweep — quarantining a row must never delete it")
	}
	if h.Coins != 10 {
		t.Errorf("hannah.Coins = %d, want 10 (the quarantined actor keeps her last durable values, not the corrupt update)", h.Coins)
	}
	// Her children survived too — the parent drop blocked their sweeps.
	if h.Inventory["porridge"] != 4 {
		t.Errorf("hannah.Inventory[porridge] = %d, want 4 — a dropped parent must block its child tables' sweeps, or she is left half-erased", h.Inventory["porridge"])
	}
	if h.Needs["hunger"] != 6 {
		t.Errorf("hannah.Needs[hunger] = %d, want 6 (child sweep ran despite the parent drop)", h.Needs["hunger"])
	}
}

// TestIntegration_SaveWorld_DroppedChildRow_NeverReachesSQL — the regression
// test for the bug code_review caught on the first pass.
//
// A dropped CHILD row is only actually quarantined if the WRITE loop skips it.
// If validation records the drop but the writer still executes the upsert, the
// bad row reaches Postgres and re-triggers the very mid-transaction content
// failure this whole mechanism exists to eliminate — a quarantine that quarantines
// nothing. (First implementation had exactly that: the skip guards were inserted
// into the validation loops instead of the write loops, because the two share
// identical `for k, v := range a.Needs` headers.)
//
// An empty need key and an empty inventory item kind are both unwritable (PK
// column; the item kind also FKs to item_kind). The checkpoint must commit with
// the actor intact and those child rows simply absent.
func TestIntegration_SaveWorld_DroppedChildRow_NeverReachesSQL(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	seedQuarantineItemKinds(t, f, ctx)

	w := checkpointableWorld(repo)
	w.Actors = map[sim.ActorID]*sim.Actor{
		josiahID: {
			ID: josiahID, Kind: sim.KindNPCShared,
			DisplayName: "Josiah", State: "idle",
			Needs: map[sim.NeedKey]int{
				"hunger": 5,
				"":       7, // empty need key — unwritable (PK column)
			},
			Inventory: map[sim.ItemKind]int{
				"bread": 2,
				"":      1, // empty item kind — unwritable (PK column + item_kind FK)
			},
		},
	}

	q, err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot())
	if err != nil {
		t.Fatalf("SaveWorld = %v — an unwritable CHILD row must be skipped at the write step, not sent to pg", err)
	}
	if !q.Dropped("actor_need", childID(josiahID, "")) {
		t.Errorf("empty need key should be quarantined; rows = %+v", q.Rows())
	}
	if !q.Dropped("actor_inventory", childID(josiahID, "")) {
		t.Errorf("empty inventory item kind should be quarantined; rows = %+v", q.Rows())
	}

	// The actor and his GOOD child rows are durable; the bad ones are absent.
	loaded, err := LoadWorld(ctx, repo, true)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	j := loaded.Actors[josiahID]
	if j == nil {
		t.Fatal("josiah was not persisted — one bad child row must not cost the actor")
	}
	if j.Needs["hunger"] != 5 {
		t.Errorf("josiah.Needs[hunger] = %d, want 5 (the GOOD sibling row must still persist)", j.Needs["hunger"])
	}
	if j.Inventory["bread"] != 2 {
		t.Errorf("josiah.Inventory[bread] = %d, want 2 (the GOOD sibling row must still persist)", j.Inventory["bread"])
	}
}

// TestIntegration_SaveWorld_ClampedNeed_PersistsActor — the clamp path. An
// out-of-range need is an arithmetic bug in the needs tick; losing the actor
// over it would be wildly disproportionate, so the value is forced into range
// and the actor persists normally. The clamp is still reported (and alarms), so
// the underlying bug does not hide.
func TestIntegration_SaveWorld_ClampedNeed_PersistsActor(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	w := checkpointableWorld(repo)
	w.Actors = map[sim.ActorID]*sim.Actor{
		josiahID: {
			ID: josiahID, Kind: sim.KindNPCShared,
			DisplayName: "Josiah", State: "idle",
			Needs: map[sim.NeedKey]int{"hunger": 99}, // CHECK allows [0,24]
		},
	}

	q, err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot())
	if err != nil {
		t.Fatalf("SaveWorld = %v, want nil (an out-of-range need must not fail the checkpoint)", err)
	}
	if q.Clean() {
		t.Fatal("an out-of-range need should have been recorded as a clamp")
	}
	if q.Dropped("actor", string(josiahID)) {
		t.Error("josiah was DROPPED for one out-of-range need — the value should be clamped and the actor kept")
	}

	loaded, err := LoadWorld(ctx, repo, true)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	j := loaded.Actors[josiahID]
	if j == nil {
		t.Fatal("josiah was not persisted")
	}
	if j.Needs["hunger"] != 24 {
		t.Errorf("josiah.Needs[hunger] = %d, want 24 (clamped to the column's max, not dropped)", j.Needs["hunger"])
	}
}

// Actor ids are UUIDs (actor.id is a uuid column), so the quarantine fixtures
// use stable named UUIDs rather than bare names.
const (
	ezekielID = sim.ActorID("11111111-0000-0000-0000-000000000392")
	hannahID  = sim.ActorID("22222222-0000-0000-0000-000000000392")
	josiahID  = sim.ActorID("33333333-0000-0000-0000-000000000392")
)

// seedQuarantineItemKinds seeds the reference goods these fixtures use.
// pay_ledger.item_kind and actor_inventory.item_kind both FK to item_kind(name),
// so the goods have to exist before any row can reference them.
func seedQuarantineItemKinds(t *testing.T, f *integrationFixture, ctx context.Context) {
	t.Helper()
	if _, err := f.Pool.Exec(ctx, `
        INSERT INTO item_kind (name, display_label, category) VALUES
            ('nights_stay', 'Night''s Stay', 'material'),
            ('bread',       'Bread',         'food'),
            ('porridge',    'Porridge',      'food')
        ON CONFLICT (name) DO NOTHING`); err != nil {
		t.Fatalf("seed item_kind: %v", err)
	}
}
