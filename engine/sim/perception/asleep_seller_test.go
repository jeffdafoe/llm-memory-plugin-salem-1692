package perception

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// asleep_seller_test.go — LLM-1010. A buy cue names a seller "here with you" in
// the buyer's building only when the pay call's bootstrap can draw him into the
// conversation: awake and not upstairs (sim.DrawableIntoBuildingConversation, the
// rule colocatedConversationalActors applies). A seller asleep in the building,
// or in his room upstairs, was named "Buy it now" and, since LLM-1007, opened a
// pay_with_item the substrate rejected.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "reseller_at_supplier_seller_asleep",
			summary: "LLM-1010: reseller_arrives_at_supplier_buy_here_no_huddle with the brewer asleep in the Brewery. " +
				"The pay call's bootstrap never draws a sleeper into the conversation, so the restock cue must not " +
				"name him 'here with you' or say 'Buy it now', and pay_with_item is not offered.",
			build: arrivalWithBrewer(func(snap *sim.Snapshot, anders *sim.ActorSnapshot) {
				anders.State = sim.StateSleeping
			}),
		},
		perceptionScenario{
			name: "reseller_at_supplier_seller_upstairs",
			summary: "LLM-1010: reseller_arrives_at_supplier_buy_here_no_huddle with the brewer awake but in the " +
				"upstairs hall of the Brewery. A scoped room is out of the common-room conversation (LLM-801), so " +
				"the bootstrap cannot reach him and the cue must not name him 'here with you'.",
			build: arrivalWithBrewer(func(snap *sim.Snapshot, anders *sim.ActorSnapshot) {
				st := snap.Structures["brewery"]
				st.Rooms = append(st.Rooms, &sim.Room{ID: 7, StructureID: "brewery", Kind: sim.RoomKindHall, Name: "upstairs_hall"})
				anders.InsideRoomID = 7
			}),
		},
	)
}

// arrivalWithBrewer wraps the LLM-286 arrival scenario, lets mut place the brewer
// where the bootstrap cannot reach him, and drops him from John's awake audience
// the way the world-side republish would.
func arrivalWithBrewer(mut func(*sim.Snapshot, *sim.ActorSnapshot)) func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
		snap, id, warrants := resellerArrivesAtSupplierBuyHereNoHuddle()
		st := *snap.Structures["brewery"]
		snap.Structures["brewery"] = &st
		mut(snap, snap.Actors["anders"])
		snap.Actors[id].ColocatedAudienceIDs = nil
		if snap.Actors["anders"].State == sim.StateSleeping {
			snap.Actors[id].ColocatedSleeperIDs = []sim.ActorID{"anders"}
		}
		return snap, id, warrants
	}
}

// TestSellerCoPresentFollowsBootstrapRule pins sellerCoPresent's building branch
// to sim.DrawableIntoBuildingConversation on both sides: the seller (asleep,
// upstairs) and the buyer (upstairs, where the bootstrap forms nothing). The
// huddle branch is unchanged: a huddle peer is reachable.
func TestSellerCoPresentFollowsBootstrapRule(t *testing.T) {
	snap := &sim.Snapshot{Structures: map[sim.StructureID]*sim.Structure{
		"inn": {ID: "inn", Rooms: []*sim.Room{
			{ID: 1, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
			{ID: 2, StructureID: "inn", Name: "common"},
		}},
	}}
	buyerDown := &sim.ActorSnapshot{InsideStructureID: "inn", InsideRoomID: 2}
	buyerUp := &sim.ActorSnapshot{InsideStructureID: "inn", InsideRoomID: 1}
	seller := func(state sim.ActorState, room sim.RoomID, huddle sim.HuddleID) *sim.ActorSnapshot {
		return &sim.ActorSnapshot{State: state, InsideStructureID: "inn", InsideRoomID: room, CurrentHuddleID: huddle}
	}
	for _, tc := range []struct {
		name   string
		buyer  *sim.ActorSnapshot
		seller *sim.ActorSnapshot
		want   bool
	}{
		{"awake in the common room", buyerDown, seller(sim.StateIdle, 2, ""), true},
		{"awake, no room", buyerDown, seller(sim.StateIdle, 0, ""), true},
		{"asleep in the common room", buyerDown, seller(sim.StateSleeping, 2, ""), false},
		{"awake upstairs", buyerDown, seller(sim.StateIdle, 1, ""), false},
		{"buyer upstairs, seller in the common room", buyerUp, seller(sim.StateIdle, 2, ""), false},
		{"sleeping seller who shares her huddle", &sim.ActorSnapshot{InsideStructureID: "inn", CurrentHuddleID: "h1"}, seller(sim.StateSleeping, 2, "h1"), true},
		// The bootstrap leaves an already-huddled buyer as she is, so a seller in a
		// different huddle in the same building is out of reach.
		{"buyer huddled elsewhere, awake seller downstairs", &sim.ActorSnapshot{InsideStructureID: "inn", InsideRoomID: 2, CurrentHuddleID: "h1"}, seller(sim.StateIdle, 2, "h2"), false},
		{"buyer huddled elsewhere, awake unhuddled seller downstairs", &sim.ActorSnapshot{InsideStructureID: "inn", InsideRoomID: 2, CurrentHuddleID: "h1"}, seller(sim.StateIdle, 2, ""), false},
	} {
		if got := buyerCoPresenceScope(snap, tc.buyer).sellerCoPresent(tc.seller); got != tc.want {
			t.Errorf("%s: sellerCoPresent = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestAsleepOrUpstairsSellerWithholdsPay asserts the gate, not only the prompt
// (code_review): the original awake arrival offers pay_with_item through the named
// building seller, and the asleep and upstairs variants do not.
func TestAsleepOrUpstairsSellerWithholdsPay(t *testing.T) {
	for name, want := range map[string]bool{
		"reseller_arrives_at_supplier_buy_here_no_huddle": true,
		"reseller_at_supplier_seller_asleep":              false,
		"reseller_at_supplier_seller_upstairs":            false,
	} {
		var sc *perceptionScenario
		for i := range perceptionScenarios {
			if perceptionScenarios[i].name == name {
				sc = &perceptionScenarios[i]
			}
		}
		if sc == nil {
			t.Fatalf("scenario %q missing", name)
		}
		snap, id, warrants := sc.build()
		p := Build(snap, id, warrants)
		if len(p.Surroundings.HuddleMembers) != 0 {
			t.Fatalf("%s: fixture has a huddle peer, so the gate test would not isolate the building seller", name)
		}
		if got := CanPayWithItem(p); got != want {
			t.Errorf("%s: CanPayWithItem = %v, want %v", name, got, want)
		}
	}
}
