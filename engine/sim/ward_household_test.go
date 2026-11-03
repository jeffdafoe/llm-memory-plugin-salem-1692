package sim_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// LLM-897: a villager puts a ward up at home.

// placeHomeWardWith runs setup and PlaceHomeWard in one world command, so the
// running world cannot act on the setup (a walk, an activity) in between.
func placeHomeWardWith(w *sim.World, actor sim.ActorID, item sim.ItemKind, now time.Time, setup func(*sim.World)) (any, error) {
	return w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		if setup != nil {
			setup(world)
		}
		return sim.PlaceHomeWard(actor, item, now).Fn(world)
	}})
}

// seedHouseholdOmen gives the Walker household a live want: bob's milk soured
// a day before now.
func seedHouseholdOmen(t *testing.T, w *sim.World, now time.Time) {
	t.Helper()
	mustSend(t, w, func(world *sim.World) {
		world.Environment.Omens.LastAt = map[sim.ActorID]time.Time{"bob": now.Add(-24 * time.Hour)}
		world.Environment.Omens.LastKind = map[sim.ActorID]sim.OmenKind{"bob": sim.OmenSourMilk}
	})
}

func homeTags(t *testing.T, w *sim.World) []string {
	t.Helper()
	var tags []string
	mustSend(t, w, func(world *sim.World) {
		tags = append(tags, world.VillageObjects["home"].Tags...)
	})
	return tags
}

func TestPlaceHomeWard_NailsTheHorseshoeOverTheDoor(t *testing.T) {
	w, cancel, now := buildBakeTestWorld(t)
	defer cancel()
	seedHouseholdOmen(t, w, now)
	rec := &wardEventRecorder{}
	mustSend(t, w, func(world *sim.World) {
		world.Actors["alice"].Inventory[sim.HorseshoeKind] = 1
		world.Subscribe(sim.SubscriberFunc(rec.handle))
	})

	res, err := placeHomeWardWith(w, "alice", sim.HorseshoeKind, now, nil)
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	r, ok := res.(sim.HomeWardPlacedResult)
	if !ok || r.Item != sim.HorseshoeKind || r.Text == "" {
		t.Fatalf("result = %#v, want a HomeWardPlacedResult with its line", res)
	}
	if tags := homeTags(t, w); !slices.Contains(tags, sim.TagWardHorseshoe) {
		t.Errorf("home tags = %v, want %s", tags, sim.TagWardHorseshoe)
	}
	mustSend(t, w, func(world *sim.World) {
		if n, held := world.Actors["alice"].Inventory[sim.HorseshoeKind]; held {
			t.Errorf("horseshoe still in the pack (%d)", n)
		}
		if !sim.HomeWarded(world.VillageObjects["home"]) {
			t.Error("HomeWarded is false after the ward went up")
		}
	})
	var placed *sim.WardPlaced
	for _, e := range rec.snapshot() {
		if p, ok := e.(*sim.WardPlaced); ok {
			placed = p
		}
	}
	if placed == nil || placed.ActorID != "alice" || placed.ObjectID != "home" || placed.Item != sim.HorseshoeKind {
		t.Errorf("WardPlaced = %+v, want alice's horseshoe at home", placed)
	}
}

