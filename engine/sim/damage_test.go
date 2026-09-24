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

// seqRoller is a DamageRoller that returns a fixed sequence, then 1 (never a
// hit) once it runs out.
type seqRoller struct{ vals []float64 }

func (r *seqRoller) Float64() float64 {
	if len(r.vals) == 0 {
		return 1
	}
	v := r.vals[0]
	r.vals = r.vals[1:]
	return v
}

// buildDamageWorld seeds two wells of the shipped shape (infinite drink row +
// finite yield-only carry row, TagWell) on an asset that carries a "damaged"
// state, a notice board with 0/2/3-slip frames, a worker (AttrWorker), and a
// non-worker. The chest holds 100; the bounty is 12 with a reserve of 50.
func buildDamageWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"water": {Name: "water", Category: sim.ItemCategoryDrink,
			Satisfies: []sim.ItemSatisfaction{{Attribute: "thirst", Immediate: 8}}},
	})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"well-asset": {ID: "well-asset", Name: "Well", States: []sim.AssetState{
			{ID: 1, State: "default"},
			{ID: 2, State: "damaged", Tags: []string{sim.TagDamaged}},
		}},
		"board-asset": {ID: "board-asset", Name: "Notice Board", States: []sim.AssetState{
			{ID: 10, State: "empty", Tags: []string{sim.TagNoticeBoard, sim.TagRotatable, "content-capacity-0"}},
			{ID: 11, State: "two", Tags: []string{sim.TagNoticeBoard, sim.TagRotatable, "content-capacity-2"}},
			{ID: 12, State: "three", Tags: []string{sim.TagNoticeBoard, sim.TagRotatable, "content-capacity-3"}},
		}},
	})
	zero := 0
	ip := func(v int) *int { return &v }
	well := func(id sim.VillageObjectID, x float64) *sim.VillageObject {
		return &sim.VillageObject{
			ID: id, DisplayName: "Well", AssetID: "well-asset", CurrentState: "default",
			Tags: []string{sim.TagWell}, LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: x, Y: 100},
			Refreshes: []*sim.ObjectRefresh{
				{Attribute: "thirst", Amount: -8},
				{Amount: 0, GatherItem: "water", AvailableQuantity: ip(20), MaxQuantity: ip(20)},
			},
		}
	}
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"well-a": well("well-a", 100),
		"well-b": well("well-b", 2000),
		"board": {ID: "board", DisplayName: "Notice Board", AssetID: "board-asset", CurrentState: "empty",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero, Pos: sim.WorldPos{X: 4000, Y: 4000}},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"anne":   {ID: "anne", DisplayName: "Anne Walker", Kind: sim.KindNPCShared, Attributes: map[string][]byte{sim.AttrWorker: nil}},
		"joseph": {ID: "joseph", DisplayName: "Joseph Scott", Kind: sim.KindNPCShared},
		"gideon": {ID: "gideon", DisplayName: "Constable Gideon Marsh", Kind: sim.KindNPCStateful, Attributes: map[string][]byte{sim.AttrConstable: nil}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	mustSend(t, w, func(world *sim.World) {
		world.Environment.TownChest = 100
		world.Settings.PublicWorksBounty = 12
		world.Settings.PublicWorksChestReserve = 50
		world.Settings.PublicWorksRepairSeconds = 3600
		world.Settings.WellDamageUseReference = 60
		world.Settings.WellDamageMinGapHours = 48
	})
	return w, cancel
}

func breakWell(t *testing.T, w *sim.World, id sim.VillageObjectID) {
	t.Helper()
	if _, err := w.Send(sim.SetObjectDamage(id, "damage")); err != nil {
		t.Fatalf("damage %s: %v", id, err)
	}
}

