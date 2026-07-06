package sim_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// source_activity_test.go — LLM-54. The timed eat/drink/harvest substrate.
// Reuses buildGatherTestWorld / placeAt / inventoryOf / setObjectOwner from
// gather_commands_test.go (same package): the well (thirst, infinite), bush
// (hunger -8 + gatherable berries, finite 2/4), dry_bush (depleted), sell_bush
// (yield-only forage-to-sell), oak (hunger, no gather), bench (decorative).

// setNeed stamps actorID's need value on the live world.
func setNeed(t *testing.T, w *sim.World, actorID sim.ActorID, need sim.NeedKey, val int) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[actorID]
		if a.Needs == nil {
			a.Needs = map[sim.NeedKey]int{}
		}
		a.Needs[need] = val
		return nil, nil
	}}); err != nil {
		t.Fatalf("setNeed: %v", err)
	}
}

// needOf reads actorID's need value off the live world.
func needOf(t *testing.T, w *sim.World, actorID sim.ActorID, need sim.NeedKey) int {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[actorID].Needs[need], nil
	}})
	if err != nil {
		t.Fatalf("needOf: %v", err)
	}
	return res.(int)
}

// availOf reads objID's first refresh row's AvailableQuantity (the fixtures put
// the supply on row 0), or -1 for an infinite source.
func availOf(t *testing.T, w *sim.World, objID sim.VillageObjectID) int {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		r := world.VillageObjects[objID].Refreshes[0]
		if r.AvailableQuantity == nil {
			return -1, nil
		}
		return *r.AvailableQuantity, nil
	}})
	if err != nil {
		t.Fatalf("availOf: %v", err)
	}
	return res.(int)
}

// liveActivity reads actorID's in-flight SourceActivity off the world goroutine
// (it is deliberately not on the published snapshot). Returns nil when idle.
func liveActivity(t *testing.T, w *sim.World, actorID sim.ActorID) *sim.SourceActivity {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[actorID].SourceActivity, nil
	}})
	if err != nil {
		t.Fatalf("liveActivity: %v", err)
	}
	sa, _ := res.(*sim.SourceActivity)
	return sa
}

// forceComplete runs the completion sweep with now set an hour ahead, so every
// in-flight activity is past its Until and lands. Returns the count completed.
func forceComplete(t *testing.T, w *sim.World) int {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.CompleteDueSourceActivities(world, time.Now().UTC().Add(time.Hour)), nil
	}})
	if err != nil {
		t.Fatalf("forceComplete: %v", err)
	}
	return res.(int)
}

// teleport moves the actor to an arbitrary far tile WITHOUT going through
// MoveActor (so it does not clear an in-flight activity) — used to exercise the
// completion-time re-resolve guard.
func teleport(t *testing.T, w *sim.World, actorID sim.ActorID, x, y int) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[actorID].Pos = sim.TilePos{X: x, Y: y}
		return nil, nil
	}}); err != nil {
		t.Fatalf("teleport: %v", err)
	}
}

// TestStartRefresh_DefersThenCompletes: a PC arriving at an edible bush STARTS a
// timed eat — the hunger drop and supply decrement do not land until the
// completion sweep, and then exactly once. Hunger 8 here is fully sated by the
// single -8 bite, so the LLM-55 auto-repeat does NOT re-arm (covered separately
// in TestRefresh_AutoRepeatsUntilFullOrEmpty). The actor is a PC because
// eat-on-arrival at a bush is PC-only since LLM-87 (NPCs gather->consume).
func TestStartRefresh_DefersThenCompletes(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindPC) // eat-on-arrival at a bush is PC-only (LLM-87)
	setNeed(t, w, "hannah", "hunger", 8)
	placeAt(t, w, "hannah", "bush")

	res, err := w.Send(sim.StartRefreshAtArrival("hannah"))
	if err != nil {
		t.Fatalf("StartRefreshAtArrival: %v", err)
	}
	sr := res.(sim.SourceActivityStartResult)
	if !sr.Started || sr.Kind != sim.SourceActivityRefresh || sr.ObjectID != "bush" {
		t.Fatalf("start result = %+v, want Started refresh @ bush", sr)
	}
	// Deferred: nothing applied yet.
	if got := needOf(t, w, "hannah", "hunger"); got != 8 {
		t.Errorf("hunger = %d, want 8 (effect deferred, not applied at start)", got)
	}
	if got := availOf(t, w, "bush"); got != 2 {
		t.Errorf("supply = %d, want 2 (not decremented at start)", got)
	}

	if n := forceComplete(t, w); n != 1 {
		t.Fatalf("completed = %d, want 1", n)
	}
	if got := needOf(t, w, "hannah", "hunger"); got != 0 { // 8 - 8, fully sated
		t.Errorf("hunger = %d, want 0 (eased at completion)", got)
	}
	if got := availOf(t, w, "bush"); got != 1 {
		t.Errorf("supply = %d, want 1 (decremented at completion)", got)
	}
	if sa := liveActivity(t, w, "hannah"); sa != nil {
		t.Errorf("activity = %+v, want nil (sated, no auto-repeat)", sa)
	}
	// No double-application: a second sweep finds nothing and changes nothing.
	if n := forceComplete(t, w); n != 0 {
		t.Errorf("second sweep completed = %d, want 0", n)
	}
	if got := needOf(t, w, "hannah", "hunger"); got != 0 {
		t.Errorf("hunger = %d after second sweep, want 0 (no double-apply)", got)
	}
}

