package sim_test

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// LLM-845: wards at the player's tent.

// wardCampWorld is the camp world with the two ward goods in the catalog and
// one of each in the player's pack.
func wardCampWorld(t *testing.T) *sim.World {
	t.Helper()
	w := buildCampWorld(t, nil, nil)
	mustSend(t, w, func(world *sim.World) {
		world.ItemKinds[sim.HorseshoeKind] = &sim.ItemKindDef{Name: sim.HorseshoeKind, DisplayLabel: "Horseshoe",
			Capabilities: []string{"portable", sim.CapabilityWard}}
		world.ItemKinds[sim.WitchBottleKind] = &sim.ItemKindDef{Name: sim.WitchBottleKind, DisplayLabel: "Witch bottle",
			Capabilities: []string{"portable", sim.CapabilityWard}}
		pc := world.Actors["pc"]
		pc.Inventory[sim.HorseshoeKind] = 1
		pc.Inventory[sim.WitchBottleKind] = 1
	})
	return w
}

// wardEventRecorder keeps every event, for the ward tests.
type wardEventRecorder struct {
	mu     sync.Mutex
	events []sim.Event
}

func (r *wardEventRecorder) handle(_ *sim.World, evt sim.Event) {
	r.mu.Lock()
	r.events = append(r.events, evt)
	r.mu.Unlock()
}

func (r *wardEventRecorder) snapshot() []sim.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sim.Event(nil), r.events...)
}

// wardActions is what the pack row of kind offers right now.
func wardActions(t *testing.T, w *sim.World, kind sim.ItemKind) []string {
	t.Helper()
	var out []string
	mustSend(t, w, func(world *sim.World) {
		pc := world.Actors["pc"]
		out = sim.ItemActions(world.ItemKinds[kind], pc.PackState().WithCamp(world.VillageObjects, "pc"), time.Now())
	})
	return out
}

func tentTags(t *testing.T, w *sim.World) []string {
	t.Helper()
	var tags []string
	mustSend(t, w, func(world *sim.World) {
		if camp := sim.FindCamp(world.VillageObjects, "pc"); camp != nil {
			tags = append(tags, camp.Tent.Tags...)
		}
	})
	return tags
}

// No tent, no ward: the buttons appear once the tent stands.
func TestWard_OfferedOnlyAtAStandingTent(t *testing.T) {
	w := wardCampWorld(t)
	if got := wardActions(t, w, sim.HorseshoeKind); len(got) != 0 {
		t.Errorf("tent in the pack: horseshoe offers %v", got)
	}
	if _, err := w.Send(sim.UseItem("pc", sim.HorseshoeKind, sim.ItemActionHang, time.Now())); !errors.Is(err, sim.ErrItemAction) {
		t.Errorf("hanging with no tent: err %v, want a refusal", err)
	}
	pitchAll(t, w)
	if got := wardActions(t, w, sim.HorseshoeKind); !slices.Equal(got, []string{sim.ItemActionHang}) {
		t.Errorf("tent standing: horseshoe offers %v, want hang", got)
	}
	if got := wardActions(t, w, sim.WitchBottleKind); !slices.Equal(got, []string{sim.ItemActionBury}) {
		t.Errorf("tent standing: witch bottle offers %v, want bury", got)
	}
}

// Hanging puts the tag on the tent, takes the shoe out of the pack, and says
// so; a second one is not offered.
func TestWard_HangAndBury(t *testing.T) {
	w := wardCampWorld(t)
	now := pitchAll(t, w)
	rec := &wardEventRecorder{}
	mustSend(t, w, func(world *sim.World) { world.Subscribe(sim.SubscriberFunc(rec.handle)) })

	if _, err := w.Send(sim.UseItem("pc", sim.HorseshoeKind, sim.ItemActionHang, now)); err != nil {
		t.Fatalf("hang: %v", err)
	}
	if _, err := w.Send(sim.UseItem("pc", sim.WitchBottleKind, sim.ItemActionBury, now)); err != nil {
		t.Fatalf("bury: %v", err)
	}
	tags := tentTags(t, w)
	if !slices.Contains(tags, sim.TagWardHorseshoe) || !slices.Contains(tags, sim.TagWardWitchBottle) {
		t.Fatalf("tent tags = %v, want both wards", tags)
	}
	mustSend(t, w, func(world *sim.World) {
		pc := world.Actors["pc"]
		if pc.Inventory[sim.HorseshoeKind] != 0 || pc.Inventory[sim.WitchBottleKind] != 0 {
			t.Errorf("pack after placing = %v, want both gone", pc.Inventory)
		}
	})
	var placed, tagFrames int
	for _, e := range rec.snapshot() {
		switch ev := e.(type) {
		case *sim.WardPlaced:
			placed++
			if ev.Text == "" || ev.ActorID != "pc" {
				t.Errorf("WardPlaced = %+v", ev)
			}
		case *sim.VillageObjectTagsUpdated:
			tagFrames++
		}
	}
	if placed != 2 || tagFrames != 2 {
		t.Errorf("events: %d WardPlaced, %d tag updates, want 2 and 2", placed, tagFrames)
	}

	// A second horseshoe is not offered while one hangs.
	mustSend(t, w, func(world *sim.World) { world.Actors["pc"].Inventory[sim.HorseshoeKind] = 1 })
	if got := wardActions(t, w, sim.HorseshoeKind); len(got) != 0 {
		t.Errorf("one already hangs: horseshoe offers %v", got)
	}
}

