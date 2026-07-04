package sim_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// stall_wear_test.go — LLM-118. External (package sim_test) tests for the repair
// tool (through the real command + activity sweep) and the degrade sales-block on
// all three trade paths (quote-post, fast-path take, slow accept). Reuses the
// shared sim_test helpers (placeAt / inventoryOf / forceComplete) plus the
// pay-with-item fixtures (buildFastPathFixture / buildPayWithItemWorld / mustSend).

func buildStallTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"nail": {Name: "nail", Category: sim.ItemCategoryMaterial},
	})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"stall-wood": {ID: "stall-wood", Name: "Market Stall (Wood)"},
	})
	zero := 0
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"stall": {
			ID: "stall", DisplayName: "Blacksmith", AssetID: "stall-wood", CurrentState: "open",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero, Pos: sim.WorldPos{X: 100, Y: 100},
			OwnerActorID: "ezekiel", Tags: []string{sim.TagBusiness}, Wear: 450,
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"ezekiel": {ID: "ezekiel", DisplayName: "Ezekiel Crane", LLMAgent: "ezekiel",
			Inventory: map[sim.ItemKind]int{"nail": 10}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	ip := func(v int) *int { return &v }
	if _, err := w.Send(sim.SetStallWearSettings(ip(1), ip(400), ip(600), ip(5), ip(90))); err != nil {
		cancel()
		t.Fatalf("SetStallWearSettings: %v", err)
	}
	return w, cancel
}

func stallWearOf(t *testing.T, w *sim.World, objID sim.VillageObjectID) int {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.VillageObjects[objID].Wear, nil
	}})
	if err != nil {
		t.Fatalf("read wear: %v", err)
	}
	return res.(int)
}

func setStallWear(t *testing.T, w *sim.World, objID sim.VillageObjectID, wear int) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.VillageObjects[objID].Wear = wear
		return nil, nil
	}}); err != nil {
		t.Fatalf("set wear: %v", err)
	}
}

func setActorNails(t *testing.T, w *sim.World, actorID sim.ActorID, n int) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[actorID].Inventory[sim.NailItemKind] = n
		return nil, nil
	}}); err != nil {
		t.Fatalf("set nails: %v", err)
	}
}

func TestStartRepair_ConsumesNailsAndResets(t *testing.T) {
	w, cancel := buildStallTestWorld(t)
	defer cancel()
	placeAt(t, w, "ezekiel", "stall")

	res, err := w.Send(sim.StartRepair("ezekiel"))
	if err != nil {
		t.Fatalf("StartRepair: %v", err)
	}
	sr := res.(sim.SourceActivityStartResult)
	if !sr.Started || sr.Kind != sim.SourceActivityRepair || sr.ObjectID != "stall" {
		t.Fatalf("start result = %+v, want Started repair @ stall", sr)
	}
	// Nails consumed up front (10 - 5).
	if got := inventoryOf(t, w, "ezekiel", "nail"); got != 5 {
		t.Errorf("nails = %d, want 5 (consumed at start)", got)
	}
	// Wear not reset until completion.
	if got := stallWearOf(t, w, "stall"); got != 450 {
		t.Errorf("wear = %d, want 450 (reset deferred to completion)", got)
	}
	if n := forceComplete(t, w); n != 1 {
		t.Fatalf("completed = %d, want 1", n)
	}
	if got := stallWearOf(t, w, "stall"); got != 0 {
		t.Errorf("wear = %d, want 0 (reset at completion)", got)
	}
}

func TestStartRepair_Rejects(t *testing.T) {
	t.Run("not at stall", func(t *testing.T) {
		w, cancel := buildStallTestWorld(t)
		defer cancel()
		// ezekiel stands at the origin, not the stall (100,100).
		_, err := w.Send(sim.StartRepair("ezekiel"))
		if err == nil || !strings.Contains(err.Error(), "walk to your stall") {
			t.Fatalf("err = %v, want a 'walk to your stall' rejection", err)
		}
	})
	t.Run("not worn", func(t *testing.T) {
		w, cancel := buildStallTestWorld(t)
		defer cancel()
		setStallWear(t, w, "stall", 100) // below the repair threshold
		placeAt(t, w, "ezekiel", "stall")
		_, err := w.Send(sim.StartRepair("ezekiel"))
		if err == nil || !strings.Contains(err.Error(), "doesn't need mending") {
			t.Fatalf("err = %v, want a 'not worn yet' rejection", err)
		}
	})
	t.Run("insufficient nails", func(t *testing.T) {
		w, cancel := buildStallTestWorld(t)
		defer cancel()
		setActorNails(t, w, "ezekiel", 2) // fewer than the 5 a repair needs
		placeAt(t, w, "ezekiel", "stall")
		_, err := w.Send(sim.StartRepair("ezekiel"))
		if err == nil || !strings.Contains(err.Error(), "nails") {
			t.Fatalf("err = %v, want an insufficient-nails rejection", err)
		}
	})
	t.Run("inside a different structure", func(t *testing.T) {
		w, cancel := buildStallTestWorld(t)
		defer cancel()
		// LLM-266: the inside-structure branch of AtBusiness keys on the business's
		// OWN id — being inside some other building, off-pin, is still not co-located.
		mustSend(t, w, func(world *sim.World) {
			a := world.Actors["ezekiel"]
			a.Pos = sim.WorldPos{X: 500, Y: 500}.Tile()
			a.InsideStructureID = "some_other_place"
		})
		_, err := w.Send(sim.StartRepair("ezekiel"))
		if err == nil || !strings.Contains(err.Error(), "walk to your stall") {
			t.Fatalf("err = %v, want a 'walk to your stall' rejection", err)
		}
	})
}