// backdateActivity forces the actor's in-flight window to read as expired
// (Until an hour ago) without going through the sweep — to exercise the START
// gates' self-heal of a finished-but-not-yet-swept activity.
func backdateActivity(t *testing.T, w *sim.World, actorID sim.ActorID) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[actorID].SourceActivity.Until = time.Now().UTC().Add(-time.Hour)
		return nil, nil
	}}); err != nil {
		t.Fatalf("backdateActivity: %v", err)
	}
}

// TestStartRefresh_SelfHealsStaleWindow: a window that has expired but the ~1s
// sweep hasn't yet landed must NOT permanently block. The next start lands the
// stale bite (eases hunger, draws the bush down); since the eater is still
// hungry with stock left, that completion auto-re-arms (LLM-55), so the eat
// continues rather than the stale window reading as "still busy" forever. The
// actor is a PC because auto-graze is PC-only since LLM-87 (the re-arm under test).
func TestStartRefresh_SelfHealsStaleWindow(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindPC) // auto-graze is PC-only (LLM-87)
	setNeed(t, w, "hannah", "hunger", 14)
	placeAt(t, w, "hannah", "bush")

	if _, err := w.Send(sim.StartRefreshAtArrival("hannah")); err != nil {
		t.Fatalf("first StartRefreshAtArrival: %v", err)
	}
	backdateActivity(t, w, "hannah") // expired, not yet swept

	res, err := w.Send(sim.StartRefreshAtArrival("hannah"))
	if err != nil {
		t.Fatalf("second StartRefreshAtArrival: %v", err)
	}
	// The stale bite landed during self-heal: hunger eased, the bush drawn down.
	if got := needOf(t, w, "hannah", "hunger"); got != 6 {
		t.Errorf("hunger = %d, want 6 (the stale bite landed during self-heal)", got)
	}
	if got := availOf(t, w, "bush"); got != 1 {
		t.Errorf("supply = %d, want 1 (decremented by the landed stale bite)", got)
	}
	// Still hungry with stock left, so the self-heal's completion auto-re-armed
	// (LLM-55): the explicit start correctly reads "busy" rather than stacking a
	// second window, and the eat carries on.
	if sr := res.(sim.SourceActivityStartResult); sr.Started {
		t.Error("start stacked a second window on top of the auto-repeat one")
	}
	if sa := liveActivity(t, w, "hannah"); sa == nil {
		t.Error("no activity after self-heal — the eat should continue via auto-repeat")
	}
}

// TestStartRefresh_OwnedByOther_NoStart: an owned source is owner-only (LLM-50
// D2) — a non-owner's arrival starts nothing.
func TestStartRefresh_OwnedByOther_NoStart(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setObjectOwner(t, w, "bush", "prudence")
	setNeed(t, w, "hannah", "hunger", 14)
	placeAt(t, w, "hannah", "bush")

	res, err := w.Send(sim.StartRefreshAtArrival("hannah"))
	if err != nil {
		t.Fatalf("StartRefreshAtArrival: %v", err)
	}
	if sr := res.(sim.SourceActivityStartResult); sr.Started {
		t.Errorf("started at an owned source, want no start")
	}
	if sa := liveActivity(t, w, "hannah"); sa != nil {
		t.Errorf("activity = %+v, want nil", sa)
	}
}

