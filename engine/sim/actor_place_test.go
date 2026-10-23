package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// TestIndoorMeal_PromptSaysEating: John Ellis eats a stew on his interior tile
// in the Tavern, two tiles from its loiter pin. His next prompt says he is
// eating it there — the published place is the building he is inside, so
// perception's co-location gate keeps the credit. Stepping out onto the same
// tile drops the line at once, as a walk-away should.
func TestIndoorMeal_PromptSaysEating(t *testing.T) {
	w, stop := buildConsumeTestWorld(t,
		[]consumeActorSpec{{
			id:          "john",
			displayName: "John",
			inventory:   map[sim.ItemKind]int{"stew": 1},
			needs:       map[sim.NeedKey]int{"hunger": 20},
		}},
		nil,
	)
	defer stop()
	seedLiveTavern(t, w, "john", true)

	if _, err := w.Send(sim.Consume("john", "stew", 1, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if got := w.Published().Actors["john"].CurrentPlaceObjectID; got != "tavern" {
		t.Fatalf("published CurrentPlaceObjectID = %q, want tavern", got)
	}
	p := perception.Build(w.Published(), "john", nil)
	text := perception.Render(p, perception.DefaultRenderConfig()).EphemeralText
	if !strings.Contains(text, "You are eating stew at Tavern") {
		t.Fatalf("prompt has no indoor meal line; ## You reads:\n%s", text)
	}

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["john"].InsideStructureID = ""
		sim.RebuildIndicesForTest(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("step outside: %v", err)
	}
	p = perception.Build(w.Published(), "john", nil)
	if text := perception.Render(p, perception.DefaultRenderConfig()).EphemeralText; strings.Contains(text, "You are eating stew") {
		t.Errorf("meal line still renders after leaving the building:\n%s", text)
	}
}

// TestOutdoorCredit_EndsOnGoingInside: an actor holding a well's thirst credit
// steps into the Tavern onto a tile still within one of the well's pin. Inside,
// its place is the Tavern, so the next dwell tick ends the well credit as walked
// away — the tick keeps only what perception renders. On the same tile
// outdoors, the well still owns the tile and the credit stays.
func TestOutdoorCredit_EndsOnGoingInside(t *testing.T) {
	wellKey := sim.DwellCreditKey{ObjectID: "well", Attribute: "thirst", Source: sim.DwellSourceObject}
	for _, tc := range []struct {
		name     string
		inside   bool
		wantKept bool
	}{
		{"inside the Tavern", true, false},
		{"same tile, outdoors", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, stop := buildConsumeTestWorld(t,
				[]consumeActorSpec{{
					id:          "john",
					displayName: "John",
					needs:       map[sim.NeedKey]int{"thirst": 15},
				}},
				[]consumeObjectSpec{{id: "well", x: tavernKeeperX, y: tavernKeeperY - 1}},
			)
			defer stop()
			seedLiveTavern(t, w, "john", tc.inside)
			at := time.Now().UTC()
			if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
				world.Actors["john"].DwellCredits = map[sim.DwellCreditKey]*sim.DwellCredit{
					wellKey: {
						ObjectID: "well", Attribute: "thirst", Source: sim.DwellSourceObject,
						LastCreditedAt: at, DwellDelta: -1, DwellPeriodMinutes: 1,
					},
				}
				return nil, nil
			}}); err != nil {
				t.Fatalf("seed well credit: %v", err)
			}
			if _, err := w.Send(sim.ApplyDwellTick(at.Add(time.Minute))); err != nil {
				t.Fatalf("ApplyDwellTick: %v", err)
			}
			_, kept := readLiveActor(t, w, "john").DwellCredits[wellKey]
			if kept != tc.wantKept {
				t.Errorf("well credit kept = %v, want %v", kept, tc.wantKept)
			}
		})
	}
}

// TestArrivalStall_WalkIntoBusiness: a walk into a business carries no
// destination object, so arrivalStall answers on where the arriver is. Inside
// the Blacksmith, on its keeper tile four from the loiter pin (the live
// geometry), it is the Blacksmith; the same tile outdoors is no stall.
func TestArrivalStall_WalkIntoBusiness(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inside bool
	}{
		{"inside", true},
		{"same tile, not inside", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, stop := buildConsumeTestWorld(t,
				[]consumeActorSpec{{id: "jeff", displayName: "Jefferey"}, {id: "ezekiel", displayName: "Ezekiel"}},
				nil,
			)
			defer stop()
			res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
				world.Assets["smithy-asset"] = &sim.Asset{
					ID: "smithy-asset", Category: "structure",
					FootprintLeft: 3, FootprintRight: 3, FootprintTop: 4, FootprintBottom: 0,
					DoorOffsetX: intp(-1), DoorOffsetY: intp(-3),
				}
				world.VillageObjects["blacksmith"] = &sim.VillageObject{
					ID: "blacksmith", DisplayName: "Blacksmith", AssetID: "smithy-asset",
					Pos:           sim.TileToWorld(sim.GridPoint{X: 100, Y: 100}),
					LoiterOffsetX: intp(-2), LoiterOffsetY: intp(1),
					OwnerActorID: "ezekiel",
					Tags:         []string{sim.TagBusiness},
				}
				world.Structures["blacksmith"] = &sim.Structure{ID: "blacksmith", DisplayName: "Blacksmith"}
				a := world.Actors["jeff"]
				a.Pos = sim.TilePos{X: 98, Y: 97}
				if tc.inside {
					a.InsideStructureID = "blacksmith"
				}
				sim.RebuildIndicesForTest(world)
				if o := sim.ArrivalStallForTest(world, "jeff"); o != nil {
					return o.ID, nil
				}
				return sim.VillageObjectID(""), nil
			}})
			if err != nil {
				t.Fatalf("arrivalStall: %v", err)
			}
			want := sim.VillageObjectID("")
			if tc.inside {
				want = "blacksmith"
			}
			if got := res.(sim.VillageObjectID); got != want {
				t.Errorf("arrivalStall = %q, want %q", got, want)
			}
		})
	}
}
