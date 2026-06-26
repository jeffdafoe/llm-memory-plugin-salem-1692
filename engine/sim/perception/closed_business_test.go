package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// closed_business_test.go — ZBBS-HOME-353 surface half: the decay-checked
// businessRememberedShut helper and the annotation it drives in the restock +
// satiation vendor cues.

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

// TestRenderRestocking_ShutAnnotation: a remembered-shut supplier gets the
// in-world annotation; a fresh one does not.
func TestRenderRestocking_ShutAnnotation(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
		Inventory:     map[sim.ItemKind]int{"ale": 2},
		RestockPolicy: buyPolicy("ale", 20),
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "brewery", Condition: sim.ObservedClosed}: now.Add(-time.Hour),
		}),
	}
	supplier := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}}
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "brewer": supplier},
		Structures:  map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
		ItemKinds:   restockCatalog(),

		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 || len(v.Items[0].Vendors) != 1 || !v.Items[0].Vendors[0].Shut {
		t.Fatalf("expected the brewery vendor marked Shut, got %+v", v)
	}
	var b strings.Builder
	renderRestocking(&b, v)
	if !strings.Contains(b.String(), "found it shut up") {
		t.Errorf("expected shut annotation in restock cue, got:\n%s", b.String())
	}

	// Same setup without the memory → no annotation.
	subj.Observed = sim.ObservedStates{}
	v2 := buildRestocking(snap, "merchant", subj)
	var b2 strings.Builder
	renderRestocking(&b2, v2)
	if strings.Contains(b2.String(), "found it shut up") {
		t.Errorf("did not expect shut annotation without a memory, got:\n%s", b2.String())
	}
}

// TestRenderSatiation_ShutAnnotation: the same annotation flows through the
// eat/drink vendor cue.
func TestRenderSatiation_ShutAnnotation(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
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
	v := buildSatiation(snap, "diner", subj)
	if v == nil {
		t.Fatal("expected a satiation view")
	}
	var b strings.Builder
	renderSatiation(&b, v)
	out := b.String()
	if !strings.Contains(out, "The Tavern") {
		t.Fatalf("expected the tavern vendor cue, got:\n%s", out)
	}
	if !strings.Contains(out, "found it shut up") {
		t.Errorf("expected shut annotation in satiation cue, got:\n%s", out)
	}
}

// TestRenderSatiation_AsleepKeeperNoLongerOmniscient — LLM-126. With the live
// keeper-asleep read retired, a sleeping keeper the buyer has NEVER visited
// produces no closed cue at all: no "(currently closed)" marker and no experiential
// "found it shut up". The buyer learns a shop is shut only by going (the John Ellis
// philosophy); once it has (an ObservedClosed memory), the decaying experiential
// annotation renders — the only path to a closed cue now. The out-of-stock suffix
// is independent and co-renders, with the shut clause preceding it.
func TestRenderSatiation_AsleepKeeperNoLongerOmniscient(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
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
		v := buildSatiation(snap, "diner", subj)
		if v == nil {
			t.Fatal("expected a satiation view")
		}
		var b strings.Builder
		renderSatiation(&b, v)
		return b.String()
	}

	// Sleeping keeper, but the buyer has never been there → no closed cue of any
	// kind (the retired live read no longer leaks the keeper's state across the map).
	out := render()
	if strings.Contains(out, "(currently closed)") {
		t.Errorf("the retired live closed-now marker must not appear, got:\n%s", out)
	}
	if strings.Contains(out, "found it shut up") {
		t.Errorf("no experiential shut annotation without a memory, got:\n%s", out)
	}

	// Once the buyer remembers finding it shut, the decaying experiential annotation
	// renders — the only path to a closed cue now.
	subj.Observed = sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
		{StructureID: "tavern", Condition: sim.ObservedClosed}: now.Add(-time.Hour),
	})
	if out := render(); !strings.Contains(out, "found it shut up") {
		t.Errorf("a remembered-shut vendor should carry the experiential annotation, got:\n%s", out)
	}

	// The shut clause and the out-of-stock suffix are independent and both render,
	// the shut clause preceding the trailing out-of-stock suffix.
	subj.Observed.Observe(sim.ObservedStateKey{StructureID: "tavern", ItemKind: "stew", Condition: sim.ObservedOutOfStock}, now.Add(-time.Hour))
	out = render()
	shutIdx := strings.Index(out, "found it shut up")
	stockIdx := strings.Index(out, "found them out")
	if shutIdx < 0 || stockIdx < 0 {
		t.Errorf("expected BOTH the shut annotation and the out-of-stock suffix, got:\n%s", out)
	}
	if shutIdx >= 0 && stockIdx >= 0 && shutIdx > stockIdx {
		t.Errorf("the shut annotation should precede the out-of-stock suffix, got:\n%s", out)
	}
}
