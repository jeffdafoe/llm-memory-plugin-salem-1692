package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ZBBS-HOME-445. EnsureKnockServiceHuddle forms the across-the-doorway
// service huddle when a knock walk ARRIVES. These tests drive the command
// directly (the cascade wiring that dispatches it from ActorArrived is
// covered in cascade/knock_arrival_test.go) plus one end-to-end locomotion
// drive asserting the Knock stamp survives the walk and rides ActorArrived.

// TestEnsureKnockServiceHuddle_JoinsKnockerAndReceiver: the arrived knocker
// and the receptive receiver inside share one huddle; the knocker stays
// physically outside.
func TestEnsureKnockServiceHuddle_JoinsKnockerAndReceiver(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	setInside(t, w, "servant", "cottage")

	if _, err := w.Send(sim.EnsureKnockServiceHuddle("stranger", "cottage", now)); err != nil {
		t.Fatalf("EnsureKnockServiceHuddle: %v", err)
	}
	knocker, receiver := huddleOf(t, w, "stranger"), huddleOf(t, w, "servant")
	if knocker == "" || knocker != receiver {
		t.Errorf("knocker and receiver should share one huddle; stranger=%q servant=%q", knocker, receiver)
	}
	if got := insideOf(t, w, "stranger"); got != "" {
		t.Errorf("knocker should stay physically outside; InsideStructureID = %q", got)
	}
}

// TestEnsureKnockServiceHuddle_MultipleReceiversAllJoin: with several
// associated receivers inside, the arrival pulls them all into one shared
// huddle with the knocker.
func TestEnsureKnockServiceHuddle_MultipleReceiversAllJoin(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	setInside(t, w, "servant", "cottage") // WorkStructureID == cottage
	setInside(t, w, "spouse", "cottage")  // HomeStructureID == cottage

	if _, err := w.Send(sim.EnsureKnockServiceHuddle("stranger", "cottage", now)); err != nil {
		t.Fatalf("EnsureKnockServiceHuddle: %v", err)
	}
	h := huddleOf(t, w, "stranger")
	if h == "" {
		t.Fatal("knocker should be in a huddle")
	}
	if got := huddleOf(t, w, "servant"); got != h {
		t.Errorf("servant should share the knocker's huddle; got %q want %q", got, h)
	}
	if got := huddleOf(t, w, "spouse"); got != h {
		t.Errorf("spouse should share the knocker's huddle; got %q want %q", got, h)
	}
}

// TestEnsureKnockServiceHuddle_NoReceiverNoHuddle: nobody receptive inside →
// the door goes unanswered, no lone-knocker huddle is minted.
func TestEnsureKnockServiceHuddle_NoReceiverNoHuddle(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.EnsureKnockServiceHuddle("stranger", "cottage", now)); err != nil {
		t.Fatalf("EnsureKnockServiceHuddle: %v", err)
	}
	if got := huddleOf(t, w, "stranger"); got != "" {
		t.Errorf("unanswered knock should mint no huddle; got %q", got)
	}
}

// TestEnsureKnockServiceHuddle_MovingReceiverNoHuddle: a receiver with a
// MoveIntent in flight (a keeper heading out the door) is no receiver —
// joining a walker would hand the HOME-340 mover-leave rule a fresh
// membership to evict, recreating the phantom farewell on the receiver's
// side. No lone-knocker huddle forms either.
func TestEnsureKnockServiceHuddle_MovingReceiverNoHuddle(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	setInside(t, w, "servant", "cottage")
	if _, err := w.Send(sim.MoveActor("servant",
		sim.NewPositionDestination(sim.Position{X: sim.PadX + 8, Y: sim.PadY + 8}), true, now)); err != nil {
		t.Fatalf("seed receiver walk: %v", err)
	}

	if _, err := w.Send(sim.EnsureKnockServiceHuddle("stranger", "cottage", now)); err != nil {
		t.Fatalf("EnsureKnockServiceHuddle: %v", err)
	}
	if got := huddleOf(t, w, "servant"); got != "" {
		t.Errorf("a moving receiver must not be huddled (HOME-340 would evict it next tick); got %q", got)
	}
	if got := huddleOf(t, w, "stranger"); got != "" {
		t.Errorf("no receptive receiver → no lone-knocker huddle; got %q", got)
	}
}

// TestEnsureKnockServiceHuddle_SleepingReceiverNoHuddle: a sleeping receiver
// is no receiver — same standard the click-time narration applies.
func TestEnsureKnockServiceHuddle_SleepingReceiverNoHuddle(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	setInside(t, w, "servant", "cottage")
	mutate(t, w, func(world *sim.World) {
		world.Actors["servant"].State = sim.StateSleeping
	})

	if _, err := w.Send(sim.EnsureKnockServiceHuddle("stranger", "cottage", now)); err != nil {
		t.Fatalf("EnsureKnockServiceHuddle: %v", err)
	}
	if got := huddleOf(t, w, "stranger"); got != "" {
		t.Errorf("a sleeping receiver should answer no knock; got huddle %q", got)
	}
	if got := huddleOf(t, w, "servant"); got != "" {
		t.Errorf("the sleeping receiver should not be huddled; got %q", got)
	}
}

