package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// closed_business_test.go — ZBBS-HOME-353 surface half: the decay-checked
// businessRememberedShut helper. It drives a DROP in the restock supplier list
// (LLM-216 — a shut supplier is not an actionable walk-to target) and an
// ANNOTATION in the satiation vendor cue (a shut consumable seller is deprioritized,
// not dropped, since a felt need still wants the closest source).

func TestBusinessRememberedShut_Decay(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "fresh", Condition: sim.ObservedClosed}: now.Add(-1 * time.Hour),                             // within 4h
			{StructureID: "stale", Condition: sim.ObservedClosed}: now.Add(-sim.ClosedBusinessMemoryTTL - time.Minute), // expired
		}),
	}
	snap := &sim.Snapshot{PublishedAt: now}

	if !businessRememberedShut(snap, subj, "fresh") {
		t.Error("a 1h-old observation should still be remembered shut")
	}
	if businessRememberedShut(snap, subj, "stale") {
		t.Error("an observation older than the TTL should have decayed")
	}
	if businessRememberedShut(snap, subj, "never") {
		t.Error("a business never observed shut should not be remembered")
	}
	if businessRememberedShut(snap, &sim.ActorSnapshot{}, "fresh") {
		t.Error("an actor with no memory map should not be remembered")
	}

	// A future-stamped observation (clock skew) must NOT read as fresh-forever.
	future := &sim.ActorSnapshot{
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "skew", Condition: sim.ObservedClosed}: now.Add(time.Hour),
		}),
	}
	if businessRememberedShut(snap, future, "skew") {
		t.Error("a future-stamped observation must not be treated as a live shut memory")
	}
}

// TestBuildRestocking_ShutSupplierDropped — LLM-216 + LLM-406. The restock path never
// offers a supplier the buyer remembers finding shut as a walk-to DESTINATION (it no
// longer annotates it "found it shut up" and leaves the id in — that
// ZBBS-HOME-353/LLM-126 posture left the weak model touring the dead ends). When the
// shut brewery is the only supplier, the item has no actionable buy path and carries
// no Vendors at all.
//
// It is NOT silent, though (LLM-406): the shut brewery is named as a BLOCKED supplier
// with its reason, so the keeper learns who has the ale he needs and why he can't have
// it today — the section renders, with no destination id and no buy imperative.
// Omitting it entirely, as LLM-216 did, is what left an illiquid keeper standing in an
// empty shop with no cue at all. Without the shut memory the open brewery surfaces as
// a normal destination. (The satiation cue still annotates — see
// TestRenderSatiation_ShutAnnotation.)
func TestBuildRestocking_ShutSupplierDropped(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
		Coins:         20, // not broke; the brewery price is unknown, so only the shut-drop is in play
		Inventory:     map[sim.ItemKind]int{"ale": 2},
		RestockPolicy: buyPolicy("ale", 20),
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "brewery", Condition: sim.ObservedClosed}: now.Add(-time.Hour),
		}),
	}
	supplier := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, RestockPolicy: producePolicy("ale", 40)}
	snap := &sim.Snapshot{
		PublishedAt:       now,
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "brewer": supplier},
		Structures:        map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	// The lone remembered-shut supplier is no destination — but it IS named, with its
	// reason, instead of the item vanishing into a silent section (LLM-406).
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("a shut lone supplier should still render the item as blocked, got %+v", v)
	}
	if it := v.Items[0]; len(it.Vendors) != 0 || it.CoPresentSeller != "" {
		t.Fatalf("a remembered-shut supplier must not be offered as a destination, got %+v", it)
	}
	if it := v.Items[0]; len(it.Blocked) != 1 || it.Blocked[0].Reason != restockBlockShut || it.Blocked[0].StructureLabel != "The Brewery" {
		t.Fatalf("want The Brewery named as blocked-shut, got %+v", it.Blocked)
	}
	if !v.AllBlocked() {
		t.Error("the only item is blocked, so the section should render its blocked lead")
	}
	// The rendered section names the supplier in prose but must NEVER hand over its
	// structure_id — that token is what the model echoes into move_to, and this is
	// precisely the place it must not go (the LLM-216 touring loop).
	var b strings.Builder
	renderRestocking(&b, v)
	out := b.String()
	if !strings.Contains(out, "The Brewery") || !strings.Contains(out, "found it shut") {
		t.Errorf("blocked render should name the supplier and the reason:\n%s", out)
	}
	if strings.Contains(out, "destination:") {
		t.Errorf("a blocked supplier must not be rendered as a move_to destination:\n%s", out)
	}

	// Same setup without the memory → the open brewery surfaces.
	subj.Observed = sim.ObservedStates{}
	v = buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 || len(v.Items[0].Vendors) != 1 || v.Items[0].Vendors[0].StructureID != "brewery" {
		t.Fatalf("without the shut memory the open brewery should surface, got %+v", v)
	}
	if len(v.Items[0].Blocked) != 0 {
		t.Errorf("an open, payable supplier is not blocked, got %+v", v.Items[0].Blocked)
	}
}

