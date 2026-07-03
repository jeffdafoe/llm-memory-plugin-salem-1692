package sim

import (
	"testing"
	"time"
)

// labor_relocate_internal_test.go — LLM-229. White-box (package sim) coverage of
// the relocation mechanics that AcceptWork's external tests can't reach: the
// at-workpost predicate, the hired-worker entry grant, the arrival subscriber's
// en-route → working flip, and the bounded-wait backstop. The inside-structure
// path needs no loiter geometry — actorAtWorkpost short-circuits on
// InsideStructureID — so a bare world with Actors + LaborLedger suffices.

func lrWorld() *World {
	return &World{
		Actors:         make(map[ActorID]*Actor),
		Structures:     make(map[StructureID]*Structure),
		VillageObjects: make(map[VillageObjectID]*VillageObject),
		Assets:         make(map[AssetID]*Asset),
		LaborLedger:    make(map[LaborID]*LaborOffer),
	}
}

func lrActor(id ActorID, work, inside StructureID) *Actor {
	return &Actor{ID: id, Kind: KindNPCStateful, State: StateIdle, WorkStructureID: work, InsideStructureID: inside}
}

func lrIntp(i int) *int { return &i }

func lrEnRouteOffer(id LaborID, worker, employer ActorID, at time.Time) *LaborOffer {
	acc := at
	return &LaborOffer{
		ID: id, WorkerID: worker, EmployerID: employer,
		Reward: 5, DurationMin: 120,
		State:           LaborStateEnRoute,
		AcceptedAt:      &acc,
		EnRouteDeadline: acc.Add(LaborEnRouteWaitDefault),
	}
}

// placeInteriorShop seeds a walkable structure with a door (an interior) so
// effectiveLoiterTile / structureEntryTile resolve, and returns its loiter pin.
func placeInteriorShop(w *World, id StructureID) TilePos {
	asset := &Asset{ID: AssetID(id) + "-asset", DoorOffsetX: lrIntp(0), DoorOffsetY: lrIntp(2)}
	vobj := &VillageObject{ID: VillageObjectID(id), AssetID: asset.ID, Pos: WorldPos{X: 320, Y: 320}}
	w.Assets[asset.ID] = asset
	w.VillageObjects[vobj.ID] = vobj
	w.Structures[id] = &Structure{ID: id, DisplayName: string(id)}
	return computeLoiterTile(vobj, asset)
}

func TestActorAtWorkpost_InsideVsLoiter(t *testing.T) {
	w := lrWorld()
	pin := placeInteriorShop(w, "store")

	inside := lrActor("a", "", "store")
	if !actorAtWorkpost(w, inside, "store") {
		t.Errorf("an actor inside the structure should be at the post")
	}
	// An interior shop requires being INSIDE — a worker standing at the loiter
	// pin outside the door is NOT at the post (they can't help through the wall,
	// and it's the "not entered ahead of the owner" spot).
	atLoiter := lrActor("b", "", "")
	atLoiter.Pos = pin
	if actorAtWorkpost(w, atLoiter, "store") {
		t.Errorf("an actor at the loiter pin of an interior shop is NOT at the post (must be inside)")
	}
	elsewhere := lrActor("c", "", "tavern")
	if actorAtWorkpost(w, elsewhere, "store") {
		t.Errorf("an actor inside a different structure is not at the post")
	}
	if actorAtWorkpost(w, inside, "") {
		t.Errorf("an empty work structure is never a post")
	}
}

func TestWorkerHiredAt(t *testing.T) {
	w := lrWorld()
	w.Actors["josiah"] = lrActor("josiah", "store", "") // owner NOT at the post yet
	w.LaborLedger[1] = lrEnRouteOffer(1, "ezekiel", "josiah", time.Now().UTC())

	// EnRoute + owner absent → NOT admitted: a relocating worker never enters
	// ahead of the owner (the invariant enforced at the membership gate).
	if workerHiredAt(w, "ezekiel", "store") {
		t.Errorf("an EnRoute worker must not be admitted while the owner is away")
	}
	// EnRoute + owner present → admitted (the owner is here to pull them in).
	w.Actors["josiah"].InsideStructureID = "store"
	if !workerHiredAt(w, "ezekiel", "store") {
		t.Errorf("an EnRoute worker should be admitted once the owner is at the post")
	}
	if workerHiredAt(w, "ezekiel", "tavern") {
		t.Errorf("the grant is scoped to the employer's own work structure")
	}
	if workerHiredAt(w, "someone_else", "store") {
		t.Errorf("only the offer's worker is granted entry")
	}
	// A merely pending offer never grants entry — the hire isn't struck yet.
	w.LaborLedger[1].State = LaborStatePending
	if workerHiredAt(w, "ezekiel", "store") {
		t.Errorf("a pending offer must not grant workplace entry")
	}
	// A Working offer keeps the grant even if the owner steps out mid-job.
	w.LaborLedger[1].State = LaborStateWorking
	w.Actors["josiah"].InsideStructureID = ""
	if !workerHiredAt(w, "ezekiel", "store") {
		t.Errorf("a Working offer keeps the worker's entry grant")
	}
}

