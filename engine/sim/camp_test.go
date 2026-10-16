package sim_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// LLM-798: a player's tent and camp.

// campStand is where the test player stands to pitch — the tile in front of the
// tent's door.
var campStand = sim.TilePos{X: sim.PadX + 30, Y: sim.PadY + 30}

func campAssets() map[sim.AssetID]*sim.Asset {
	zero := 0
	return map[sim.AssetID]*sim.Asset{
		sim.TentAssetID: {
			ID: sim.TentAssetID, Name: "Tent", Category: "structure", DefaultState: sim.TentStateClosed,
			IsObstacle: true, FootprintLeft: 1, FootprintRight: 2, FootprintTop: 3,
			DoorOffsetX: &zero, DoorOffsetY: &zero,
			States: []sim.AssetState{
				{ID: 1, State: sim.TentStatePacked},
				{ID: 2, State: sim.TentStateClosed, Tags: []string{sim.TagUnoccupied}},
				{ID: 3, State: sim.TentStateOpen, Tags: []string{sim.TagOccupied}},
			},
			OccupiedMinCount: 1,
		},
		sim.CampFireAssetID: {
			ID: sim.CampFireAssetID, Name: "Camp Fire", Category: "camp", DefaultState: sim.CampFireStateUnlit, IsObstacle: true,
			States: []sim.AssetState{
				{ID: 4, State: sim.CampFireStateUnlit}, {ID: 5, State: sim.CampFireStateBurning},
				{ID: 6, State: sim.CampFireStateDying}, {ID: 7, State: sim.CampFireStateAshes},
			},
		},
		sim.CampStoolAssetID:    {ID: sim.CampStoolAssetID, Name: "Camp Stool", Category: "camp", DefaultState: "default"},
		sim.CampWoodpileAssetID: {ID: sim.CampWoodpileAssetID, Name: "Camp Woodpile", Category: "camp", DefaultState: "default"},
		"house-asset": {ID: "house-asset", Name: "House", Category: "structure", IsObstacle: true,
			DoorOffsetX: &zero, DoorOffsetY: &zero},
		"rock-asset":   {ID: "rock-asset", Name: "Rock", Category: "nature", IsObstacle: true},
		"flower-asset": {ID: "flower-asset", Name: "Flowers", Category: "nature"},
	}
}

// buildCampWorld seeds an all-grass village with a house well away from the
// test player, who stands at campStand with a tent in their pack. extra adds
// objects for a case.
func buildCampWorld(t *testing.T, extra map[sim.VillageObjectID]*sim.VillageObject, terrain func(*sim.Terrain)) *sim.World {
	t.Helper()
	repo, handles := mem.NewRepository()
	tr := makeAllGrassTerrain()
	if terrain != nil {
		terrain(tr)
	}
	handles.Terrain.Seed(tr)
	handles.Assets.Seed(campAssets())
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		sim.TentItemKind: {Name: sim.TentItemKind, DisplayLabel: "Tent", Capabilities: []string{sim.CapabilityShelter}},
	})
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		"house": {ID: "house", AssetID: "house-asset", DisplayName: "Inn",
			Pos: sim.TilePos{X: campStand.X + 20, Y: campStand.Y}.Center()},
	}
	for id, o := range extra {
		objects[id] = o
	}
	handles.VillageObjects.Seed(objects)
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{"house": {ID: "house", DisplayName: "Inn"}})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"pc": {ID: "pc", DisplayName: "Jefferey", Kind: sim.KindPC, LoginUsername: "jeff",
			State: sim.StateIdle, Pos: campStand,
			Inventory: map[sim.ItemKind]int{sim.TentItemKind: 1},
			Needs:     map[sim.NeedKey]int{"hunger": 0, "thirst": 0, "tiredness": 0}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)
	mustSend(t, w, func(world *sim.World) {
		world.Settings.PitchSeconds = 10
		world.Settings.StrikeSeconds = 8
		world.Settings.TentRestFloor = 6
		world.Settings.TentRestFloorFire = 3
		world.Settings.CampFireBurnMinutes = 180
		world.Settings.CampFireDyingMinutes = 60
	})
	return w
}

