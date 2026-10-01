package sim_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// buildPCRepairWorld is buildDamageWorld plus two players, Pat and Quinn, with
// short games (3 steps, 1 s apart, 90 s idle), well-a broken and Pat at it.
// Returns a recorder subscribed to every event.
func buildPCRepairWorld(t *testing.T) (*sim.World, context.CancelFunc, *eventRec) {
	t.Helper()
	w, cancel := buildDamageWorld(t)
	rec := &eventRec{}
	mustSend(t, w, func(world *sim.World) {
		world.Subscribe(sim.SubscriberFunc(rec.handle))
		world.Actors["pat"] = &sim.Actor{ID: "pat", DisplayName: "Pat", Kind: sim.KindPC, Needs: map[sim.NeedKey]int{}}
		world.Actors["quinn"] = &sim.Actor{ID: "quinn", DisplayName: "Quinn", Kind: sim.KindPC, Needs: map[sim.NeedKey]int{}}
		world.Settings.PCRepairWellSteps = 3
		world.Settings.PCRepairWellStepGapMs = 1000
		world.Settings.PCRepairIdleSeconds = 90
	})
	breakWell(t, w, "well-a")
	placeAt(t, w, "pat", "well-a")
	return w, cancel, rec
}

func pcRepairOffer(t *testing.T, w *sim.World, actorID sim.ActorID) *sim.PCRepairOffer {
	t.Helper()
	res, err := w.Send(sim.PCRepairOfferAt(actorID))
	if err != nil {
		t.Fatalf("PCRepairOfferAt(%s): %v", actorID, err)
	}
	return res.(*sim.PCRepairOffer)
}

func pcRepairStep(t *testing.T, w *sim.World, actorID sim.ActorID, at time.Time) (sim.PCRepairStepResult, error) {
	t.Helper()
	res, err := w.Send(sim.StepPCRepair(actorID, at))
	if err != nil {
		return sim.PCRepairStepResult{}, err
	}
	return res.(sim.PCRepairStepResult), nil
}

// TestPCRepairEndToEnd — a player at a broken well sees the offer, takes it,
// plays the steps, and on the last one the well is mended and the chest pays
// the bounty, with the `collected` row and the player's own completion line.
func TestPCRepairEndToEnd(t *testing.T) {
	w, cancel, rec := buildPCRepairWorld(t)
	defer cancel()

	offer := pcRepairOffer(t, w, "pat")
	if offer == nil {
		t.Fatal("no offer at the broken well")
	}
	if offer.SiteKind != sim.PublicWorksWell || offer.Bounty != 12 || !offer.ChestCanPay || offer.Steps != 3 || offer.Yours || offer.MenderName != "" {
		t.Errorf("offer = %+v, want an open well repair for 12 in 3 steps", offer)
	}
	if !strings.Contains(offer.Fact, "windlass") {
		t.Errorf("offer fact = %q, want the DamageFact line", offer.Fact)
	}

	t0 := time.Now().UTC()
	res, err := w.Send(sim.StartPCRepair("pat", t0))
	if err != nil {
		t.Fatalf("StartPCRepair: %v", err)
	}
	if mine := res.(*sim.PCRepairOffer); !mine.Yours || mine.StepsDone != 0 || mine.StepGap != time.Second {
		t.Errorf("start result = %+v, want Yours at step 0 with a 1s gap", mine)
	}

	// Nothing lands on a clock: the window's Until is the idle deadline.
	mustSend(t, w, func(world *sim.World) {
		sim.CompleteDueSourceActivities(world, t0.Add(30*time.Second))
		if world.Actors["pat"].SourceActivity == nil {
			t.Error("the sweep cleared a player's repair before the idle deadline")
		}
	})

	for i, at := range []time.Time{t0.Add(time.Second), t0.Add(2 * time.Second)} {
		step, err := pcRepairStep(t, w, "pat", at)
		if err != nil {
			t.Fatalf("step %d: %v", i+1, err)
		}
		if step.Done || step.StepsDone != i+1 {
			t.Errorf("step %d = %+v, want not done at %d", i+1, step, i+1)
		}
	}
	last, err := pcRepairStep(t, w, "pat", t0.Add(3*time.Second))
	if err != nil {
		t.Fatalf("last step: %v", err)
	}
	if !last.Done || !last.Landed || last.Paid != 12 {
		t.Errorf("last step = %+v, want done, landed, paid 12", last)
	}

	mustSend(t, w, func(world *sim.World) {
		if world.VillageObjects["well-a"].Damaged() {
			t.Error("well still broken after the last step")
		}
		pat := world.Actors["pat"]
		if pat.Coins != 12 {
			t.Errorf("pat coins = %d, want 12", pat.Coins)
		}
		if pat.SourceActivity != nil {
			t.Error("the window was not cleared after the last step")
		}
		if got := world.Environment.TownChest; got != 88 {
			t.Errorf("chest = %d, want 88", got)
		}
	})
	narrated := rec.countEvents(func(e sim.Event) bool {
		n, ok := e.(*sim.PCRepairNarrated)
		return ok && n.ActorID == "pat" && n.Paid == 12 && strings.Contains(n.Text, "pays you 12 coins")
	})
	if narrated != 1 {
		t.Errorf("PCRepairNarrated events = %d, want 1 naming the 12 coins", narrated)
	}
	if _, err := pcRepairStep(t, w, "pat", t0.Add(10*time.Second)); !errors.Is(err, sim.ErrNoPCRepair) {
		t.Errorf("step after the repair landed: err = %v, want ErrNoPCRepair", err)
	}
}

