package pg

// Real-pg integration tests for the checkpoint clamp pass (LLM-392).
//
// The unit tests in engine/sim prove the projection arithmetic. These prove the
// part no fake can: that the values the clamp corrects are EXACTLY the values
// real Postgres refuses, and that correcting them turns a checkpoint that aborts
// its transaction into one that commits and reloads.
//
// Each test asserts BOTH halves deliberately — first that the un-clamped snapshot
// really does fail (otherwise the clamp is defending against nothing and the test
// would keep passing after the constraint was dropped), then that the clamped one
// commits. That pairing is the regression contract.

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestIntegration_SaveWorld_OutOfRangeNeed_FailsUnclamped_CommitsClamped is the
// LLM-392 outage in miniature.
//
// One actor's hunger sits at 25 on a column CHECKed to [0,24] — the shape of
// arithmetic that overshoots a ceiling. Un-clamped, that single value aborts the
// whole checkpoint: the OTHER actor, who is perfectly valid, is not persisted
// either, and because the next snapshot contains the same bad need, so is every
// checkpoint after it. That is how 17.5 hours of durability was lost.
//
// Clamped, the need is written as 24, the village persists, and the alarm carries
// the correction. Note what is asserted about the bystander: his survival IS the
// feature.
func TestIntegration_SaveWorld_OutOfRangeNeed_FailsUnclamped_CommitsClamped(t *testing.T) {
	requireIntegration(t)
	ctx := t.Context()

	const (
		overshotID  = sim.ActorID("aaaaaaaa-0000-0000-0000-000000000392")
		bystanderID = sim.ActorID("bbbbbbbb-0000-0000-0000-000000000392")
	)
	newWorld := func(repo sim.Repository) *sim.World {
		w := checkpointableWorld(repo)
		w.Actors = map[sim.ActorID]*sim.Actor{
			overshotID: {
				ID: overshotID, DisplayName: "Overshot", State: sim.StateIdle,
				Needs: map[sim.NeedKey]int{"hunger": 25},
			},
			bystanderID: {
				ID: bystanderID, DisplayName: "Bystander", State: sim.StateIdle,
				Needs: map[sim.NeedKey]int{"hunger": 12},
			},
		}
		return w
	}

	// Half 1: un-clamped, the checkpoint aborts and NOTHING lands.
	f1 := newFixture(t)
	repo1 := NewRepository(f1.Pool)
	err := SaveWorld(ctx, repo1, newWorld(repo1).BuildCheckpointSnapshot())
	if err == nil {
		t.Fatal("SaveWorld with need=25 must fail un-clamped — if this ever passes, the [0,24] guard is gone and the clamp below is defending against nothing")
	}
	// Count the rows rather than LoadWorld: the rollback is so total that even the
	// environment singleton the checkpoint would have seeded is gone, and LoadWorld
	// refuses a world with no world_state row. That refusal is itself the proof, but
	// counting says it in the terms the test is actually about.
	var durableActors int
	if err := f1.Pool.QueryRow(ctx, `SELECT count(*) FROM actor`).Scan(&durableActors); err != nil {
		t.Fatalf("count actors after failed checkpoint: %v", err)
	}
	if durableActors != 0 {
		t.Fatalf("after the aborted checkpoint %d actor(s) are durable, want 0 — the Tx must roll back whole", durableActors)
	}

	// Half 2: clamped, the same snapshot commits.
	f2 := newFixture(t)
	repo2 := NewRepository(f2.Pool)
	cp := newWorld(repo2).BuildCheckpointSnapshot()
	report := cp.ClampToPersistable()

	if report.Total() != 1 {
		t.Fatalf("clamp report Total = %d, want exactly 1 (only the overshot need)", report.Total())
	}
	if got := report.Clamps()[0]; got.Table != "actor_need" || got.From != "25" || got.To != "24" {
		t.Errorf("clamp = %+v, want actor_need 25→24", got)
	}
	if err := SaveWorld(ctx, repo2, cp); err != nil {
		t.Fatalf("SaveWorld after clamp: %v — the clamped snapshot must commit", err)
	}

	loaded2, err := LoadWorld(ctx, repo2, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld after clamped checkpoint: %v", err)
	}
	overshot := loaded2.Actors[overshotID]
	if overshot == nil {
		t.Fatal("the overshot actor did not round-trip — the clamp must persist him, not drop him")
	}
	if got := overshot.Needs["hunger"]; got != 24 {
		t.Errorf("overshot hunger = %d after reload, want 24 (projected onto the ceiling)", got)
	}
	// The whole point of the ticket: one bad number must not cost anyone else
	// their durability.
	bystander := loaded2.Actors[bystanderID]
	if bystander == nil {
		t.Fatal("the BYSTANDER did not round-trip — this is the actual defect in LLM-392: a value on one actor cost an unrelated actor his persistence")
	}
	if got := bystander.Needs["hunger"]; got != 12 {
		t.Errorf("bystander hunger = %d after reload, want 12 untouched", got)
	}
}