// campState is what a test reads back off the live world.
type campState struct {
	TentID    sim.VillageObjectID
	TentState string
	Structure bool
	Fire      string // the fire's state, "" when there is no fire ring
	Stool     bool
	Woodpile  bool
	Tents     int // in the pack
	Home      sim.StructureID
	Working   sim.SourceActivityKind
	Step      int
}

func readCamp(t *testing.T, w *sim.World) campState {
	t.Helper()
	var s campState
	mustSend(t, w, func(world *sim.World) {
		pc := world.Actors["pc"]
		s.Tents = pc.Inventory[sim.TentItemKind]
		s.Home = pc.HomeStructureID
		if pc.SourceActivity != nil {
			s.Working = pc.SourceActivity.Kind
			s.Step = pc.SourceActivity.StepsDone
		}
		camp := sim.FindCamp(world.VillageObjects, "pc")
		if camp == nil {
			return
		}
		s.TentID = camp.Tent.ID
		s.TentState = camp.Tent.CurrentState
		_, s.Structure = world.Structures[sim.StructureID(camp.Tent.ID)]
		if camp.Fire != nil {
			s.Fire = camp.Fire.CurrentState
		}
		s.Stool = camp.Stool != nil
		s.Woodpile = camp.Woodpile != nil
	})
	return s
}

// landBeats lands the in-flight beats one at a time, each a minute after the
// last, until the work is done or n beats have landed. Returns the clock.
func landBeats(t *testing.T, w *sim.World, at time.Time, n int) time.Time {
	t.Helper()
	for i := 0; i < n; i++ {
		at = at.Add(time.Minute)
		mustSend(t, w, func(world *sim.World) { sim.CompleteDueSourceActivities(world, at) })
	}
	return at
}

func pitchAll(t *testing.T, w *sim.World) time.Time {
	t.Helper()
	now := time.Now().UTC()
	if _, err := w.Send(sim.PitchTent("pc", now)); err != nil {
		t.Fatalf("PitchTent: %v", err)
	}
	return landBeats(t, w, now, 4)
}

// The camp goes up a beat at a time: the bundle first (the spot taken, the tent
// out of the pack), the tent standing as the player's home, then each prop.
func TestPitchTent_BuildsCampBeatByBeat(t *testing.T) {
	w := buildCampWorld(t, nil, nil)
	now := time.Now().UTC()
	if _, err := w.Send(sim.PitchTent("pc", now)); err != nil {
		t.Fatalf("PitchTent: %v", err)
	}
	s := readCamp(t, w)
	if s.TentState != sim.TentStatePacked || s.Structure || s.Tents != 0 || s.Home != "" {
		t.Fatalf("after start = %+v, want a packed bundle, no structure, no tent in pack, no home", s)
	}
	if s.Working != sim.SourceActivityPitch || s.Step != 0 {
		t.Fatalf("work = %q step %d, want pitch step 0", s.Working, s.Step)
	}

	now = landBeats(t, w, now, 1)
	s = readCamp(t, w)
	if s.TentState != sim.TentStateClosed || !s.Structure || s.Home != sim.StructureID(s.TentID) {
		t.Fatalf("after the tent beat = %+v, want a standing tent that is the player's home", s)
	}
	if s.Fire != "" || s.Stool || s.Woodpile {
		t.Fatalf("after the tent beat = %+v, want no props yet", s)
	}

	landBeats(t, w, now, 3)
	s = readCamp(t, w)
	if s.Fire != sim.CampFireStateUnlit || !s.Stool || !s.Woodpile {
		t.Fatalf("after every beat = %+v, want an unlit fire ring, a stool and a woodpile", s)
	}
	if s.Working != "" {
		t.Fatalf("work = %q, want done", s.Working)
	}
	if _, err := w.Send(sim.PitchTent("pc", time.Now().UTC())); !errors.Is(err, sim.ErrCampBusy) && !errors.Is(err, sim.ErrCampNoTent) {
		t.Fatalf("second pitch err = %v, want a refusal", err)
	}
}

