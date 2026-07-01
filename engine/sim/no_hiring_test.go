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
// exactly the gap this subscriber fills. The capture is scoped to the actor class that
// actually reads ObservedNoHiring: a WORKLESS worker (the seek-work directory subject).

func noHiringKey(structure StructureID) ObservedStateKey {
	return ObservedStateKey{StructureID: structure, Condition: ObservedNoHiring}
}

// nhWorklessWorker is a workless seek-work subject: an agent NPC carrying AttrWorker
// with no workplace — the class buildSeekWorkPlaces (the sole reader of ObservedNoHiring)
// serves. cbAgent alone is an agent NPC but carries no worker attribute.
func nhWorklessWorker(id ActorID, inside StructureID) *Actor {
	a := cbAgent(id, "", inside) // no workplace → workless
	a.Attributes = map[string][]byte{AttrWorker: {}}
	return a
}

// A keeper on break is present (not shut) but cannot take on a worker → the arriving
// workless worker remembers the business as no-hiring. The closed-business capture records
// nothing here, so without this subscriber the business is never dropped from the directory.
func TestNoHiring_RecordsWhenKeeperResting(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["store"] = &Structure{ID: "store", DisplayName: "General Store"}
	keeper := cbAgent("josiah", "store", "store")
	keeper.State = StateResting
	w.Actors["josiah"] = keeper
	lewis := nhWorklessWorker("lewis", "store")
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
	lewis := nhWorklessWorker("lewis", "store")
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
// (ObservedClosed) — the no-hiring subscriber must NOT stamp it, and it clears any stale
// no-hiring belief so ObservedNoHiring only ever reflects the current condition.
func TestNoHiring_KeeperlessClearsAndLeftToClosedMemory(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["store"] = &Structure{ID: "store", DisplayName: "General Store"}
	w.Actors["josiah"] = cbAgent("josiah", "store", "elsewhere") // keeper away
	lewis := nhWorklessWorker("lewis", "store")
	lewis.Observed = NewObservedStates(map[ObservedStateKey]time.Time{
		noHiringKey("store"): now.Add(-time.Hour), // stale belief from an earlier break
	})
	w.Actors["lewis"] = lewis

	handleNoHiringOnArrival(w, arrivedInside("lewis", "store", now))

	if lewis.Observed.Len() != 0 {
		t.Fatalf("a keeperless business is the closed-business memory's job → the stale no-hiring belief clears, got %v", lewis.Observed)
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
	lewis := nhWorklessWorker("lewis", "inn")
	w.Actors["lewis"] = lewis

	handleNoHiringOnArrival(w, arrivedInside("lewis", "inn", now))

	if lewis.Observed.Len() != 0 {
		t.Fatalf("a sleeping keeper is the closed-business memory's job → no no-hiring stamp, got %v", lewis.Observed)
	}
}

// A non-worker NPC (a customer) doesn't consult the seek-work directory, so it accrues
// no no-hiring memory even at a resting-keeper business (code_review).
func TestNoHiring_NonWorkerIgnored(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["store"] = &Structure{ID: "store", DisplayName: "General Store"}
	keeper := cbAgent("josiah", "store", "store")
	keeper.State = StateResting
	w.Actors["josiah"] = keeper
	customer := cbAgent("ruth", "", "store") // agent NPC but NOT a worker (no AttrWorker)
	w.Actors["ruth"] = customer

	handleNoHiringOnArrival(w, arrivedInside("ruth", "store", now))

	if customer.Observed.Len() != 0 {
		t.Fatalf("a non-worker NPC does not use the seek-work directory → no no-hiring memory, got %v", customer.Observed)
	}
}

// An employed worker (a resolvable workplace) is steered to its own post by the duty
// steer, not the seek-work directory, so it accrues no no-hiring memory when visiting
// another business whose keeper is on break (code_review).
func TestNoHiring_EmployedWorkerIgnored(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["store"] = &Structure{ID: "store", DisplayName: "General Store"}
	w.Structures["blacksmith"] = &Structure{ID: "blacksmith", DisplayName: "Blacksmith"}
	keeper := cbAgent("josiah", "store", "store")
	keeper.State = StateResting
	w.Actors["josiah"] = keeper
	smith := cbAgent("ezekiel", "blacksmith", "store") // employed at the blacksmith
	smith.Attributes = map[string][]byte{AttrWorker: {}}
	w.Actors["ezekiel"] = smith

	handleNoHiringOnArrival(w, arrivedInside("ezekiel", "store", now))

	if smith.Observed.Len() != 0 {
		t.Fatalf("an employed worker uses the duty steer, not the seek-work directory → no no-hiring memory, got %v", smith.Observed)
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
