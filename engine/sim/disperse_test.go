package sim_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// disperse_test.go — LLM-453. The disperse verb: a graceful, terminal exit from a
// wound-down conversation, plus the structure-scoped re-huddle cooldown that keeps
// the loop from re-forming the moment a housemate speaks. Uses buildHuddleTestWorld
// (huddle_commands_test.go), which seeds the "tavern" structure + alice/bob/charlie.

// TestDisperse_LeavesForBusinessWithCooldown covers the core: a disperse leaves the
// actor's huddle with the "for business" classification (so peers read a graceful
// leave-taking, not a bare "stepped away"), names the peers left behind, and stamps
// the structure-scoped re-huddle cooldown.
func TestDisperse_LeavesForBusinessWithCooldown(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	for _, id := range []sim.ActorID{"alice", "bob", "charlie"} {
		setActor(t, w, id, func(a *sim.Actor) {
			a.Kind = sim.KindNPCStateful
			a.InsideStructureID = "tavern"
		})
	}
	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now.Add(time.Second)))
	sendT(t, w, sim.JoinHuddle("charlie", "tavern", "", now.Add(2*time.Second)))

	sendT(t, w, sim.Disperse("alice", "I'll see you at supper, then.", false, now.Add(time.Minute)))

	if h := huddleOf(t, w, "alice"); h != "" {
		t.Errorf("alice still huddled after disperse: %q", h)
	}

	// Her own departure warrant is the "for business" leave, naming the peers.
	aliceLeft := findWarrant(readWarrants(t, w, "alice"), sim.WarrantKindHuddleLeftForBusiness)
	if aliceLeft == nil {
		t.Fatal("alice did not get a HuddleLeftForBusiness warrant after disperse")
	}
	leftReason, ok := aliceLeft.Reason.(sim.HuddlePartReason)
	if !ok {
		t.Fatalf("alice disperse Reason type = %T, want HuddlePartReason", aliceLeft.Reason)
	}
	assertPeerIDs(t, "alice dispersed", leftReason.PeerIDs, "bob", "charlie")

	// Each remaining peer gets the "peer left for business" warrant — the cue that
	// legitimizes taking their own leave next.
	for _, peer := range []sim.ActorID{"bob", "charlie"} {
		if findWarrant(readWarrants(t, w, peer), sim.WarrantKindHuddlePeerLeftForBusiness) == nil {
			t.Errorf("%s did not get a HuddlePeerLeftForBusiness warrant after alice dispersed", peer)
		}
	}

	// The re-huddle cooldown is set, scoped to the structure she left.
	var until *time.Time
	var from sim.StructureID
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		until, from = a.DispersedUntil, a.DispersedFromStructureID
		return nil, nil
	}})
	if until == nil {
		t.Error("alice.DispersedUntil not set after disperse")
	}
	if from != "tavern" {
		t.Errorf("alice.DispersedFromStructureID = %q, want tavern", from)
	}
}

// TestDisperse_RejectsWhenNotHuddled: disperse called outside a conversation is a
// model-facing rejection (defensive — the tool is gated to wound-down huddles).
func TestDisperse_RejectsWhenNotHuddled(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})
	_, err := w.Send(sim.Disperse("alice", "bye", false, time.Now().UTC()))
	if err == nil {
		t.Fatal("disperse while not in a huddle should reject, got nil error")
	}
	var mfe sim.ModelFacingError
	if !errors.As(err, &mfe) {
		t.Errorf("disperse reject should be a ModelFacingError, got %T: %v", err, err)
	}
}

// TestDisperse_CooldownSuppressesNPCRepullButNotPC covers the re-huddle cooldown:
// while a dispersed actor is within its cooldown at a structure, another NPC's speak
// does NOT draw it back into a huddle there (breaking the loop), but a PC may still
// engage it — player-facing conversation is never blocked.
func TestDisperse_CooldownSuppressesNPCRepullButNotPC(t *testing.T) {
	base := time.Unix(1_000_000, 0).UTC()
	cool := base.Add(time.Hour) // well within the cooldown window

	t.Run("another NPC's speak does not re-pull", func(t *testing.T) {
		w, cancel := buildHuddleTestWorld(t)
		defer cancel()
		setActor(t, w, "alice", func(a *sim.Actor) {
			a.Kind = sim.KindNPCStateful
			a.InsideStructureID = "tavern"
			a.DispersedUntil = &cool
			a.DispersedFromStructureID = "tavern"
		})
		setActor(t, w, "bob", func(a *sim.Actor) {
			a.Kind = sim.KindNPCStateful
			a.InsideStructureID = "tavern"
		})
		sendT(t, w, sim.EnsureColocatedHuddle("bob", base))
		if h := huddleOf(t, w, "alice"); h != "" {
			t.Errorf("dispersed alice re-pulled by an NPC's speak within cooldown: %q", h)
		}
	})

	t.Run("a PC's speak re-engages", func(t *testing.T) {
		w, cancel := buildHuddleTestWorld(t)
		defer cancel()
		setActor(t, w, "alice", func(a *sim.Actor) {
			a.Kind = sim.KindNPCStateful
			a.InsideStructureID = "tavern"
			a.DispersedUntil = &cool
			a.DispersedFromStructureID = "tavern"
		})
		setActor(t, w, "charlie", func(a *sim.Actor) {
			a.Kind = sim.KindPC
			a.LoginUsername = "tester"
			a.InsideStructureID = "tavern"
		})
		sendT(t, w, sim.EnsureColocatedHuddle("charlie", base))
		ah, ch := huddleOf(t, w, "alice"), huddleOf(t, w, "charlie")
		if ah == "" || ah != ch {
			t.Errorf("PC did not re-engage a dispersed NPC: alice=%q charlie=%q", ah, ch)
		}
	})
}

// TestDisperse_OwnSpeakDoesNotReform: a dispersed actor's OWN speak must not re-form
// a huddle at the structure it just left (EnsureColocatedHuddle bails on the
// cooldown), so it cannot yank itself back into the loop it left.
func TestDisperse_OwnSpeakDoesNotReform(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	base := time.Unix(1_000_000, 0).UTC()
	cool := base.Add(time.Hour)
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
		a.DispersedUntil = &cool
		a.DispersedFromStructureID = "tavern"
	})
	setActor(t, w, "bob", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})
	sendT(t, w, sim.EnsureColocatedHuddle("alice", base))
	if h := huddleOf(t, w, "alice"); h != "" {
		t.Errorf("dispersed alice re-formed a huddle by her own speak within cooldown: %q", h)
	}
}