// TestDamagedWellIsOutOfUse — a broken well gives no drink and no water, and
// shows its damaged sprite state; mending it restores both.
func TestDamagedWellIsOutOfUse(t *testing.T) {
	w, cancel := buildDamageWorld(t)
	defer cancel()
	breakWell(t, w, "well-a")
	placeAt(t, w, "joseph", "well-a")
	grantForageEntry(t, w, "joseph", "water") // the water carrier's claim on the commons pail (LLM-610)

	mustSend(t, w, func(world *sim.World) {
		if got := world.VillageObjects["well-a"].CurrentState; got != "damaged" {
			t.Errorf("state = %q, want damaged", got)
		}
		world.Actors["joseph"].Needs = map[sim.NeedKey]int{"thirst": 50}
	})
	res, err := w.Send(sim.ApplyObjectRefreshAtArrival("joseph"))
	if err != nil {
		t.Fatalf("arrival: %v", err)
	}
	if hits := res.(sim.ArrivalRefreshResult).Hits; len(hits) != 0 {
		t.Errorf("drank at a broken well: %+v", hits)
	}
	if _, err := w.Send(sim.Gather("joseph", 5, time.Now())); !errors.Is(err, sim.ErrSourceDamaged) {
		t.Errorf("Gather err = %v, want ErrSourceDamaged", err)
	}

	if _, err := w.Send(sim.SetObjectDamage("well-a", "repair")); err != nil {
		t.Fatalf("repair: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		if got := world.VillageObjects["well-a"].CurrentState; got != "default" {
			t.Errorf("state after repair = %q, want default", got)
		}
	})
	res, _ = w.Send(sim.ApplyObjectRefreshAtArrival("joseph"))
	if hits := res.(sim.ArrivalRefreshResult).Hits; len(hits) == 0 {
		t.Error("no drink at the mended well")
	}
}

// TestRollWellDamageGuards — the roll breaks at most one well, never while one
// is already broken, never inside the min gap after a repair, and not at all
// with the chance at 0. The use factor scales the chance.
func TestRollWellDamageGuards(t *testing.T) {
	w, cancel := buildDamageWorld(t)
	defer cancel()
	now := time.Now().UTC()

	roll := func(permille int, vals ...float64) *sim.VillageObject {
		t.Helper()
		var got *sim.VillageObject
		mustSend(t, w, func(world *sim.World) {
			world.Settings.WellDamageStormChancePermille = permille
			got = sim.RollStormDamage(world, &seqRoller{vals: vals}, now)
		})
		return got
	}
	mustSend(t, w, func(world *sim.World) {
		world.VillageObjects["well-a"].UseSinceRepair = 30 // factor 0.5
		world.VillageObjects["well-b"].UseSinceRepair = 60 // factor 1
	})

	if got := roll(0, 0); got != nil {
		t.Errorf("chance 0 broke %s", got.ID)
	}
	// 100‰ × 0.5 = 0.05 for well-a: 0.06 misses it; 0.09 < 0.10 hits well-b.
	if got := roll(100, 0.06, 0.09); got == nil || got.ID != "well-b" {
		t.Fatalf("roll = %v, want well-b", got)
	}
	if got := roll(1000, 0, 0); got != nil {
		t.Errorf("a second well broke while one is out of use: %s", got.ID)
	}
	if _, err := w.Send(sim.SetObjectDamage("well-b", "repair")); err != nil {
		t.Fatal(err)
	}
	if got := roll(1000, 0, 0); got != nil {
		t.Errorf("a well broke inside the min gap after a repair: %s", got.ID)
	}
	mustSend(t, w, func(world *sim.World) { world.Environment.LastRepairAt = now.Add(-49 * time.Hour) })
	if got := roll(1000, 0); got == nil || got.ID != "well-a" {
		t.Errorf("roll after the gap = %v, want well-a", got)
	}
}

// TestPublicWorksRepairPaysFromTheChest — a worker at a broken well starts the
// town's repair; a second hand and a non-worker are refused; on completion the
// well is sound and the chest pays the bounty.
func TestPublicWorksRepairPaysFromTheChest(t *testing.T) {
	w, cancel := buildDamageWorld(t)
	defer cancel()
	breakWell(t, w, "well-a")
	placeAt(t, w, "anne", "well-a")
	placeAt(t, w, "joseph", "well-a")

	if _, err := w.Send(sim.StartRepair("joseph")); err == nil {
		t.Error("a non-worker took the town's work")
	}
	if _, err := w.Send(sim.StartRepair("anne")); err != nil {
		t.Fatalf("worker StartRepair: %v", err)
	}
	// Land the window now instead of waiting an hour.
	mustSend(t, w, func(world *sim.World) {
		world.Actors["anne"].SourceActivity.Until = time.Now().UTC().Add(-time.Second)
	})
	mustSend(t, w, func(world *sim.World) { sim.CompleteDueSourceActivities(world, time.Now().UTC()) })
	mustSend(t, w, func(world *sim.World) {
		if world.VillageObjects["well-a"].Damaged() {
			t.Error("well still broken after the repair landed")
		}
		if got := world.Actors["anne"].Coins; got != 12 {
			t.Errorf("anne coins = %d, want the 12 bounty", got)
		}
		if got := world.Environment.TownChest; got != 88 {
			t.Errorf("chest = %d, want 88", got)
		}
	})
}

