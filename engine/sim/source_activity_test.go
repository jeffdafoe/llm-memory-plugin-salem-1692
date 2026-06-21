package sim_test

import (
	"errors"
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

// TestStartRefresh_DefersThenCompletes: arriving at an edible bush STARTS a
// timed eat — the hunger drop and supply decrement do not land until the
// completion sweep, and then exactly once.
func TestStartRefresh_DefersThenCompletes(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setNeed(t, w, "hannah", "hunger", 14)
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
	if got := needOf(t, w, "hannah", "hunger"); got != 14 {
		t.Errorf("hunger = %d, want 14 (effect deferred, not applied at start)", got)
	}
	if got := availOf(t, w, "bush"); got != 2 {
		t.Errorf("supply = %d, want 2 (not decremented at start)", got)
	}

	if n := forceComplete(t, w); n != 1 {
		t.Fatalf("completed = %d, want 1", n)
	}
	if got := needOf(t, w, "hannah", "hunger"); got != 6 { // 14 - 8
		t.Errorf("hunger = %d, want 6 (eased at completion)", got)
	}
	if got := availOf(t, w, "bush"); got != 1 {
		t.Errorf("supply = %d, want 1 (decremented at completion)", got)
	}
	if sa := liveActivity(t, w, "hannah"); sa != nil {
		t.Errorf("activity = %+v, want nil (cleared at completion)", sa)
	}
	// No double-application: a second sweep finds nothing and changes nothing.
	if n := forceComplete(t, w); n != 0 {
		t.Errorf("second sweep completed = %d, want 0", n)
	}
	if got := needOf(t, w, "hannah", "hunger"); got != 6 {
		t.Errorf("hunger = %d after second sweep, want 6 (no double-apply)", got)
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
// sweep hasn't yet landed must NOT block a fresh start. The next arrival lands
// the stale bite (eases hunger once, draws the bush down) and starts anew,
// instead of reading as "still busy" and no-op'ing.
func TestStartRefresh_SelfHealsStaleWindow(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
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
	if sr := res.(sim.SourceActivityStartResult); !sr.Started {
		t.Error("did not start a fresh window after self-healing the stale one")
	}
	if got := needOf(t, w, "hannah", "hunger"); got != 6 {
		t.Errorf("hunger = %d, want 6 (the stale bite landed during self-heal)", got)
	}
	if got := availOf(t, w, "bush"); got != 1 {
		t.Errorf("supply = %d, want 1 (decremented by the landed stale bite)", got)
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
// start another at the same source.
func TestStartHarvest_AlreadyBusy_Rejects(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setNeed(t, w, "hannah", "hunger", 14)
	placeAt(t, w, "hannah", "bush")

	if _, err := w.Send(sim.StartRefreshAtArrival("hannah")); err != nil {
		t.Fatalf("StartRefreshAtArrival: %v", err)
	}
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