// TestStartRefresh_YieldOnly_NoStart: a yield-only forage-to-sell bush (amount=0)
// has nothing to eat in place — arrival starts no refresh activity.
func TestStartRefresh_YieldOnly_NoStart(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindPC) // PC, so the yield-only gate is the reason — not the LLM-87 NPC-at-bush suppression
	setNeed(t, w, "hannah", "hunger", 14)
	placeAt(t, w, "hannah", "sell_bush")

	res, _ := w.Send(sim.StartRefreshAtArrival("hannah"))
	if sr := res.(sim.SourceActivityStartResult); sr.Started {
		t.Errorf("started at a yield-only bush, want no start")
	}
}

// TestStartRefresh_Depleted_NoStart: a finite source with no stock left has no
// applicable refresh row — no start.
func TestStartRefresh_Depleted_NoStart(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindPC) // PC, so the depleted gate is the reason — not the LLM-87 NPC-at-bush suppression
	setNeed(t, w, "hannah", "hunger", 14)
	placeAt(t, w, "hannah", "dry_bush")

	res, _ := w.Send(sim.StartRefreshAtArrival("hannah"))
	if sr := res.(sim.SourceActivityStartResult); sr.Started {
		t.Errorf("started at a depleted bush, want no start")
	}
}

// TestStartHarvest_DefersThenMints: the gather verb now STARTS a timed pick —
// the inventory credit and supply decrement land at completion, not at start.
func TestStartHarvest_DefersThenMints(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "bush")

	res, err := w.Send(sim.StartHarvest("hannah", 2))
	if err != nil {
		t.Fatalf("StartHarvest: %v", err)
	}
	sr := res.(sim.SourceActivityStartResult)
	if !sr.Started || sr.Kind != sim.SourceActivityHarvest || sr.ObjectID != "bush" {
		t.Fatalf("start result = %+v, want Started harvest @ bush", sr)
	}
	if sr.SourceName != "Berry Bush" {
		t.Errorf("SourceName = %q, want Berry Bush", sr.SourceName)
	}
	// Deferred: nothing minted yet.
	if got := inventoryOf(t, w, "hannah", "berries"); got != 0 {
		t.Errorf("berries = %d, want 0 (mint deferred)", got)
	}
	if got := availOf(t, w, "bush"); got != 2 {
		t.Errorf("supply = %d, want 2 (not drawn down at start)", got)
	}

	if n := forceComplete(t, w); n != 1 {
		t.Fatalf("completed = %d, want 1", n)
	}
	if got := inventoryOf(t, w, "hannah", "berries"); got != 2 {
		t.Errorf("berries = %d, want 2 (minted at completion)", got)
	}
	if got := availOf(t, w, "bush"); got != 0 {
		t.Errorf("supply = %d, want 0 (drawn down at completion)", got)
	}
}

// TestGather_PicksSourceClean (LLM-87): a gather takes ALL ripe units in one go,
// ignoring the requested qty. A bush with 2 ripe, gathered with qty=1, mints 2 and
// leaves the bush bare — so an NPC empties a bush in a single gather.
func TestGather_PicksSourceClean(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "bush")

	// Ask for 1, but the bush has 2 ripe — pick-clean takes both.
	if _, err := w.Send(sim.StartHarvest("hannah", 1)); err != nil {
		t.Fatalf("StartHarvest: %v", err)
	}
	if n := forceComplete(t, w); n != 1 {
		t.Fatalf("completed = %d, want 1", n)
	}
	if got := inventoryOf(t, w, "hannah", "berries"); got != 2 {
		t.Errorf("berries = %d, want 2 (qty=1 ignored; the bush was picked clean)", got)
	}
	if got := availOf(t, w, "bush"); got != 0 {
		t.Errorf("supply = %d, want 0 (bush picked clean)", got)
	}
}

// TestStartHarvest_OwnedByOther_Rejects: harvesting an owned source is refused
// up front (LLM-50 D2), the same ErrNotYourSource the instant Gather raised.
func TestStartHarvest_OwnedByOther_Rejects(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setObjectOwner(t, w, "bush", "prudence")
	placeAt(t, w, "hannah", "bush")

	_, err := w.Send(sim.StartHarvest("hannah", 1))
	if !errors.Is(err, sim.ErrNotYourSource) {
		t.Errorf("err = %v, want ErrNotYourSource", err)
	}
	if sa := liveActivity(t, w, "hannah"); sa != nil {
		t.Errorf("activity = %+v, want nil (rejected, none started)", sa)
	}
}

