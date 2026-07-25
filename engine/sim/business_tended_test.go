package sim

import (
	"testing"
)

// business_tended_test.go — LLM-527. businessTendedAt answers "is anyone minding
// this place", as distinct from keeperPresentAt's "is someone here with the
// authority to trade with me or take me on". A hired hand working a live job
// satisfies the first and not the second, and the two must stay separable: the
// shut-business memory, the shut dead-end cue, the cross-threshold huddle scope
// and the "already at, and it's shut" move reply all read presence, while the
// visitor trade-errand binding and the no-hiring memory still read the keeper.
//
// The live case behind it: Abraham Warren, a free laborer with no workplace of
// his own, worked the Ellis Farm through the afternoon while Elizabeth was across
// the village. The constable standing at the farm on his rounds was told the
// place was shut and that no one was there to hear him speak, with a man plainly
// at work in front of him.

// tendedTestWorld builds a world holding one business (a named, structure-backed
// object whose loiter pin is its anchor tile) plus the labor ledger the hired-hand
// arm reads. The keeper works there; the hand does not.
func tendedTestWorld() (w *World, pin TilePos) {
	z := 0
	w = &World{
		Actors:         make(map[ActorID]*Actor),
		Huddles:        make(map[HuddleID]*Huddle),
		Structures:     map[StructureID]*Structure{"farm": {ID: "farm"}},
		VillageObjects: make(map[VillageObjectID]*VillageObject),
		Assets:         map[AssetID]*Asset{"a": {ID: "a", Category: "structure"}},
		LaborLedger:    make(map[LaborID]*LaborOffer),
		actorsByHuddle: make(map[HuddleID]map[ActorID]struct{}),
	}
	w.VillageObjects["farm"] = &VillageObject{ID: "farm", AssetID: "a", DisplayName: "Ellis Farm",
		Pos: WorldPos{X: 160, Y: 160}, LoiterOffsetX: &z, LoiterOffsetY: &z}
	return w, WorldPos{X: 160, Y: 160}.Tile()
}

// hiredHandAtFarm places the keeper away from the farm and a hired hand inside it
// on a live (Working) job for that keeper — the Ellis Farm shape.
func hiredHandAtFarm(w *World) {
	w.Actors["elizabeth"] = &Actor{ID: "elizabeth", Kind: KindNPCStateful, WorkStructureID: "farm",
		Pos: TilePos{X: 68, Y: 79}} // across the village, tending nothing
	w.Actors["abraham"] = &Actor{ID: "abraham", Kind: KindNPCShared, State: StateLaboring,
		InsideStructureID: "farm"} // no WorkStructureID of his own — a free laborer
	w.LaborLedger[1] = &LaborOffer{ID: 1, WorkerID: "abraham", EmployerID: "elizabeth", State: LaborStateWorking}
}

// The core split: a hired hand makes the place TENDED without making it keepered.
// keeperPresentAt staying false is the load-bearing half — bindBuyErrand and the
// no-hiring memory read it, and a laborer can neither sell the stock nor hire.
func TestBusinessTendedAt_HiredHandTendsButIsNotKeeper(t *testing.T) {
	w, _ := tendedTestWorld()
	hiredHandAtFarm(w)

	if !businessTendedAt(w, "farm") {
		t.Error("hired hand at work inside: businessTendedAt = false, want true — someone IS minding the farm")
	}
	if keeperPresentAt(w, "farm") {
		t.Error("hired hand at work inside: keeperPresentAt = true, want false — he holds no keeper's authority")
	}
}

// Only a job actually under way counts. An EnRoute hand is still walking to the
// farm (or waiting at the door for the owner to show), so nobody is minding it
// yet — workerHiredAt takes the looser reading because it gates ENTRY instead.
func TestBusinessTendedAt_EnRouteHandDoesNotTend(t *testing.T) {
	w, _ := tendedTestWorld()
	hiredHandAtFarm(w)
	w.LaborLedger[1].State = LaborStateEnRoute

	if businessTendedAt(w, "farm") {
		t.Error("en-route hand: businessTendedAt = true, want false — the job hasn't started")
	}
}

// A job does not hold a place open once the hand walks off it — the same rule
// workerTendsStructure applies to a keeper who drifts away.
func TestBusinessTendedAt_WanderedHandDoesNotTend(t *testing.T) {
	w, _ := tendedTestWorld()
	hiredHandAtFarm(w)
	w.Actors["abraham"].InsideStructureID = ""
	w.Actors["abraham"].Pos = TilePos{X: 90, Y: 90} // far from the pin

	if businessTendedAt(w, "farm") {
		t.Error("wandered hand: businessTendedAt = true, want false — he is not at the farm")
	}
}

// Asleep is not tending, mirroring the LLM-126 gate on the keeper arm.
func TestBusinessTendedAt_SleepingHandDoesNotTend(t *testing.T) {
	w, _ := tendedTestWorld()
	hiredHandAtFarm(w)
	w.Actors["abraham"].State = StateSleeping

	if businessTendedAt(w, "farm") {
		t.Error("sleeping hand: businessTendedAt = true, want false")
	}
}

// The hand must be hired by THIS structure's keeper. A job for someone who works
// elsewhere doesn't open a farm he happens to be standing in.
func TestBusinessTendedAt_HandHiredElsewhereDoesNotTend(t *testing.T) {
	w, _ := tendedTestWorld()
	hiredHandAtFarm(w)
	w.Actors["elizabeth"].WorkStructureID = "tavern" // the employer keeps a different post

	if businessTendedAt(w, "farm") {
		t.Error("hand hired by another keeper: businessTendedAt = true, want false")
	}
}

// TestColocatedAudienceIDs_HiredHandOpensCrossThreshold is the LLM-527 scene
// itself: the constable stands at the farm's loiter pin — his rounds stop — while
// a hired hand works inside and the farm's own keeper is across the village.
// Before the fix loiterScopeConversable read keeperPresentAt, scoped him to open
// ground, and rendered "with no one else here to hear you speak" at a man in
// plain view. The threshold carries conversation because there is a PERSON on the
// other side of it, not because that person owns the shop.
func TestColocatedAudienceIDs_HiredHandOpensCrossThreshold(t *testing.T) {
	w, pin := tendedTestWorld()
	hiredHandAtFarm(w)
	w.Actors["gideon"] = &Actor{ID: "gideon", Kind: KindNPCStateful, Pos: pin}

	if got := colocatedAudienceIDs(w, w.Actors["gideon"], audienceNow()); !sameIDs(got, "abraham") {
		t.Errorf("hired hand at work: constable's audience = %v, want [abraham]", got)
	}

	// Send the hand home and the farm is genuinely deserted again — the LLM-359
	// wall still stands for an empty shop, so this is not a blanket widening.
	w.LaborLedger[1].State = LaborStateCompleted
	if got := colocatedAudienceIDs(w, w.Actors["gideon"], audienceNow()); got != nil {
		t.Errorf("job over: constable's audience = %v, want nil (nobody is minding the farm)", got)
	}
}