// A ward goes up with the player at the tent, standing still.
func TestWard_Refusals(t *testing.T) {
	w := wardCampWorld(t)
	now := pitchAll(t, w)
	mustSend(t, w, func(world *sim.World) {
		world.Actors["pc"].Pos = sim.TilePos{X: campStand.X + 15, Y: campStand.Y}
	})
	if _, err := w.Send(sim.UseItem("pc", sim.HorseshoeKind, sim.ItemActionHang, now)); !errors.Is(err, sim.ErrItemAction) || err.Error() != "Walk up to your tent first." {
		t.Errorf("far from the tent: err %v", err)
	}
	if tags := tentTags(t, w); slices.Contains(tags, sim.TagWardHorseshoe) {
		t.Error("a refused hang left the tag")
	}
	mustSend(t, w, func(world *sim.World) {
		pc := world.Actors["pc"]
		pc.Pos = campStand
		pc.MoveIntent = &sim.MoveIntent{}
	})
	if _, err := w.Send(sim.UseItem("pc", sim.HorseshoeKind, sim.ItemActionHang, now)); !errors.Is(err, sim.ErrItemAction) {
		t.Errorf("walking: err %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		if world.Actors["pc"].Inventory[sim.HorseshoeKind] != 1 {
			t.Error("a refused hang took the horseshoe")
		}
	})
}

// Striking the tent puts the wards back in the pack.
func TestWard_StrikeReturnsWards(t *testing.T) {
	w := wardCampWorld(t)
	now := pitchAll(t, w)
	for _, use := range []struct {
		kind   sim.ItemKind
		action string
	}{{sim.HorseshoeKind, sim.ItemActionHang}, {sim.WitchBottleKind, sim.ItemActionBury}} {
		if _, err := w.Send(sim.UseItem("pc", use.kind, use.action, now)); err != nil {
			t.Fatalf("%s: %v", use.action, err)
		}
	}
	if _, err := w.Send(sim.StrikeTent("pc", now)); err != nil {
		t.Fatalf("StrikeTent: %v", err)
	}
	landBeats(t, w, now, 4)
	mustSend(t, w, func(world *sim.World) {
		pc := world.Actors["pc"]
		if pc.Inventory[sim.HorseshoeKind] != 1 || pc.Inventory[sim.WitchBottleKind] != 1 || pc.Inventory[sim.TentItemKind] != 1 {
			t.Errorf("pack after the strike = %v, want the tent and both wards back", pc.Inventory)
		}
	})
	if got := wardActions(t, w, sim.HorseshoeKind); len(got) != 0 {
		t.Errorf("tent struck: horseshoe offers %v", got)
	}
	// Pitched again, the wards wait in the pack to be placed by hand.
	pitchAll(t, w)
	if tags := tentTags(t, w); slices.Contains(tags, sim.TagWardHorseshoe) {
		t.Errorf("a new pitch carries %v, want no ward until one is hung", tags)
	}
}

// wardHoldings counts a ward kind in the pack plus on the tent: placing,
// striking, undoing and restarting move it, never make or lose one.
func wardHoldings(t *testing.T, w *sim.World, kind sim.ItemKind, tag string) int {
	t.Helper()
	n := 0
	mustSend(t, w, func(world *sim.World) {
		n = world.Actors["pc"].Inventory[kind]
		for _, o := range world.VillageObjects {
			if sim.IsTent(o) && o.OwnerActorID == "pc" && o.HasTag(tag) {
				n++
			}
		}
	})
	return n
}

// A second horseshoe is refused even when asked for directly, and nothing moves.
func TestWard_SecondHangRefused(t *testing.T) {
	w := wardCampWorld(t)
	now := pitchAll(t, w)
	if _, err := w.Send(sim.UseItem("pc", sim.HorseshoeKind, sim.ItemActionHang, now)); err != nil {
		t.Fatalf("hang: %v", err)
	}
	mustSend(t, w, func(world *sim.World) { world.Actors["pc"].Inventory[sim.HorseshoeKind] = 1 })
	rec := &wardEventRecorder{}
	mustSend(t, w, func(world *sim.World) { world.Subscribe(sim.SubscriberFunc(rec.handle)) })
	if _, err := w.Send(sim.UseItem("pc", sim.HorseshoeKind, sim.ItemActionHang, now)); !errors.Is(err, sim.ErrItemAction) {
		t.Fatalf("second hang: err %v, want a refusal", err)
	}
	if n := wardHoldings(t, w, sim.HorseshoeKind, sim.TagWardHorseshoe); n != 2 {
		t.Errorf("holdings = %d, want the hung one and the spare", n)
	}
	for _, e := range rec.snapshot() {
		if _, ok := e.(*sim.WardPlaced); ok {
			t.Error("a refused hang emitted WardPlaced")
		}
	}
}