func TestLaborArrival_StartsWhenWorkerAtPostWithOwner(t *testing.T) {
	w := lrWorld()
	now := time.Date(2026, 6, 24, 13, 0, 0, 0, time.UTC)
	w.Actors["josiah"] = lrActor("josiah", "store", "store") // owner at the post
	worker := lrActor("ezekiel", "", "store")                // worker arrived inside
	w.Actors["ezekiel"] = worker
	offer := lrEnRouteOffer(1, "ezekiel", "josiah", now.Add(-5*time.Minute))
	w.LaborLedger[1] = offer

	handleLaborArrivalOnArrival(w, arrivedInside("ezekiel", "store", now))

	if offer.State != LaborStateWorking {
		t.Fatalf("offer State = %q, want working once at the post with the owner", offer.State)
	}
	if offer.WorkStartedAt == nil || !offer.WorkStartedAt.Equal(now) {
		t.Errorf("WorkStartedAt = %v, want the arrival time %v", offer.WorkStartedAt, now)
	}
	if offer.WorkingUntil == nil || !offer.WorkingUntil.Equal(now.Add(120*time.Minute)) {
		t.Errorf("WorkingUntil = %v, want arrival + 120m", offer.WorkingUntil)
	}
	if worker.State != StateLaboring || worker.LaborID != 1 || worker.LaboringUntil == nil {
		t.Errorf("worker mirror not set on start: State=%q LaborID=%d LaboringUntil=%v", worker.State, worker.LaborID, worker.LaboringUntil)
	}
}

func TestLaborArrival_WaitsAtLoiterWhenOwnerAbsent(t *testing.T) {
	w := lrWorld()
	now := time.Now().UTC()
	pin := placeInteriorShop(w, "store")
	w.Actors["josiah"] = lrActor("josiah", "store", "tavern") // owner NOT at the post
	// Worker has reached the store but waits at the loiter pin OUTSIDE — never
	// enters ahead of the owner (InsideStructureID stays empty).
	worker := lrActor("ezekiel", "", "")
	worker.Pos = pin
	w.Actors["ezekiel"] = worker
	offer := lrEnRouteOffer(1, "ezekiel", "josiah", now.Add(-time.Minute))
	w.LaborLedger[1] = offer

	handleLaborArrivalOnArrival(w, &ActorArrived{ActorID: "ezekiel", At: now})

	if offer.State != LaborStateEnRoute {
		t.Fatalf("offer State = %q, want en_route while the owner is away", offer.State)
	}
	if !offer.EnRouteWaiting {
		t.Errorf("EnRouteWaiting = false, want true (worker waits at the loiter for the owner)")
	}
	if worker.InsideStructureID != "" {
		t.Errorf("worker entered ahead of the owner (InsideStructureID = %q) — must wait outside", worker.InsideStructureID)
	}
	if worker.State == StateLaboring {
		t.Errorf("worker must not be laboring before the owner shows")
	}
}

func TestLaborArrival_OwnerArrivalStartsWaitingWorker(t *testing.T) {
	w := lrWorld()
	now := time.Now().UTC()
	// Worker has been waiting inside the store; the owner now arrives at the post.
	worker := lrActor("ezekiel", "", "store")
	w.Actors["ezekiel"] = worker
	w.Actors["josiah"] = lrActor("josiah", "store", "store") // owner arrived
	offer := lrEnRouteOffer(1, "ezekiel", "josiah", now.Add(-10*time.Minute))
	offer.EnRouteWaiting = true
	w.LaborLedger[1] = offer

	handleLaborArrivalOnArrival(w, arrivedInside("josiah", "store", now))

	if offer.State != LaborStateWorking {
		t.Fatalf("offer State = %q, want working once the owner arrives to a waiting worker", offer.State)
	}
	if worker.State != StateLaboring {
		t.Errorf("worker State = %q, want laboring", worker.State)
	}
}

func TestLaborSweep_VoidsEnRoutePastDeadline(t *testing.T) {
	w := lrWorld()
	now := time.Now().UTC()
	worker := lrActor("ezekiel", "", "")
	w.Actors["ezekiel"] = worker
	w.Actors["josiah"] = lrActor("josiah", "store", "")
	offer := lrEnRouteOffer(1, "ezekiel", "josiah", now.Add(-2*time.Hour))
	offer.EnRouteDeadline = now.Add(-time.Minute) // deadline already passed
	w.LaborLedger[1] = offer

	if _, err := EvaluateLaborLedgerSweep(now).Fn(w); err != nil {
		t.Fatalf("EvaluateLaborLedgerSweep: %v", err)
	}

	if offer.State != LaborStateFailedUnavailable {
		t.Fatalf("offer State = %q, want failed_unavailable (never reached the post before the deadline)", offer.State)
	}
	// No work happened and no worker mirror was ever set — nothing to free, but
	// the worker must certainly not be left laboring.
	if worker.State == StateLaboring || worker.LaborID != 0 {
		t.Errorf("worker left committed after an unpaid void: State=%q LaborID=%d", worker.State, worker.LaborID)
	}
}