// Pitching is refused where a camp can't go, and nothing changes.
func TestPitchTent_RefusesBadGround(t *testing.T) {
	cases := []struct {
		name    string
		extra   map[sim.VillageObjectID]*sim.VillageObject
		terrain func(*sim.Terrain)
		setup   func(world *sim.World)
		want    error
	}{
		{
			name:  "no tent in the pack",
			setup: func(world *sim.World) { delete(world.Actors["pc"].Inventory, sim.TentItemKind) },
			want:  sim.ErrCampNoTent,
		},
		{
			name: "dirt under the tent",
			terrain: func(tr *sim.Terrain) {
				tr.Data[(campStand.Y-3)*sim.MapW+campStand.X] = sim.TerrainDirt
			},
			want: sim.ErrCampNotGrass,
		},
		{
			name: "cobblestone under the fire ring",
			terrain: func(tr *sim.Terrain) {
				tr.Data[(campStand.Y+1)*sim.MapW+campStand.X+2] = sim.TerrainCobblestone
			},
			want: sim.ErrCampNotGrass,
		},
		{
			name: "a rock in the way",
			extra: map[sim.VillageObjectID]*sim.VillageObject{
				"rock": {ID: "rock", AssetID: "rock-asset", Pos: sim.TilePos{X: campStand.X + 1, Y: campStand.Y - 2}.Center()},
			},
			want: sim.ErrCampBlocked,
		},
		{
			name: "flowers where the stool goes",
			extra: map[sim.VillageObjectID]*sim.VillageObject{
				"flowers": {ID: "flowers", AssetID: "flower-asset", Pos: sim.TilePos{X: campStand.X + 3, Y: campStand.Y + 2}.Center()},
			},
			want: sim.ErrCampBlocked,
		},
		{
			name: "beside a doorway",
			setup: func(world *sim.World) {
				world.VillageObjects["house"].Pos = sim.TilePos{X: campStand.X - 2, Y: campStand.Y}.Center()
			},
			want: sim.ErrCampNearDoor,
		},
		{
			name:  "inside a pen",
			extra: penAround(campStand, 8),
			want:  sim.ErrCampInPen,
		},
		{
			name: "indoors",
			setup: func(world *sim.World) {
				sim.SetActorInsideStructure(world, world.Actors["pc"], "house")
			},
			want: sim.ErrCampIndoors,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := buildCampWorld(t, tc.extra, tc.terrain)
			if tc.setup != nil {
				mustSend(t, w, tc.setup)
			}
			before := readCamp(t, w)
			if _, err := w.Send(sim.PitchTent("pc", time.Now().UTC())); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if after := readCamp(t, w); after != before {
				t.Fatalf("a refused pitch changed the world: %+v -> %+v", before, after)
			}
		})
	}
}

// penAround rings center with rocks at Chebyshev radius r, a closed pen.
func penAround(center sim.TilePos, r int) map[sim.VillageObjectID]*sim.VillageObject {
	out := map[sim.VillageObjectID]*sim.VillageObject{}
	n := 0
	for y := center.Y - r; y <= center.Y+r; y++ {
		for x := center.X - r; x <= center.X+r; x++ {
			if x != center.X-r && x != center.X+r && y != center.Y-r && y != center.Y+r {
				continue
			}
			id := sim.VillageObjectID("pen-" + string(rune('a'+n/26)) + string(rune('a'+n%26)))
			out[id] = &sim.VillageObject{ID: id, AssetID: "rock-asset", Pos: sim.TilePos{X: x, Y: y}.Center()}
			n++
		}
	}
	return out
}

// Striking takes the props up one beat each, then packs the tent: the whole
// camp gone, the home cleared, the tent back in the pack.
func TestStrikeTent_PacksCampAway(t *testing.T) {
	w := buildCampWorld(t, nil, nil)
	now := pitchAll(t, w)
	tentID := readCamp(t, w).TentID

	if _, err := w.Send(sim.StrikeTent("pc", now)); err != nil {
		t.Fatalf("StrikeTent: %v", err)
	}
	now = landBeats(t, w, now, 3)
	s := readCamp(t, w)
	if s.Fire != "" || s.Stool || s.Woodpile {
		t.Fatalf("after the prop beats = %+v, want every prop taken up", s)
	}
	if s.TentState != sim.TentStatePacked || s.Home == "" {
		t.Fatalf("before the tent beat = %+v, want the tent down to its bundle and still home", s)
	}
	landBeats(t, w, now, 1)
	s = readCamp(t, w)
	if s.TentID != "" || s.Home != "" || s.Tents != 1 || s.Working != "" {
		t.Fatalf("after the strike = %+v, want no camp, no home, the tent in the pack", s)
	}
	mustSend(t, w, func(world *sim.World) {
		if _, ok := world.Structures[sim.StructureID(tentID)]; ok {
			t.Error("the tent's structure survived the strike")
		}
	})
}