// TestStartHarvest_Depleted_Rejects: a finite source with no stock rejects at
// start rather than starting a pick that would yield nothing.
func TestStartHarvest_Depleted_Rejects(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "dry_bush")

	_, err := w.Send(sim.StartHarvest("hannah", 1))
	if !errors.Is(err, sim.ErrGatherableDepleted) {
		t.Errorf("err = %v, want ErrGatherableDepleted", err)
	}
}

// TestStartHarvest_AlreadyBusy_Rejects: an actor already mid-activity can't
// start another at the same source. The busy state is set up with a first gather
// (a timed harvest window) — eat-on-arrival no longer occupies an NPC at a bush.
func TestStartHarvest_AlreadyBusy_Rejects(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "bush")

	if _, err := w.Send(sim.StartHarvest("hannah", 1)); err != nil {
		t.Fatalf("first StartHarvest: %v", err)
	}
	// A second gather while that window is still in flight is rejected.
	_, err := w.Send(sim.StartHarvest("hannah", 1))
	if err == nil {
		t.Fatal("StartHarvest while busy returned nil, want a busy error")
	}
}

// TestComplete_MovedAway_NoEffect: the completion-time re-resolve guard — if the
// actor is no longer at the object it began at, the effect is skipped (a
// deliberate move already clears the activity; this is the defensive backstop).
func TestComplete_MovedAway_NoEffect(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindPC) // eat-on-arrival at a bush is PC-only (LLM-87)
	setNeed(t, w, "hannah", "hunger", 14)
	placeAt(t, w, "hannah", "bush")

	if _, err := w.Send(sim.StartRefreshAtArrival("hannah")); err != nil {
		t.Fatalf("StartRefreshAtArrival: %v", err)
	}
	teleport(t, w, "hannah", 9000, 9000) // far from the bush, activity left in place

	if n := forceComplete(t, w); n != 1 { // it IS due, so it's swept + cleared
		t.Fatalf("completed = %d, want 1 (swept)", n)
	}
	if got := needOf(t, w, "hannah", "hunger"); got != 14 {
		t.Errorf("hunger = %d, want 14 (no effect — actor walked off)", got)
	}
	if got := availOf(t, w, "bush"); got != 2 {
		t.Errorf("supply = %d, want 2 (no decrement — actor walked off)", got)
	}
}

// TestBusyAtSource_TracksWindow: the helper the reactor/move gates read is true
// while the window is open and false once it's past.
func TestBusyAtSource_TracksWindow(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindPC) // eat-on-arrival at a bush is PC-only (LLM-87)
	setNeed(t, w, "hannah", "hunger", 14)
	placeAt(t, w, "hannah", "bush")
	if _, err := w.Send(sim.StartRefreshAtArrival("hannah")); err != nil {
		t.Fatalf("StartRefreshAtArrival: %v", err)
	}

	res, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["hannah"]
		now := time.Now().UTC()
		return []bool{a.BusyAtSource(now), a.BusyAtSource(now.Add(time.Hour))}, nil
	}})
	got := res.([]bool)
	if !got[0] {
		t.Error("BusyAtSource(now) = false, want true while the window is open")
	}
	if got[1] {
		t.Error("BusyAtSource(now+1h) = true, want false past the window")
	}
}

