package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cascade"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// stall_click_realwalk_test.go — LLM-793. A player's click on a market stall,
// end to end: EnterOrKnock → the real walk (locomotion ticker → finishArrival →
// ActorArrived) → business_arrival. Every live stall is owner-only with a door
// offset, the shape that made each stall click a knock (the LLM-785 door popup).
// The click must be a plain visit, and the arrival must form the keeper's
// huddle (LLM-384) — or, with the keeper abed, nothing at all.

// buildStallClickWorld seeds a live-shaped stall (owner-only, a door, tagged
// business + market_stall) with its keeper inside, and a player on open grass.
func buildStallClickWorld(t *testing.T, keeperState sim.ActorState) (*sim.World, func()) {
	t.Helper()
	now := time.Now().UTC()
	repo, h := mem.NewRepository()
	h.Terrain.Seed(makeAllGrassTerrain())
	h.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"stall-asset": {ID: "stall-asset", Category: "structure", DoorOffsetX: intp(0), DoorOffsetY: intp(0)},
	})
	h.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"stall": {
			ID: "stall", AssetID: "stall-asset", DisplayName: "Blacksmith",
			Pos: sim.WorldPos{X: 320, Y: 320}, LoiterOffsetX: intp(0), LoiterOffsetY: intp(5),
			EntryPolicy: sim.EntryPolicyOwner, OwnerActorID: "keeper",
			Tags: []string{sim.TagBusiness, sim.TagMarketStall},
		},
	})
	h.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"stall": {ID: "stall", DisplayName: "Blacksmith"},
	})
	h.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"keeper": {
			ID: "keeper", DisplayName: "Ezekiel", Kind: sim.KindNPCShared,
			State: keeperState, InsideStructureID: "stall", WorkStructureID: "stall",
			Pos:                sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10},
			BusinessownerState: &sim.BusinessownerState{Flavor: "gruff"},
		},
		"player": {
			ID: "player", DisplayName: "Jeff", Kind: sim.KindPC, State: sim.StateIdle,
			Pos: sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 5}, LastPCSeenAt: &now,
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		cascade.RegisterEncounter(world)
		cascade.RegisterBusinessArrival(world)
		cascade.RegisterBusinessowner(ctx, world)
		return nil, nil
	}}); err != nil {
		cancel()
		<-done
		t.Fatalf("register cascades: %v", err)
	}
	return w, func() { cancel(); <-done }
}

// clickStall sends the player's click on the stall and walks it to arrival,
// failing if the click came back as a knock.
func clickStall(t *testing.T, w *sim.World, now time.Time) {
	t.Helper()
	res, err := w.Send(sim.EnterOrKnock("player", "stall", true, now))
	if err != nil {
		t.Fatalf("EnterOrKnock: %v", err)
	}
	if res.(sim.EnterOrKnockResult).Knocked {
		t.Fatal("a click on a market stall came back as a knock — the door popup would open")
	}
	driveToArrival(t, w, "player", now, 40)
	if _, inside := actorSpatial(t, w, "player"); inside != "" {
		t.Fatalf("the player ended up inside %q, want at the counter outside", inside)
	}
}

func TestStallClick_KeeperAtPostServesAtTheCounter(t *testing.T) {
	w, stop := buildStallClickWorld(t, sim.StateIdle)
	defer stop()
	clickStall(t, w, time.Now().UTC())

	ph, kh := readHuddleOf(t, w, "player"), readHuddleOf(t, w, "keeper")
	if ph == "" {
		t.Fatal("no conversation formed at the stall's counter — the keeper never greets the player")
	}
	if ph != kh {
		t.Fatalf("player and keeper are in different conversations: player=%q keeper=%q", ph, kh)
	}
}

func TestStallClick_KeeperAbedIsASilentVisit(t *testing.T) {
	w, stop := buildStallClickWorld(t, sim.StateSleeping)
	defer stop()
	clickStall(t, w, time.Now().UTC())

	if ph := readHuddleOf(t, w, "player"); ph != "" {
		t.Fatalf("a stall with its keeper abed formed a conversation %q, want a silent visit", ph)
	}
}

// The keeper awake but away from the stall (out on the lane, not inside it):
// still a silent visit — WorkStructureID alone must not pull them in.
func TestStallClick_KeeperAwayIsASilentVisit(t *testing.T) {
	w, stop := buildStallClickWorld(t, sim.StateIdle)
	defer stop()
	setInside(t, w, "keeper", "")
	mutate(t, w, func(world *sim.World) {
		world.Actors["keeper"].Pos = sim.TilePos{X: sim.PadX + 40, Y: sim.PadY + 40}
	})
	clickStall(t, w, time.Now().UTC())

	if ph := readHuddleOf(t, w, "player"); ph != "" {
		t.Fatalf("a stall with its keeper away formed a conversation %q, want a silent visit", ph)
	}
	if kh := readHuddleOf(t, w, "keeper"); kh != "" {
		t.Fatalf("the keeper away from the stall was pulled into conversation %q", kh)
	}
}
