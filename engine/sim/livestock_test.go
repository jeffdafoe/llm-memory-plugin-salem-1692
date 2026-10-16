package sim_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// livestock_test.go — LLM-797: a cow is a berry bush that walks.

const (
	dairyID  sim.ActorID = "elizabeth"
	milkCow  sim.ActorID = "cow-milk"
	heiferID sim.ActorID = "heifer"
	dairyPC  sim.ActorID = "pc-feeder"
)

func milkYield(avail, max int) *sim.LivestockYield {
	period := 12
	return &sim.LivestockYield{Row: sim.ObjectRefresh{
		GatherItem:         "milk",
		AvailableQuantity:  &avail,
		MaxQuantity:        &max,
		RefreshMode:        sim.RefreshModePeriodic,
		RefreshPeriodHours: &period,
	}}
}

// buildDairyWorld: open grass, a grazing cow with 4 milk beside Elizabeth (who
// forages milk and carries one sage), a heifer that gives nothing beside her too,
// and a PC a long way off.
func buildDairyWorld(t *testing.T) *sim.World {
	t.Helper()
	data := make([]byte, sim.MapW*sim.MapH)
	for i := range data {
		data[i] = sim.TerrainLightGrass
	}
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(&sim.Terrain{Data: data})
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"milk": {Name: "milk", Category: sim.ItemCategoryDrink},
		"sage": {Name: "sage"},
	})
	handles.Sprites.Seed(map[sim.SpriteID]*sim.Sprite{
		cowSpriteID: {ID: cowSpriteID, Name: "Cow (white)", Behaviors: []string{sim.BehaviorGrazer, sim.BehaviorAmbient}},
	})
	base := sim.Position{X: sim.PadX + 20, Y: sim.PadY + 20}
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		dairyID: {
			ID: dairyID, DisplayName: "Elizabeth Ellis", Kind: sim.KindNPCShared, LLMAgent: "salem-vendor",
			Pos:       base,
			Inventory: map[sim.ItemKind]int{"sage": 1},
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "milk", Source: sim.RestockSourceForage, Max: 30},
			}},
		},
		milkCow: {
			ID: milkCow, DisplayName: "Cow 2", Kind: sim.KindDecorative, SpriteID: cowSpriteID,
			Pos: sim.Position{X: base.X + 1, Y: base.Y}, Facing: "south", Yield: milkYield(4, 4),
		},
		heiferID: {
			ID: heiferID, DisplayName: "Heifer", Kind: sim.KindDecorative, SpriteID: cowSpriteID,
			Pos: sim.Position{X: base.X - 1, Y: base.Y}, Facing: "south",
		},
		dairyPC: {
			ID: dairyPC, DisplayName: "Jefferey", Kind: sim.KindPC,
			Pos:       sim.Position{X: base.X + 10, Y: base.Y + 10},
			Inventory: map[sim.ItemKind]int{"sage": 2},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return w
}

// finishWindow lands the actor's open window as if its clock ran out.
func finishWindow(t *testing.T, w *sim.World, id sim.ActorID) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		act := world.Actors[id].SourceActivity
		if act == nil {
			t.Fatalf("%s has no open window", id)
		}
		act.Until = time.Now().UTC().Add(-time.Second)
		sim.CompleteDueSourceActivities(world, time.Now().UTC())
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
}

func dairyActor(t *testing.T, w *sim.World, id sim.ActorID) *sim.Actor {
	t.Helper()
	r, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.CloneActor(world.Actors[id]), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return r.(*sim.Actor)
}

func TestMilking_MilksTheCowAndFeedsHerSage(t *testing.T) {
	w := buildDairyWorld(t)
	var fed []*sim.LivestockFed
	var done []*sim.SourceActivityCompleted
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, e sim.Event) {
			switch ev := e.(type) {
			case *sim.LivestockFed:
				fed = append(fed, ev)
			case *sim.SourceActivityCompleted:
				done = append(done, ev)
			}
		}))
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}

	r, err := w.Send(sim.StartHarvest(dairyID, 0))
	if err != nil {
		t.Fatalf("StartHarvest: %v", err)
	}
	start := r.(sim.SourceActivityStartResult)
	if start.Kind != sim.SourceActivityMilk || start.SourceName != "Cow 2" {
		t.Fatalf("start = %+v, want a milking of Cow 2", start)
	}
	finishWindow(t, w, dairyID)

	e := dairyActor(t, w, dairyID)
	if e.Inventory["milk"] != 4 {
		t.Errorf("milk = %d, want 4", e.Inventory["milk"])
	}
	if e.Inventory["sage"] != 0 {
		t.Errorf("sage = %d, want 0 (fed to the cow)", e.Inventory["sage"])
	}
	cow := dairyActor(t, w, milkCow)
	if got := *cow.Yield.Row.AvailableQuantity; got != 0 {
		t.Errorf("cow milk left = %d, want 0", got)
	}
	if cow.Yield.FedBonus != sim.LivestockFeedBonus {
		t.Errorf("cow fed bonus = %d, want %d", cow.Yield.FedBonus, sim.LivestockFeedBonus)
	}
	if len(fed) != 1 || fed[0].AnimalID != milkCow || fed[0].FeederID != dairyID {
		t.Errorf("LivestockFed events = %+v, want one for the cow by Elizabeth", fed)
	}
	if len(done) != 1 || !done[0].Fed || done[0].AnimalID != milkCow {
		t.Fatalf("completions = %+v, want one fed milking", done)
	}
	line := sim.SourceActivityCompletionNarration(done[0].Kind, done[0].Item, done[0].Qty, "", done[0].SourceName, false)
	if !strings.Contains(line, "milking Cow 2") || !strings.Contains(line, "4 milk") {
		t.Errorf("narration = %q", line)
	}
}

