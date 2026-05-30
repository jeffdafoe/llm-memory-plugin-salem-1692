package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ZBBS-HOME-358. EnsureColocatedHuddle forms the indoor huddle a PC needs to
// talk to / transact with co-located actors, since a plain walk-in mints none.
// Uses buildHuddleTestWorld (huddle_commands_test.go), which seeds the "tavern"
// structure + placement and actors alice/bob/charlie.

// setActor mutates an existing seeded actor's fields on the world goroutine.
func setActor(t *testing.T, w *sim.World, id sim.ActorID, mut func(*sim.Actor)) {
	t.Helper()
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		if a == nil {
			t.Fatalf("setActor: %q not seeded", id)
		}
		mut(a)
		return nil, nil
	}})
}

// huddleOf (reads an actor's CurrentHuddleID) is defined in pc_enter_test.go —
// reused here.

func ensureColocated(t *testing.T, w *sim.World, id sim.ActorID) {
	t.Helper()
	sendT(t, w, sim.EnsureColocatedHuddle(id, time.Unix(0, 0).UTC()))
}

// TestEnsureColocatedHuddle_FormsHuddleWithColocated: a PC inside the tavern
// with a conversational NPC and no huddle ends up co-huddled WITH it.
func TestEnsureColocatedHuddle_FormsHuddleWithColocated(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindPC
		a.LoginUsername = "tester"
		a.InsideStructureID = "tavern"
	})
	setActor(t, w, "bob", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})

	ensureColocated(t, w, "alice")

	ah, bh := huddleOf(t, w, "alice"), huddleOf(t, w, "bob")
	if ah == "" {
		t.Fatal("PC has no huddle after EnsureColocatedHuddle")
	}
	if ah != bh {
		t.Errorf("not co-huddled: alice=%q bob=%q", ah, bh)
	}
}

// TestEnsureColocatedHuddle_AloneInside: a PC inside with no other
// conversational actor forms no huddle (speak-to-no-one stays valid).
func TestEnsureColocatedHuddle_AloneInside(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindPC
		a.InsideStructureID = "tavern"
	})

	ensureColocated(t, w, "alice")

	if h := huddleOf(t, w, "alice"); h != "" {
		t.Errorf("PC alone inside should form no huddle, got %q", h)
	}
}

// TestEnsureColocatedHuddle_Outdoor: a PC not inside any structure is out of
// scope (indoor-only fix), even with a co-located actor.
func TestEnsureColocatedHuddle_Outdoor(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	setActor(t, w, "alice", func(a *sim.Actor) { a.Kind = sim.KindPC }) // InsideStructureID == ""
	setActor(t, w, "bob", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful })

	ensureColocated(t, w, "alice")

	if h := huddleOf(t, w, "alice"); h != "" {
		t.Errorf("outdoor PC should be out of scope, got huddle %q", h)
	}
}

// TestEnsureColocatedHuddle_AlreadyHuddled: an actor already in a huddle is left
// untouched (never disturbs an existing conversation).
func TestEnsureColocatedHuddle_AlreadyHuddled(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindPC
		a.InsideStructureID = "tavern"
	})
	setActor(t, w, "bob", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})
	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", time.Unix(0, 0).UTC()))
	before := huddleOf(t, w, "alice")

	ensureColocated(t, w, "alice")

	if after := huddleOf(t, w, "alice"); after != before {
		t.Errorf("already-huddled PC changed huddle %q → %q", before, after)
	}
}

// TestEnsureColocatedHuddle_DoesNotYankAlreadyHuddled: a co-located actor who
// is already in a huddle (here: attached at "smithy" while physically inside
// "tavern" — the stale/cross-structure case) must NOT be pulled into the
// speaker's new huddle (JoinHuddle is leave-first). It keeps its own huddle; a
// separate unhuddled peer still joins the speaker. ZBBS-HOME-358 (code_review).
func TestEnsureColocatedHuddle_DoesNotYankAlreadyHuddled(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindPC
		a.InsideStructureID = "tavern"
	})
	setActor(t, w, "bob", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})
	setActor(t, w, "charlie", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})
	// bob is already in a huddle anchored at smithy (not tavern).
	sendT(t, w, sim.JoinHuddle("bob", "smithy", "", time.Unix(0, 0).UTC()))
	bobHuddle := huddleOf(t, w, "bob")

	ensureColocated(t, w, "alice")

	if huddleOf(t, w, "bob") != bobHuddle {
		t.Errorf("already-huddled bob was yanked: %q → %q", bobHuddle, huddleOf(t, w, "bob"))
	}
	ah := huddleOf(t, w, "alice")
	if ah == "" || ah == bobHuddle {
		t.Errorf("alice should form a NEW huddle with the unhuddled peer, got %q (bob's=%q)", ah, bobHuddle)
	}
	if huddleOf(t, w, "charlie") != ah {
		t.Error("unhuddled peer charlie should have joined the speaker's huddle")
	}
}

