package sim

import (
	"testing"
	"time"
)

// stall_hired_repair_internal_test.go — LLM-271. Internal (package sim) tests for
// the hired-worker repair path: the owner-or-hire target resolver, the on-hire
// wake stamp, and the reactor's laboring shelve-gate yielding to that wake. The
// end-to-end perception render is pinned by the golden harness
// (perception/golden_test.go) and the dispatch by the external StartRepair test.

func TestWearableStallToMend(t *testing.T) {
	prudenceShop := &VillageObject{ID: "shop", OwnerActorID: "prudence", Tags: []string{TagBusiness}}
	objects := map[VillageObjectID]*VillageObject{"shop": prudenceShop}
	working := func(worker, employer ActorID) map[LaborID]*LaborOffer {
		return map[LaborID]*LaborOffer{1: {ID: 1, WorkerID: worker, EmployerID: employer, State: LaborStateWorking}}
	}

	t.Run("owner resolves own business, not hired", func(t *testing.T) {
		got, hired := WearableStallToMend(objects, nil, "prudence")
		if got != prudenceShop || hired {
			t.Fatalf("got (%v, hired=%v), want (shop, false)", got, hired)
		}
	})
	t.Run("Working hired worker resolves employer's business, hired", func(t *testing.T) {
		got, hired := WearableStallToMend(objects, working("lewis", "prudence"), "lewis")
		if got != prudenceShop || !hired {
			t.Fatalf("got (%v, hired=%v), want (shop, true)", got, hired)
		}
	})
	t.Run("EnRoute worker does not resolve (not yet on post)", func(t *testing.T) {
		ledger := map[LaborID]*LaborOffer{1: {ID: 1, WorkerID: "lewis", EmployerID: "prudence", State: LaborStateEnRoute}}
		got, hired := WearableStallToMend(objects, ledger, "lewis")
		if got != nil || hired {
			t.Fatalf("got (%v, hired=%v), want (nil, false) for an EnRoute worker", got, hired)
		}
	})
	t.Run("worker hired by an owner of nothing wearable resolves nil", func(t *testing.T) {
		got, hired := WearableStallToMend(objects, working("lewis", "someone-else"), "lewis")
		if got != nil || hired {
			t.Fatalf("got (%v, hired=%v), want (nil, false)", got, hired)
		}
	})
	t.Run("neither owner nor hired resolves nil", func(t *testing.T) {
		got, hired := WearableStallToMend(objects, nil, "stranger")
		if got != nil || hired {
			t.Fatalf("got (%v, hired=%v), want (nil, false)", got, hired)
		}
	})
}

func TestMaybeStampHiredRepairWarrant(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	build := func(wear int, tags []string) (*World, *Actor, *Actor) {
		employer := &Actor{ID: "prudence", Kind: KindNPCStateful}
		worker := &Actor{ID: "lewis", Kind: KindNPCShared}
		w := &World{
			Actors: map[ActorID]*Actor{"prudence": employer, "lewis": worker},
			VillageObjects: map[VillageObjectID]*VillageObject{
				"shop": {ID: "shop", OwnerActorID: "prudence", Tags: tags, Wear: wear},
			},
			Settings: WorldSettings{StallWearRepairThreshold: 400, StallWearDegradeThreshold: 600},
		}
		return w, worker, employer
	}

	t.Run("worn business wakes the worker", func(t *testing.T) {
		w, worker, employer := build(450, []string{TagBusiness}) // >= repair 400
		maybeStampHiredRepairWarrant(w, worker, employer, now)
		if !hasHiredRepairWarrant(worker.Warrants) {
			t.Fatalf("want a hired-repair warrant on the worker; warrants=%+v", worker.Warrants)
		}
	})
	t.Run("un-worn business does not wake", func(t *testing.T) {
		w, worker, employer := build(100, []string{TagBusiness}) // < repair 400
		maybeStampHiredRepairWarrant(w, worker, employer, now)
		if hasHiredRepairWarrant(worker.Warrants) {
			t.Errorf("un-worn business must not wake the worker; warrants=%+v", worker.Warrants)
		}
	})
	t.Run("employer with no wearable business does not wake", func(t *testing.T) {
		w, worker, employer := build(450, nil) // no TagBusiness → not a wearable stall
		maybeStampHiredRepairWarrant(w, worker, employer, now)
		if hasHiredRepairWarrant(worker.Warrants) {
			t.Errorf("employer owns no wearable business; the worker must not be woken; warrants=%+v", worker.Warrants)
		}
	})
}

// TestActorCanReactNow_LaboringYieldsToHiredRepair pins the LLM-271 reactor change:
// a StateLaboring worker is shelved (the LLM-190 busy-with-the-job gate) EXCEPT when
// carrying the hired-repair warrant, which pierces the gate so the just-hired hand
// gets one tick to mend the worn business. The owner's plain StallRepair warrant does
// NOT pierce it — only the hired kind — since an owner is never laboring for someone
// else at their own stall. Mirrors TestActorCanReactNow_SourceActivityShelvesExceptInterrupts.
func TestActorCanReactNow_LaboringYieldsToHiredRepair(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	future := now.Add(30 * time.Minute)
	cases := []struct {
		name         string
		mutate       func(a *Actor)
		wantEligible bool
	}{
		{"laboring, no interrupt — shelved", func(a *Actor) {}, false},
		{"laboring + hired repair warrant — interrupts", func(a *Actor) {
			a.Warrants = []WarrantMeta{{Reason: StallRepairHiredWarrantReason{StallID: "shop"}}}
		}, true},
		{"laboring + OWNER repair warrant — still shelved (not a hire)", func(a *Actor) {
			a.Warrants = []WarrantMeta{{Reason: StallRepairWarrantReason{StallID: "shop"}}}
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := npc("lewis", KindNPCShared)
			a.State = StateLaboring
			a.LaboringUntil = &future
			tc.mutate(a)
			w := sleepTestWorld(a)
			eligible, stale := actorCanReactNow(w, a, now)
			if eligible != tc.wantEligible || stale {
				t.Errorf("got (eligible=%v, stale=%v), want (eligible=%v, stale=false)", eligible, stale, tc.wantEligible)
			}
		})
	}
}