// Strike is refused away from the tent and while someone is inside.
func TestStrikeTent_Refusals(t *testing.T) {
	w := buildCampWorld(t, nil, nil)
	now := pitchAll(t, w)
	tentID := readCamp(t, w).TentID

	mustSend(t, w, func(world *sim.World) { world.Actors["pc"].Pos = sim.TilePos{X: campStand.X + 6, Y: campStand.Y} })
	if _, err := w.Send(sim.StrikeTent("pc", now)); !errors.Is(err, sim.ErrCampNotAtTent) {
		t.Fatalf("away from the tent err = %v, want ErrCampNotAtTent", err)
	}
	mustSend(t, w, func(world *sim.World) {
		world.Actors["pc"].Pos = campStand
		world.Actors["guest"] = &sim.Actor{ID: "guest", DisplayName: "Guest", Kind: sim.KindNPCShared, State: sim.StateIdle}
		sim.RebuildIndicesForTest(world)
		sim.SetActorInsideStructure(world, world.Actors["guest"], sim.StructureID(tentID))
	})
	if _, err := w.Send(sim.StrikeTent("pc", now)); !errors.Is(err, sim.ErrCampOccupied) {
		t.Fatalf("someone inside err = %v, want ErrCampOccupied", err)
	}
}

// Walking off mid-pitch takes the camp down and returns the tent; walking off
// mid-strike stands the camp back up.
func TestCampWork_WalkingOffUndoes(t *testing.T) {
	walkOff := func(t *testing.T, w *sim.World, at time.Time) {
		t.Helper()
		dest := sim.NewPositionDestination(sim.TilePos{X: campStand.X - 5, Y: campStand.Y + 5})
		if _, err := w.Send(sim.MoveActor("pc", dest, false, at)); err != nil {
			t.Fatalf("MoveActor: %v", err)
		}
	}

	t.Run("mid-pitch", func(t *testing.T) {
		w := buildCampWorld(t, nil, nil)
		now := time.Now().UTC()
		if _, err := w.Send(sim.PitchTent("pc", now)); err != nil {
			t.Fatalf("PitchTent: %v", err)
		}
		now = landBeats(t, w, now, 2) // the tent stands, the fire ring is down
		walkOff(t, w, now)
		s := readCamp(t, w)
		if s.TentID != "" || s.Home != "" || s.Tents != 1 || s.Working != "" {
			t.Fatalf("after walking off = %+v, want no camp, no home, the tent back in the pack", s)
		}
		mustSend(t, w, func(world *sim.World) {
			for _, o := range world.VillageObjects {
				if o.OwnerActorID == "pc" {
					t.Errorf("camp object %s (%s) left behind", o.ID, o.AssetID)
				}
			}
		})
	})

	t.Run("mid-strike", func(t *testing.T) {
		w := buildCampWorld(t, nil, nil)
		now := pitchAll(t, w)
		if _, err := w.Send(sim.StrikeTent("pc", now)); err != nil {
			t.Fatalf("StrikeTent: %v", err)
		}
		now = landBeats(t, w, now, 3) // every prop up, the tent packed
		walkOff(t, w, now)
		s := readCamp(t, w)
		if s.TentState != sim.TentStateClosed || s.Fire != sim.CampFireStateUnlit || !s.Stool || !s.Woodpile {
			t.Fatalf("after walking off = %+v, want the whole camp standing again", s)
		}
		if s.Home != sim.StructureID(s.TentID) || s.Tents != 0 {
			t.Fatalf("after walking off = %+v, want the tent still home and out of the pack", s)
		}
	})
}