// Not while pitching or striking: the ward waits for a finished camp.
func TestWard_RefusedWhileBusy(t *testing.T) {
	w := wardCampWorld(t)
	now := time.Now().UTC()
	if _, err := w.Send(sim.PitchTent("pc", now)); err != nil {
		t.Fatalf("PitchTent: %v", err)
	}
	now = landBeats(t, w, now, 2) // the tent stands as home; props still going up
	if _, err := w.Send(sim.UseItem("pc", sim.HorseshoeKind, sim.ItemActionHang, now)); !errors.Is(err, sim.ErrItemAction) {
		t.Errorf("mid-pitch: err %v, want a refusal", err)
	}
	now = landBeats(t, w, now, 2)
	if _, err := w.Send(sim.UseItem("pc", sim.HorseshoeKind, sim.ItemActionHang, now)); err != nil {
		t.Fatalf("hang after the pitch: %v", err)
	}
	mustSend(t, w, func(world *sim.World) { world.Actors["pc"].Inventory[sim.HorseshoeKind] = 1 })
	if _, err := w.Send(sim.StrikeTent("pc", now)); err != nil {
		t.Fatalf("StrikeTent: %v", err)
	}
	now = landBeats(t, w, now, 1)
	if _, err := w.Send(sim.UseItem("pc", sim.WitchBottleKind, sim.ItemActionBury, now)); !errors.Is(err, sim.ErrItemAction) {
		t.Errorf("mid-strike: err %v, want a refusal", err)
	}
	landBeats(t, w, now, 3)
	if n := wardHoldings(t, w, sim.HorseshoeKind, sim.TagWardHorseshoe); n != 2 {
		t.Errorf("horseshoes after the strike = %d, want 2", n)
	}
	if n := wardHoldings(t, w, sim.WitchBottleKind, sim.TagWardWitchBottle); n != 1 {
		t.Errorf("witch bottles after the strike = %d, want 1", n)
	}
}

// Walking off mid-strike stands the camp up again with its wards; the strike
// done later returns each exactly once.
func TestWard_StrikeUndoneKeepsWards(t *testing.T) {
	w := wardCampWorld(t)
	now := pitchAll(t, w)
	if _, err := w.Send(sim.UseItem("pc", sim.HorseshoeKind, sim.ItemActionHang, now)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Send(sim.StrikeTent("pc", now)); err != nil {
		t.Fatal(err)
	}
	now = landBeats(t, w, now, 3) // the tent down to its bundle
	dest := sim.NewPositionDestination(sim.TilePos{X: campStand.X - 5, Y: campStand.Y + 5})
	if _, err := w.Send(sim.MoveActor("pc", dest, false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	if tags := tentTags(t, w); !slices.Contains(tags, sim.TagWardHorseshoe) {
		t.Fatalf("restored tent tags = %v, want the horseshoe still over the door", tags)
	}
	if n := wardHoldings(t, w, sim.HorseshoeKind, sim.TagWardHorseshoe); n != 1 {
		t.Fatalf("holdings after the undo = %d, want 1", n)
	}
	mustSend(t, w, func(world *sim.World) {
		pc := world.Actors["pc"]
		pc.MoveIntent = nil
		pc.Pos = campStand
	})
	if _, err := w.Send(sim.StrikeTent("pc", now)); err != nil {
		t.Fatalf("second StrikeTent: %v", err)
	}
	landBeats(t, w, now, 4)
	mustSend(t, w, func(world *sim.World) {
		if n := world.Actors["pc"].Inventory[sim.HorseshoeKind]; n != 1 {
			t.Errorf("horseshoes in the pack = %d, want 1", n)
		}
	})
}

// A restart that finds a warded tent mid-strike: a home tent is made whole and
// keeps its wards; a packed tent that is nobody's home comes down and its wards
// go back to the pack, once.
func TestWard_SettledOnLoad(t *testing.T) {
	for _, home := range []bool{true, false} {
		w := wardCampWorld(t)
		now := pitchAll(t, w)
		if _, err := w.Send(sim.UseItem("pc", sim.HorseshoeKind, sim.ItemActionHang, now)); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Send(sim.StrikeTent("pc", now)); err != nil {
			t.Fatal(err)
		}
		landBeats(t, w, now, 3)
		mustSend(t, w, func(world *sim.World) {
			pc := world.Actors["pc"]
			pc.SourceActivity = nil // the window a restart loses
			if !home {
				pc.HomeStructureID = ""
			}
			sim.SettleUnfinishedCampsOnLoad(world)
		})
		n := wardHoldings(t, w, sim.HorseshoeKind, sim.TagWardHorseshoe)
		if n != 1 {
			t.Errorf("home=%v: holdings after settling = %d, want 1", home, n)
		}
		tags := tentTags(t, w)
		if home != slices.Contains(tags, sim.TagWardHorseshoe) {
			t.Errorf("home=%v: tent tags %v", home, tags)
		}
	}
}
