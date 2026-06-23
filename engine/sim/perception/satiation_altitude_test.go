package perception

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// satiation_altitude_test.go — ZBBS-HOME-363. The buy-menu altitude pass
// (dedup-by-structure, nearest-first, cap, exclude-own-workplace) and the
// out-of-stock experiential-memory surface.

func altitudeCatalog() map[sim.ItemKind]*sim.ItemKindDef {
	return map[sim.ItemKind]*sim.ItemKindDef{
		"bread": {Name: "bread", DisplayLabel: "bread", Category: sim.ItemCategoryFood, Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 6}}},
		"stew":  {Name: "stew", DisplayLabel: "stew", Category: sim.ItemCategoryFood, Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 12}}},
	}
}

// TestGatherSatiation_DedupByStructure: a vendor selling multiple hunger items
// at one workplace collapses to a SINGLE buy bullet — the strongest satisfier.
func TestGatherSatiation_DedupByStructure(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold}}
	keeper := &sim.ActorSnapshot{WorkStructureID: "tavern", Inventory: map[sim.ItemKind]int{"bread": 9, "stew": 4}}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "john": keeper},
		Structures: map[sim.StructureID]*sim.Structure{"tavern": {ID: "tavern", DisplayName: "The Tavern"}},
		ItemKinds:  altitudeCatalog(),
	}
	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 1 {
		t.Fatalf("want 1 pressing need, got %+v", v)
	}
	vendors := v.Needs[0].Vendors
	if len(vendors) != 1 {
		t.Fatalf("dedup: want 1 vendor bullet for the tavern, got %d: %+v", len(vendors), vendors)
	}
	if vendors[0].ItemLabel != "stew" {
		t.Errorf("representative item = %q, want stew (strongest satisfier)", vendors[0].ItemLabel)
	}
}

// TestGatherSatiation_NearestFirstAndCap: more vendor structures than the cap
// are reduced to the nearest maxSatiationVendors, ordered nearest-first.
func TestGatherSatiation_NearestFirstAndCap(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Needs: map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		Pos:   sim.TilePos{X: 0, Y: 0},
	}
	actors := map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj}
	structures := map[sim.StructureID]*sim.Structure{}
	objects := map[sim.VillageObjectID]*sim.VillageObject{}
	n := maxSatiationVendors + 2
	for i := 0; i < n; i++ {
		sid := sim.StructureID(fmt.Sprintf("s%02d", i)) // s00 nearest … ascending distance
		structures[sid] = &sim.Structure{ID: sid, DisplayName: fmt.Sprintf("Shop %02d", i)}
		objects[sim.VillageObjectID(sid)] = &sim.VillageObject{Pos: sim.WorldPos{X: float64((i + 1) * 64), Y: 0}}
		actors[sim.ActorID(fmt.Sprintf("k%02d", i))] = &sim.ActorSnapshot{WorkStructureID: sid, Inventory: map[sim.ItemKind]int{"bread": 5}}
	}
	snap := &sim.Snapshot{Actors: actors, Structures: structures, VillageObjects: objects, ItemKinds: altitudeCatalog()}

	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 1 {
		t.Fatalf("want 1 pressing need, got %+v", v)
	}
	vendors := v.Needs[0].Vendors
	if len(vendors) != maxSatiationVendors {
		t.Fatalf("cap: want %d vendors, got %d", maxSatiationVendors, len(vendors))
	}
	// Nearest-first: the first cap structures (s00..s0N-1) survive, in order.
	for i, vd := range vendors {
		want := sim.StructureID(fmt.Sprintf("s%02d", i))
		if vd.StructureID != want {
			t.Errorf("vendor[%d] = %q, want %q (nearest-first)", i, vd.StructureID, want)
		}
	}
}

// TestGatherSatiation_ExcludesOwnWorkplace: a vendor at the buyer's OWN
// workplace is dropped — a hungry vendor shouldn't be steered to buy from
// itself.
func TestGatherSatiation_ExcludesOwnWorkplace(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Needs:           map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		WorkStructureID: "general_store",
	}
	// A co-worker selling food at the buyer's own store.
	coworker := &sim.ActorSnapshot{WorkStructureID: "general_store", Inventory: map[sim.ItemKind]int{"bread": 9}}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"josiah": subj, "clerk": coworker},
		Structures: map[sim.StructureID]*sim.Structure{"general_store": {ID: "general_store", DisplayName: "General Store"}},
		ItemKinds:  altitudeCatalog(),
	}
	v := buildSatiation(snap, "josiah", subj)
	// Own workplace was the only vendor → no vendor cues at all → with no own
	// stock / peers / free sources either, the whole view is nil.
	if v != nil {
		for _, n := range v.Needs {
			if len(n.Vendors) != 0 {
				t.Errorf("own workplace should be excluded from the buy menu, got %+v", n.Vendors)
			}
		}
	}
}

// TestGatherSatiation_OutOfStockPrefersInStockRepresentative: when one item at a
// structure is remembered out of stock and another is not, the in-stock item is
// the representative (so the menu shows something buyable).
func TestGatherSatiation_OutOfStockPrefersInStockRepresentative(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
		Needs: map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		// Remembers the tavern out of STEW (the stronger item) recently.
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "tavern", ItemKind: "stew", Condition: sim.ObservedOutOfStock}: now.Add(-time.Hour),
		}),
	}
	keeper := &sim.ActorSnapshot{WorkStructureID: "tavern", Inventory: map[sim.ItemKind]int{"bread": 9, "stew": 4}}
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "john": keeper},
		Structures:  map[sim.StructureID]*sim.Structure{"tavern": {ID: "tavern", DisplayName: "The Tavern"}},
		ItemKinds:   altitudeCatalog(),
	}
	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 1 || len(v.Needs[0].Vendors) != 1 {
		t.Fatalf("want 1 vendor bullet, got %+v", v)
	}
	vd := v.Needs[0].Vendors[0]
	// stew is stronger but remembered out → bread (in stock) is the representative.
	if vd.ItemLabel != "bread" || vd.OutOfStock {
		t.Errorf("want in-stock bread as representative (OutOfStock=false), got %+v", vd)
	}
}

// TestRenderSatiation_OutOfStockAnnotation: a remembered-out item flows the
// out-of-stock annotation into the rendered cue.
func TestRenderSatiation_OutOfStockAnnotation(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
		Needs: map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "farm", ItemKind: "bread", Condition: sim.ObservedOutOfStock}: now.Add(-time.Hour),
		}),
	}
	keeper := &sim.ActorSnapshot{WorkStructureID: "farm", Inventory: map[sim.ItemKind]int{"bread": 9}}
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "moses": keeper},
		Structures:  map[sim.StructureID]*sim.Structure{"farm": {ID: "farm", DisplayName: "James Farm"}},
		ItemKinds:   altitudeCatalog(),
	}
	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil {
		t.Fatal("expected a satiation view")
	}
	var b strings.Builder
	renderSatiation(&b, v)
	out := b.String()
	if !strings.Contains(out, "James Farm") {
		t.Fatalf("expected the farm vendor cue, got:\n%s", out)
	}
	if !strings.Contains(out, "found them out") {
		t.Errorf("expected out-of-stock annotation, got:\n%s", out)
	}
}