// TestRefresh_AutoRepeatsUntilFullOrEmpty (LLM-55; PC-only since LLM-87): the PC
// standing at a finite bush eats berry-by-berry — each completion re-arms a fresh
// window while still hungry and stock remains, and stops once sated (or empty).
// Hunger 14 against a 2-berry bush (−8 each) takes exactly two bites: 14→6
// (re-armed), then 6→0 (sated, not re-armed); supply 2→1→0. NPCs are NOT
// auto-re-armed — see TestRefresh_NPCEatsOneBiteNoAutoRepeat.
func TestRefresh_AutoRepeatsUntilFullOrEmpty(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindPC) // auto-graze is PC-only (LLM-87)
	setNeed(t, w, "hannah", "hunger", 14)
	placeAt(t, w, "hannah", "bush")

	if _, err := w.Send(sim.StartRefreshAtArrival("hannah")); err != nil {
		t.Fatalf("StartRefreshAtArrival: %v", err)
	}

	// Bite 1: eases hunger, draws the bush down, and RE-ARMS (still hungry + stock).
	if n := forceComplete(t, w); n != 1 {
		t.Fatalf("first completion = %d, want 1", n)
	}
	if got := needOf(t, w, "hannah", "hunger"); got != 6 {
		t.Errorf("hunger after bite 1 = %d, want 6", got)
	}
	if got := availOf(t, w, "bush"); got != 1 {
		t.Errorf("supply after bite 1 = %d, want 1", got)
	}
	if sa := liveActivity(t, w, "hannah"); sa == nil || sa.Kind != sim.SourceActivityRefresh {
		t.Fatalf("activity after bite 1 = %+v, want a re-armed refresh window", sa)
	}

	// Bite 2: sates (6−8 clamps to 0) and empties the bush — no re-arm. (The
	// re-armed window's Until was stamped from forceComplete's +1h clock, so
	// backdate it to be due for the next sweep — the production ticker just waits.)
	backdateActivity(t, w, "hannah")
	if n := forceComplete(t, w); n != 1 {
		t.Fatalf("second completion = %d, want 1", n)
	}
	if got := needOf(t, w, "hannah", "hunger"); got != 0 {
		t.Errorf("hunger after bite 2 = %d, want 0 (sated)", got)
	}
	if got := availOf(t, w, "bush"); got != 0 {
		t.Errorf("supply after bite 2 = %d, want 0 (picked clean)", got)
	}
	if sa := liveActivity(t, w, "hannah"); sa != nil {
		t.Errorf("activity after bite 2 = %+v, want nil (sated + empty, no re-arm)", sa)
	}
}

// TestRefresh_NPCNoEatOnArrivalAtBush (LLM-87): an NPC does NOT auto-eat on
// arrival at a bush (a finite gatherable source) — it eats via gather->consume
// instead. StartRefreshAtArrival no-ops for it, applying nothing, even though the
// bush is edible and the NPC is hungry.
func TestRefresh_NPCNoEatOnArrivalAtBush(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindNPCStateful)
	setNeed(t, w, "hannah", "hunger", 14)
	placeAt(t, w, "hannah", "bush")

	res, err := w.Send(sim.StartRefreshAtArrival("hannah"))
	if err != nil {
		t.Fatalf("StartRefreshAtArrival: %v", err)
	}
	if sr := res.(sim.SourceActivityStartResult); sr.Started {
		t.Error("NPC auto-started an eat at a bush — it should gather->consume, not eat on arrival")
	}
	if sa := liveActivity(t, w, "hannah"); sa != nil {
		t.Errorf("activity = %+v, want nil (NPC does not eat-on-arrival at a bush)", sa)
	}
	if got := needOf(t, w, "hannah", "hunger"); got != 14 {
		t.Errorf("hunger = %d, want 14 (no bite landed)", got)
	}
	if got := availOf(t, w, "bush"); got != 2 {
		t.Errorf("supply = %d, want 2 (bush untouched)", got)
	}
}

// TestRefresh_NPCDrinksAtWellOnArrival (LLM-87): a well is gatherable but INFINITE
// — not a bush — so the NPC-at-bush eat-on-arrival suppression does NOT apply. An
// NPC still drinks on arrival at a well, preserving its arrival + dwell drink path.
func TestRefresh_NPCDrinksAtWellOnArrival(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindNPCStateful)
	setNeed(t, w, "hannah", "thirst", 14)
	placeAt(t, w, "hannah", "well")

	res, err := w.Send(sim.StartRefreshAtArrival("hannah"))
	if err != nil {
		t.Fatalf("StartRefreshAtArrival: %v", err)
	}
	if sr := res.(sim.SourceActivityStartResult); !sr.Started {
		t.Error("NPC did not start drinking at a well on arrival — a well is not a bush, so eat-on-arrival should fire")
	}
}