// TestRenderSatiation_ShutVendorDropped — LLM-222. A vendor the buyer remembers
// finding shut is DROPPED from the eat/drink cue, not annotated: the shared
// closed-business annotation no longer flows through satiation (it still does for
// the recovery/rest cue). The buyer holds coins, so the drop is the seller-
// availability gate, not affordability; the shut Tavern being the only satisfier,
// the view is nil.
func TestRenderSatiation_ShutVendorDropped(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
		Coins: 10,
		Needs: map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "tavern", Condition: sim.ObservedClosed}: now.Add(-time.Hour),
		}),
	}
	cook := &sim.ActorSnapshot{WorkStructureID: "tavern", Inventory: map[sim.ItemKind]int{"stew": 5}}
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"diner": subj, "cook": cook},
		Structures:  map[sim.StructureID]*sim.Structure{"tavern": {ID: "tavern", DisplayName: "The Tavern"}},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"stew": {Name: "stew", DisplayLabel: "stew", Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 4}}},
		},
	}
	if v := buildSatiation(snap, "diner", subj); v != nil {
		t.Fatalf("remembered-shut Tavern was the only satisfier — it must be dropped, leaving a nil view, got %+v", v)
	}
}

// TestRenderSatiation_AsleepKeeperNoLongerOmniscient — LLM-126 + LLM-222. With the
// live keeper-asleep read retired (LLM-126), a sleeping keeper the buyer has NEVER
// visited produces no closed cue: the shop shows as a normal buyable vendor, since
// perception never leaks the remote keeper's live state across the map. Once the
// buyer REMEMBERS finding it shut, LLM-222 DROPS the vendor from the eat/drink cue
// entirely — the experiential "found it shut up" annotation was retired along with
// the tour-the-dead-end behavior it produced. The buyer holds coins throughout, so
// a dropped vendor is the seller-availability gate, not affordability.
func TestRenderSatiation_AsleepKeeperNoLongerOmniscient(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
		Coins: 10,
		Needs: map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
	}
	cook := &sim.ActorSnapshot{
		State:           sim.StateSleeping,
		WorkStructureID: "tavern",
		Inventory:       map[sim.ItemKind]int{"stew": 5},
	}
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"diner": subj, "cook": cook},
		Structures:  map[sim.StructureID]*sim.Structure{"tavern": {ID: "tavern", DisplayName: "The Tavern"}},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"stew": {Name: "stew", DisplayLabel: "stew", Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 4}}},
		},
	}
	render := func() string {
		var b strings.Builder
		renderSatiation(&b, buildSatiation(snap, "diner", subj))
		return b.String()
	}

	// Sleeping keeper, buyer never visited → the shop shows as a normal buyable
	// vendor with no closed cue of any kind (the retired live read no longer leaks
	// the keeper's state across the map).
	out := render()
	if !strings.Contains(out, "The Tavern") {
		t.Fatalf("a shop with a sleeping keeper the buyer has not visited must still show as a normal vendor, got:\n%s", out)
	}
	if strings.Contains(out, "(currently closed)") {
		t.Errorf("the retired live closed-now marker must not appear, got:\n%s", out)
	}
	if strings.Contains(out, "found it shut up") {
		t.Errorf("no experiential shut annotation without a memory, got:\n%s", out)
	}

	// Once the buyer remembers finding it shut, LLM-222 DROPS the vendor from the
	// eat/drink cue — no annotation, no vendor line at all.
	subj.Observed = sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
		{StructureID: "tavern", Condition: sim.ObservedClosed}: now.Add(-time.Hour),
	})
	out = render()
	if strings.Contains(out, "The Tavern") {
		t.Errorf("a remembered-shut vendor must be dropped from the eat/drink cue (LLM-222), got:\n%s", out)
	}
	if strings.Contains(out, "found it shut up") {
		t.Errorf("the retired experiential shut annotation must not render (vendor is dropped), got:\n%s", out)
	}
}

