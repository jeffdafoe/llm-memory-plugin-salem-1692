package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// The live Tavern's geometry (asset footprint, door, per-placement loiter
// offset) around an anchor tile at (100,100). Its loiter pin lands at (97,101),
// outside the footprint past the door; the keeper's interior tile (97,99) is two
// rows from it — beyond LoiterAttributionTiles — so the pin alone never pinned
// John Ellis's stew, and every meal he ate there gave only the immediate bite.
const (
	tavernAnchorX, tavernAnchorY = 100, 100
	tavernKeeperX, tavernKeeperY = 97, 99
)

// seedLiveTavern places the Tavern and stands actorID on its keeper tile,
// inside it when inside is true.
func seedLiveTavern(t *testing.T, w *sim.World, actorID sim.ActorID, inside bool) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Assets["tavern-asset"] = &sim.Asset{
			ID: "tavern-asset", Category: "structure",
			FootprintLeft: 3, FootprintRight: 4, FootprintTop: 6, FootprintBottom: 0,
			DoorOffsetX: intp(-2), DoorOffsetY: intp(-1),
		}
		world.VillageObjects["tavern"] = &sim.VillageObject{
			ID: "tavern", DisplayName: "Tavern", AssetID: "tavern-asset",
			Pos:           sim.TileToWorld(sim.GridPoint{X: tavernAnchorX, Y: tavernAnchorY}),
			LoiterOffsetX: intp(-3), LoiterOffsetY: intp(1),
		}
		world.Structures["tavern"] = &sim.Structure{ID: "tavern", DisplayName: "Tavern"}
		a := world.Actors[actorID]
		a.Pos = sim.TilePos{X: tavernKeeperX, Y: tavernKeeperY}
		if inside {
			a.InsideStructureID = "tavern"
		}
		sim.RebuildIndicesForTest(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed tavern: %v", err)
	}
}

var tavernMealKey = sim.DwellCreditKey{ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem}

// TestConsume_IndoorMeal_DwellPinsToBuilding: eating from the pack inside the
// Tavern, on a tile out of the loiter pin's reach, stamps the meal's dwell
// credit on the Tavern, and the next dwell tick finds the eater still there and
// drips the meal rather than ending it as walked away. The same tile outside
// the building stamps nothing — the inside-first rule is what pins it.
func TestConsume_IndoorMeal_DwellPinsToBuilding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inside bool
	}{
		{"inside", true},
		{"same tile, not inside", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			seedLiveTavern(t, w, "john", tc.inside)

			at := time.Now().UTC()
			if _, err := w.Send(sim.Consume("john", "stew", 1, at)); err != nil {
				t.Fatalf("Consume: %v", err)
			}
			view := readLiveActor(t, w, "john")
			_, pinned := view.DwellCredits[tavernMealKey]
			if pinned != tc.inside {
				t.Fatalf("meal credit at Tavern = %v, want %v; credits %v", pinned, tc.inside, view.DwellCredits)
			}
			if !tc.inside {
				return
			}

			if _, err := w.Send(sim.ApplyDwellTick(at.Add(2 * time.Minute))); err != nil {
				t.Fatalf("ApplyDwellTick: %v", err)
			}
			view = readLiveActor(t, w, "john")
			credit, ok := view.DwellCredits[tavernMealKey]
			if !ok {
				t.Fatalf("meal credit ended on the first tick (walked away?); credits %v", view.DwellCredits)
			}
			if credit.RemainingTicks == nil || *credit.RemainingTicks != 7 {
				t.Errorf("RemainingTicks = %v, want 7", credit.RemainingTicks)
			}
			// 20 - 4 immediate - 1 drip.
			if got, want := view.Needs["hunger"], 15; got != want {
				t.Errorf("hunger after one drip = %d, want %d", got, want)
			}
		})
	}
}

// TestAcceptPay_ConsumeNow_IndoorMeal_DwellPinsToBuilding: an eat-here
// purchase served to a buyer inside the Tavern, off the loiter pin's reach,
// pins its dwell credit to the Tavern too.
func TestAcceptPay_ConsumeNow_IndoorMeal_DwellPinsToBuilding(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "john", displayName: "John", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	seedLiveTavern(t, w, "alice", true)
	at := time.Now().UTC()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["alice"].Needs = map[sim.NeedKey]int{"hunger": 20}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed needs: %v", err)
	}
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "john",
		ItemKind: "stew", Qty: 1, Amount: 5, ConsumeNow: true,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.AcceptPay("john", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	view := readLiveActor(t, w, "alice")
	if _, ok := view.DwellCredits[tavernMealKey]; !ok {
		t.Fatalf("no meal credit at Tavern for the indoor buyer; credits %v", view.DwellCredits)
	}
}