// seedTwoRowWell plants the LIVE post-LLM-254 Well shape at a tile clear of
// every fixture object (the bench sits at 2000,2000): the infinite in-place
// drink row NEXT TO a finite yield-only water-pail forage row with the given
// stock. The pail row makes the object an IsFiniteGatherableSource — the shape
// that neutered the NPC drink paths until LLM-288 made the carve-out row-aware.
func seedTwoRowWell(t *testing.T, w *sim.World, pailStock int) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		zero := 0
		ip := func(v int) *int { return &v }
		world.VillageObjects["town_well"] = &sim.VillageObject{
			ID: "town_well", DisplayName: "Town Well", AssetID: "well-stone", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 3000, Y: 3000},
			Refreshes: []*sim.ObjectRefresh{
				{Attribute: "thirst", Amount: -8, DwellDelta: ip(-1), DwellPeriodMinutes: ip(2)},
				{
					Amount:             0, // yield-only water-pail forage row (LLM-254)
					AvailableQuantity:  ip(pailStock),
					MaxQuantity:        ip(20),
					RefreshMode:        sim.RefreshModePeriodic,
					RefreshPeriodHours: ip(6),
					GatherItem:         "water",
				},
			},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed town_well: %v", err)
	}
}

// TestRefresh_NPCDrinksAtTwoRowWellOnArrival (LLM-288): the LLM-87 NPC-at-bush
// suppression must stay row-aware — the infinite in-place drink row keeps the
// arrival drink alive next to the finite pail row (the hud-843da92a "parched
// at the Well forever" regression). Both pail states: dry (0, as observed
// live) and full (20) — the bug was classification-based, not stock-based, so
// a full pail must not block the drink either. The full-pail case also proves
// the drink can't draw the pail down: the completed drink leaves the water
// stock untouched.
func TestRefresh_NPCDrinksAtTwoRowWellOnArrival(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pailStock int
	}{
		{"dry_pail", 0},
		{"full_pail", 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, cancel := buildGatherTestWorld(t)
			defer cancel()
			seedTwoRowWell(t, w, tc.pailStock)
			setActorKind(t, w, "hannah", sim.KindNPCStateful)
			setNeed(t, w, "hannah", "thirst", 14)
			placeAt(t, w, "hannah", "town_well")

			res, err := w.Send(sim.StartRefreshAtArrival("hannah"))
			if err != nil {
				t.Fatalf("StartRefreshAtArrival: %v", err)
			}
			if sr := res.(sim.SourceActivityStartResult); !sr.Started {
				t.Fatal("NPC did not start drinking at the two-row well — the finite pail row must not suppress the infinite drink row (LLM-288)")
			}
			if n := forceComplete(t, w); n != 1 {
				t.Fatalf("completion = %d, want 1", n)
			}
			if got := needOf(t, w, "hannah", "thirst"); got != 6 { // 14 - 8
				t.Errorf("thirst = %d, want 6 (drink applied)", got)
			}
			// The drink must ride the infinite row only — the pail stock
			// (Refreshes[1]) stays exactly where it was seeded.
			res, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
				return *world.VillageObjects["town_well"].Refreshes[1].AvailableQuantity, nil
			}})
			if err != nil {
				t.Fatalf("read pail stock: %v", err)
			}
			if got := res.(int); got != tc.pailStock {
				t.Errorf("pail stock = %d, want %d (a drink must not draw the pail)", got, tc.pailStock)
			}
		})
	}
}

// seedMixedSource plants the latent MIXED shape the LLM-288 review flagged:
// an infinite in-place thirst row (a drink) NEXT TO a finite gatherable
// eat+pick hunger row (a bush: Amount < 0 AND GatherItem). No such object
// exists live, but it is one set-refresh call away — the row-level NPC filter
// in applyObjectRefreshEffect is what keeps the bush row safe here.
func seedMixedSource(t *testing.T, w *sim.World) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		zero := 0
		ip := func(v int) *int { return &v }
		world.VillageObjects["oasis"] = &sim.VillageObject{
			ID: "oasis", DisplayName: "Oasis", AssetID: "well-stone", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 3500, Y: 3500},
			Refreshes: []*sim.ObjectRefresh{
				{Attribute: "thirst", Amount: -8},
				{
					Attribute:          "hunger",
					Amount:             -8, // eat+pick: a REAL bush row, not yield-only
					AvailableQuantity:  ip(2),
					MaxQuantity:        ip(4),
					RefreshMode:        sim.RefreshModeContinuous,
					RefreshPeriodHours: ip(6),
					GatherItem:         "berries",
				},
			},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed oasis: %v", err)
	}
}