func TestMilking_NoSageNoFeed(t *testing.T) {
	w := buildDairyWorld(t)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.Actors[dairyID].Inventory, "sage")
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Send(sim.StartHarvest(dairyID, 0)); err != nil {
		t.Fatalf("StartHarvest: %v", err)
	}
	finishWindow(t, w, dairyID)
	if cow := dairyActor(t, w, milkCow); cow.Yield.Fed() {
		t.Error("cow fed with no sage held")
	}
	if e := dairyActor(t, w, dairyID); e.Inventory["milk"] != 4 {
		t.Errorf("milk = %d, want 4", e.Inventory["milk"])
	}
}

func TestMilking_AlreadyFedKeepsTheSage(t *testing.T) {
	w := buildDairyWorld(t)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[milkCow].Yield.FedBonus = sim.LivestockFeedBonus
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Send(sim.StartHarvest(dairyID, 0)); err != nil {
		t.Fatalf("StartHarvest: %v", err)
	}
	finishWindow(t, w, dairyID)
	if e := dairyActor(t, w, dairyID); e.Inventory["sage"] != 1 {
		t.Errorf("sage = %d, want 1 kept", e.Inventory["sage"])
	}
}

func TestMilking_Gates(t *testing.T) {
	t.Run("no forage entry for milk", func(t *testing.T) {
		w := buildDairyWorld(t)
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Actors[dairyID].RestockPolicy = nil
			return nil, nil
		}}); err != nil {
			t.Fatal(err)
		}
		_, err := w.Send(sim.StartHarvest(dairyID, 0))
		if !errors.Is(err, sim.ErrNoGatherSource) {
			t.Fatalf("err = %v, want ErrNoGatherSource", err)
		}
	})
	t.Run("only the heifer in reach", func(t *testing.T) {
		w := buildDairyWorld(t)
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Actors[milkCow].Pos = sim.Position{X: sim.PadX + 40, Y: sim.PadY + 40}
			return nil, nil
		}}); err != nil {
			t.Fatal(err)
		}
		_, err := w.Send(sim.StartHarvest(dairyID, 0))
		if !errors.Is(err, sim.ErrNoGatherSource) {
			t.Fatalf("err = %v, want ErrNoGatherSource", err)
		}
	})
	t.Run("milked dry", func(t *testing.T) {
		w := buildDairyWorld(t)
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			zero := 0
			world.Actors[milkCow].Yield.Row.AvailableQuantity = &zero
			return nil, nil
		}}); err != nil {
			t.Fatal(err)
		}
		_, err := w.Send(sim.StartHarvest(dairyID, 0))
		if err == nil || !strings.Contains(err.Error(), "Cow 2 has no milk") {
			t.Fatalf("err = %v, want Cow 2 has no milk", err)
		}
	})
	t.Run("cow walked off mid-milking lands nothing", func(t *testing.T) {
		w := buildDairyWorld(t)
		if _, err := w.Send(sim.StartHarvest(dairyID, 0)); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Actors[milkCow].Pos = sim.Position{X: sim.PadX + 40, Y: sim.PadY + 40}
			return nil, nil
		}}); err != nil {
			t.Fatal(err)
		}
		finishWindow(t, w, dairyID)
		if e := dairyActor(t, w, dairyID); e.Inventory["milk"] != 0 {
			t.Errorf("milk = %d, want 0", e.Inventory["milk"])
		}
	})
}

