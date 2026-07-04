package sim_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// closed_business_realwalk_test.go — LLM-270 regression guard for the shut-
// business capture path. The hand-built ActorArrived tests in
// closed_business_test.go synthesize the event; this drives a REAL walk
// (locomotion ticker → finishArrival → ActorArrived → the closed-business
// subscriber) so the capture is exercised against the genuine arrival shape,
// end to end through the published snapshot the restock drop actually reads.
//
// LLM-270 investigated a live symptom — Hannah Boggs looping Inn↔shut-Tavern all
// afternoon, every Restocking render still listing "buy from Tavern" — on the
// theory that the arrival capture never fires. It does: this test proves the
// whole path works (capture → snapshot clone → the Active read the drop
// consults). The real cause was the in-memory Observed store being wiped by
// frequent engine restarts (deploys), not a capture defect, and the ticket
// closed working-as-designed. This guards the capture path so a future genuine
// break is caught.

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
		// hannah's workplace — a real structure elsewhere (well clear of the
		// tavern's arrival ring) so "workplace is not the tavern" is exercised
		// against a seeded structure, not a dangling WorkStructureID.
		"inn": {ID: "inn", AssetID: "tavern-asset", DisplayName: "Inn", Pos: sim.WorldPos{X: 96, Y: 96}},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "Tavern"},
		"inn":    {ID: "inn", DisplayName: "Inn"},
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
			if a == nil {
				return false, fmt.Errorf("actor %q missing from world", actor)
			}
			_, ok := a.Observed.At(sim.ObservedStateKey{StructureID: structure, Condition: sim.ObservedClosed})
			return ok, nil
		},
	})
	if err != nil {
		t.Fatalf("observedClosedPresent: %v", err)
	}
	return res.(bool)
}

// TestClosedBusiness_RealWalkToShutTavernCaptures is the LLM-270 regression
// guard: a real StructureVisit walk to a keeperless (asleep-keeper) business must
// leave the arriving agent with an ObservedClosed memory of it. This is the
// "integration through the event" the ticket's definition of done calls for —
// locomotion → ActorArrived → subscriber — not a synthesized ActorArrived.
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
	//
	// Explicit publish barrier: Run republishes after every command and blocks the
	// Send reply until it has, so any completed Send guarantees Published() reflects
	// the post-arrival Observed mutation. Make that dependency explicit rather than
	// leaning on the observedClosedPresent call above, so a later refactor can't
	// silently read a pre-arrival snapshot.
	if _, err := w.Send(sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}); err != nil {
		t.Fatalf("publish barrier: %v", err)
	}
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
