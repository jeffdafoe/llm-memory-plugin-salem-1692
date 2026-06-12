package cascade

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// ZBBS-HOME-445 — knock-arrival dispatch wiring. The sim-level join
// behavior lives in sim's knock_huddle_test; these cover the two cascade
// seams: handleBusinessArrival routes a Knocked outdoor arrival to the
// knock bootstrap, and handleArrivalEncounter leaves Knocked arrivals
// alone (so the outdoor encounter can't grab the knocker first and starve
// the knock's already-huddled gate).

// buildKnockArrivalWorld seeds an owner-only shop with a receptive keeper
// inside, the knocker at the doorway, and a bystander a tile away (the
// encounter-skip case). Actor kinds are NPCStateful so the PC-staleness
// gates stay out of the way.
func buildKnockArrivalWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"shop-asset": {ID: "shop-asset", Category: "structure"},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		// The placement backs findOrCreateStructureScene — the knock join
		// anchors the huddle to the structure scene (ZBBS-HOME-375).
		"shop": {ID: "shop", AssetID: "shop-asset", Pos: sim.WorldPos{X: 320, Y: 320}},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"keeper": {
			ID: "keeper", DisplayName: "Keeper",
			Kind:              sim.KindNPCStateful,
			State:             sim.StateIdle,
			WorkStructureID:   "shop",
			InsideStructureID: "shop",
		},
		"knocker": {
			ID: "knocker", DisplayName: "Knocker",
			Kind:  sim.KindNPCStateful,
			State: sim.StateIdle,
			Pos:   sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 5},
		},
		"bystander": {
			ID: "bystander", DisplayName: "Bystander",
			Kind:  sim.KindNPCStateful,
			State: sim.StateIdle,
			Pos:   sim.TilePos{X: sim.PadX + 6, Y: sim.PadY + 5},
		},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"shop": {ID: "shop", DisplayName: "the shop"},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() { cancel(); <-done }
}

func knockHuddleOf(t *testing.T, w *sim.World, actorID sim.ActorID) sim.HuddleID {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[actorID].CurrentHuddleID, nil
	}})
	if err != nil {
		t.Fatalf("read %s huddle: %v", actorID, err)
	}
	return res.(sim.HuddleID)
}

// TestHandleBusinessArrival_KnockedOutdoorFormsServiceHuddle: a Knocked
// outdoor arrival routes to EnsureKnockServiceHuddle — knocker and keeper
// end up in one shared huddle.
func TestHandleBusinessArrival_KnockedOutdoorFormsServiceHuddle(t *testing.T) {
	w, cleanup := buildKnockArrivalWorld(t)
	defer cleanup()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handleBusinessArrival(world, &sim.ActorArrived{
			ActorID:         "knocker",
			FinalPosition:   sim.Position{X: sim.PadX + 5, Y: sim.PadY + 5},
			DestStructureID: "shop",
			Knocked:         true,
			At:              time.Now().UTC(),
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("dispatch knock arrival: %v", err)
	}

	knocker, keeper := knockHuddleOf(t, w, "knocker"), knockHuddleOf(t, w, "keeper")
	if knocker == "" || knocker != keeper {
		t.Errorf("knocker and keeper should share one huddle; knocker=%q keeper=%q", knocker, keeper)
	}
}

// TestHandleBusinessArrival_PlainOutdoorVisitDoesNotKnock: the same outdoor
// arrival WITHOUT the Knocked stamp (a plain StructureVisit — chore walks,
// loiter relocates) must not pull the keeper into a doorway huddle.
func TestHandleBusinessArrival_PlainOutdoorVisitDoesNotKnock(t *testing.T) {
	w, cleanup := buildKnockArrivalWorld(t)
	defer cleanup()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handleBusinessArrival(world, &sim.ActorArrived{
			ActorID:         "knocker",
			FinalPosition:   sim.Position{X: sim.PadX + 5, Y: sim.PadY + 5},
			DestStructureID: "shop",
			At:              time.Now().UTC(),
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("dispatch plain arrival: %v", err)
	}

	if got := knockHuddleOf(t, w, "knocker"); got != "" {
		t.Errorf("plain visit must not knock-join; knocker in %q", got)
	}
	if got := knockHuddleOf(t, w, "keeper"); got != "" {
		t.Errorf("plain visit must not pull the keeper in; keeper in %q", got)
	}
}

// TestHandleArrivalEncounter_SkipsKnockedArrival: the outdoor encounter
// cascade must leave a Knocked arrival alone even with an eligible
// bystander in radius — the knock bootstrap owns it. The control case
// (same arrival, Knocked=false) forms the encounter huddle, proving the
// only discriminator is the stamp.
func TestHandleArrivalEncounter_SkipsKnockedArrival(t *testing.T) {
	w, cleanup := buildKnockArrivalWorld(t)
	defer cleanup()
	now := time.Now().UTC()

	arrival := func(knocked bool) *sim.ActorArrived {
		return &sim.ActorArrived{
			ActorID:       "knocker",
			FinalPosition: sim.Position{X: sim.PadX + 5, Y: sim.PadY + 5},
			Knocked:       knocked,
			At:            now,
		}
	}

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handleArrivalEncounter(world, arrival(true))
		return nil, nil
	}}); err != nil {
		t.Fatalf("dispatch knocked arrival: %v", err)
	}
	if got := knockHuddleOf(t, w, "knocker"); got != "" {
		t.Errorf("encounter must skip a Knocked arrival; knocker in %q", got)
	}
	if got := knockHuddleOf(t, w, "bystander"); got != "" {
		t.Errorf("encounter must skip a Knocked arrival; bystander in %q", got)
	}

	// Control: identical arrival without the stamp forms the encounter.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handleArrivalEncounter(world, arrival(false))
		return nil, nil
	}}); err != nil {
		t.Fatalf("dispatch control arrival: %v", err)
	}
	if got := knockHuddleOf(t, w, "knocker"); got == "" {
		t.Error("control arrival (Knocked=false) should form an encounter huddle — the skip test would be vacuous otherwise")
	}
}
