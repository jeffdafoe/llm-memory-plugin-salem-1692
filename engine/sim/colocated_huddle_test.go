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

// TestEnsureColocatedHuddle_NPCSpeakerForms: ZBBS-HOME-363 widened the bootstrap
// to conversational NPCs. A stateful NPC speaking indoors with a co-located
// conversational NPC now forms/joins the structure huddle (the live Tavern buy
// bug: an NPC with no huddle could never transact). Both end up huddled together.
func TestEnsureColocatedHuddle_NPCSpeakerForms(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})
	setActor(t, w, "bob", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})

	ensureColocated(t, w, "alice")

	ah := huddleOf(t, w, "alice")
	if ah == "" {
		t.Fatal("NPC speaker should have formed an indoor huddle")
	}
	if huddleOf(t, w, "bob") != ah {
		t.Errorf("co-located NPC bob should have joined the speaker's huddle %q, got %q", ah, huddleOf(t, w, "bob"))
	}
}

// TestEnsureColocatedHuddle_NPCJoinsExistingStructureHuddle: the exact live
// bug (ZBBS-HOME-363). The structure already has an active huddle (bob+charlie,
// e.g. a keeper already conversing); a starving NPC (alice) walks in to buy and
// speaks. find-or-create returns the EXISTING huddle, so alice JOINS it rather
// than minting a second — and can now transact with the keeper.
func TestEnsureColocatedHuddle_NPCJoinsExistingStructureHuddle(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	for _, id := range []sim.ActorID{"alice", "bob", "charlie"} {
		setActor(t, w, id, func(a *sim.Actor) {
			a.Kind = sim.KindNPCStateful
			a.InsideStructureID = "tavern"
		})
	}
	// bob + charlie are already conversing in the tavern's active huddle.
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", time.Unix(0, 0).UTC()))
	sendT(t, w, sim.JoinHuddle("charlie", "tavern", "", time.Unix(0, 0).UTC()))
	existing := huddleOf(t, w, "bob")
	if existing == "" || huddleOf(t, w, "charlie") != existing {
		t.Fatalf("setup: bob/charlie not co-huddled (bob=%q charlie=%q)", existing, huddleOf(t, w, "charlie"))
	}

	ensureColocated(t, w, "alice")

	if got := huddleOf(t, w, "alice"); got != existing {
		t.Errorf("alice should JOIN the existing structure huddle %q, got %q", existing, got)
	}
	// bob + charlie are undisturbed — same huddle, no second huddle minted.
	if huddleOf(t, w, "bob") != existing || huddleOf(t, w, "charlie") != existing {
		t.Error("existing members were disturbed by the join")
	}
}

// TestEnsureColocatedHuddle_DecorativeCallerNoOp: a decorative actor (sprite-
// only, never ticks) speaking is not a real conversation and must not mint a
// huddle — the kind boundary the widened bootstrap still enforces.
func TestEnsureColocatedHuddle_DecorativeCallerNoOp(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindDecorative
		a.InsideStructureID = "tavern"
	})
	setActor(t, w, "bob", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})

	ensureColocated(t, w, "alice")

	if h := huddleOf(t, w, "alice"); h != "" {
		t.Errorf("decorative caller should be a no-op, got huddle %q", h)
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
	// daisy is a decorative NPC co-located inside the tavern (not in the base
	// seed set, so create her directly).
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["daisy"] = &sim.Actor{
			ID: "daisy", DisplayName: "Daisy", Kind: sim.KindDecorative,
			State: sim.StateIdle, InsideStructureID: "tavern",
		}
		return nil, nil
	}})

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
	if huddleOf(t, w, "daisy") != "" {
		t.Error("decorative actor should not be pulled into the huddle")
	}
}