// TestRefresh_NPCMixedSourceDrinksButNeverAutoEats (LLM-288): at a mixed
// source an NPC's arrival refresh still fires (the drink row qualifies), but
// the bush row must NOT ride along — hunger stays put and the bush stock is
// untouched; the NPC eats that row via gather->consume (LLM-87). This is the
// case a plain object-level carve-out cannot get right in both directions.
func TestRefresh_NPCMixedSourceDrinksButNeverAutoEats(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	seedMixedSource(t, w)
	setActorKind(t, w, "hannah", sim.KindNPCStateful)
	setNeed(t, w, "hannah", "thirst", 14)
	setNeed(t, w, "hannah", "hunger", 14)
	placeAt(t, w, "hannah", "oasis")

	res, err := w.Send(sim.StartRefreshAtArrival("hannah"))
	if err != nil {
		t.Fatalf("StartRefreshAtArrival: %v", err)
	}
	if sr := res.(sim.SourceActivityStartResult); !sr.Started {
		t.Fatal("NPC did not start the in-place refresh at a mixed source — the drink row must keep arrival alive")
	}
	if n := forceComplete(t, w); n != 1 {
		t.Fatalf("completion = %d, want 1", n)
	}
	if got := needOf(t, w, "hannah", "thirst"); got != 6 { // 14 - 8
		t.Errorf("thirst = %d, want 6 (drink applied)", got)
	}
	if got := needOf(t, w, "hannah", "hunger"); got != 14 {
		t.Errorf("hunger = %d, want 14 (bush row must NOT auto-apply for an NPC)", got)
	}
	res, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return *world.VillageObjects["oasis"].Refreshes[1].AvailableQuantity, nil
	}})
	if err != nil {
		t.Fatalf("read bush stock: %v", err)
	}
	if got := res.(int); got != 2 {
		t.Errorf("bush stock = %d, want 2 (untouched)", got)
	}
}

// TestRefresh_InfiniteSource_NoAutoRepeat (LLM-55): an INFINITE source (the well)
// is never auto-repeated — it drinks once on arrival and stops, keeping its
// arrival + dwell behavior, even though the actor is still thirsty afterward. The
// actor is a PC so the only thing suppressing the re-arm is the infinite source,
// not the LLM-87 NPC gate.
func TestRefresh_InfiniteSource_NoAutoRepeat(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindPC) // isolate infinite-source as the no-repeat reason (LLM-87)
	setNeed(t, w, "hannah", "thirst", 14)
	placeAt(t, w, "hannah", "well")

	if _, err := w.Send(sim.StartRefreshAtArrival("hannah")); err != nil {
		t.Fatalf("StartRefreshAtArrival: %v", err)
	}
	if n := forceComplete(t, w); n != 1 {
		t.Fatalf("completion = %d, want 1", n)
	}
	if got := needOf(t, w, "hannah", "thirst"); got != 2 { // 14 - 12
		t.Errorf("thirst = %d, want 2 (one drink applied)", got)
	}
	if sa := liveActivity(t, w, "hannah"); sa != nil {
		t.Errorf("activity = %+v, want nil (infinite source not auto-repeated, despite still being thirsty)", sa)
	}
}

// TestRefresh_EmptyStopsEvenWhileHungry (LLM-55; PC-only since LLM-87):
// termination is "sated OR empty", not "sated" — a bush that runs out stops the
// PC's auto-graze even while still hungry. Hunger 24 against a 2-berry bush (−8
// each) empties it after two bites (24→16→8, supply 2→1→0); the eater is still
// hungry (8 > 0) but the loop ends because there's no stock left.
func TestRefresh_EmptyStopsEvenWhileHungry(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindPC) // auto-graze is PC-only (LLM-87)
	setNeed(t, w, "hannah", "hunger", 24)    // more than two berries can clear
	placeAt(t, w, "hannah", "bush")

	if _, err := w.Send(sim.StartRefreshAtArrival("hannah")); err != nil {
		t.Fatalf("StartRefreshAtArrival: %v", err)
	}
	// Bite 1: 24→16, supply 2→1, re-armed (still hungry + stock).
	if n := forceComplete(t, w); n != 1 {
		t.Fatalf("bite 1 = %d, want 1", n)
	}
	backdateActivity(t, w, "hannah")
	// Bite 2: 16→8, supply 1→0 — bush empty, so NO re-arm despite hunger 8 > 0.
	if n := forceComplete(t, w); n != 1 {
		t.Fatalf("bite 2 = %d, want 1", n)
	}
	if got := needOf(t, w, "hannah", "hunger"); got != 8 {
		t.Errorf("hunger = %d, want 8 (still hungry after the bush emptied)", got)
	}
	if got := availOf(t, w, "bush"); got != 0 {
		t.Errorf("supply = %d, want 0 (picked clean)", got)
	}
	if sa := liveActivity(t, w, "hannah"); sa != nil {
		t.Errorf("activity = %+v, want nil (empty stops the loop even while hungry)", sa)
	}
}

