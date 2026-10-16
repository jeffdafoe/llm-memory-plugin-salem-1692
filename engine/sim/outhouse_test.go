package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// outhouse_test.go — LLM-811: the end of an outhouse stay eases the player's
// tiredness, once per player every OuthouseReliefCooldown. No real timer is
// armed (CaptureOuthouseTimersForTest); the stay's end is driven by calling
// its callback (FinishOuthouseStay) at chosen instants.

// outhouseWorld is a bare world with one PC at (10,10) and an outhouse whose
// loiter pin is beside them.
func outhouseWorld(t *testing.T, tiredness int) *sim.World {
	t.Helper()
	w := newBareWorld(t)
	sim.CaptureOuthouseTimersForTest(t)
	zero := 0
	asset := &sim.Asset{ID: "asset-outhouse", Name: "Outhouse 1", Category: "prop"}
	w.Assets[asset.ID] = asset
	w.VillageObjects["outhouse"] = &sim.VillageObject{
		ID: "outhouse", AssetID: asset.ID, Tags: []string{sim.TagOuthouse},
		Pos:           sim.WorldPos{X: float64(11-sim.PadX)*sim.TileSize + 1, Y: float64(10-sim.PadY)*sim.TileSize + 1},
		LoiterOffsetX: &zero, LoiterOffsetY: &zero,
	}
	w.Actors["pc"] = &sim.Actor{
		ID: "pc", Kind: sim.KindPC, State: sim.StateIdle, Pos: sim.TilePos{X: 10, Y: 10},
		Needs: map[sim.NeedKey]int{"tiredness": tiredness},
	}
	return w
}

// goIn starts a stay at now and returns when it ends.
func goIn(t *testing.T, w *sim.World, now time.Time) time.Time {
	t.Helper()
	res, err := sim.UseOuthouse("pc", "outhouse", now).Fn(w)
	if err != nil {
		t.Fatalf("UseOuthouse: %v", err)
	}
	return res.(sim.OuthouseResult).Until
}

func finish(t *testing.T, w *sim.World, until, now time.Time) bool {
	t.Helper()
	moved, err := sim.FinishOuthouseStay("outhouse", "pc", until, now).Fn(w)
	if err != nil {
		t.Fatalf("FinishOuthouseStay: %v", err)
	}
	return moved.(bool)
}

func TestOuthouse_StayEndEasesTirednessOncePerCooldown(t *testing.T) {
	w := outhouseWorld(t, 10)
	var pushed []*sim.PCNeedsChanged
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, e sim.Event) {
		if ev, ok := e.(*sim.PCNeedsChanged); ok {
			pushed = append(pushed, ev)
		}
	}))
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	until := goIn(t, w, start)
	if w.Actors["pc"].Needs["tiredness"] != 10 {
		t.Fatalf("tiredness on going in = %d, want 10 (eased only on coming out)", w.Actors["pc"].Needs["tiredness"])
	}
	if !finish(t, w, until, until) {
		t.Fatal("first stay: tiredness did not move")
	}
	if got := w.Actors["pc"].Needs["tiredness"]; got != 10-sim.OuthouseTirednessRelief {
		t.Errorf("after the first stay: tiredness = %d, want %d", got, 10-sim.OuthouseTirednessRelief)
	}
	if len(pushed) != 1 || pushed[0].Needs["tiredness"] != 10-sim.OuthouseTirednessRelief {
		t.Errorf("HUD pushes = %+v, want one with the eased tiredness", pushed)
	}

	// Back in an hour later: the stay plays out, tiredness stays put.
	later := until.Add(time.Hour)
	until = goIn(t, w, later)
	if finish(t, w, until, until) {
		t.Error("inside the cooldown: tiredness moved")
	}
	if got := w.Actors["pc"].Needs["tiredness"]; got != 10-sim.OuthouseTirednessRelief {
		t.Errorf("inside the cooldown: tiredness = %d, want %d", got, 10-sim.OuthouseTirednessRelief)
	}

	// Once the cooldown has run from the first relief, it eases again.
	again := start.Add(sim.OuthouseStay + sim.OuthouseReliefCooldown)
	until = goIn(t, w, again)
	if !finish(t, w, until, until) {
		t.Error("after the cooldown: tiredness did not move")
	}
	if got := w.Actors["pc"].Needs["tiredness"]; got != 10-2*sim.OuthouseTirednessRelief {
		t.Errorf("after the cooldown: tiredness = %d, want %d", got, 10-2*sim.OuthouseTirednessRelief)
	}
}

// A timer armed for another stay does nothing: no relief, and the current
// stay is still held until its own timer ends it.
func TestOuthouse_StaleStayTimerDoesNothing(t *testing.T) {
	w := outhouseWorld(t, 10)
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	until := goIn(t, w, start)
	if finish(t, w, until.Add(-time.Second), until) {
		t.Error("a timer armed for another stay eased tiredness")
	}
	if _, err := sim.UseOuthouse("pc", "outhouse", until.Add(-time.Millisecond)).Fn(w); err == nil {
		t.Error("a timer armed for another stay ended the current one")
	}
	if !finish(t, w, until, until) {
		t.Error("the stay's own timer did not ease tiredness")
	}
	if finish(t, w, until, until) {
		t.Error("a stay ended twice")
	}
}

// A rested player does not spend their relief: tired a moment later, the next
// stay eases at once.
func TestOuthouse_RestedPlayerKeepsTheRelief(t *testing.T) {
	w := outhouseWorld(t, 0)
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	until := goIn(t, w, start)
	if finish(t, w, until, until) {
		t.Error("a rested player: tiredness moved below 0")
	}
	if got := w.Actors["pc"].Needs["tiredness"]; got != 0 {
		t.Errorf("rested player: tiredness = %d, want 0", got)
	}
	w.Actors["pc"].Needs["tiredness"] = 5
	until = goIn(t, w, until.Add(time.Minute))
	if !finish(t, w, until, until) {
		t.Error("the relief was spent by a stay that eased nothing")
	}
}

// The stay's end is timed from its deadline, not from when the command ran: a
// command that runs 3 s after its now ends the stay about 7 s later.
func TestOuthouse_StayEndTimedFromTheDeadline(t *testing.T) {
	w := outhouseWorld(t, 10)
	delays := sim.CaptureOuthouseTimersForTest(t)
	goIn(t, w, time.Now().UTC().Add(-3*time.Second))
	if len(*delays) != 1 {
		t.Fatalf("timers armed = %d, want 1", len(*delays))
	}
	if d := (*delays)[0]; d <= 0 || d > sim.OuthouseStay-2*time.Second {
		t.Errorf("stay end in %v, want about %v (the deadline, not a fresh stay)", d, sim.OuthouseStay-3*time.Second)
	}
}