// TestIntegration_SaveWorld_ClampsEveryRangedColumn drives one actor, one order
// and one scene with EVERY clamped field simultaneously out of range, and proves
// the result commits and reloads at the projected values.
//
// Per-field coverage matters here because each clamp encodes a real CHECK
// constraint or column width. A wrong bound (clamping to 0 where the column
// demands > 0) would produce a checkpoint that still aborts — the exact failure
// the pass exists to remove — and only a real database can catch that.
func TestIntegration_SaveWorld_ClampsEveryRangedColumn(t *testing.T) {
	requireIntegration(t)
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	const (
		actorID = sim.ActorID("cccccccc-0000-0000-0000-000000000392")
		peerID  = sim.ActorID("dddddddd-0000-0000-0000-000000000392")
	)
	// actor_inventory.item_kind FKs to item_kind(name) — seed the goods under test.
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO item_kind (name, display_label, category) VALUES
		     ('bread', 'Bread', 'food'), ('nails', 'Nails', 'material')`,
	); err != nil {
		t.Fatalf("seed item_kind: %v", err)
	}
	badStart, badEnd := 5000, -30
	w := checkpointableWorld(repo)
	w.Actors = map[sim.ActorID]*sim.Actor{
		peerID: {ID: peerID, DisplayName: "Peer", State: sim.StateIdle},
		actorID: {
			ID: actorID, DisplayName: "Broken", State: sim.StateIdle,
			Facing:           "sideways", // not in the facing enum
			ScheduleStartMin: &badStart,  // > 1439
			ScheduleEndMin:   &badEnd,    // < 0
			Needs:            map[sim.NeedKey]int{"hunger": 99, "thirst": -4},
			Inventory:        map[sim.ItemKind]int{"bread": -3, "nails": 2},
			ToolWear:         map[sim.ItemKind]int{"nails": 0}, // spent, must not be < 1
			Relationships: map[sim.ActorID]*sim.Relationship{
				peerID: {InteractionCount: -7, DroppedFactCount: -1},
			},
			ProductionActivity: &sim.ProductionActivity{
				Item: "stew", BatchQty: 0, RemainingSeconds: -12,
			},
		},
	}

	cp := w.BuildCheckpointSnapshot()
	report := cp.ClampToPersistable()
	if report.Clean() {
		t.Fatal("clamp report is clean, want corrections on every out-of-range field")
	}

	if err := SaveWorld(ctx, repo, cp); err != nil {
		t.Fatalf("SaveWorld after clamp: %v — every clamp must land inside its column's legal domain", err)
	}
	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	got := loaded.Actors[actorID]
	if got == nil {
		t.Fatal("actor did not round-trip")
	}

	if got.Facing != "south" {
		t.Errorf("Facing = %q, want south (the column DEFAULT)", got.Facing)
	}
	if got.ScheduleStartMin == nil || *got.ScheduleStartMin != 1439 {
		t.Errorf("ScheduleStartMin = %v, want 1439", got.ScheduleStartMin)
	}
	if got.ScheduleEndMin == nil || *got.ScheduleEndMin != 0 {
		t.Errorf("ScheduleEndMin = %v, want 0", got.ScheduleEndMin)
	}
	if got.Needs["hunger"] != 24 {
		t.Errorf("hunger = %d, want 24", got.Needs["hunger"])
	}
	if got.Needs["thirst"] != 0 {
		t.Errorf("thirst = %d, want 0", got.Needs["thirst"])
	}
	// -3 bread clamps to 0, and zero quantity already means "no row" on the write
	// path — so the item is absent after reload rather than present-and-negative.
	if _, present := got.Inventory["bread"]; present {
		t.Errorf("bread = %d after reload, want absent (a negative quantity clamps to 0, which is the no-row case)", got.Inventory["bread"])
	}
	if got.Inventory["nails"] != 2 {
		t.Errorf("nails = %d, want 2 untouched", got.Inventory["nails"])
	}
	if got.ToolWear["nails"] != 1 {
		t.Errorf("nails uses_left = %d, want 1 (nearest legal to spent; the column CHECKs > 0)", got.ToolWear["nails"])
	}
	rel := got.Relationships[peerID]
	if rel == nil {
		t.Fatal("relationship did not round-trip")
	}
	if rel.InteractionCount != 0 || rel.DroppedFactCount != 0 {
		t.Errorf("relationship counts = %d/%d, want 0/0", rel.InteractionCount, rel.DroppedFactCount)
	}
	pa := got.ProductionActivity
	if pa == nil {
		t.Fatal("ProductionActivity = nil after reload — a non-positive cycle must be clamped and KEPT, not silently dropped by the load side")
	}
	if pa.BatchQty != 1 || pa.RemainingSeconds != 1 {
		t.Errorf("production = %d/%d, want 1/1", pa.BatchQty, pa.RemainingSeconds)
	}
}

// TestIntegration_SaveWorld_UnclampableStillFails is the other half of the
// contract, and the reason this design was chosen over dropping rows: a value
// that has no legal projection must STILL take the checkpoint down.
//
// An actor with an empty FSM state is not a number out of range — it is an
// incoherent actor, and there is no defensible value to write in his place.
// Guessing one would fabricate world state; that is precisely the "magically fix
// the problematic row" behaviour this ticket rejected. So the clamp pass leaves
// him alone, the writer refuses him, the transaction rolls back whole, and
// LLM-394's alarm fires within ~3 minutes.
func TestIntegration_SaveWorld_UnclampableStillFails(t *testing.T) {
	requireIntegration(t)
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	const brokenID = sim.ActorID("ffffffff-0000-0000-0000-000000000392")
	w := checkpointableWorld(repo)
	w.Actors = map[sim.ActorID]*sim.Actor{
		// Empty State — no ceiling to project onto, no default to fall back to.
		brokenID: {ID: brokenID, DisplayName: "Stateless", State: ""},
	}

	cp := w.BuildCheckpointSnapshot()
	report := cp.ClampToPersistable()
	if !report.Clean() {
		t.Errorf("clamp report = %s, want CLEAN — an empty FSM state is not a range violation and must not be silently repaired", report.Summary())
	}
	err := SaveWorld(ctx, repo, cp)
	if err == nil {
		t.Fatal("SaveWorld with an empty FSM state must FAIL — an incoherent actor is refused, not repaired")
	}
	if !strings.Contains(err.Error(), "empty State") {
		t.Errorf("error = %v, want it to name the empty State", err)
	}
}
