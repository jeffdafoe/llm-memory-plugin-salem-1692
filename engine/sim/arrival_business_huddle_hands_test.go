package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// arrival_business_huddle_hands_test.go — LLM-760. A hand working a job at
// a shop joins the customer's arrival conversation with the keeper, so the
// customer's words reach them (live: Abraham Warren hired at Ezekiel Crane's
// Blacksmith never heard the player).

// hireAt gives worker a working labor offer from employer, running an hour.
func hireAt(t *testing.T, w *sim.World, laborID sim.LaborID, worker, employer sim.ActorID, now time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		until := now.Add(time.Hour)
		world.Actors[worker].LaboringUntil = &until
		world.Actors[worker].State = sim.StateLaboring
		if world.LaborLedger == nil {
			world.LaborLedger = map[sim.LaborID]*sim.LaborOffer{}
		}
		world.LaborLedger[laborID] = &sim.LaborOffer{
			ID: laborID, WorkerID: worker, EmployerID: employer,
			State: sim.LaborStateWorking, WorkingUntil: &until,
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("hireAt: %v", err)
	}
}

// The keeper's hand at work in the shop joins the conversation, and the
// keeper greets the customer once — never their own hand.
func TestArrivalBusinessHuddle_HandJoins(t *testing.T) {
	now := time.Now().UTC()
	w, rec, cancel := buildBusinessArrivalWorld(t, []businessArrivalActor{
		{id: "keeper", inside: tavern, kind: sim.KindNPCStateful, keeperOf: tavern},
		{id: "hand", inside: tavern, kind: sim.KindNPCShared},
		{id: "pc", inside: tavern, kind: sim.KindPC, lastPCSeenAt: freshStampAt(now)},
	})
	defer cancel()
	hireAt(t, w, 1, "hand", "keeper", now)

	emitIndoorArrival(t, w, "pc", now)

	pcHuddle := readHuddleOf(t, w, "pc")
	if pcHuddle == "" || readHuddleOf(t, w, "keeper") != pcHuddle || readHuddleOf(t, w, "hand") != pcHuddle {
		t.Fatalf("huddles: pc=%q keeper=%q hand=%q, want all the same", pcHuddle,
			readHuddleOf(t, w, "keeper"), readHuddleOf(t, w, "hand"))
	}
	greets := rec.bySpeaker("keeper")
	if len(greets) != 1 {
		t.Fatalf("keeper greets = %d, want 1 (the customer only)", len(greets))
	}
	// The greet cooldown is per listener, so a greet to the hand would be a
	// second Spoke; the one greet carries the customer's own recipients.
	if got := greets[0].RecipientIDs; !containsID(got, "pc") {
		t.Errorf("keeper greet recipients = %v, want the customer among them", got)
	}
}

// Joining an existing keeper conversation pulls the hand in too.
func TestArrivalBusinessHuddle_HandJoinsExistingKeeperHuddle(t *testing.T) {
	now := time.Now().UTC()
	w, _, cancel := buildBusinessArrivalWorld(t, []businessArrivalActor{
		{id: "keeper", inside: tavern, kind: sim.KindNPCStateful, keeperOf: tavern, huddleID: "hud-existing"},
		{id: "regular", inside: tavern, kind: sim.KindNPCStateful, huddleID: "hud-existing"},
		{id: "hand", inside: tavern, kind: sim.KindNPCShared},
		{id: "pc", inside: tavern, kind: sim.KindPC, lastPCSeenAt: freshStampAt(now)},
	})
	defer cancel()
	hireAt(t, w, 1, "hand", "keeper", now)

	emitIndoorArrival(t, w, "pc", now)

	if got := readHuddleOf(t, w, "hand"); got != "hud-existing" {
		t.Fatalf("hand huddle = %q, want hud-existing", got)
	}
}

