package sim_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// visitor_slot_outside_test.go — a visit stands outside the building. The
// three residence shapes are the live Tiny Swords assets: the Small house's
// front door on its south wall, the Medium house's side door opening west, the
// Tiny house's back door on its north edge. None carries a per-instance loiter
// offset, so each takes the door fallback pin.

type houseShape struct {
	name       string
	asset      *sim.Asset
	wantOffset sim.Position // the door fallback pin, relative to the anchor
}

func houseShapes() []houseShape {
	return []houseShape{
		{"small", &sim.Asset{ID: "small", Category: "structure", IsObstacle: true,
			FootprintLeft: 3, FootprintRight: 2, FootprintTop: 8, FootprintBottom: 0,
			DoorOffsetX: intp(0), DoorOffsetY: intp(-1)}, sim.Position{X: 0, Y: 1}},
		{"medium", &sim.Asset{ID: "medium", Category: "structure", IsObstacle: true,
			FootprintLeft: 3, FootprintRight: 3, FootprintTop: 6, FootprintBottom: 0,
			DoorOffsetX: intp(-2), DoorOffsetY: intp(-2)}, sim.Position{X: -4, Y: -2}},
		{"tiny", &sim.Asset{ID: "tiny", Category: "structure", IsObstacle: true,
			FootprintLeft: 3, FootprintRight: 3, FootprintTop: 8, FootprintBottom: 0,
			DoorOffsetX: intp(0), DoorOffsetY: intp(-8)}, sim.Position{X: 0, Y: -9}},
	}
}

// TestDoorLoiterPinSitsOutsideTheFootprint — the fallback pin is one tile past
// the wall the door opens through, in line with the door.
func TestDoorLoiterPinSitsOutsideTheFootprint(t *testing.T) {
	for _, h := range houseShapes() {
		vobj := &sim.VillageObject{ID: "house", AssetID: h.asset.ID, Pos: sim.WorldPos{X: 1000, Y: 1000}}
		x, y := sim.EffectiveLoiterOffset(vobj, h.asset)
		if (sim.Position{X: x, Y: y}) != h.wantOffset {
			t.Errorf("%s: loiter offset = (%d,%d), want %+v", h.name, x, y, h.wantOffset)
		}
	}
}

// TestVisitorsOfAClosedHouseStandOutside — six non-members walk to each
// owner-only house in turn. Every one arrives outside its footprint, is not
// attributed inside, and stands by the pin. The door fallback used to put the
// pin in or against the wall, where the door tile was a visitor slot: a
// visitor stood in the doorway, counted as inside the house.
func TestVisitorsOfAClosedHouseStandOutside(t *testing.T) {
	for _, h := range houseShapes() {
		t.Run(h.name, func(t *testing.T) {
			repo, handles := mem.NewRepository()
			handles.Terrain.Seed(makeAllGrassTerrain())
			handles.Assets.Seed(map[sim.AssetID]*sim.Asset{h.asset.ID: h.asset})
			house := sim.WorldPos{X: 640, Y: 960}
			handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
				"house": {ID: "house", AssetID: h.asset.ID, Pos: house, EntryPolicy: sim.EntryPolicyOwner},
			})
			handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
				"house": {ID: "house", DisplayName: "House"},
			})
			actors := map[sim.ActorID]*sim.Actor{}
			for i := 0; i < 6; i++ {
				id := sim.ActorID(fmt.Sprintf("visitor-%d", i))
				actors[id] = &sim.Actor{ID: id, DisplayName: string(id),
					Pos: sim.TilePos{X: sim.PadX + 10 + i, Y: sim.PadY + 50}}
			}
			handles.Actors.Seed(actors)
			w, err := sim.LoadWorld(context.Background(), repo)
			if err != nil {
				t.Fatalf("LoadWorld: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go w.Run(ctx)

			anchor := house.Tile()
			inFootprint := func(p sim.Position) bool {
				a := h.asset
				return p.X >= anchor.X-a.FootprintLeft && p.X <= anchor.X+a.FootprintRight &&
					p.Y >= anchor.Y-a.FootprintTop && p.Y <= anchor.Y+a.FootprintBottom
			}
			pin := sim.Position{X: anchor.X + h.wantOffset.X, Y: anchor.Y + h.wantOffset.Y}
			now := time.Now().UTC()
			standing := map[sim.Position]sim.ActorID{}
			for i := 0; i < 6; i++ {
				id := sim.ActorID(fmt.Sprintf("visitor-%d", i))
				if _, err := w.Send(sim.MoveToStructure(id, "house", now)); err != nil {
					t.Fatalf("%s move_to the house: %v", id, err)
				}
				driveToArrival(t, w, id, now, 120)
				pos, inside := actorSpatial(t, w, id)
				if other, ok := standing[pos]; ok {
					t.Errorf("%s and %s both stand at %+v", other, id, pos)
				}
				standing[pos] = id
				if inside != "" || inFootprint(pos) {
					t.Errorf("%s stands at %+v (inside=%q) — on the footprint of a house it may not enter", id, pos, inside)
				}
				if pos.Chebyshev(pin) > sim.LoiterAttributionTiles {
					t.Errorf("%s stands at %+v, more than %d tile from the pin %+v", id, pos, sim.LoiterAttributionTiles, pin)
				}
			}
		})
	}
}