func TestFeedLivestock(t *testing.T) {
	w := buildDairyWorld(t)
	_, err := w.Send(sim.FeedLivestock(dairyPC, milkCow, time.Now()))
	if !errors.Is(err, sim.ErrLivestockOutOfReach) {
		t.Fatalf("far feed err = %v, want ErrLivestockOutOfReach", err)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		cow := world.Actors[milkCow]
		world.Actors[dairyPC].Pos = sim.Position{X: cow.Pos.X, Y: cow.Pos.Y + 1}
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Send(sim.FeedLivestock(dairyPC, heiferID, time.Now())); !errors.Is(err, sim.ErrNotLivestock) {
		t.Fatalf("heifer feed err = %v, want ErrNotLivestock", err)
	}
	r, err := w.Send(sim.FeedLivestock(dairyPC, milkCow, time.Now()))
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	if r.(sim.FeedResult).AnimalName != "Cow 2" {
		t.Errorf("result = %+v", r)
	}
	if _, err := w.Send(sim.FeedLivestock(dairyPC, milkCow, time.Now())); !errors.Is(err, sim.ErrAlreadyFed) {
		t.Fatalf("second feed err = %v, want ErrAlreadyFed", err)
	}
	pc := dairyActor(t, w, dairyPC)
	if pc.Inventory["sage"] != 1 {
		t.Errorf("pc sage = %d, want 1 (one spent, the refused one kept)", pc.Inventory["sage"])
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[milkCow].Yield.FedBonus = 0
		delete(world.Actors[dairyPC].Inventory, "sage")
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Send(sim.FeedLivestock(dairyPC, milkCow, time.Now())); !errors.Is(err, sim.ErrNoFeed) {
		t.Fatalf("no-sage feed err = %v, want ErrNoFeed", err)
	}
}

func TestRegenLivestockYield_FedBonusLandsOnTheRefill(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Add(-13 * time.Hour)

	unfed := milkYield(0, 4)
	unfed.Row.LastRefreshAt = &anchor
	if !sim.RegenLivestockYield(unfed, now) || *unfed.Row.AvailableQuantity != 4 {
		t.Fatalf("unfed refill = %d, want 4", *unfed.Row.AvailableQuantity)
	}

	fed := milkYield(0, 4)
	fed.Row.LastRefreshAt = &anchor
	fed.FedBonus = 4
	if !sim.RegenLivestockYield(fed, now) || *fed.Row.AvailableQuantity != 8 || fed.FedBonus != 0 {
		t.Fatalf("fed refill = %d bonus %d, want 8 and 0", *fed.Row.AvailableQuantity, fed.FedBonus)
	}

	// A fed cow that is not yet due keeps the bonus for the refill to come.
	early := milkYield(0, 4)
	recent := now.Add(-time.Hour)
	early.Row.LastRefreshAt = &recent
	early.FedBonus = 4
	if sim.RegenLivestockYield(early, now) || early.FedBonus != 4 {
		t.Fatalf("early regen changed stock or spent the bonus: %+v", early)
	}
}

func TestGrazer_HoldsAnAnimalBeingMilked(t *testing.T) {
	w := buildDairyWorld(t)
	if _, err := w.Send(sim.StartHarvest(dairyID, 0)); err != nil {
		t.Fatal(err)
	}
	before := dairyActor(t, w, milkCow).Pos
	now := time.Now()
	for i := 0; i < 30; i++ {
		now = now.Add(time.Hour)
		if _, err := w.Send(sim.EvaluateGrazerTick(now)); err != nil {
			t.Fatal(err)
		}
	}
	after := dairyActor(t, w, milkCow)
	if after.Pos != before || after.MoveIntent != nil {
		t.Errorf("cow moved while being milked: %v -> %v (intent %v)", before, after.Pos, after.MoveIntent)
	}
	held, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.LivestockHeld(world, world.Actors[heiferID]), nil
	}})
	if held.(bool) {
		t.Error("heifer (no yield) reported held")
	}
}

func TestMoveTo_WalksToTheCowByName(t *testing.T) {
	w := buildDairyWorld(t)
	far := sim.Position{X: sim.PadX + 30, Y: sim.PadY + 30}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[dairyID].Pos = far
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Send(sim.MoveToDestination(dairyID, "cow 2", nil, sim.RememberedPlaces{}, time.Now())); err != nil {
		t.Fatalf("move_to: %v", err)
	}
	e := dairyActor(t, w, dairyID)
	if e.HerdTargetID != milkCow {
		t.Errorf("herd target = %q, want the cow", e.HerdTargetID)
	}
	if e.MoveIntent == nil || e.MoveIntent.Destination.Position == nil {
		t.Fatalf("intent = %+v, want a position walk", e.MoveIntent)
	}
	cow := dairyActor(t, w, milkCow)
	if d := e.MoveIntent.Destination.Position.Chebyshev(cow.Pos); d != sim.MilkingReach {
		t.Errorf("destination is %d tiles from the cow, want %d", d, sim.MilkingReach)
	}
	// The milking pose is side-on: she kneels level with the cow, not above or
	// below her, even coming from the south-east.
	if e.MoveIntent.Destination.Position.Y != cow.Pos.Y {
		t.Errorf("destination %v is not level with the cow at %v", *e.MoveIntent.Destination.Position, cow.Pos)
	}
	// The heifer gives nothing, so she is no destination.
	if _, err := w.Send(sim.MoveToDestination(dairyID, "Heifer", nil, sim.RememberedPlaces{}, time.Now())); err == nil {
		t.Error("move_to the heifer succeeded, want no such place")
	}
}