// Who is NOT pulled in: a hand already talking elsewhere, a hand working for
// another shop, someone inside who is not working a job, and a hand whose
// job has run out.
func TestArrivalBusinessHuddle_OnlyThisShopsWorkingHands(t *testing.T) {
	now := time.Now().UTC()
	w, _, cancel := buildBusinessArrivalWorld(t, []businessArrivalActor{
		{id: "keeper", inside: tavern, kind: sim.KindNPCStateful, keeperOf: tavern},
		{id: "other-keeper", inside: "smithy", kind: sim.KindNPCStateful, keeperOf: "smithy"},
		// The first member seeds the huddle's structure: hud-elsewhere is at
		// the market, though busy-hand stands in the tavern.
		{id: "friend", inside: "market", kind: sim.KindNPCShared, huddleID: "hud-elsewhere"},
		{id: "busy-hand", inside: tavern, kind: sim.KindNPCShared, huddleID: "hud-elsewhere"},
		{id: "smithy-hand", inside: tavern, kind: sim.KindNPCShared},
		{id: "idler", inside: tavern, kind: sim.KindNPCShared},
		{id: "done-hand", inside: tavern, kind: sim.KindNPCShared},
		{id: "pc", inside: tavern, kind: sim.KindPC, lastPCSeenAt: freshStampAt(now)},
	})
	defer cancel()
	hireAt(t, w, 1, "busy-hand", "keeper", now)
	hireAt(t, w, 2, "smithy-hand", "other-keeper", now)
	hireAt(t, w, 3, "done-hand", "keeper", now.Add(-2*time.Hour))

	emitIndoorArrival(t, w, "pc", now)

	pcHuddle := readHuddleOf(t, w, "pc")
	if pcHuddle == "" {
		t.Fatal("pc not huddled with the keeper")
	}
	if got := readHuddleOf(t, w, "busy-hand"); got != "hud-elsewhere" {
		t.Errorf("busy-hand huddle = %q, want left in hud-elsewhere", got)
	}
	for _, id := range []sim.ActorID{"smithy-hand", "idler", "done-hand"} {
		if got := readHuddleOf(t, w, id); got != "" {
			t.Errorf("%s huddle = %q, want none", id, got)
		}
	}
}

// No keeper at post: no conversation forms, hand or not.
func TestArrivalBusinessHuddle_HandWithoutKeeperNoHuddle(t *testing.T) {
	now := time.Now().UTC()
	w, _, cancel := buildBusinessArrivalWorld(t, []businessArrivalActor{
		{id: "keeper", inside: tavern, kind: sim.KindNPCStateful, keeperOf: tavern, state: sim.StateSleeping},
		{id: "hand", inside: tavern, kind: sim.KindNPCShared},
		{id: "pc", inside: tavern, kind: sim.KindPC, lastPCSeenAt: freshStampAt(now)},
	})
	defer cancel()
	hireAt(t, w, 1, "hand", "keeper", now)

	emitIndoorArrival(t, w, "pc", now)

	if got := readHuddleOf(t, w, "hand"); got != "" {
		t.Errorf("hand huddle = %q, want none (no keeper to receive the customer)", got)
	}
	if got := readHuddleOf(t, w, "pc"); got != "" {
		t.Errorf("pc huddle = %q, want none", got)
	}
}

// The live shape: a customer walks up to a stall's counter (the loiter pin,
// outside) while the keeper and their hand both work inside. The hand joins
// the conversation with the keeper.
func TestArrivalBusinessHuddle_StallHandJoinsWalkup(t *testing.T) {
	now := time.Now().UTC()
	w, rec, stop := buildStallGreetWorld(t, sim.StateIdle)
	defer stop()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hand"] = &sim.Actor{
			ID: "hand", DisplayName: "Abraham", Kind: sim.KindNPCShared,
			State: sim.StateIdle, InsideStructureID: "stall",
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed hand: %v", err)
	}
	hireAt(t, w, 1, "hand", "keeper", now)

	emitLoiterVisitArrival(t, w, "patron", "stall", now)

	ph := readHuddleOf(t, w, "patron")
	if ph == "" || readHuddleOf(t, w, "keeper") != ph || readHuddleOf(t, w, "hand") != ph {
		t.Fatalf("huddles: patron=%q keeper=%q hand=%q, want all the same", ph,
			readHuddleOf(t, w, "keeper"), readHuddleOf(t, w, "hand"))
	}
	if got := len(rec.bySpeaker("keeper")); got != 1 {
		t.Fatalf("keeper greets = %d, want 1 (the patron only)", got)
	}
}