// TestPCRepairStepPacing — a step sooner than the gap after the start or the
// last step is refused and not counted.
func TestPCRepairStepPacing(t *testing.T) {
	w, cancel, _ := buildPCRepairWorld(t)
	defer cancel()
	t0 := time.Now().UTC()
	if _, err := w.Send(sim.StartPCRepair("pat", t0)); err != nil {
		t.Fatalf("StartPCRepair: %v", err)
	}
	if _, err := pcRepairStep(t, w, "pat", t0.Add(500*time.Millisecond)); !errors.Is(err, sim.ErrPCRepairStepTooSoon) {
		t.Errorf("step 0.5s after start: err = %v, want ErrPCRepairStepTooSoon", err)
	}
	if step, err := pcRepairStep(t, w, "pat", t0.Add(time.Second)); err != nil || step.StepsDone != 1 {
		t.Fatalf("step at 1s = %+v, %v; want step 1 counted", step, err)
	}
	if _, err := pcRepairStep(t, w, "pat", t0.Add(1900*time.Millisecond)); !errors.Is(err, sim.ErrPCRepairStepTooSoon) {
		t.Errorf("step 0.9s after the last: err = %v, want ErrPCRepairStepTooSoon", err)
	}
	if step, err := pcRepairStep(t, w, "pat", t0.Add(2*time.Second)); err != nil || step.StepsDone != 2 {
		t.Errorf("step at 2s = %+v, %v; want step 2 — the refused step was not counted", step, err)
	}
}

// TestPCRepairTakenSiteIsNotOnOffer — while a player mends the well, a hand and
// a second player are refused and see who is at it; while a hand mends it, the
// player is refused the same way.
func TestPCRepairTakenSiteIsNotOnOffer(t *testing.T) {
	w, cancel, _ := buildPCRepairWorld(t)
	defer cancel()
	placeAt(t, w, "quinn", "well-a")
	placeAt(t, w, "anne", "well-a")
	t0 := time.Now().UTC()
	if _, err := w.Send(sim.StartPCRepair("pat", t0)); err != nil {
		t.Fatalf("StartPCRepair: %v", err)
	}
	if _, err := w.Send(sim.StartRepair("anne")); err == nil || !strings.Contains(err.Error(), "already mending") {
		t.Errorf("hand StartRepair err = %v, want the already-mending refusal", err)
	}
	if _, err := w.Send(sim.StartPCRepair("quinn", t0)); err == nil || !strings.Contains(err.Error(), "already mending") {
		t.Errorf("second player StartPCRepair err = %v, want the already-mending refusal", err)
	}
	if offer := pcRepairOffer(t, w, "quinn"); offer == nil || offer.MenderName != "Pat" || offer.Yours {
		t.Errorf("quinn's offer = %+v, want Pat named as the mender", offer)
	}
	if offer := pcRepairOffer(t, w, "pat"); offer == nil || !offer.Yours || offer.MenderName != "" {
		t.Errorf("pat's offer = %+v, want Yours", offer)
	}

	// The other way round: a hand at work, a player refused.
	w2, cancel2, _ := buildPCRepairWorld(t)
	defer cancel2()
	placeAt(t, w2, "anne", "well-a")
	if _, err := w2.Send(sim.StartRepair("anne")); err != nil {
		t.Fatalf("hand StartRepair: %v", err)
	}
	if _, err := w2.Send(sim.StartPCRepair("pat", time.Now().UTC())); err == nil || !strings.Contains(err.Error(), "already mending") {
		t.Errorf("player StartPCRepair err = %v, want the already-mending refusal", err)
	}
	if offer := pcRepairOffer(t, w2, "pat"); offer == nil || offer.MenderName != "Anne Walker" {
		t.Errorf("pat's offer = %+v, want Anne Walker named as the mender", offer)
	}
}

