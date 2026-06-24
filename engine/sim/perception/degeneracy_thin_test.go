package perception

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// degeneracy_thin_test.go — LLM-94 Stage-1 perception thinning. A degeneracy-
// flagged actor's place-naming movement steers are dropped (subtractively) so
// nothing prompts the futile walk; the at-post stabilizer and every non-
// movement section are untouched, and the thinning lifts the moment the flag
// clears (DegenStage→None) since it is recomputed from the snapshot each tick.

func TestDegeneracyFlagged(t *testing.T) {
	if degeneracyFlagged(nil) {
		t.Error("a nil actor must read as not flagged")
	}
	if degeneracyFlagged(&sim.ActorSnapshot{DegenStage: sim.DegeneracyNone}) {
		t.Error("DegeneracyNone must read as not flagged")
	}
	if !degeneracyFlagged(&sim.ActorSnapshot{DegenStage: sim.DegeneracyFlagged}) {
		t.Error("DegeneracyFlagged must read as flagged")
	}
	if !degeneracyFlagged(&sim.ActorSnapshot{DegenStage: sim.DegeneracyThrottled}) {
		t.Error("DegeneracyThrottled must read as flagged (Stage-1 thinning stays engaged at Stage 2)")
	}
}

func TestThinDegenerateSteer_DropsGoArmAndErrands(t *testing.T) {
	p := &Payload{
		DutySteer:  &DutySteerView{ToWork: true, TargetID: "tavern", TargetLabel: "the Tavern"},
		Restocking: &RestockingView{},
		Forage:     &ForageView{},
	}
	thinDegenerateSteer(p)
	if p.DutySteer != nil {
		t.Errorf("a to-work steer (TargetID set) must be dropped, got %+v", p.DutySteer)
	}
	if p.Restocking != nil {
		t.Error("the restock errand must be dropped")
	}
	if p.Forage != nil {
		t.Error("the forage errand must be dropped")
	}
}

func TestThinDegenerateSteer_KeepsAtPostStabilizerClearsForageErrand(t *testing.T) {
	// The at-post stabilizer carries no TargetID — a "stay put" cue, not a "go"
	// cue — so it survives. But its ForageErrand modifier (the "step out to your
	// bushes" reframe) must be cleared in lockstep with Forage, or the actor
	// reads a step-out line with no harvest cue behind it.
	end := 180
	p := &Payload{
		DutySteer: &DutySteerView{AtPost: true, ShiftEndMin: &end, ForageErrand: true},
		Forage:    &ForageView{},
	}
	thinDegenerateSteer(p)
	if p.DutySteer == nil || !p.DutySteer.AtPost {
		t.Fatalf("the at-post stabilizer must survive thinning, got %+v", p.DutySteer)
	}
	if p.DutySteer.ForageErrand {
		t.Error("ForageErrand must be cleared when the forage cue is thinned away")
	}
	if p.DutySteer.ShiftEndMin == nil {
		t.Error("the close-time clause (ShiftEndMin) is not a movement cue — it must survive")
	}
	if p.Forage != nil {
		t.Error("the forage cue itself must be dropped")
	}
}

func TestThinDegenerateSteer_KeepsPlacelessWander(t *testing.T) {
	// A directionless off-shift nudge (ToWork false, no TargetID) names no place
	// to walk to, so it is not a futile-move driver — keep it.
	p := &Payload{DutySteer: &DutySteerView{ToWork: false}}
	thinDegenerateSteer(p)
	if p.DutySteer == nil {
		t.Error("a placeless steer (no TargetID) must survive thinning")
	}
}

func TestThinDegenerateSteer_NilSteer(t *testing.T) {
	// A flagged actor with no steer at all must thin cleanly (no panic).
	p := &Payload{Restocking: &RestockingView{}, Forage: &ForageView{}}
	thinDegenerateSteer(p)
	if p.Restocking != nil || p.Forage != nil {
		t.Error("errands must still be dropped when there is no DutySteer")
	}
}

// TestBuild_FlaggedActor_ThinsForageErrand drives the thinning end to end
// through Build, proving the snapshot→Build→thin wiring (a unit test of
// thinDegenerateSteer alone can't catch a mis-placed or missing call site).
// The fixture mirrors TestBuild_ForageErrandWiring's actionable harvest setup:
// Prudence on-shift at her own apothecary, berry shelf low, remembering her own
// ripe bush — which Build resolves into p.Forage + an at-post ForageErrand
// steer. Flagging the actor must thin both away.
func TestBuild_FlaggedActor_ThinsForageErrand(t *testing.T) {
	base := func(stage sim.DegeneracyStage) *sim.Snapshot {
		seller := &sim.ActorSnapshot{
			DisplayName:        "Prudence Ward",
			Kind:               sim.KindNPCStateful,
			BusinessownerState: &sim.BusinessownerState{},
			WorkStructureID:    "apothecary",
			InsideStructureID:  "apothecary",
			ScheduleStartMin:   dutyMinPtr(480),
			ScheduleEndMin:     dutyMinPtr(1080),
			Inventory:          map[sim.ItemKind]int{"raspberries": 1},
			RestockPolicy:      &sim.RestockPolicy{Restock: []sim.RestockEntry{{Item: "raspberries", Source: sim.RestockSourceForage, Max: 10}}},
			KnownPlaces: map[sim.PlaceRef]*sim.KnownPlace{
				"bushA": {Ref: "bushA", Kind: sim.PlaceKindObject, Affordances: []string{"gather:raspberries"}},
			},
			DegenStage: stage,
		}
		return &sim.Snapshot{
			Actors:     map[sim.ActorID]*sim.ActorSnapshot{"prudence": seller},
			Structures: map[sim.StructureID]*sim.Structure{"apothecary": {ID: "apothecary", DisplayName: "PW Apothecary"}},
			VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
				"bushA": {OwnerActorID: "prudence", Refreshes: []*sim.ObjectRefresh{
					{Amount: 0, GatherItem: "raspberries", AvailableQuantity: dutyMinPtr(10)},
				}},
			},
			RestockReorderPct: 25,
			LocalMinuteOfDay:  dutyMinPtr(600),
		}
	}

	// Baseline: an unflagged actor gets the forage cue + the ForageErrand steer.
	unflagged := Build(base(sim.DegeneracyNone), "prudence", nil)
	if unflagged.Forage == nil {
		t.Fatal("setup: expected the forage cue for the unflagged actor")
	}
	if unflagged.DutySteer == nil || !unflagged.DutySteer.ForageErrand {
		t.Fatalf("setup: expected an at-post ForageErrand steer, got %+v", unflagged.DutySteer)
	}

	// Flagged: the forage cue is thinned away and the at-post stabilizer drops
	// its step-out reframe, leaving the plain stay-put line.
	flagged := Build(base(sim.DegeneracyFlagged), "prudence", nil)
	if flagged.Forage != nil {
		t.Errorf("a flagged actor's forage cue must be thinned away, got %+v", flagged.Forage)
	}
	if flagged.DutySteer == nil || !flagged.DutySteer.AtPost {
		t.Fatalf("the at-post stabilizer must survive, got %+v", flagged.DutySteer)
	}
	if flagged.DutySteer.ForageErrand {
		t.Error("the surviving stabilizer must not keep the ForageErrand step-out reframe")
	}
}
