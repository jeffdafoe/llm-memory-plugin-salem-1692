package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// closed_business_realwalk_test.go — LLM-270. The hand-built ActorArrived tests
// in closed_business_test.go all pass, yet live (Hannah Boggs ↔ the shut Tavern,
// 2026-07-04) the ObservedClosed memory never takes: she loops Inn↔Tavern all
// afternoon and every Restocking render keeps listing "buy from Tavern". The
// defect can only live in the REAL arrival shape — a driven locomotion walk →
// finishArrival → ActorArrived → the closed-business subscriber — which the
// hand-built tests bypass by synthesizing the event. This drives that real walk.

// buildShutTavernWorld seeds a running world mirroring the live Hannah↔Tavern
// case: a named business ("tavern") whose ONLY keeper ("john") is asleep, and a
// stateful agent NPC ("hannah") whose workplace is elsewhere, parked on open
// grass so she can walk to the tavern's visitor slot.
//
//   - "tavern": a 1x1 structure at world (320,320) → anchor tile (PadX+10,
//     PadY+10). Loiter offset (0,5) puts the visitor-slot ring at
//     (PadX+10, PadY+15), clear of the footprint, so a StructureVisit arrival
//     parks hannah OUTSIDE (InsideStructureID == "") — the live "You are
//     outdoors. John Ellis is asleep" arrival shape. The offset does not change
//     the capture geometry under test: resolveLoiteringObject and the arrival
//     registration both derive the pin from the same computeLoiterTile, so the
//     resolution is symmetric with or without an explicit offset.
//   - "john": the tavern's sole worker, StateSleeping → keeperPresentAt reads
//     the business shut (LLM-126, an abed keeper is not tending).
func buildShutTavernWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"tavern-asset": {ID: "tavern-asset", Category: "structure", DoorOffsetX: intp(0), DoorOffsetY: intp(0)},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"tavern": {
			ID: "tavern", AssetID: "tavern-asset", DisplayName: "Tavern",
			Pos: sim.WorldPos{X: 320, Y: 320}, LoiterOffsetX: intp(0), LoiterOffsetY: intp(5),
		},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "Tavern"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID: "hannah", DisplayName: "Hannah Boggs", Kind: sim.KindNPCStateful,
			WorkStructureID: "inn", Pos: sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 5},
		},
		"john": {
			ID: "john", DisplayName: "John Ellis", Kind: sim.KindNPCStateful,
			WorkStructureID: "tavern", InsideStructureID: "tavern", State: sim.StateSleeping,
			Pos: sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	sim.RegisterClosedBusinessSubscriber(w)
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// observedClosedPresent reports whether actor holds a raw ObservedClosed entry
// for structure (TTL ignored — this is the capture assertion, not the decay read).
func observedClosedPresent(t *testing.T, w *sim.World, actor sim.ActorID, structure sim.StructureID) bool {
	t.Helper()
	res, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors[actor]
			_, ok := a.Observed.At(sim.ObservedStateKey{StructureID: structure, Condition: sim.ObservedClosed})
			return ok, nil
		},
	})
	if err != nil {
		t.Fatalf("observedClosedPresent: %v", err)
	}
	return res.(bool)
}

// TestClosedBusiness_RealWalkToShutTavernCaptures is the LLM-270 repro: a real
// StructureVisit walk to a keeperless (asleep-keeper) business must leave the
// arriving agent with an ObservedClosed memory of it. This is the "integration
// through the event" the ticket's definition of done calls for — locomotion →
// ActorArrived → subscriber — not a synthesized ActorArrived.
func TestClosedBusiness_RealWalkToShutTavernCaptures(t *testing.T) {
	w, cancel := buildShutTavernWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.MoveActor("hannah", sim.NewStructureVisitDestination("tavern"), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	driveToArrival(t, w, "hannah", now, 40)

	// She parked at a visitor slot OUTSIDE the tavern (the live "outdoors"
	// arrival), and the sole keeper is asleep — so the trip must be remembered.
	if _, inside := actorSpatial(t, w, "hannah"); inside != "" {
		t.Fatalf("precondition failed: hannah ended up inside %q, want outdoors (visit path)", inside)
	}
	if !observedClosedPresent(t, w, "hannah", "tavern") {
		t.Fatal("hannah walked to the shut Tavern and found it keeperless, but captured NO ObservedClosed memory for it (LLM-270)")
	}

	// The restock DROP (findItemVendors → businessRememberedShut) does not read the
	// live actor — it reads the PUBLISHED SNAPSHOT's cloned Observed and gates on
	// Active(key, snap.PublishedAt), whose age>=0 guard rejects a future-stamped
	// entry. Assert the captured memory survives the snapshot clone AND reads active
	// against the snapshot clock — the exact predicate the "buy from" cue consults.
	key := sim.ObservedStateKey{StructureID: "tavern", Condition: sim.ObservedClosed}
	snap := w.Published()
	hs := snap.Actors["hannah"]
	if hs == nil {
		t.Fatal("hannah missing from published snapshot")
	}
	if !hs.Observed.Active(key, snap.PublishedAt) {
		raw, ok := hs.Observed.At(key)
		t.Fatalf("ObservedClosed(tavern) not Active in published snapshot: present=%v observedAt=%v publishedAt=%v (drop-read seam, LLM-270)",
			ok, raw, snap.PublishedAt)
	}
}