// A player sleeps in their own tent and wakes stiff at the tent's rest floor —
// lower when their fire was lit as they lay down.
func TestTentSleep_RestFloor(t *testing.T) {
	sleepFrom := func(t *testing.T, light bool) int {
		t.Helper()
		w := buildCampWorld(t, nil, nil)
		now := pitchAll(t, w)
		if light {
			mustSend(t, w, func(world *sim.World) {
				world.Actors["pc"].Pos = sim.TilePos{X: campStand.X + 1, Y: campStand.Y + 1}
			})
			if _, err := w.Send(sim.LightCampFire("pc", now)); err != nil {
				t.Fatalf("LightCampFire: %v", err)
			}
		}
		tentID := readCamp(t, w).TentID
		mustSend(t, w, func(world *sim.World) {
			pc := world.Actors["pc"]
			pc.Needs["tiredness"] = 20
			seen := now
			pc.LastPCSeenAt = &seen
			pc.Pos = campStand
			sim.SetActorInsideStructure(world, pc, sim.StructureID(tentID))
			world.Settings.TirednessRecoveryPerMinuteX100 = 10
		})
		if _, err := w.Send(sim.SleepPC("pc", now)); err != nil {
			t.Fatalf("SleepPC in own tent: %v", err)
		}
		later := now.Add(10 * time.Hour)
		if _, err := w.Send(sim.RecoverTiredness(later)); err != nil {
			t.Fatalf("RecoverTiredness: %v", err)
		}
		var tired int
		var asleep bool
		mustSend(t, w, func(world *sim.World) {
			pc := world.Actors["pc"]
			seen := later
			pc.LastPCSeenAt = &seen
			tired = pc.Needs["tiredness"]
		})
		if _, err := w.Send(sim.WakeExpiredPCSleepers(later)); err != nil {
			t.Fatalf("WakeExpiredPCSleepers: %v", err)
		}
		mustSend(t, w, func(world *sim.World) { asleep = world.Actors["pc"].SleepingUntil != nil })
		if asleep {
			t.Errorf("still asleep at the rest floor, want woken stiff")
		}
		return tired
	}

	if got := sleepFrom(t, false); got != 6 {
		t.Errorf("tiredness after a night in the tent = %d, want 6 (the tent's floor)", got)
	}
	if got := sleepFrom(t, true); got != 3 {
		t.Errorf("tiredness after a night by a lit fire = %d, want 3 (the fire's floor)", got)
	}
}

// Someone else's tent, or no home at all, is no bed.
func TestTentSleep_OnlyYourOwn(t *testing.T) {
	w := buildCampWorld(t, nil, nil)
	pitchAll(t, w)
	tentID := readCamp(t, w).TentID
	mustSend(t, w, func(world *sim.World) {
		world.Actors["other"] = &sim.Actor{ID: "other", DisplayName: "Other", Kind: sim.KindPC, LoginUsername: "other",
			State: sim.StateIdle, Needs: map[sim.NeedKey]int{"tiredness": 20}}
		sim.RebuildIndicesForTest(world)
		sim.SetActorInsideStructure(world, world.Actors["other"], sim.StructureID(tentID))
	})
	if _, err := w.Send(sim.SleepPC("other", time.Now().UTC())); !errors.Is(err, sim.ErrPCCannotSleepHere) {
		t.Fatalf("a guest sleeping in another's tent err = %v, want ErrPCCannotSleepHere", err)
	}
}

// The fire is lit only by its owner at the ring, then burns, dies down and goes
// to ashes on the clock; it can be lit again from ashes.
func TestLightCampFire_BurnsDownByClock(t *testing.T) {
	w := buildCampWorld(t, nil, nil)
	now := pitchAll(t, w)

	mustSend(t, w, func(world *sim.World) {
		world.Actors["pc"].Pos = sim.TilePos{X: campStand.X - 4, Y: campStand.Y}
	})
	if _, err := w.Send(sim.LightCampFire("pc", now)); !errors.Is(err, sim.ErrCampNotAtFire) {
		t.Fatalf("lighting from across the camp err = %v, want ErrCampNotAtFire", err)
	}
	mustSend(t, w, func(world *sim.World) {
		world.Actors["pc"].Pos = sim.TilePos{X: campStand.X + 2, Y: campStand.Y + 2}
	})
	res, err := w.Send(sim.LightCampFire("pc", now))
	if err != nil {
		t.Fatalf("LightCampFire: %v", err)
	}
	lit := res.(sim.LightCampFireResult)
	if got := lit.OutAt.Sub(now); got != 4*time.Hour {
		t.Errorf("out after %v, want 4h (3h burning + 1h dying)", got)
	}
	if s := readCamp(t, w); s.Fire != sim.CampFireStateBurning {
		t.Fatalf("fire = %q, want burning", s.Fire)
	}
	if _, err := w.Send(sim.LightCampFire("pc", now)); !errors.Is(err, sim.ErrCampFireLit) {
		t.Fatalf("relight while burning err = %v, want ErrCampFireLit", err)
	}

	for _, step := range []struct {
		at   time.Duration
		want string
	}{
		{2 * time.Hour, sim.CampFireStateBurning},
		{3*time.Hour + 30*time.Minute, sim.CampFireStateDying},
		{4*time.Hour + time.Minute, sim.CampFireStateAshes},
	} {
		if _, err := w.Send(sim.UpdateCampFires(now.Add(step.at))); err != nil {
			t.Fatalf("UpdateCampFires: %v", err)
		}
		if s := readCamp(t, w); s.Fire != step.want {
			t.Errorf("at +%v fire = %q, want %q", step.at, s.Fire, step.want)
		}
	}
	if _, err := w.Send(sim.LightCampFire("pc", now.Add(5*time.Hour))); err != nil {
		t.Fatalf("relight from ashes: %v", err)
	}
}