// TestEnsureColocatedHuddle_LoiterStallJoinsOwner: ZBBS-HOME-378 — a customer
// standing OUTSIDE at an owner-only stall's loiter point (InsideStructureID == "")
// forms/joins the owner's structure huddle on speak, WITHOUT entering the stall.
// This is the write-path half of the loiter-commerce fix: the customer stays
// outside (InsideStructureID unchanged) but gains the huddle, so speak/pay/order
// reach the owner working within.
func TestEnsureColocatedHuddle_LoiterStallJoinsOwner(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	// Make the tavern resolvable as a loiter object: a named vobj with zero
	// loiter offsets so its loiter pin == its anchor tile (160px/32 + pad).
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		v := world.VillageObjects["tavern"]
		v.DisplayName = "Tavern"
		z := 0
		v.LoiterOffsetX, v.LoiterOffsetY = &z, &z
		return nil, nil
	}})
	loiterPin := sim.WorldPos{X: 160, Y: 160}.Tile()
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindPC
		a.LoginUsername = "tester"
		a.InsideStructureID = "" // OUTSIDE — standing at the stall's loiter point
		a.Pos = loiterPin
	})
	setActor(t, w, "bob", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern" // owner working inside
	})

	ensureColocated(t, w, "alice")

	ah, bh := huddleOf(t, w, "alice"), huddleOf(t, w, "bob")
	if ah == "" {
		t.Fatal("loitering customer formed no huddle with the stall owner")
	}
	if ah != bh {
		t.Errorf("customer not co-huddled with owner: alice=%q bob=%q", ah, bh)
	}
	// The customer must NOT have been moved inside — commerce is conducted from
	// the loiter point, across the threshold.
	inside := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["alice"].InsideStructureID, nil
	}}).(sim.StructureID)
	if inside != "" {
		t.Errorf("loitering customer was moved inside the stall (InsideStructureID=%q); should stay outside", inside)
	}
}

// structureScenesFor returns the SceneIDs of every structure-bound scene
// anchored to structureID. Reads world state on the world goroutine.
func structureScenesFor(t *testing.T, w *sim.World, structureID sim.StructureID) []sim.SceneID {
	t.Helper()
	res := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		var out []sim.SceneID
		for id, scene := range world.Scenes {
			if scene == nil || scene.Bound.Kind != sim.SceneBoundStructure {
				continue
			}
			if scene.Bound.StructureID == nil || *scene.Bound.StructureID != structureID {
				continue
			}
			out = append(out, id)
		}
		return out, nil
	}})
	return res.([]sim.SceneID)
}

// huddleAnchoredToScene reports whether the actor's current huddle is observed
// by sceneID — i.e. resolveSellerScene (and so scene_quote / pay_with_item)
// would resolve a scene for it.
func huddleAnchoredToScene(t *testing.T, w *sim.World, id sim.ActorID, sceneID sim.SceneID) bool {
	t.Helper()
	res := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		if a == nil || a.CurrentHuddleID == "" {
			return false, nil
		}
		scene := world.Scenes[sceneID]
		if scene == nil {
			return false, nil
		}
		_, ok := scene.Huddles[a.CurrentHuddleID]
		return ok, nil
	}})
	return res.(bool)
}

// TestEnsureColocatedHuddle_AnchorsStructureScene: ZBBS-HOME-375. The indoor
// huddle must be anchored to a structure-bound scene, or scene_quote /
// pay_with_item reject "isn't anchored to a scene" — indoor commerce dies even
// with a keeper present. Exactly one structure scene is minted for the
// structure, and it observes the huddle. Two co-located NPCs = the live buy-bug
// shape (an NPC buyer walking up to an NPC keeper).
func TestEnsureColocatedHuddle_AnchorsStructureScene(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})
	setActor(t, w, "bob", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})

	ensureColocated(t, w, "alice")

	if huddleOf(t, w, "alice") == "" {
		t.Fatal("no huddle formed")
	}
	scenes := structureScenesFor(t, w, "tavern")
	if len(scenes) != 1 {
		t.Fatalf("want exactly 1 structure scene for tavern, got %d (%v)", len(scenes), scenes)
	}
	if !huddleAnchoredToScene(t, w, "alice", scenes[0]) {
		t.Error("huddle not anchored to the tavern structure scene — scene_quote/pay_with_item would reject")
	}
}

// TestEnsureColocatedHuddle_ReusesExistingStructureScene: ZBBS-HOME-375. A
// structure that already has a structure scene (from a prior conversation) must
// REUSE it — find-or-create never mints a second, keeping accumulation bounded
// at one scene per structure (the basis for choosing persist over
// conclude-on-orphan). The new huddle attaches to the pre-existing scene.
func TestEnsureColocatedHuddle_ReusesExistingStructureScene(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	pre := sendT(t, w, sim.CreateScene("colocated_talk", sim.NewStructureBound("tavern"), time.Unix(0, 0).UTC())).(sim.SceneID)
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})
	setActor(t, w, "bob", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})

	ensureColocated(t, w, "alice")

	scenes := structureScenesFor(t, w, "tavern")
	if len(scenes) != 1 || scenes[0] != pre {
		t.Fatalf("want the single pre-existing scene %q reused, got %v", pre, scenes)
	}
	if !huddleAnchoredToScene(t, w, "alice", pre) {
		t.Error("huddle did not attach to the pre-existing structure scene")
	}
}
