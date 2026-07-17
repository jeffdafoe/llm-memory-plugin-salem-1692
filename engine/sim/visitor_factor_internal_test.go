package sim

import (
	"math/rand"
	"testing"
)

// visitor_factor_internal_test.go — LLM-410 wholesale factor spawn internals: the landing
// weight, the factor pack seed, and the distributor-targeted arrival picker. Package-internal
// (these helpers are unexported); the end-to-end spawn wiring is in visitor_factor_test.go.

// TestLandingWeightPermille — the factor is rarity-tuned (300); every other archetype lands
// always (1000); an unknown archetype defaults to always-land.
func TestLandingWeightPermille(t *testing.T) {
	if got := landingWeightPermille(FactorArchetype); got != FactorLandingWeightPermille {
		t.Errorf("factor landing weight = %d, want %d", got, FactorLandingWeightPermille)
	}
	if got := landingWeightPermille("peddler"); got != DefaultLandingWeightPermille {
		t.Errorf("peddler landing weight = %d, want %d (always lands)", got, DefaultLandingWeightPermille)
	}
	if got := landingWeightPermille("no-such-archetype"); got != DefaultLandingWeightPermille {
		t.Errorf("unknown archetype landing weight = %d, want %d", got, DefaultLandingWeightPermille)
	}
}

// TestSeedFactorPack — a factor carries every factorWareKind (unitsPerKind..+1 of each),
// an iron shipment (ironUnits..+2 — LLM-442), a salt shipment (saltUnits..+2 — LLM-444),
// and a purse inside the configured [min,max]; a min==max range gives a fixed purse.
func TestSeedFactorPack(t *testing.T) {
	valid := map[ItemKind]bool{factorIronKind: true, factorSaltKind: true}
	for _, k := range factorWareKinds {
		valid[k] = true
	}
	for seed := int64(0); seed < 50; seed++ {
		pack, purse := seedFactorPack(rand.New(rand.NewSource(seed)), 2, 10, 12, 120, 200)
		if len(pack) != len(factorWareKinds)+2 {
			t.Fatalf("seed %d: pack has %d kinds, want %d (one per factorWareKind plus iron and salt)", seed, len(pack), len(factorWareKinds)+2)
		}
		for kind, qty := range pack {
			if !valid[kind] {
				t.Errorf("seed %d: pack carries %q, not a factorWareKind", seed, kind)
			}
			if kind == factorIronKind {
				if qty < 10 || qty > 12 {
					t.Errorf("seed %d: iron qty %d out of [10,12]", seed, qty)
				}
				continue
			}
			if kind == factorSaltKind {
				if qty < 12 || qty > 14 {
					t.Errorf("seed %d: salt qty %d out of [12,14]", seed, qty)
				}
				continue
			}
			if qty < 2 || qty > 3 {
				t.Errorf("seed %d: %q qty %d out of [2,3]", seed, kind, qty)
			}
		}
		if purse < 120 || purse > 200 {
			t.Errorf("seed %d: purse %d out of [120,200]", seed, purse)
		}
	}
	if _, purse := seedFactorPack(rand.New(rand.NewSource(1)), 1, 1, 1, 150, 150); purse != 150 {
		t.Errorf("purse = %d, want 150 when min==max", purse)
	}
}

// TestCloneVisitorState_DistributorOnly guards that the clone/snapshot copy path carries the
// factor flag (LLM-410). cloneVisitorState backs ActorSnapshot publication (world.go), the
// mem-repo boundary, and the ActorDeparted event; a field-by-field copy that dropped
// DistributorOnly would let a live factor lose its gate between snapshots even though the
// plan-jsonb persistence round-trips.
func TestCloneVisitorState_DistributorOnly(t *testing.T) {
	cp := cloneVisitorState(&VisitorState{Archetype: FactorArchetype, Origin: FactorOrigin, DistributorOnly: true})
	if cp == nil || !cp.DistributorOnly {
		t.Fatalf("cloneVisitorState dropped DistributorOnly: %+v", cp)
	}
	if cloneVisitorState(&VisitorState{}).DistributorOnly {
		t.Error("cloneVisitorState invented DistributorOnly on an ordinary traveler")
	}
}

// TestPickDistributorArrival — the factor targets the distributor-tagged structure (smallest ID
// on a tie); an ordinary traveler targets the tavern; a factor in a village with no distributor
// falls back to the tavern anchor.
func TestPickDistributorArrival(t *testing.T) {
	w := &World{
		VillageObjects: map[VillageObjectID]*VillageObject{
			"store_b": {ID: "store_b", Pos: WorldPos{X: 200, Y: 200}, Tags: []string{TagDistributor}},
			"store_a": {ID: "store_a", Pos: WorldPos{X: 100, Y: 100}, Tags: []string{TagDistributor}},
			"tavern":  {ID: "tavern", Pos: WorldPos{X: 300, Y: 300}, Tags: []string{VisitorTagTavern}},
		},
		Structures: map[StructureID]*Structure{
			"store_a": {ID: "store_a"},
			"store_b": {ID: "store_b"},
			"tavern":  {ID: "tavern"},
		},
	}
	if id, _, ok := pickDistributorDestination(w); !ok || id != "store_a" {
		t.Fatalf("pickDistributorDestination = (%q, %v), want (store_a, true) — smallest-ID distributor", id, ok)
	}
	if fid, _, fok := pickArrivalDestination(w, true); !fok || fid != "store_a" {
		t.Errorf("factor arrival = (%q, %v), want (store_a, true)", fid, fok)
	}
	if oid, _, ook := pickArrivalDestination(w, false); !ook || oid != "tavern" {
		t.Errorf("ordinary arrival = (%q, %v), want (tavern, true)", oid, ook)
	}
	// A distributor-tagged object NOT backed by a structure is not a valid target.
	delete(w.Structures, "store_a")
	delete(w.Structures, "store_b")
	if _, _, ok := pickDistributorDestination(w); ok {
		t.Error("pickDistributorDestination should reject a distributor object with no backing structure")
	}
	if fid, _, fok := pickArrivalDestination(w, true); !fok || fid != "tavern" {
		t.Errorf("factor arrival with no valid distributor = (%q, %v), want tavern fallback", fid, fok)
	}
}