// A restart settles a half-done camp: a packed tent that is nobody's home comes
// down and the tent goes back in the pack; a home tent missing props is made
// whole.
func TestSettleUnfinishedCampsOnLoad(t *testing.T) {
	t.Run("an unfinished pitch comes down", func(t *testing.T) {
		w := buildCampWorld(t, nil, nil)
		if _, err := w.Send(sim.PitchTent("pc", time.Now().UTC())); err != nil {
			t.Fatalf("PitchTent: %v", err)
		}
		mustSend(t, w, func(world *sim.World) {
			world.Actors["pc"].SourceActivity = nil // the window a restart loses
			if n := sim.SettleUnfinishedCampsOnLoad(world); n != 1 {
				t.Errorf("settled %d, want 1", n)
			}
		})
		if s := readCamp(t, w); s.TentID != "" || s.Tents != 1 {
			t.Fatalf("after settling = %+v, want no camp and the tent in the pack", s)
		}
	})

	t.Run("a home tent is made whole", func(t *testing.T) {
		w := buildCampWorld(t, nil, nil)
		now := pitchAll(t, w)
		if _, err := w.Send(sim.StrikeTent("pc", now)); err != nil {
			t.Fatalf("StrikeTent: %v", err)
		}
		landBeats(t, w, now, 3)
		mustSend(t, w, func(world *sim.World) {
			world.Actors["pc"].SourceActivity = nil
			if n := sim.SettleUnfinishedCampsOnLoad(world); n != 1 {
				t.Errorf("settled %d, want 1", n)
			}
		})
		s := readCamp(t, w)
		if s.TentState != sim.TentStateClosed || s.Fire == "" || !s.Stool || !s.Woodpile || s.Home != sim.StructureID(s.TentID) {
			t.Fatalf("after settling = %+v, want the whole camp standing as home", s)
		}
	})

	t.Run("a whole camp is left alone", func(t *testing.T) {
		w := buildCampWorld(t, nil, nil)
		pitchAll(t, w)
		mustSend(t, w, func(world *sim.World) {
			if n := sim.SettleUnfinishedCampsOnLoad(world); n != 0 {
				t.Errorf("settled %d, want 0", n)
			}
		})
	})
}

// The tent is a player's shelter: never barterable.
func TestTentIsNotBarterable(t *testing.T) {
	def := &sim.ItemKindDef{Name: sim.TentItemKind, Capabilities: []string{sim.CapabilityShelter}}
	if sim.KindBarterable(def) {
		t.Error("KindBarterable(tent) = true, want false")
	}
}

// The pack row names a camp by the nearest building that isn't a tent.
func TestCampPlaceLabel(t *testing.T) {
	w := buildCampWorld(t, nil, nil)
	pitchAll(t, w)
	mustSend(t, w, func(world *sim.World) {
		camp := sim.FindCamp(world.VillageObjects, "pc")
		if got := sim.CampPlaceLabel(world.VillageObjects, world.Structures, camp.Tent); got != "by the Inn" {
			t.Errorf("CampPlaceLabel = %q, want %q", got, "by the Inn")
		}
	})
}
