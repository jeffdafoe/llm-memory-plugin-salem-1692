package perception

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// hired_hand_tended_golden_test.go — LLM-527 fixture. An onlooker stands at a
// business whose own keeper is away but whose work is being done by a hired hand.
//
// The live case: Constable Gideon Marsh stopped at the Ellis Farm on his rounds
// while Abraham Warren — a free laborer with no workplace of his own — worked it
// and Elizabeth Ellis was across the village. Both the shut cue and the
// co-presence line keyed on keeper presence alone, so his prompt read "You are
// outdoors by the Ellis Farm, with no one else here to hear you speak. The Ellis
// Farm is shut — no one is tending it." with a man visibly at work in front of
// him. That is the single most interesting thing a round can turn up, and the
// engine hid it.
//
// The golden pins the repaired scene: the farm does not read shut, and Abraham is
// named in "## Around you" as busy on Elizabeth's work. The scope rule that puts
// him in the constable's audience is asserted world-side in
// sim/business_tended_test.go; this fixture carries the resolved audience the way
// every other co-presence golden does.

// constableAtFarmWorkedByHiredHand — Gideon at the Ellis Farm's loiter pin,
// Abraham inside on a live job for the absent Elizabeth.
func constableAtFarmWorkedByHiredHand() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		constableID = sim.ActorID("gideon")
		handID      = sim.ActorID("abraham")
		keeperID    = sim.ActorID("elizabeth")
		farm        = sim.StructureID("ellis_farm")
	)
	zero := 0
	minuteOfDay := 14 * 60 // mid-afternoon — on shift, no sleep or return-to-post cue competing
	published := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	workingUntil := published.Add(2 * time.Hour)
	acceptedAt := published.Add(-time.Hour)
	farmPin := sim.WorldPos{X: 120, Y: 120}

	constable := &sim.ActorSnapshot{
		Kind:                 sim.KindNPCStateful,
		DisplayName:          "Constable Gideon Marsh",
		Role:                 "constable",
		State:                sim.StateIdle,
		Coins:                15,
		Pos:                  farmPin.Tile(), // outdoors, at the farm's loiter pin — his rounds stop
		Needs:                map[sim.NeedKey]int{},
		Acquaintances:        map[string]sim.Acquaintance{"Abraham Warren": {}, "Elizabeth Ellis": {}},
		ColocatedAudienceIDs: []sim.ActorID{handID},
	}
	hand := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Abraham Warren",
		Role:              "laborer",
		State:             sim.StateLaboring,
		InsideStructureID: farm, // at work in the farmyard; no WorkStructureID of his own
		Coins:             12,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Elizabeth Ellis": {}},
	}
	keeper := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Elizabeth Ellis",
		Role:            "farmer",
		State:           sim.StateIdle,
		WorkStructureID: farm,
		Pos:             sim.TilePos{X: 68, Y: 79}, // across the village, tending nothing
		Coins:           30,
		Needs:           map[sim.NeedKey]int{},
	}
	nm := minuteOfDay
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &nm,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			constableID: constable, handID: hand, keeperID: keeper,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			farm: plainStructure(farm, "Ellis Farm"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(farm): {
				ID:            sim.VillageObjectID(farm),
				DisplayName:   "Ellis Farm",
				Pos:           farmPin,
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
			},
		},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {
				ID:           1,
				WorkerID:     handID,
				EmployerID:   keeperID,
				Reward:       12,
				DurationMin:  180,
				State:        sim.LaborStateWorking,
				AcceptedAt:   &acceptedAt,
				WorkingUntil: &workingUntil,
			},
		},
	}
	return snap, constableID, nil
}

// TestSnapshotBusinessTended pins the perception mirror of sim.businessTendedAt
// directly rather than only through the golden. The three copies of the rule
// (live world, published snapshot, this one) resolve position slightly
// differently — this one measures to objectLoiterPin, the sim pair to
// ResolveLoiteringObject — so a shared fixture is not enough to keep them
// honest; each needs its own matrix. A drift here is what makes the rendered
// "is shut" line contradict the engine's own shut-business memory on the same
// tick, which is the LLM-526 half of this bug.
func TestSnapshotBusinessTended(t *testing.T) {
	const farm = sim.StructureID("ellis_farm")
	cases := []struct {
		name  string
		setup func(snap *sim.Snapshot)
		want  bool
	}{
		{"hand at work inside", func(snap *sim.Snapshot) {}, true},
		{"hand at the loiter pin", func(snap *sim.Snapshot) {
			snap.Actors["abraham"].InsideStructureID = ""
			snap.Actors["abraham"].Pos = snap.VillageObjects[sim.VillageObjectID(farm)].Pos.Tile()
		}, true},
		{"keeper back at her post", func(snap *sim.Snapshot) {
			delete(snap.LaborLedger, 1)
			snap.Actors["elizabeth"].InsideStructureID = farm
		}, true},
		{"en-route hand", func(snap *sim.Snapshot) {
			snap.LaborLedger[1].State = sim.LaborStateEnRoute
		}, false},
		{"wandered hand", func(snap *sim.Snapshot) {
			snap.Actors["abraham"].InsideStructureID = ""
			snap.Actors["abraham"].Pos = sim.TilePos{X: 90, Y: 90}
		}, false},
		{"sleeping hand", func(snap *sim.Snapshot) {
			snap.Actors["abraham"].State = sim.StateSleeping
		}, false},
		// The hand is at the farm, but his job is for a keeper of somewhere else —
		// he is passing the time here, not minding the place. Elizabeth stays the
		// farm's keeper (so it is still a business) and stays away (so it is shut).
		{"hand's job is for another keeper", func(snap *sim.Snapshot) {
			snap.Actors["john"] = &sim.ActorSnapshot{
				Kind: sim.KindNPCStateful, DisplayName: "John Ellis",
				State: sim.StateIdle, WorkStructureID: "tavern",
			}
			snap.LaborLedger[1].EmployerID = "john"
		}, false},
		{"nobody at all", func(snap *sim.Snapshot) {
			delete(snap.LaborLedger, 1)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap, constableID, _ := constableAtFarmWorkedByHiredHand()
			tc.setup(snap)

			if got := snapshotBusinessTended(snap, farm); got != tc.want {
				t.Errorf("snapshotBusinessTended = %v, want %v", got, tc.want)
			}
			// The shut cue is the inverse for an onlooker who doesn't work there.
			if got := isShutBusiness(snap, snap.Actors[constableID], farm); got == tc.want {
				t.Errorf("isShutBusiness = %v, want %v (the inverse of tended)", got, !tc.want)
			}
		})
	}
}