// TestPCRepairRefusedWhenTheChestIsLow — the same chest gate a hand meets.
func TestPCRepairRefusedWhenTheChestIsLow(t *testing.T) {
	w, cancel, _ := buildPCRepairWorld(t)
	defer cancel()
	mustSend(t, w, func(world *sim.World) { world.Environment.TownChest = 61 }) // < 12 + 50
	if offer := pcRepairOffer(t, w, "pat"); offer == nil || offer.ChestCanPay {
		t.Errorf("offer = %+v, want ChestCanPay false", offer)
	}
	if _, err := w.Send(sim.StartPCRepair("pat", time.Now().UTC())); err == nil || !strings.Contains(err.Error(), "chest") {
		t.Errorf("StartPCRepair err = %v, want the empty-chest refusal", err)
	}
}

// TestPCRepairAwayFromASite — no offer and no start where nothing is broken.
func TestPCRepairAwayFromASite(t *testing.T) {
	w, cancel, _ := buildPCRepairWorld(t)
	defer cancel()
	placeAt(t, w, "quinn", "well-b")
	if offer := pcRepairOffer(t, w, "quinn"); offer != nil {
		t.Errorf("offer at a sound well = %+v, want nil", offer)
	}
	if _, err := w.Send(sim.StartPCRepair("quinn", time.Now().UTC())); !errors.Is(err, sim.ErrNoRepairSite) {
		t.Errorf("StartPCRepair err = %v, want ErrNoRepairSite", err)
	}
	if _, err := w.Send(sim.StartPCRepair("anne", time.Now().UTC())); err == nil {
		t.Error("StartPCRepair accepted an NPC")
	}
}

// TestPCRepairIdleGivesUp — each step moves the idle deadline on; a window
// that reaches it is given up (cancelled, not landed), the site stays broken,
// and a hand may take it.
func TestPCRepairIdleGivesUp(t *testing.T) {
	w, cancel, rec := buildPCRepairWorld(t)
	defer cancel()
	t0 := time.Now().UTC()
	if _, err := w.Send(sim.StartPCRepair("pat", t0)); err != nil {
		t.Fatalf("StartPCRepair: %v", err)
	}
	if _, err := pcRepairStep(t, w, "pat", t0.Add(60*time.Second)); err != nil {
		t.Fatalf("step at 60s: %v", err)
	}
	// 100s is past the start's deadline (90s) but inside the step's (150s).
	mustSend(t, w, func(world *sim.World) {
		sim.CompleteDueSourceActivities(world, t0.Add(100*time.Second))
		if world.Actors["pat"].SourceActivity == nil {
			t.Error("a step did not move the idle deadline on")
		}
	})
	mustSend(t, w, func(world *sim.World) {
		sim.CompleteDueSourceActivities(world, t0.Add(151*time.Second))
		if world.Actors["pat"].SourceActivity != nil {
			t.Error("the repair outlived its idle deadline")
		}
		if !world.VillageObjects["well-a"].Damaged() {
			t.Error("a given-up repair mended the well")
		}
		if got := world.Actors["pat"].Coins; got != 0 {
			t.Errorf("pat paid %d for a given-up repair", got)
		}
	})
	cancelled := rec.countEvents(func(e sim.Event) bool {
		c, ok := e.(*sim.SourceActivityCancelled)
		return ok && c.ActorID == "pat"
	})
	if cancelled != 1 {
		t.Errorf("SourceActivityCancelled for pat = %d, want 1", cancelled)
	}
	if _, err := pcRepairStep(t, w, "pat", t0.Add(152*time.Second)); !errors.Is(err, sim.ErrNoPCRepair) {
		t.Errorf("step after giving up: err = %v, want ErrNoPCRepair", err)
	}
	placeAt(t, w, "anne", "well-a")
	if _, err := w.Send(sim.StartRepair("anne")); err != nil {
		t.Errorf("a hand could not take the freed site: %v", err)
	}
}