// TestPublicWorksRefusedWhenTheChestIsLow — below bounty + reserve the town
// cannot pay, so the work is not on offer.
func TestPublicWorksRefusedWhenTheChestIsLow(t *testing.T) {
	w, cancel := buildDamageWorld(t)
	defer cancel()
	breakWell(t, w, "well-a")
	placeAt(t, w, "anne", "well-a")
	mustSend(t, w, func(world *sim.World) { world.Environment.TownChest = 61 }) // < 12 + 50
	if _, err := w.Send(sim.StartRepair("anne")); err == nil || !strings.Contains(err.Error(), "chest") {
		t.Errorf("StartRepair err = %v, want the empty-chest refusal", err)
	}
}

// TestPublicWorksNoticesPinToTheBoard — a break posts two pinned lines on the
// board (a real 2-slip frame; there is no 1-slip art), and a repair takes them
// down again.
func TestPublicWorksNoticesPinToTheBoard(t *testing.T) {
	w, cancel := buildDamageWorld(t)
	defer cancel()
	breakWell(t, w, "well-a")
	mustSend(t, w, func(world *sim.World) {
		c := world.NoticeboardContent["board"]
		if c == nil || c.Pinned != 2 {
			t.Fatalf("board content = %+v, want 2 pinned lines", c)
		}
		if world.VillageObjects["board"].CurrentState != "two" {
			t.Errorf("board state = %q, want the 2-slip frame", world.VillageObjects["board"].CurrentState)
		}
		if !strings.Contains(c.Text, "windlass") || !strings.Contains(c.Text, "12 coins") {
			t.Errorf("board text = %q", c.Text)
		}
	})
	if _, err := w.Send(sim.SetObjectDamage("well-a", "repair")); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		if c := world.NoticeboardContent["board"]; c != nil {
			t.Errorf("board still carries %q after the repair", c.Text)
		}
		if world.VillageObjects["board"].CurrentState != "empty" {
			t.Errorf("board state = %q, want empty", world.VillageObjects["board"].CurrentState)
		}
	})
}

// TestPublicWorksTakesPrecedenceOverARemoteStall — a hand who owns a worn stall
// elsewhere, standing at the broken well, mends the WELL (the cue offers it on
// the same terms); the stall path is only for standing at the stall.
func TestPublicWorksTakesPrecedenceOverARemoteStall(t *testing.T) {
	w, cancel := buildDamageWorld(t)
	defer cancel()
	breakWell(t, w, "well-a")
	mustSend(t, w, func(world *sim.World) {
		world.VillageObjects["anne-stall"] = &sim.VillageObject{
			ID: "anne-stall", DisplayName: "Anne's Stall", OwnerActorID: "anne",
			Tags: []string{sim.TagBusiness}, Wear: 999, Pos: sim.WorldPos{X: 6000, Y: 6000},
		}
	})
	placeAt(t, w, "anne", "well-a")
	if _, err := w.Send(sim.StartRepair("anne")); err != nil {
		t.Fatalf("StartRepair at the well with a stall elsewhere: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		if act := world.Actors["anne"].SourceActivity; act == nil || act.ObjectID != "well-a" {
			t.Errorf("activity = %+v, want a repair at well-a", act)
		}
	})
}

// TestPublicWorksBountyFixedAtStart — the bounty agreed when the work began is
// what the hand is paid, whatever the setting says by the time it lands.
func TestPublicWorksBountyFixedAtStart(t *testing.T) {
	w, cancel := buildDamageWorld(t)
	defer cancel()
	breakWell(t, w, "well-a")
	placeAt(t, w, "anne", "well-a")
	if _, err := w.Send(sim.StartRepair("anne")); err != nil {
		t.Fatalf("StartRepair: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		world.Settings.PublicWorksBounty = 30
		world.Actors["anne"].SourceActivity.Until = time.Now().UTC().Add(-time.Second)
	})
	mustSend(t, w, func(world *sim.World) { sim.CompleteDueSourceActivities(world, time.Now().UTC()) })
	mustSend(t, w, func(world *sim.World) {
		if got := world.Actors["anne"].Coins; got != 12 {
			t.Errorf("anne coins = %d, want the 12 agreed at start", got)
		}
	})
}

