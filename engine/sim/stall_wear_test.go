package sim_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cascade"
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
// pay-with-item fixture. LLM-304: degrade blocks REFILL (restock/production), not
// selling — so his on-hand sales still go through.
func degradeBobStall(t *testing.T, w *sim.World) {
	t.Helper()
	mustSend(t, w, func(world *sim.World) {
		world.Settings.StallWearDegradeThreshold = 600
		world.VillageObjects["bob_stall"] = &sim.VillageObject{
			ID: "bob_stall", OwnerActorID: "bob", Tags: []string{sim.TagBusiness}, Wear: 650,
		}
	})
}

func TestDegradedStall_AllowsQuotePost(t *testing.T) {
	// LLM-304: a degraded shop still sells what's on hand — degrade blocks refill
	// (restock/production), not selling — so posting a sell quote succeeds.
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	degradeBobStall(t, w)
	if _, err := w.Send(sim.SceneQuoteCreate("bob",
		[]sim.QuoteLineInput{{ItemName: "stew", Qty: 1}}, 4, true, "", nil, at)); err != nil {
		t.Fatalf("SceneQuoteCreate at a degraded stall: unexpected error %v (LLM-304: degrade no longer blocks selling)", err)
	}
}

func TestDegradedStall_AllowsFastPathTake(t *testing.T) {
	// LLM-304: a buyer takes Bob's on-hand stew against his standing quote even while
	// his stall is degraded — selling from remaining stock is no longer blocked.
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	degradeBobStall(t, w)
	if _, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 7, 0, "", at)); err != nil {
		t.Fatalf("PayWithItem take at a degraded stall: unexpected error %v (LLM-304)", err)
	}
}

func TestDegradedStall_AllowsSlowAccept(t *testing.T) {
	// LLM-304: Bob accepts a pending pay offer and hands over on-hand stew even after
	// his stall degrades — no disrepair block, and the sale completes (physical
	// handover is immediate at accept). No standing quote here (buildPayWithItemWorld)
	// so alice's bare offer stays pending instead of auto-matching a quote.
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

	degradeBobStall(t, w) // bob's stall degrades before he accepts — no longer blocks the sale
	if _, err := w.Send(sim.AcceptPay("bob", ledgerID, at)); err != nil {
		t.Fatalf("AcceptPay: unexpected tool error %v", err)
	}

	var msg string
	var stew int
	mustSend(t, w, func(world *sim.World) {
		msg = world.PayLedger[ledgerID].Message
		stew = world.Actors["bob"].Inventory["stew"]
	})
	if strings.Contains(msg, "disrepair") {
		t.Fatalf("accept message = %q, want no disrepair block (LLM-304: degrade no longer blocks selling)", msg)
	}
	if stew != 4 {
		t.Errorf("bob's stew = %d, want 4 (1 transferred on the now-allowed sale)", stew)
	}
}

// TestAccrueStallWear_NetMarginEndToEnd (LLM-411) drives the distributor's whole leg
// through the REAL pipeline — buy from a producer (the accepted offer records a price
// observation via the cascade subscriber), then sell the goods on — and asserts his
// business wears on the MARGIN, not the turnover. The internal accrual tests hit
// accrueStallWear directly and so can't see the seam this covers: commitPayTransfer
// resolving the sale's goods, unit count, and full price out of the ledger entry. The
// wear number is the whole point of the ticket, and it is only correct if that
// resolution is correct.
func TestAccrueStallWear_NetMarginEndToEnd(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "carol", displayName: "Carol", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 20}},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", coins: 50, inventory: map[sim.ItemKind]int{}},
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
	})
	defer stop()
	at := time.Now().UTC()

	// Bob keeps a wearable business, and the price book records what he pays for stock.
	mustSend(t, w, func(world *sim.World) {
		world.Settings.StallWearPerCoin = 1
		world.Settings.StallWearRepairThreshold = 60
		world.Settings.StallWearDegradeThreshold = 90
		world.VillageObjects["bob_stall"] = &sim.VillageObject{
			ID: "bob_stall", OwnerActorID: "bob", Tags: []string{sim.TagBusiness},
		}
		cascade.RegisterPriceBook(world)
	})

	// Bob restocks: 6 stew from Carol for 6 coins (1 coin/unit). Carol produces her own
	// stew and owns no business, so nothing wears on her side.
	res, err := w.Send(sim.PayWithItem("bob", "Carol", "stew", 6, 6, false, nil, nil, 0, 0, "", at))
	if err != nil {
		t.Fatalf("Bob's restock offer: %v", err)
	}
	if _, err := w.Send(sim.AcceptPay("carol", res.(sim.PayWithItemResult).LedgerID, at)); err != nil {
		t.Fatalf("Carol accepts Bob's restock: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		if wear := world.VillageObjects["bob_stall"].Wear; wear != 0 {
			t.Fatalf("Bob's business wore %d on a purchase — only SALES wear a business", wear)
		}
	})

	// Bob sells 3 of them on to Alice for 6 coins. Cost basis 3 (3 units × 1 coin), so
	// the margin — and the wear — is 3. Under the old gross accrual this was 6.
	sale, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 3, 6, false, nil, nil, 0, 0, "", at))
	if err != nil {
		t.Fatalf("Alice's offer: %v", err)
	}
	if _, err := w.Send(sim.AcceptPay("bob", sale.(sim.PayWithItemResult).LedgerID, at)); err != nil {
		t.Fatalf("Bob accepts Alice's offer: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		if wear := world.VillageObjects["bob_stall"].Wear; wear != 3 {
			t.Errorf("Wear = %d, want 3 — the resale earned 3 coins over the 3 it cost him; wear taxes the margin, not the 6-coin turnover (LLM-411)", wear)
		}
	})
}