// TestEnsureKnockServiceHuddle_StaleArrivalGuards: a knocker who has since
// started another walk, gone inside somewhere, or joined a huddle is left
// alone — a stale ActorArrived degrades to a no-op.
func TestEnsureKnockServiceHuddle_StaleArrivalGuards(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	setInside(t, w, "servant", "cottage")

	// Guard 1: a new MoveIntent (clicked elsewhere) suppresses the join.
	if _, err := w.Send(sim.MoveActor("stranger",
		sim.NewPositionDestination(sim.Position{X: sim.PadX + 8, Y: sim.PadY + 8}), true, now)); err != nil {
		t.Fatalf("seed superseding move: %v", err)
	}
	if _, err := w.Send(sim.EnsureKnockServiceHuddle("stranger", "cottage", now)); err != nil {
		t.Fatalf("EnsureKnockServiceHuddle: %v", err)
	}
	if got := huddleOf(t, w, "stranger"); got != "" {
		t.Errorf("a knocker mid-new-walk must not be yanked into a doorway huddle; got %q", got)
	}
	mutate(t, w, func(world *sim.World) {
		world.Actors["stranger"].MoveIntent = nil
	})

	// Guard 2: a knocker who is somehow inside a structure is the indoor
	// bootstrap's business, not the knock's.
	setInside(t, w, "stranger", "cottage")
	if _, err := w.Send(sim.EnsureKnockServiceHuddle("stranger", "cottage", now)); err != nil {
		t.Fatalf("EnsureKnockServiceHuddle: %v", err)
	}
	if got := huddleOf(t, w, "stranger"); got != "" {
		t.Errorf("an inside knocker must be skipped; got %q", got)
	}
	setInside(t, w, "stranger", "")

	// Guard 3: already conversing — the whole command must no-op. The
	// knocker's huddle id alone can't prove that (a re-join of the same
	// structure huddle is idempotent), so the receiver staying UNhuddled is
	// the discriminating assert.
	if _, err := w.Send(sim.JoinHuddle("stranger", "cottage", "", now)); err != nil {
		t.Fatalf("seed huddle: %v", err)
	}
	prior := huddleOf(t, w, "stranger")
	if _, err := w.Send(sim.EnsureKnockServiceHuddle("stranger", "cottage", now)); err != nil {
		t.Fatalf("EnsureKnockServiceHuddle: %v", err)
	}
	if got := huddleOf(t, w, "stranger"); got != prior {
		t.Errorf("an already-huddled knocker must be left alone; %q -> %q", prior, got)
	}
	if got := huddleOf(t, w, "servant"); got != "" {
		t.Errorf("the no-op must not pull the receiver in; servant in %q", got)
	}
}

// TestKnockWalk_ArrivedEventCarriesKnocked drives a knock walk end to end
// through the locomotion ticker: the Knock stamp survives the walk (no
// mid-walk huddle to evict — the original ZBBS-HOME-445 bug emitted a
// HuddleLeft one tick after the click, which the businessowner cascade voiced
// as a farewell to a customer still walking in), and the ActorArrived event
// carries Knocked=true for the cascade dispatch.
func TestKnockWalk_ArrivedEventCarriesKnocked(t *testing.T) {
	w, cancel, rec := buildLocomotionTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Make the cottage owner-only with an absent owner so walker's click
	// resolves to a knock.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.VillageObjects["cottage"].EntryPolicy = sim.EntryPolicyOwner
		world.VillageObjects["cottage"].OwnerActorID = "absent-owner"
		return nil, nil
	}}); err != nil {
		t.Fatalf("mutate cottage: %v", err)
	}

	res, err := w.Send(sim.EnterOrKnock("walker", "cottage", true, now))
	if err != nil {
		t.Fatalf("EnterOrKnock: %v", err)
	}
	if !res.(sim.EnterOrKnockResult).Knocked {
		t.Fatal("expected the click to resolve to a knock")
	}

	driveToArrival(t, w, "walker", now, 40)

	left := rec.countEvents(func(e sim.Event) bool {
		hl, ok := e.(*sim.HuddleLeft)
		return ok && hl.ActorID == "walker"
	})
	if left != 0 {
		t.Errorf("HuddleLeft{walker} count = %d, want 0 — the knock walk must carry no evictable membership", left)
	}
	arrived := rec.countEvents(func(e sim.Event) bool {
		a, ok := e.(*sim.ActorArrived)
		return ok && a.ActorID == "walker" && a.Knocked && a.DestStructureID == "cottage" && a.FinalStructureID == ""
	})
	if arrived != 1 {
		t.Errorf("ActorArrived{walker, Knocked, dest=cottage, outside} count = %d, want 1", arrived)
	}
}
