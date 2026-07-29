package sim

import (
	"testing"
	"time"
)

// seek_work_visited_test.go — LLM-563 capture subscriber. White-box (package sim),
// reusing the closed_business_test.go fixtures (cbWorld / cbAgent / arrivedInside)
// and the no_hiring_test.go workless-worker helper, since the visited capture
// shares the same arrival plumbing (businessArrivedAt) and the same subject gate
// (a workless worker — the seek-work directory's subject). The distinguishing
// axis is that this memory stamps on ANY qualifying arrival: no rejection event
// is required, which is exactly the gap it fills (a visit where nothing happened
// taught the directory nothing).

func visitedKey(structure StructureID) ObservedStateKey {
	return ObservedStateKey{StructureID: structure, Condition: ObservedSeekWorkVisited}
}

// A workless worker arriving at a business with a hireable keeper — the state
// where NONE of the three rejection memories stamp — earns a visited memory, so
// the directory can rank the business behind untried ones.
func TestSeekWorkVisited_StampsOnArrivalWithHireableKeeper(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["store"] = &Structure{ID: "store", DisplayName: "General Store"}
	keeper := cbAgent("josiah", "store", "store")
	keeper.State = StateIdle
	w.Actors["josiah"] = keeper
	patience := nhWorklessWorker("patience", "store")
	w.Actors["patience"] = patience

	handleSeekWorkVisitedOnArrival(w, arrivedInside("patience", "store", now))

	if got, ok := patience.Observed.At(visitedKey("store")); !ok || !got.Equal(now) {
		t.Fatalf("arrival at a business must stamp a visited memory at the arrival time, got (%v, %v)", got, ok)
	}
}

// The stamp is unconditional on a qualifying arrival: a resting keeper (the
// no-hiring case) still stamps visited — the sibling memories layer, they don't
// partition. The no-hiring DROP outranks the visited ranking while it lasts.
func TestSeekWorkVisited_StampsAlongsideNoHiring(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["store"] = &Structure{ID: "store", DisplayName: "General Store"}
	keeper := cbAgent("josiah", "store", "store")
	keeper.State = StateResting
	w.Actors["josiah"] = keeper
	patience := nhWorklessWorker("patience", "store")
	w.Actors["patience"] = patience

	handleSeekWorkVisitedOnArrival(w, arrivedInside("patience", "store", now))

	if _, ok := patience.Observed.At(visitedKey("store")); !ok {
		t.Fatalf("a resting-keeper arrival still stamps visited, got %v", patience.Observed)
	}
}

// A re-visit refreshes the stamp (Observe overwrites), so least-recently-visited
// ordering counts from the LAST call, not the first.
func TestSeekWorkVisited_RevisitRefreshesStamp(t *testing.T) {
	w := cbWorld()
	first := time.Now()
	second := first.Add(30 * time.Minute)
	w.Structures["store"] = &Structure{ID: "store", DisplayName: "General Store"}
	w.Actors["josiah"] = cbAgent("josiah", "store", "store")
	patience := nhWorklessWorker("patience", "store")
	w.Actors["patience"] = patience

	handleSeekWorkVisitedOnArrival(w, arrivedInside("patience", "store", first))
	handleSeekWorkVisitedOnArrival(w, arrivedInside("patience", "store", second))

	if got, _ := patience.Observed.At(visitedKey("store")); !got.Equal(second) {
		t.Fatalf("a re-visit must refresh the stamp to the later arrival, got %v want %v", got, second)
	}
}

// A non-worker NPC (a customer) doesn't consult the seek-work directory, so it
// accrues no visited memory — same scoping as the no-hiring capture.
func TestSeekWorkVisited_NonWorkerIgnored(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["store"] = &Structure{ID: "store", DisplayName: "General Store"}
	w.Actors["josiah"] = cbAgent("josiah", "store", "store")
	customer := cbAgent("ruth", "", "store") // agent NPC but NOT a worker (no AttrWorker)
	w.Actors["ruth"] = customer

	handleSeekWorkVisitedOnArrival(w, arrivedInside("ruth", "store", now))

	if customer.Observed.Len() != 0 {
		t.Fatalf("a non-worker NPC does not use the seek-work directory → no visited memory, got %v", customer.Observed)
	}
}

// An employed worker (a resolvable workplace) is steered by the duty steer, not
// the directory, so it accrues no visited memory when calling at another business.
func TestSeekWorkVisited_EmployedWorkerIgnored(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["store"] = &Structure{ID: "store", DisplayName: "General Store"}
	w.Structures["blacksmith"] = &Structure{ID: "blacksmith", DisplayName: "Blacksmith"}
	w.Actors["josiah"] = cbAgent("josiah", "store", "store")
	smith := cbAgent("ezekiel", "blacksmith", "store") // employed at the blacksmith
	smith.Attributes = map[string][]byte{AttrWorker: {}}
	w.Actors["ezekiel"] = smith

	handleSeekWorkVisitedOnArrival(w, arrivedInside("ezekiel", "store", now))

	if smith.Observed.Len() != 0 {
		t.Fatalf("an employed worker uses the duty steer, not the seek-work directory → no visited memory, got %v", smith.Observed)
	}
}

// PCs don't perceive the seek-work directory, so they accrue no visited memory.
func TestSeekWorkVisited_NonAgentIgnored(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["store"] = &Structure{ID: "store", DisplayName: "General Store"}
	w.Actors["josiah"] = cbAgent("josiah", "store", "store")
	pc := &Actor{ID: "player", Kind: KindPC, InsideStructureID: "store"}
	w.Actors["player"] = pc

	handleSeekWorkVisitedOnArrival(w, arrivedInside("player", "store", now))

	if pc.Observed.Len() != 0 {
		t.Fatalf("PCs don't accrue visited memory, got %v", pc.Observed)
	}
}

// An arrival somewhere that isn't a business (the worker's own home, a workerless
// residence) stamps nothing — businessArrivedAt already rejects it; pinned here so
// the gate can't silently widen to "every arrival anywhere".
func TestSeekWorkVisited_NonBusinessArrivalIgnored(t *testing.T) {
	w := cbWorld()
	now := time.Now()
	w.Structures["residence"] = &Structure{ID: "residence", DisplayName: "Walker Residence"}
	patience := nhWorklessWorker("patience", "residence")
	w.Actors["patience"] = patience

	handleSeekWorkVisitedOnArrival(w, arrivedInside("patience", "residence", now))

	if patience.Observed.Len() != 0 {
		t.Fatalf("a workerless residence is not a business → no visited memory, got %v", patience.Observed)
	}
}