// Every refusal leaves the house and the pack as they were.
func TestPlaceHomeWard_Refusals(t *testing.T) {
	cases := []struct {
		name  string
		actor sim.ActorID
		item  sim.ItemKind
		setup func(*sim.World)
		model bool // a ModelFacingError, not an internal one
	}{
		{name: "not a villager ward", actor: "alice", item: sim.WitchBottleKind, model: true,
			setup: func(w *sim.World) { w.Actors["alice"].Inventory[sim.WitchBottleKind] = 1 }},
		{name: "away from home", actor: "alice", item: sim.HorseshoeKind, model: true,
			setup: func(w *sim.World) { w.Actors["alice"].InsideStructureID = "" }},
		{name: "walking", actor: "alice", item: sim.HorseshoeKind, model: true,
			setup: func(w *sim.World) { w.Actors["alice"].MoveIntent = &sim.MoveIntent{} }},
		{name: "busy", actor: "alice", item: sim.HorseshoeKind, model: true,
			setup: func(w *sim.World) {
				w.Actors["alice"].SourceActivity = &sim.SourceActivity{Kind: sim.SourceActivityBake, ObjectID: "home",
					StartedAt: time.Date(2026, 7, 17, 15, 0, 0, 0, time.UTC), Until: time.Date(2026, 7, 17, 19, 0, 0, 0, time.UTC)}
			}},
		{name: "asleep", actor: "alice", item: sim.HorseshoeKind, model: true,
			setup: func(w *sim.World) { w.Actors["alice"].State = sim.StateSleeping }},
		{name: "no horseshoe in the pack", actor: "bob", item: sim.HorseshoeKind, model: true},
		{name: "already up", actor: "alice", item: sim.HorseshoeKind, model: true,
			setup: func(w *sim.World) {
				w.VillageObjects["home"].Tags = append(w.VillageObjects["home"].Tags, sim.TagWardHorseshoe)
			}},
		{name: "a player", actor: "alice", item: sim.HorseshoeKind, model: true,
			setup: func(w *sim.World) { w.Actors["alice"].Kind = sim.KindPC }},
		{name: "a visitor", actor: "alice", item: sim.HorseshoeKind, model: true,
			setup: func(w *sim.World) { w.Actors["alice"].VisitorState = &sim.VisitorState{} }},
		{name: "no omen in the household", actor: "alice", item: sim.HorseshoeKind, model: true,
			setup: func(w *sim.World) { w.Environment.Omens.LastAt = nil }},
		{name: "the want has run out", actor: "alice", item: sim.HorseshoeKind, model: true,
			setup: func(w *sim.World) {
				w.Environment.Omens.LastAt["bob"] = time.Date(2026, 7, 9, 16, 0, 0, 0, time.UTC) // 8 days before now
			}},
		{name: "another ward is up", actor: "alice", item: sim.HorseshoeKind, model: true,
			setup: func(w *sim.World) {
				w.VillageObjects["home"].Tags = append(w.VillageObjects["home"].Tags, sim.TagWardWitchBottle)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, cancel, now := buildBakeTestWorld(t)
			defer cancel()
			seedHouseholdOmen(t, w, now)
			mustSend(t, w, func(world *sim.World) { world.Actors["alice"].Inventory[sim.HorseshoeKind] = 1 })
			before := len(homeTags(t, w))
			if tc.name == "already up" || tc.name == "another ward is up" {
				before++
			}
			_, err := placeHomeWardWith(w, tc.actor, tc.item, now, tc.setup)
			if err == nil {
				t.Fatal("placed, want a refusal")
			}
			var mf sim.ModelFacingError
			if tc.model && !errors.As(err, &mf) {
				t.Errorf("err %v, want a ModelFacingError", err)
			}
			if got := len(homeTags(t, w)); got != before {
				t.Errorf("home tags changed on a refusal: %d → %d", before, got)
			}
			mustSend(t, w, func(world *sim.World) {
				if world.Actors["alice"].Inventory[sim.HorseshoeKind] != 1 {
					t.Errorf("alice's horseshoe left the pack on a refusal")
				}
			})
		})
	}
}

func TestOmenHouseholdClause_NeverSaysWitch(t *testing.T) {
	for _, k := range sim.AllOmenKinds {
		if c := sim.OmenHouseholdClause(k); containsWitch(c) {
			t.Errorf("clause for %s names witchcraft: %q", k, c)
		}
	}
	for _, k := range sim.VillagerWardKinds {
		if containsWitch(sim.WardLogText(k)) || containsWitch(string(k)) {
			t.Errorf("villager ward %s names witchcraft", k)
		}
		if sim.WardLogText(k) == "" {
			t.Errorf("villager ward %s has no log text", k)
		}
	}
}

func containsWitch(s string) bool {
	return strings.Contains(strings.ToLower(s), "witch")
}
