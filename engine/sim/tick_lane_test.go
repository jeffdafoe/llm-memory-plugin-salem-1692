package sim

import "testing"

// tick_lane_test.go — WarrantCycleLane (LLM-723): a cycle goes to the player
// lane only when a player character caused one of its warrants.

func TestWarrantCycleLane(t *testing.T) {
	w := &World{Actors: map[ActorID]*Actor{
		"jefferey": {ID: "jefferey", Kind: KindPC},
		"john":     {ID: "john", Kind: KindNPCStateful},
		"hannah":   {ID: "hannah", Kind: KindNPCShared},
	}}
	cases := []struct {
		name     string
		warrants []WarrantMeta
		want     TickLane
	}{
		{"empty cycle", nil, TickLaneVillage},
		{"npc trigger", []WarrantMeta{{TriggerActorID: "hannah"}}, TickLaneVillage},
		{"pc trigger", []WarrantMeta{{TriggerActorID: "jefferey"}}, TickLanePlayer},
		{"pc source only", []WarrantMeta{{SourceActorID: "jefferey"}}, TickLanePlayer},
		{"self-triggered", []WarrantMeta{{TriggerActorID: "john", SourceActorID: "john"}}, TickLaneVillage},
		{"unknown actor", []WarrantMeta{{TriggerActorID: "departed-visitor"}}, TickLaneVillage},
		// The John Ellis case: his own huddle_joined plus the peer-joined
		// warrant a player's arrival stamped.
		{"mixed cycle", []WarrantMeta{
			{TriggerActorID: "john", Reason: BasicWarrantReason{K: WarrantKindHuddleJoined}},
			{TriggerActorID: "jefferey", Reason: BasicWarrantReason{K: WarrantKindHuddlePeerJoined}},
		}, TickLanePlayer},
	}
	for _, tc := range cases {
		if got := WarrantCycleLane(w, tc.warrants); got != tc.want {
			t.Errorf("%s: lane = %v, want %v", tc.name, got, tc.want)
		}
	}
}
