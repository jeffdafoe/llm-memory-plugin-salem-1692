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
	// Coins clear the LLM-222 means-to-pay gate; this test's focus is dedup-by-structure.
	subj := &sim.ActorSnapshot{Coins: 10, Needs: map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold}}
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
		Coins: 10, // clears the LLM-222 means-to-pay gate; focus is nearest-first + cap
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
		Coins: 10, // clears the LLM-222 means-to-pay gate; focus is the in-stock representative
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
		Coins: 10, // clears the LLM-222 means-to-pay gate; focus is the out-of-stock annotation
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

// --- LLM-139: free-source altitude (dedup-by-label, cap, keep stood-on rep,
// suppress the directory at a mild need already covered by own stock) ---

// freeBush builds a free, public hunger source (an unowned fruit bush) — a
// VillageObject carrying a hunger arrival-refresh — for the free-source altitude
// tests. 32 world px per tile (the same scale thirstWell's tests use). LLM-139.
func freeBush(id sim.VillageObjectID, name string, x, y float64) *sim.VillageObject {
	return &sim.VillageObject{
		ID: id, DisplayName: name, Pos: sim.WorldPos{X: x, Y: y},
		Refreshes: []*sim.ObjectRefresh{{Attribute: "hunger", Amount: -8}},
	}
}

// TestGatherSatiation_FreeSourceDedupByLabel: several sources of the SAME kind
// collapse to a single nearest representative, so a farm's many same-kind bushes
// stop flooding the cue. Distinct kinds each keep their own (nearest) bullet.
func TestGatherSatiation_FreeSourceDedupByLabel(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Pos:   sim.WorldToTile(0, 0),
		Needs: map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
	}
	objs := map[sim.VillageObjectID]*sim.VillageObject{
		"b1": freeBush("b1", "Blueberry bush", 64, 0),  // 2 tiles — nearest blueberry
		"b2": freeBush("b2", "Blueberry bush", 128, 0), // 4 tiles
		"b3": freeBush("b3", "Blueberry bush", 192, 0), // 6 tiles
		"r1": freeBush("r1", "Raspberry bush", 96, 0),  // 3 tiles — nearest raspberry
		"r2": freeBush("r2", "Raspberry bush", 160, 0), // 5 tiles
	}
	snap := &sim.Snapshot{
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		VillageObjects: objs,
		ItemKinds:      altitudeCatalog(),
	}
	v := buildSatiation(snap, "prudence", subj)
	if v == nil || len(v.Needs) != 1 {
		t.Fatalf("want 1 pressing need, got %+v", v)
	}
	free := v.Needs[0].FreeSources
	if len(free) != 2 {
		t.Fatalf("dedup: want 2 distinct kinds (blueberry + raspberry), got %d: %+v", len(free), free)
	}
	// One representative per kind, each the NEAREST of its kind, nearest-first overall.
	if free[0].Label != "Blueberry bush" || free[0].ObjectID != "b1" {
		t.Errorf("rep[0] = %+v, want nearest Blueberry bush b1", free[0])
	}
	if free[1].Label != "Raspberry bush" || free[1].ObjectID != "r1" {
		t.Errorf("rep[1] = %+v, want nearest Raspberry bush r1", free[1])
	}
}

// TestGatherSatiation_FreeSourceCap: more distinct free-source kinds than the cap
// are reduced to the nearest maxSatiationFreeSources, ordered nearest-first.
func TestGatherSatiation_FreeSourceCap(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Pos:   sim.WorldToTile(0, 0),
		Needs: map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
	}
	objs := map[sim.VillageObjectID]*sim.VillageObject{}
	n := maxSatiationFreeSources + 2
	for i := 0; i < n; i++ {
		id := sim.VillageObjectID(fmt.Sprintf("k%02d", i))
		// Distinct labels so dedup keeps them all; ascending distance (k00 nearest).
		objs[id] = freeBush(id, fmt.Sprintf("Bush %02d", i), float64((i+1)*64), 0)
	}
	snap := &sim.Snapshot{
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		VillageObjects: objs,
		ItemKinds:      altitudeCatalog(),
	}
	v := buildSatiation(snap, "ezekiel", subj)
	if v == nil || len(v.Needs) != 1 {
		t.Fatalf("want 1 pressing need, got %+v", v)
	}
	free := v.Needs[0].FreeSources
	if len(free) != maxSatiationFreeSources {
		t.Fatalf("cap: want %d free sources, got %d", maxSatiationFreeSources, len(free))
	}
	for i, fs := range free {
		want := sim.VillageObjectID(fmt.Sprintf("k%02d", i))
		if fs.ObjectID != want {
			t.Errorf("free[%d] = %q, want %q (nearest-first)", i, fs.ObjectID, want)
		}
	}
}