// TestEnsureColocatedHuddle_NonPCCallerNoOp: the bootstrap is PC-only. An NPC
// "speaker" never forms an indoor huddle through this path (NPC conversation
// forms via the cascade/reactor). ZBBS-HOME-358 (code_review #3).
func TestEnsureColocatedHuddle_NonPCCallerNoOp(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful // NOT a PC
		a.InsideStructureID = "tavern"
	})
	setActor(t, w, "bob", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})

	ensureColocated(t, w, "alice")

	if h := huddleOf(t, w, "alice"); h != "" {
		t.Errorf("non-PC caller should be a no-op, got huddle %q", h)
	}
}

// TestEnsureColocatedHuddle_ExcludesStalePC: a co-located PC whose presence has
// gone stale (closed tab — here, a never-polled PC with nil LastPCSeenAt) must
// not be resurrected into the huddle. The speaker still forms one if another
// live peer is present. ZBBS-HOME-358 / ZBBS-WORK-326 (code_review #2).
func TestEnsureColocatedHuddle_ExcludesStalePC(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Unix(1_000_000, 0).UTC()
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindPC
		a.LastPCSeenAt = &now // speaker is present
		a.InsideStructureID = "tavern"
	})
	setActor(t, w, "bob", func(a *sim.Actor) {
		// A SECOND player, co-located but STALE: nil LastPCSeenAt counts as stale
		// per PCPresenceStale's documented contract (a PC that never polled).
		a.Kind = sim.KindPC
		a.InsideStructureID = "tavern"
	})
	setActor(t, w, "charlie", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful // a live NPC peer
		a.InsideStructureID = "tavern"
	})

	sendT(t, w, sim.EnsureColocatedHuddle("alice", now))

	ah := huddleOf(t, w, "alice")
	if ah == "" {
		t.Fatal("speaker formed no huddle despite a live NPC peer")
	}
	if huddleOf(t, w, "charlie") != ah {
		t.Error("live NPC peer should have joined")
	}
	if huddleOf(t, w, "bob") != "" {
		t.Error("stale PC should not be pulled into the huddle")
	}
}

// TestEnsureColocatedHuddle_SpeakerJoinFailureNoSideEffects: if the speaker's
// own join fails (here: standing in a structure with no Structures row, which
// JoinHuddle rejects), no co-located other is joined either — a failed bootstrap
// must not mutate conversation state. ZBBS-HOME-358 (code_review #4).
func TestEnsureColocatedHuddle_SpeakerJoinFailureNoSideEffects(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	// "ghosttown" is not in w.Structures → JoinHuddle returns "structure not found".
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindPC
		a.InsideStructureID = "ghosttown"
	})
	setActor(t, w, "bob", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "ghosttown"
	})

	ensureColocated(t, w, "alice")

	if h := huddleOf(t, w, "alice"); h != "" {
		t.Errorf("speaker join should have failed, got huddle %q", h)
	}
	if h := huddleOf(t, w, "bob"); h != "" {
		t.Errorf("other must not be joined when speaker join failed, got %q", h)
	}
}

// TestEnsureColocatedHuddle_ExcludesSleeperAndDecorative: a sleeping co-located
// actor and a decorative NPC are not pulled into the huddle.
func TestEnsureColocatedHuddle_ExcludesSleeperAndDecorative(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindPC
		a.InsideStructureID = "tavern"
	})
	setActor(t, w, "bob", func(a *sim.Actor) { // awake conversational peer
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})
	setActor(t, w, "charlie", func(a *sim.Actor) { // sleeping
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
		a.State = sim.StateSleeping
	})

	ensureColocated(t, w, "alice")

	ah := huddleOf(t, w, "alice")
	if ah == "" {
		t.Fatal("PC formed no huddle despite an awake conversational peer")
	}
	if huddleOf(t, w, "bob") != ah {
		t.Error("awake conversational peer was not joined")
	}
	if huddleOf(t, w, "charlie") != "" {
		t.Error("sleeping actor should not be pulled into the huddle")
	}
}