// TestBusinessRememberedShut_InFlightDestinationGuard — LLM-366 / ZBBS-HOME-405.
// The remembered-shut avoidance read is SUPPRESSED for the actor's own in-flight
// move destination, so a mid-walk re-tick can't read a stale "shut" label and
// steer the actor off the place it just chose to go. This is the narrow guard that
// replaces the old commit-time memory wipe (move_to.go's forgetSupplierStaleMemory):
// the memory itself is untouched, so it applies again the moment the walk is no
// longer aimed there (the next decision) — which is what stops a workless NPC from
// re-picking the same shut shop every idle cycle.
func TestBusinessRememberedShut_InFlightDestinationGuard(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	base := func() *sim.ActorSnapshot {
		return &sim.ActorSnapshot{
			Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
				{StructureID: "farm", Condition: sim.ObservedClosed}: now.Add(-time.Hour),
			}),
		}
	}
	snap := &sim.Snapshot{PublishedAt: now}

	if !businessRememberedShut(snap, base(), "farm") {
		t.Fatal("baseline: a fresh shut memory (not walking anywhere) should read as remembered-shut")
	}

	// Walking TO the farm — a visit (the owner-only buy path) or an enter — both
	// suppress the farm's own shut memory so the mid-walk cue can't redirect.
	for _, kind := range []sim.MoveDestinationKind{sim.MoveDestinationStructureVisit, sim.MoveDestinationStructureEnter} {
		a := base()
		a.MoveDestStructureID = "farm"
		a.MoveDestKind = kind
		if businessRememberedShut(snap, a, "farm") {
			t.Errorf("walking to the farm (kind %v) must suppress its shut memory (HOME-405 guard)", kind)
		}
	}

	// Walking to a DIFFERENT place must NOT suppress the farm's shut memory.
	elsewhere := base()
	elsewhere.MoveDestStructureID = "tavern"
	elsewhere.MoveDestKind = sim.MoveDestinationStructureVisit
	if !businessRememberedShut(snap, elsewhere, "farm") {
		t.Error("walking elsewhere must leave the farm's shut memory in force")
	}
}

// TestBusinessRememberedOutOfStock_InFlightDestinationGuard — LLM-366. The
// out-of-stock avoidance read carries the same in-flight-destination guard as the
// shut read: the old commit-time wipe cleared BOTH memories, so both must now be
// suppressed only while the actor is actively walking to the structure.
func TestBusinessRememberedOutOfStock_InFlightDestinationGuard(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "store", ItemKind: "milk", Condition: sim.ObservedOutOfStock}: now.Add(-time.Hour),
		}),
	}
	snap := &sim.Snapshot{PublishedAt: now}

	if !businessRememberedOutOfStock(snap, subj, "store", "milk") {
		t.Fatal("baseline: a fresh out-of-stock memory should read as remembered")
	}
	// Both walk kinds suppress it — the old commit-time wipe cleared out-of-stock
	// regardless of enter/visit, so the guard must pin the same equivalence.
	for _, kind := range []sim.MoveDestinationKind{sim.MoveDestinationStructureVisit, sim.MoveDestinationStructureEnter} {
		subj.MoveDestStructureID = "store"
		subj.MoveDestKind = kind
		if businessRememberedOutOfStock(snap, subj, "store", "milk") {
			t.Errorf("walking to the store (kind %v) must suppress its out-of-stock memory (HOME-405 guard)", kind)
		}
	}
}