// TestPCRepairWalkAwayCancels — a committed move gives the repair up.
func TestPCRepairWalkAwayCancels(t *testing.T) {
	w, cancel, rec := buildPCRepairWorld(t)
	defer cancel()
	if _, err := w.Send(sim.StartPCRepair("pat", time.Now().UTC())); err != nil {
		t.Fatalf("StartPCRepair: %v", err)
	}
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) { return world.Actors["pat"].Pos, nil }})
	if err != nil {
		t.Fatal(err)
	}
	pos := res.(sim.TilePos)
	dest := sim.NewPositionDestination(sim.Position{X: pos.X + 1, Y: pos.Y})
	if _, err := w.Send(sim.MoveActor("pat", dest, false, time.Now().UTC())); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	if sa := liveActivity(t, w, "pat"); sa != nil {
		t.Errorf("SourceActivity = %+v after walking off, want cleared", sa)
	}
	if n := rec.countEvents(func(e sim.Event) bool { c, ok := e.(*sim.SourceActivityCancelled); return ok && c.ActorID == "pat" }); n != 1 {
		t.Errorf("SourceActivityCancelled for pat = %d, want 1", n)
	}
}

// TestPCSleepGivesUpTheRepair — going to bed (here the shared bed-down every
// sleep path runs) gives up a repair in hand, so a sleeping player never holds
// a site.
func TestPCSleepGivesUpTheRepair(t *testing.T) {
	w, cancel, rec := buildPCRepairWorld(t)
	defer cancel()
	now := time.Now().UTC()
	if _, err := w.Send(sim.StartPCRepair("pat", now)); err != nil {
		t.Fatalf("StartPCRepair: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		if !sim.ExecutePCSleep(world, world.Actors["pat"], 1, now) {
			t.Fatal("ExecutePCSleep did not bed pat")
		}
		if world.Actors["pat"].SourceActivity != nil {
			t.Error("pat went to bed still holding the repair")
		}
	})
	if n := rec.countEvents(func(e sim.Event) bool { c, ok := e.(*sim.SourceActivityCancelled); return ok && c.ActorID == "pat" }); n != 1 {
		t.Errorf("SourceActivityCancelled for pat = %d, want 1", n)
	}
}

// TestDamagedObjectNarrationCarriesTheOffer — the arrival thought carries the
// same offer the click path reads.
func TestDamagedObjectNarrationCarriesTheOffer(t *testing.T) {
	w, cancel, rec := buildPCRepairWorld(t)
	defer cancel()
	mustSend(t, w, func(world *sim.World) {
		sim.EmitDamagedObjectNarration(world, world.Actors["pat"], &sim.ActorArrived{DestObjectID: "well-a"}, time.Now().UTC())
	})
	var got *sim.ObjectConditionNarrated
	rec.countEvents(func(e sim.Event) bool {
		if n, ok := e.(*sim.ObjectConditionNarrated); ok {
			got = n
		}
		return false
	})
	if got == nil {
		t.Fatal("no ObjectConditionNarrated")
	}
	if got.Offer == nil || got.Offer.ObjectID != "well-a" || got.Offer.Bounty != 12 || !got.Offer.ChestCanPay || got.Offer.Steps != 3 {
		t.Errorf("narration offer = %+v, want well-a for 12 in 3 steps", got.Offer)
	}
}
