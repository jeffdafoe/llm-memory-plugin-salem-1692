package sim_test

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// colocated_reachable_test.go — LLM-1010. The bootstrap draws a co-located actor
// into the building's conversation only when sim.DrawableIntoBuildingConversation
// holds (awake, not in a scoped room). Perception's buy cues test the same rule
// (sellerCoPresent), so a cue never names a seller the pay call cannot reach.

func TestDrawableIntoBuildingConversation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state sim.ActorState
		scope sim.RoomID
		want  bool
	}{
		{"awake in the common room", sim.StateIdle, 0, true},
		{"asleep in the common room", sim.StateSleeping, 0, false},
		{"awake upstairs", sim.StateIdle, 7, false},
		{"asleep upstairs", sim.StateSleeping, 7, false},
	} {
		if got := sim.DrawableIntoBuildingConversation(tc.state, tc.scope); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRoomScopeIn(t *testing.T) {
	structures := map[sim.StructureID]*sim.Structure{
		"inn": {ID: "inn", Rooms: []*sim.Room{
			{ID: 1, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
			{ID: 2, StructureID: "inn", Name: "common"},
		}},
	}
	for _, tc := range []struct {
		name      string
		structure sim.StructureID
		room      sim.RoomID
		want      sim.RoomID
	}{
		{"bedroom is scoped", "inn", 1, 1},
		{"common room is public", "inn", 2, 0},
		{"no room", "inn", 0, 0},
		{"unknown room", "inn", 9, 0},
		{"unknown structure", "barn", 1, 0},
		{"outdoors", "", 1, 0},
	} {
		if got := sim.RoomScopeIn(structures, tc.structure, tc.room); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestEnsureColocatedHuddle_DoesNotDrawSleeper: an NPC asleep in the building is
// not pulled into the speaker's conversation, so the speaker stays alone.
func TestEnsureColocatedHuddle_DoesNotDrawSleeper(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
	})
	setActor(t, w, "bob", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.InsideStructureID = "tavern"
		a.State = sim.StateSleeping
	})

	ensureColocated(t, w, "alice")

	if h := huddleOf(t, w, "bob"); h != "" {
		t.Errorf("sleeping bob was drawn into huddle %q", h)
	}
	if h := huddleOf(t, w, "alice"); h != "" {
		t.Errorf("alice formed huddle %q with only a sleeper present", h)
	}
}