// pcNeedsRecorder captures PCNeedsChanged events for the LLM-56 emit tests.
type pcNeedsRecorder struct {
	mu     sync.Mutex
	events []*sim.PCNeedsChanged
}

func (r *pcNeedsRecorder) record(_ *sim.World, evt sim.Event) {
	if e, ok := evt.(*sim.PCNeedsChanged); ok {
		r.mu.Lock()
		r.events = append(r.events, e)
		r.mu.Unlock()
	}
}

func (r *pcNeedsRecorder) snapshot() []*sim.PCNeedsChanged {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*sim.PCNeedsChanged, len(r.events))
	copy(out, r.events)
	return out
}

func subscribeNeedsRecorder(t *testing.T, w *sim.World) *pcNeedsRecorder {
	t.Helper()
	rec := &pcNeedsRecorder{}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(rec.record))
		return nil, nil
	}}); err != nil {
		t.Fatalf("subscribe recorder: %v", err)
	}
	return rec
}

func setActorKind(t *testing.T, w *sim.World, actorID sim.ActorID, kind sim.ActorKind) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[actorID]
		if a == nil {
			return nil, fmt.Errorf("setActorKind: actor %q not found", actorID)
		}
		a.Kind = kind
		return nil, nil
	}}); err != nil {
		t.Fatalf("setActorKind: %v", err)
	}
}

// TestRefresh_PCEmitsNeedsChanged (LLM-56): a PC eating emits PCNeedsChanged
// carrying its post-bite needs — the realtime HUD push the hub turns into a
// pc_needs_changed frame.
func TestRefresh_PCEmitsNeedsChanged(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindPC)
	setNeed(t, w, "hannah", "hunger", 14)
	placeAt(t, w, "hannah", "bush")
	rec := subscribeNeedsRecorder(t, w)

	if _, err := w.Send(sim.ApplyObjectRefreshAtArrival("hannah")); err != nil {
		t.Fatalf("ApplyObjectRefreshAtArrival: %v", err)
	}
	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("PCNeedsChanged count = %d, want 1", len(got))
	}
	if got[0].ActorID != "hannah" || got[0].Needs["hunger"] != 6 {
		t.Errorf("event = {%s, %v}, want hannah hunger 6", got[0].ActorID, got[0].Needs)
	}
	// Non-aliasing: mutating the actor's needs after the emit must not change the
	// captured snapshot — the event copies the map.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"].Needs["hunger"] = 99
		return nil, nil
	}}); err != nil {
		t.Fatalf("mutate actor need: %v", err)
	}
	if got[0].Needs["hunger"] != 6 {
		t.Errorf("event needs aliased actor needs: hunger = %d, want 6 (must be a copy)", got[0].Needs["hunger"])
	}
}

// TestRefresh_NPCDoesNotEmitNeedsChanged (LLM-56): an NPC eating does NOT emit
// the HUD push — NPC needs aren't client-rendered, so the wire stays quiet.
func TestRefresh_NPCDoesNotEmitNeedsChanged(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	// hannah stays an NPC (seeded with LLMAgent, default non-PC kind).
	setNeed(t, w, "hannah", "hunger", 14)
	placeAt(t, w, "hannah", "bush")
	rec := subscribeNeedsRecorder(t, w)

	if _, err := w.Send(sim.ApplyObjectRefreshAtArrival("hannah")); err != nil {
		t.Fatalf("ApplyObjectRefreshAtArrival: %v", err)
	}
	if got := rec.snapshot(); len(got) != 0 {
		t.Errorf("PCNeedsChanged count = %d, want 0 (NPC needs not pushed)", len(got))
	}
}