// TestStartRepair_AcceptsOwnerInsideStructure is the LLM-266 command-side arm: a
// keeper standing INSIDE their own business structure (InsideStructureID == the
// business id, since structures share the village_object's id) but NOT at the
// outdoor loiter pin can still mend it. The old pin-only gate rejected them with
// "walk to your stall" even though they were at post — the same defect that kept
// the perception cue and the repair tool from ever firing for an indoor keeper.
func TestStartRepair_AcceptsOwnerInsideStructure(t *testing.T) {
	w, cancel := buildStallTestWorld(t)
	defer cancel()
	// Inside the business but far from the loiter pin at (100,100): only the
	// inside-structure branch of AtBusiness can admit this repair.
	mustSend(t, w, func(world *sim.World) {
		a := world.Actors["ezekiel"]
		a.Pos = sim.WorldPos{X: 500, Y: 500}.Tile()
		a.InsideStructureID = "stall"
	})
	res, err := w.Send(sim.StartRepair("ezekiel"))
	if err != nil {
		t.Fatalf("StartRepair (inside, off-pin): %v", err)
	}
	sr := res.(sim.SourceActivityStartResult)
	if !sr.Started || sr.ObjectID != "stall" {
		t.Fatalf("start result = %+v, want Started @ stall", sr)
	}
	// Nails consumed up front (10 - 5) confirms the repair actually began.
	if got := inventoryOf(t, w, "ezekiel", "nail"); got != 5 {
		t.Errorf("nails = %d, want 5 (consumed at start)", got)
	}
}

// degradeBobStall sets the degrade threshold + gives bob a degraded business
// (business-tagged, not market_stall — the LLM-247 generalized gate) in the
// pay-with-item fixture, so his trades should be blocked.
func degradeBobStall(t *testing.T, w *sim.World) {
	t.Helper()
	mustSend(t, w, func(world *sim.World) {
		world.Settings.StallWearDegradeThreshold = 600
		world.VillageObjects["bob_stall"] = &sim.VillageObject{
			ID: "bob_stall", OwnerActorID: "bob", Tags: []string{sim.TagBusiness}, Wear: 650,
		}
	})
}

func TestDegradedStall_BlocksQuotePost(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	degradeBobStall(t, w)
	_, err := w.Send(sim.SceneQuoteCreate("bob",
		[]sim.QuoteLineInput{{ItemName: "stew", Qty: 1}}, 4, true, "", nil, at))
	if err == nil || !strings.Contains(err.Error(), "too worn to trade") {
		t.Fatalf("err = %v, want a degraded-stall quote-post rejection", err)
	}
}

func TestDegradedStall_BlocksFastPathTake(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	degradeBobStall(t, w)
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 7, 0, "", at))
	if err == nil || !strings.Contains(err.Error(), "disrepair") {
		t.Fatalf("err = %v, want a degraded-stall take rejection", err)
	}
}

func TestDegradedStall_BlocksSlowAccept(t *testing.T) {
	// No standing quote here (buildPayWithItemWorld, not the fast-path fixture) so
	// alice's bare offer stays pending instead of auto-matching a quote.
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 0, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem (slow offer): %v", err)
	}
	ledgerID := res.(sim.PayWithItemResult).LedgerID

	degradeBobStall(t, w) // bob's stall degrades before he can accept
	if _, err := w.Send(sim.AcceptPay("bob", ledgerID, at)); err != nil {
		t.Fatalf("AcceptPay: unexpected tool error %v", err)
	}

	var msg string
	var stew int
	mustSend(t, w, func(world *sim.World) {
		msg = world.PayLedger[ledgerID].Message
		stew = world.Actors["bob"].Inventory["stew"]
	})
	if !strings.Contains(msg, "disrepair") {
		t.Fatalf("accept terminal message = %q, want a disrepair reason", msg)
	}
	if stew != 5 {
		t.Errorf("bob's stew = %d, want 5 (no transfer on a blocked accept)", stew)
	}
}
