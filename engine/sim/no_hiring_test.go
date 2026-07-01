package sim

import (
	"testing"
	"time"
)

// no_hiring_test.go — LLM-210 capture subscriber. White-box (package sim), reusing the
// closed_business_test.go fixtures (cbWorld / cbAgent / arrivedInside) since the no-hiring
// capture shares the same arrival plumbing (businessArrivedAt). The distinguishing axis
// is keeper STATE: a resting keeper is "present" — so the closed-business capture records
// nothing (TestClosedBusiness_RestingKeeperStaysPresent) — yet is not hireable, which is
// exactly the gap this subscriber fills.

func noHiringKey(structure StructureID) ObservedStateKey {
	return ObservedStateKey{StructureID: structure, Condition: ObservedNoHiring}
}

// A keeper on break is present (not shut) but cannot take on a worker → the arriving
// worker remembers the business as no-hiring. The closed-business capture records nothing
// here, so without this subscriber the business is never dropped from the directory.
func TestNoHiring_RecordsWhenKeeperResting(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["store"] = &Structure{ID: "store", DisplayName: "General Store"}
	keeper := cbAgent("josiah", "store", "store")
	keeper.State = StateResting
	w.Actors["josiah"] = keeper
	lewis := cbAgent("lewis", "", "store")
	w.Actors["lewis"] = lewis

	handleNoHiringOnArrival(w, arrivedInside("lewis", "store", now))

	if _, ok := lewis.Observed.At(noHiringKey("store")); !ok {
		t.Fatalf("a resting keeper is present but not hireable → expected a no-hiring memory, got %v", lewis.Observed)
	}
}

// A keeper present and awake (idle) can take on a worker → no memory, and any stale
// no-hiring belief clears (self-heal).
func TestNoHiring_HireableKeeperNoRecordAndClears(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["store"] = &Structure{ID: "store", DisplayName: "General Store"}
	keeper := cbAgent("josiah", "store", "store")
	keeper.State = StateIdle
	w.Actors["josiah"] = keeper
	lewis := cbAgent("lewis", "", "store")
	lewis.Observed = NewObservedStates(map[ObservedStateKey]time.Time{
		noHiringKey("store"): now.Add(-time.Hour), // stale belief from an earlier break
	})
	w.Actors["lewis"] = lewis

	handleNoHiringOnArrival(w, arrivedInside("lewis", "store", now))

	if _, ok := lewis.Observed.At(noHiringKey("store")); ok {
		t.Fatalf("a hireable keeper must clear the stale no-hiring memory, got %v", lewis.Observed)
	}
}

// A keeperless business (worker elsewhere) is the closed-business capture's job
// (ObservedClosed) — the no-hiring subscriber must NOT also stamp it.
func TestNoHiring_KeeperlessLeftToClosedMemory(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["store"] = &Structure{ID: "store", DisplayName: "General Store"}
	w.Actors["josiah"] = cbAgent("josiah", "store", "elsewhere") // keeper away
	lewis := cbAgent("lewis", "", "store")
	w.Actors["lewis"] = lewis

	handleNoHiringOnArrival(w, arrivedInside("lewis", "store", now))

	if lewis.Observed.Len() != 0 {
		t.Fatalf("a keeperless business is left to the closed-business memory → no no-hiring stamp, got %v", lewis.Observed)
	}
}

// A sleeping keeper reads keeperless to keeperPresentAt (only awake counts), so the
// no-hiring subscriber leaves it to the closed-business memory too — it must not
// double-record a sleeping keeper as no-hiring.
func TestNoHiring_SleepingKeeperLeftToClosedMemory(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["inn"] = &Structure{ID: "inn", DisplayName: "Inn"}
	keeper := cbAgent("hannah", "inn", "inn")
	keeper.State = StateSleeping
	w.Actors["hannah"] = keeper
	lewis := cbAgent("lewis", "", "inn")
	w.Actors["lewis"] = lewis

	handleNoHiringOnArrival(w, arrivedInside("lewis", "inn", now))

	if lewis.Observed.Len() != 0 {
		t.Fatalf("a sleeping keeper is the closed-business memory's job → no no-hiring stamp, got %v", lewis.Observed)
	}
}

// PCs don't perceive the seek-work directory, so they accrue no no-hiring memory.
func TestNoHiring_NonAgentIgnored(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["store"] = &Structure{ID: "store", DisplayName: "General Store"}
	keeper := cbAgent("josiah", "store", "store")
	keeper.State = StateResting
	w.Actors["josiah"] = keeper
	pc := &Actor{ID: "player", Kind: KindPC, InsideStructureID: "store"}
	w.Actors["player"] = pc

	handleNoHiringOnArrival(w, arrivedInside("player", "store", now))

	if pc.Observed.Len() != 0 {
		t.Fatalf("PCs don't accrue no-hiring memory, got %v", pc.Observed)
	}
}
