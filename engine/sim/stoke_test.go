package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// stoke_test.go — LLM-412. External integration tests for the stoke command:
// the full validate → consume-wood → window → completion-extends-fire path
// against a real (mem-repo) world, mirroring the StartRepair suite.

// buildHearthTestWorld: Hannah owns the hearth-tagged tavern (fire out) and
// stands inside it holding firewood; Anne is a hired hand Working for her.
func buildHearthTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		sim.FirewoodItemKind: {Name: "firewood", Category: sim.ItemCategoryMaterial},
	})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"tavern-asset": {ID: "tavern-asset", Name: "Tavern"},
	})
	zero := 0
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"tavern": {
			ID: "tavern", DisplayName: "Tavern", AssetID: "tavern-asset", CurrentState: "open",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero, Pos: sim.WorldPos{X: 100, Y: 100},
			OwnerActorID: "hannah", Tags: []string{sim.TagBusiness, sim.TagHearth},
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {ID: "hannah", DisplayName: "Hannah Boggs", LLMAgent: "hannah",
			InsideStructureID: "tavern",
			Inventory:         map[sim.ItemKind]int{sim.FirewoodItemKind: 3}},
		"anne": {ID: "anne", DisplayName: "Anne Walker", LLMAgent: "anne",
			InsideStructureID: "tavern",
			Inventory:         map[sim.ItemKind]int{sim.FirewoodItemKind: 1}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

func hearthLitUntilOf(t *testing.T, w *sim.World, objID sim.VillageObjectID) time.Time {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.VillageObjects[objID].HearthLitUntil, nil
	}})
	if err != nil {
		t.Fatalf("read hearth: %v", err)
	}
	return res.(time.Time)
}

func TestStartStoke_OwnerConsumesWoodAndCompletionLightsFire(t *testing.T) {
	w, cancel := buildHearthTestWorld(t)
	defer cancel()

	res, err := w.Send(sim.StartStoke("hannah"))
	if err != nil {
		t.Fatalf("StartStoke: %v", err)
	}
	sr := res.(sim.SourceActivityStartResult)
	if !sr.Started || sr.Kind != sim.SourceActivityStoke || sr.ObjectID != "tavern" {
		t.Fatalf("start result = %+v, want started stoke @ tavern", sr)
	}
	// Firewood consumed up front (3 - 1).
	if got := inventoryOf(t, w, "hannah", sim.FirewoodItemKind); got != 2 {
		t.Errorf("firewood = %d, want 2 (consumed at start)", got)
	}
	// The fire is NOT lit yet — it lands at completion.
	if lit := hearthLitUntilOf(t, w, "tavern"); !lit.IsZero() {
		t.Fatalf("fire lit before the window completed: %v", lit)
	}
	// Drive the completion sweep past the window.
	after := sr.Until.Add(time.Second)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.CompleteDueSourceActivities(world, after), nil
	}}); err != nil {
		t.Fatalf("complete sweep: %v", err)
	}
	lit := hearthLitUntilOf(t, w, "tavern")
	want := after.Add(time.Duration(sim.DefaultHearthBurnMinutesPerWood) * time.Minute)
	if !lit.Equal(want) {
		t.Errorf("HearthLitUntil = %v, want %v (one wood from completion instant)", lit, want)
	}
}

func TestStartStoke_HiredWorkerMayStoke(t *testing.T) {
	w, cancel := buildHearthTestWorld(t)
	defer cancel()
	// Anne is Working a hired job for Hannah — HearthToStoke resolves the
	// employer's hearth (the work-vs-leaving principle: no task field, the
	// worker simply keeps her hands).
	mustSend(t, w, func(world *sim.World) {
		world.LaborLedger[1] = &sim.LaborOffer{
			ID: 1, WorkerID: "anne", EmployerID: "hannah", State: sim.LaborStateWorking,
		}
	})
	res, err := w.Send(sim.StartStoke("anne"))
	if err != nil {
		t.Fatalf("StartStoke (hired): %v", err)
	}
	if sr := res.(sim.SourceActivityStartResult); !sr.Started || sr.ObjectID != "tavern" {
		t.Fatalf("start result = %+v, want started @ employer's tavern", sr)
	}
	if got := inventoryOf(t, w, "anne", sim.FirewoodItemKind); got != 0 {
		t.Errorf("firewood = %d, want 0 (consumed at start)", got)
	}
}

func TestStartStoke_Rejects(t *testing.T) {
	w, cancel := buildHearthTestWorld(t)
	defer cancel()

	// Not responsible for any hearth.
	mustSend(t, w, func(world *sim.World) {
		world.Actors["anne"].InsideStructureID = "tavern"
	})
	if _, err := w.Send(sim.StartStoke("anne")); err == nil {
		t.Errorf("non-owner, non-hire stoked anyway")
	}

	// Owner outside the structure.
	mustSend(t, w, func(world *sim.World) {
		world.Actors["hannah"].InsideStructureID = ""
	})
	if _, err := w.Send(sim.StartStoke("hannah")); err == nil {
		t.Errorf("outside owner stoked anyway")
	}
	mustSend(t, w, func(world *sim.World) {
		world.Actors["hannah"].InsideStructureID = "tavern"
	})

	// Fire already well banked.
	mustSend(t, w, func(world *sim.World) {
		world.VillageObjects["tavern"].HearthLitUntil = time.Now().UTC().Add(6 * time.Hour)
	})
	if _, err := w.Send(sim.StartStoke("hannah")); err == nil {
		t.Errorf("well-banked fire stoked anyway")
	}
	mustSend(t, w, func(world *sim.World) {
		world.VillageObjects["tavern"].HearthLitUntil = time.Time{}
	})

	// Not enough firewood.
	mustSend(t, w, func(world *sim.World) {
		delete(world.Actors["hannah"].Inventory, sim.FirewoodItemKind)
	})
	if _, err := w.Send(sim.StartStoke("hannah")); err == nil {
		t.Errorf("wood-less owner stoked anyway")
	}
}
