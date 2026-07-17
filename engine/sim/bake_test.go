package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// bake_test.go — LLM-454. Integration tests for the evening bake occupation against a
// real (mem-repo) world: start creates the shared per-home session and occupies the
// baker WITHOUT consuming flour up front (restart-safety); a co-resident joins the
// same batch flourless; completion mints the batch to the initiator and consumes its
// flour there.

// buildBakeTestWorld seeds a home with two residents — alice (holds 2 flour) and bob
// (holds none) — both inside it, plus a deterministic 22:00 bedtime clock. Returns an
// evening "now" (20:00) two hours before bedtime.
func buildBakeTestWorld(t *testing.T) (*sim.World, context.CancelFunc, time.Time) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"flour": {Name: "flour", Category: sim.ItemCategoryMaterial},
		"bread": {Name: "bread", Category: sim.ItemCategoryFood},
	})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"home-asset": {ID: "home-asset", Name: "Walker Residence"},
	})
	zero := 0
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"home": {
			ID: "home", DisplayName: "Walker Residence", AssetID: "home-asset", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero, Pos: sim.WorldPos{X: 50, Y: 50},
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {ID: "alice", DisplayName: "Silence Walker", LLMAgent: "silence",
			Kind: sim.KindNPCStateful, HomeStructureID: "home", InsideStructureID: "home",
			Inventory: map[sim.ItemKind]int{"flour": 2}},
		"bob": {ID: "bob", DisplayName: "Patience Walker", LLMAgent: "patience",
			Kind: sim.KindNPCStateful, HomeStructureID: "home", InsideStructureID: "home",
			Inventory: map[sim.ItemKind]int{}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	mustSend(t, w, func(world *sim.World) {
		world.Settings.Location = time.UTC
		world.Settings.LodgingBedtimeHour = 22
	})
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC) // evening, two hours before bed
	return w, cancel, now
}

func homeBakeSession(t *testing.T, w *sim.World) *sim.HomeBake {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.HomeBakes["home"], nil
	}})
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	hb, _ := res.(*sim.HomeBake)
	return hb
}

func bakeActivityKind(t *testing.T, w *sim.World, id sim.ActorID) sim.SourceActivityKind {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		if a == nil || a.SourceActivity == nil {
			return sim.SourceActivityKind(""), nil
		}
		return a.SourceActivity.Kind, nil
	}})
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	return res.(sim.SourceActivityKind)
}

func TestBake_StartCreatesSessionAndOccupiesWithoutConsumingFlour(t *testing.T) {
	w, cancel, now := buildBakeTestWorld(t)
	defer cancel()

	res, err := w.Send(sim.StartOrJoinBake("alice", "I'll get the bread on", false, now))
	if err != nil {
		t.Fatalf("StartOrJoinBake: %v", err)
	}
	if sr := res.(sim.SourceActivityStartResult); !sr.Started || sr.Kind != sim.SourceActivityBake {
		t.Fatalf("start result = %+v, want started bake", sr)
	}
	if k := bakeActivityKind(t, w, "alice"); k != sim.SourceActivityBake {
		t.Errorf("alice activity = %q, want bake (occupied)", k)
	}
	hb := homeBakeSession(t, w)
	if hb == nil || hb.InitiatorID != "alice" || hb.BatchQty != sim.BakeBatchQty {
		t.Fatalf("session = %+v, want alice's batch of %d", hb, sim.BakeBatchQty)
	}
	// Flour NOT consumed at start — spent only at completion, so a restart forfeits none.
	if got := inventoryOf(t, w, "alice", "flour"); got != 2 {
		t.Errorf("flour = %d after start, want 2 (consumed only at completion)", got)
	}
}

func TestBake_JoinAttachesToSameBatchWithoutFlour(t *testing.T) {
	w, cancel, now := buildBakeTestWorld(t)
	defer cancel()

	if _, err := w.Send(sim.StartOrJoinBake("alice", "", false, now)); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Bob holds no flour but joins the batch alice started.
	if _, err := w.Send(sim.StartOrJoinBake("bob", "", false, now.Add(time.Minute))); err != nil {
		t.Fatalf("join (flourless): %v", err)
	}
	if k := bakeActivityKind(t, w, "bob"); k != sim.SourceActivityBake {
		t.Errorf("bob activity = %q, want bake (joined the batch)", k)
	}
	// Still ONE session, still alice's — joining minted no second batch.
	if hb := homeBakeSession(t, w); hb == nil || hb.InitiatorID != "alice" {
		t.Fatalf("session = %+v, want the single alice batch after bob joined", hb)
	}
}

func TestBake_CompletionMintsBatchToInitiatorAndConsumesFlour(t *testing.T) {
	w, cancel, now := buildBakeTestWorld(t)
	defer cancel()

	res, err := w.Send(sim.StartOrJoinBake("alice", "", false, now))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	until := res.(sim.SourceActivityStartResult).Until
	// Drive the completion sweep past the window (bedtime).
	after := until.Add(time.Second)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.CompleteDueSourceActivities(world, after), nil
	}}); err != nil {
		t.Fatalf("complete sweep: %v", err)
	}
	if got := inventoryOf(t, w, "alice", "bread"); got != sim.BakeBatchQty {
		t.Errorf("bread = %d, want %d (batch minted to the initiator)", got, sim.BakeBatchQty)
	}
	if got := inventoryOf(t, w, "alice", "flour"); got != 0 {
		t.Errorf("flour = %d, want 0 (consumed at completion)", got)
	}
	if hb := homeBakeSession(t, w); hb != nil {
		t.Errorf("session still present after completion: %+v", hb)
	}
}
