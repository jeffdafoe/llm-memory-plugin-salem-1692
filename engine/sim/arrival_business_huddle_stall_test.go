package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cascade"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// arrival_business_huddle_stall_test.go — LLM-384, the market-stall half of the
// HOME-425 arrival greet. At a stall the proprietor works INSIDE the structure
// and the patron stops at the loiter pin OUTSIDE it, so a walk-up is a non-knock
// structure VISIT (FinalStructureID=="", DestStructureID names the stall). The
// indoor-arrival gate (arriver physically inside) never matched, and once
// LLM-375 excluded stall loiterers from the outdoor encounter nothing formed the
// keeper's huddle until the patron transacted — so the patron spoke FIRST and the
// keeper never got its HuddleJoined greet turn. business_arrival now routes the
// loiter visit to EnsureArrivalBusinessHuddle, which forms the keeper's structure
// huddle keeper-first.
//
// The seeded keeper is agent-less, so the deterministic ENGINE greet fires and is
// assertable; a real shared-VA keeper (e.g. Elizabeth Ellis) instead greets in
// character on the same huddle-peer-joined tick (HOME-461). Both hang off the one
// keeper-first HuddleJoined this fix restores.

// buildStallGreetWorld seeds an OPEN stall — no interior, keeper working inside,
// patron standing at the resolvable loiter pin outside — with the REAL encounter,
// business-arrival, and businessowner cascades registered and a Spoke recorder
// subscribed. keeperState selects an OPEN stall (StateIdle) vs a SHUT one
// (StateSleeping ⇒ keeperPresentAt false ⇒ no cross-threshold scope).
func buildStallGreetWorld(t *testing.T, keeperState sim.ActorState) (*sim.World, *greetSpokeRecorder, func()) {
	t.Helper()
	repo, h := mem.NewRepository()
	h.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"stall": {ID: "stall", DisplayName: "Ellis Farm"},
	})
	h.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"bldg-asset": {ID: "bldg-asset", Category: "structure"},
	})
	z := 0
	h.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"stall": {
			ID: "stall", AssetID: "bldg-asset", DisplayName: "Ellis Farm",
			Pos:           sim.WorldPos{X: 160, Y: 160},
			LoiterOffsetX: &z, LoiterOffsetY: &z,
		},
	})
	pin := sim.WorldPos{X: 160, Y: 160}.Tile()
	h.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"keeper": {
			ID: "keeper", DisplayName: "Elizabeth", Kind: sim.KindNPCShared,
			State:              keeperState,
			InsideStructureID:  "stall",
			WorkStructureID:    "stall",
			BusinessownerState: &sim.BusinessownerState{Flavor: "flamboyant"},
		},
		"patron": {
			ID: "patron", DisplayName: "Josiah", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, Pos: pin, // walked up to the pin, OUTSIDE the stall
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	rec := &greetSpokeRecorder{}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		cascade.RegisterEncounter(world)
		cascade.RegisterBusinessArrival(world)
		cascade.RegisterBusinessowner(ctx, world)
		world.Subscribe(sim.SubscriberFunc(rec.record))
		return nil, nil
	}}); err != nil {
		cancel()
		<-done
		t.Fatalf("register cascades: %v", err)
	}
	return w, rec, func() { cancel(); <-done }
}

// emitLoiterVisitArrival synthesizes the ActorArrived for a non-knock structure
// VISIT that lands the actor at the loiter pin OUTSIDE the structure, exactly what
// finishArrival emits for a StructureVisit: FinalStructureID is empty (the mover
// is outside) and DestStructureID names the visited structure.
func emitLoiterVisitArrival(t *testing.T, w *sim.World, actorID sim.ActorID, dest sim.StructureID, now time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		actor, ok := world.Actors[actorID]
		if !ok {
			t.Fatalf("emitLoiterVisitArrival: actor %q not found", actorID)
		}
		sim.EmitForTest(world, &sim.ActorArrived{
			ActorID:          actorID,
			FinalPosition:    sim.Position{X: actor.Pos.X, Y: actor.Pos.Y},
			FinalStructureID: "", // outside — a stall has no interior to stand in
			DestStructureID:  dest,
			At:               now,
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("emitLoiterVisitArrival: %v", err)
	}
}

// A patron walking up to an OPEN stall's loiter pin is pulled into the keeper's
// structure huddle keeper-first and greeted — the same hospitality a walk-in to
// an open shop gets, restored for the stall (loiter) arrival path.
func TestArrivalBusinessHuddle_OpenStallWalkupGreeted(t *testing.T) {
	now := time.Now().UTC()
	w, rec, stop := buildStallGreetWorld(t, sim.StateIdle)
	defer stop()

	emitLoiterVisitArrival(t, w, "patron", "stall", now)

	ph, kh := readHuddleOf(t, w, "patron"), readHuddleOf(t, w, "keeper")
	if ph == "" {
		t.Fatal("patron formed no huddle on arrival at the open stall — the keeper never gets a greet turn")
	}
	if ph != kh {
		t.Fatalf("patron and keeper are not in the same huddle: patron=%q keeper=%q", ph, kh)
	}
	if greets := rec.bySpeaker("keeper"); len(greets) == 0 {
		t.Fatal("keeper did not greet the walk-up patron")
	}
}

// Composition guard with LLM-359: a SHUT stall (keeper abed ⇒ keeperPresentAt
// false ⇒ no cross-threshold loiter scope) forms no keeper huddle and no greet —
// the patron at the pin is outside a shut wall, not received.
func TestArrivalBusinessHuddle_ShutStallWalkupNotGreeted(t *testing.T) {
	now := time.Now().UTC()
	w, rec, stop := buildStallGreetWorld(t, sim.StateSleeping)
	defer stop()

	emitLoiterVisitArrival(t, w, "patron", "stall", now)

	if ph := readHuddleOf(t, w, "patron"); ph != "" {
		t.Fatalf("patron should not huddle with an abed keeper behind a shut wall, got %q", ph)
	}
	if greets := rec.bySpeaker("keeper"); len(greets) != 0 {
		t.Fatalf("a shut stall's keeper should not greet, got %d greet(s)", len(greets))
	}
}