// TestPublicWorksNoticesFollowTheChest — with a well still broken, the board's
// bounty line follows the chest across the pay line in both directions: the
// constable's wage takes it under (the "other well" line), an estate collection
// or a retune back over.
func TestPublicWorksNoticesFollowTheChest(t *testing.T) {
	w, cancel := buildDamageWorld(t)
	defer cancel()
	breakWell(t, w, "well-a")
	boardText := func() string {
		var text string
		mustSend(t, w, func(world *sim.World) {
			if c := world.NoticeboardContent["board"]; c != nil {
				text = c.Text
			}
		})
		return text
	}
	if !strings.Contains(boardText(), "12 coins") {
		t.Fatalf("board before = %q, want the bounty line", boardText())
	}
	mustSend(t, w, func(world *sim.World) { world.Settings.ConstableWagePerDay = 45 }) // 100 → 55, under 12+50
	if _, err := w.Send(sim.ApplyConstableWage(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if got := boardText(); !strings.Contains(got, "other well") || strings.Contains(got, "12 coins") {
		t.Errorf("board after the wage = %q, want the other-well line", got)
	}
	mustSend(t, w, func(world *sim.World) {
		world.Settings.PublicWorksChestReserve = 0 // 55 >= 12 + 0: the town can pay again
		sim.SyncPublicWorksNews(world, time.Now().UTC())
	})
	if got := boardText(); !strings.Contains(got, "12 coins") {
		t.Errorf("board after the retune = %q, want the bounty line back", got)
	}
}

// TestSetObjectDamageRejectsNonWells — the operator control refuses anything
// but a well, for both actions.
func TestSetObjectDamageRejectsNonWells(t *testing.T) {
	w, cancel := buildDamageWorld(t)
	defer cancel()
	for _, action := range []string{"damage", "repair"} {
		if _, err := w.Send(sim.SetObjectDamage("board", action)); !errors.Is(err, sim.ErrNotDamageable) {
			t.Errorf("%s on a board: err = %v, want ErrNotDamageable", action, err)
		}
	}
}

// TestStartRepairAwayFromEverything — no stall of one's own and no broken well
// underfoot: StartRepair refuses cleanly (the site lookup is nil-safe).
func TestStartRepairAwayFromEverything(t *testing.T) {
	w, cancel := buildDamageWorld(t)
	defer cancel()
	breakWell(t, w, "well-a")
	mustSend(t, w, func(world *sim.World) { world.Actors["anne"].Pos = sim.WorldPos{X: 9000, Y: 9000}.Tile() })
	if _, err := w.Send(sim.StartRepair("anne")); err == nil {
		t.Error("StartRepair away from any stall or well should refuse")
	}
}

// TestPublicWorksCompletionPaysOnlyForAWell — a due repair window whose target
// is a damaged NON-well (drifted data) lands no town repair and no bounty.
func TestPublicWorksCompletionPaysOnlyForAWell(t *testing.T) {
	w, cancel := buildDamageWorld(t)
	defer cancel()
	mustSend(t, w, func(world *sim.World) {
		world.VillageObjects["board"].DamagedAt = time.Now().UTC()
		world.Actors["anne"].SourceActivity = &sim.SourceActivity{
			Kind: sim.SourceActivityRepair, ObjectID: "board", Bounty: 12,
			StartedAt: time.Now().UTC().Add(-time.Hour), Until: time.Now().UTC().Add(-time.Second),
		}
	})
	mustSend(t, w, func(world *sim.World) { sim.CompleteDueSourceActivities(world, time.Now().UTC()) })
	mustSend(t, w, func(world *sim.World) {
		if got := world.Actors["anne"].Coins; got != 0 {
			t.Errorf("paid %d for a non-well", got)
		}
		if got := world.Environment.TownChest; got != 100 {
			t.Errorf("chest = %d, want the untouched 100", got)
		}
		if !world.VillageObjects["board"].Damaged() {
			t.Error("a non-well was mended by the town's repair")
		}
	})
}