// TestBuildSatiation_FreeSourceStandingOnFarm: the live hud-6a887a… blast — a
// farm OWNER standing ON one of her own blueberry bushes, with four co-located
// blueberry bushes (one at her tile) plus several raspberry bushes. The altitude
// pass collapses it to one nearest representative per kind. The bush she stands
// on is the nearest blueberry, so it SURVIVES and renders "right nearby" — the
// stood-on source is the actionable eat-here line, not dropped. LLM-139.
func TestBuildSatiation_FreeSourceStandingOnFarm(t *testing.T) {
	subj := &sim.ActorSnapshot{Pos: sim.WorldToTile(0, 0), Needs: map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold}}
	owned := func(id sim.VillageObjectID, name string, x, y float64) *sim.VillageObject {
		o := freeBush(id, name, x, y)
		o.OwnerActorID = "prudence" // her own farm — the owner-gate lets the owner eat here
		return o
	}
	objs := map[sim.VillageObjectID]*sim.VillageObject{
		"bb0": owned("bb0", "Blueberry bush", 0, 0), // her tile — distance 0
		"bb1": owned("bb1", "Blueberry bush", 64, 0),
		"bb2": owned("bb2", "Blueberry bush", 128, 0),
		"bb3": owned("bb3", "Blueberry bush", 192, 0),
		"rb0": owned("rb0", "Raspberry bush", 96, 0),
		"rb1": owned("rb1", "Raspberry bush", 160, 0),
	}
	snap := &sim.Snapshot{
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		VillageObjects: objs,
		ItemKinds:      altitudeCatalog(),
	}
	v := buildSatiation(snap, "prudence", subj)
	if v == nil || len(v.Needs) != 1 {
		t.Fatalf("want 1 pressing need, got %+v", v)
	}
	free := v.Needs[0].FreeSources
	if len(free) != 2 {
		t.Fatalf("blast collapsed wrong: want 2 (blueberry + raspberry), got %d: %+v", len(free), free)
	}
	if free[0].ObjectID != "bb0" || free[0].Distance != "right nearby" {
		t.Errorf("nearest rep must be the stood-on bush bb0 at 'right nearby', got %+v", free[0])
	}
	if free[1].Label != "Raspberry bush" {
		t.Errorf("second rep = %+v, want a Raspberry bush", free[1])
	}
	// Render proves the stood-on source is a real eat-here line, not dropped.
	var b strings.Builder
	renderSatiation(&b, v)
	if out := b.String(); !strings.Contains(out, "Blueberry bush") || !strings.Contains(out, "right nearby") {
		t.Errorf("stood-on free source must render as an actionable eat-here line:\n%s", out)
	}
}

// TestBuildSatiation_MildWithOwnFoodSuppressesDirectory: a mild (felt-but-sub-red)
// need with personal food already on hand is resolved by the own-stock line, so
// the walk-to directory (free sources + vendors + peers) is suppressed. At the
// red tier the same setup shows the full list. LLM-139 point 4.
func TestBuildSatiation_MildWithOwnFoodSuppressesDirectory(t *testing.T) {
	build := func(hunger int) SatiationNeedView {
		subj := &sim.ActorSnapshot{
			Pos:       sim.WorldToTile(0, 0),
			Needs:     map[sim.NeedKey]int{"hunger": hunger},
			Inventory: map[sim.ItemKind]int{"bread": 2}, // personal (no RestockPolicy)
		}
		keeper := &sim.ActorSnapshot{WorkStructureID: "tavern", Inventory: map[sim.ItemKind]int{"stew": 9}}
		snap := &sim.Snapshot{
			Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "john": keeper},
			Structures: map[sim.StructureID]*sim.Structure{"tavern": {ID: "tavern", DisplayName: "The Tavern"}},
			VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
				"tavern": {Pos: sim.WorldPos{X: 64, Y: 0}},
				"bush":   freeBush("bush", "Fruit bush", 96, 0),
			},
			ItemKinds: altitudeCatalog(),
		}
		v := buildSatiation(snap, "ezekiel", subj)
		if v == nil || len(v.Needs) != 1 {
			t.Fatalf("want 1 pressing need at hunger=%d, got %+v", hunger, v)
		}
		return v.Needs[0]
	}

	// Mild: own bread present → the walk-to directory is suppressed.
	mild := build(14)
	if len(mild.OwnStock) != 1 {
		t.Errorf("mild: want the own-stock bread line, got %+v", mild.OwnStock)
	}
	if len(mild.FreeSources) != 0 || len(mild.Vendors) != 0 || len(mild.CoPresentPeers) != 0 {
		t.Errorf("mild + own food: directory must be suppressed, got free=%+v vendors=%+v peers=%+v",
			mild.FreeSources, mild.Vendors, mild.CoPresentPeers)
	}

	// Red: the full directory rides alongside the own-stock line.
	red := build(sim.DefaultHungerRedThreshold)
	if len(red.FreeSources) == 0 || len(red.Vendors) == 0 {
		t.Errorf("red: full directory must show, got free=%+v vendors=%+v", red.FreeSources, red.Vendors)
	}
}
