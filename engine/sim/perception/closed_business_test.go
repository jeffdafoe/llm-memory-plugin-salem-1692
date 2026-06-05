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
		ClosedBusinessObs: map[sim.StructureID]time.Time{
			"fresh": now.Add(-1 * time.Hour),                             // within 4h
			"stale": now.Add(-sim.ClosedBusinessMemoryTTL - time.Minute), // expired
		},
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
		ClosedBusinessObs: map[sim.StructureID]time.Time{"skew": now.Add(time.Hour)},
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
		Inventory:         map[sim.ItemKind]int{"ale": 2},
		RestockPolicy:     buyPolicy("ale", 20),
		ClosedBusinessObs: map[sim.StructureID]time.Time{"brewery": now.Add(-time.Hour)},
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
	subj.ClosedBusinessObs = nil
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
		Needs:             map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold},
		ClosedBusinessObs: map[sim.StructureID]time.Time{"tavern": now.Add(-time.Hour)},
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

// TestRenderSatiation_KeeperAsleepAnnotation: a vendor whose keeper is asleep at
// snapshot time gets the live present-tense "no one tending it" annotation
// (ZBBS-HOME-387) — distinct from the experiential Shut memory, and taking
// precedence over it when both point at the same shop.
func TestRenderSatiation_KeeperAsleepAnnotation(t *testing.T) {
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

	// Sleeping keeper → live closed-now annotation.
	if out := render(); !strings.Contains(out, "no one is tending it just now") {
		t.Errorf("expected the live closed-now annotation for a sleeping keeper, got:\n%s", out)
	}

	// Awake keeper → no annotation.
	cook.State = sim.StateIdle
	if out := render(); strings.Contains(out, "no one is tending it just now") {
		t.Errorf("did not expect the closed-now annotation for an awake keeper, got:\n%s", out)
	}

	// Live closed-now wins over the stale experiential Shut memory: only the
	// present-tense line shows, not "found it shut up".
	cook.State = sim.StateSleeping
	subj.ClosedBusinessObs = map[sim.StructureID]time.Time{"tavern": now.Add(-time.Hour)}
	out := render()
	if !strings.Contains(out, "no one is tending it just now") {
		t.Errorf("expected closed-now annotation to win, got:\n%s", out)
	}
	if strings.Contains(out, "found it shut up") {
		t.Errorf("experiential Shut annotation should be suppressed when live closed-now applies, got:\n%s", out)
	}

	// ClosedNow and OutOfStock are independent suffixes and both render when
	// both apply — closed-now first (it shares the Shut slot), then out-of-stock.
	subj.ClosedBusinessObs = nil
	subj.OutOfStockObs = map[sim.OutOfStockKey]time.Time{
		{StructureID: "tavern", ItemKind: "stew"}: now.Add(-time.Hour),
	}
	out = render()
	closedIdx := strings.Index(out, "no one is tending it just now")
	stockIdx := strings.Index(out, "found them out")
	if closedIdx < 0 || stockIdx < 0 {
		t.Errorf("expected BOTH closed-now and out-of-stock suffixes, got:\n%s", out)
	}
	if closedIdx >= 0 && stockIdx >= 0 && closedIdx > stockIdx {
		t.Errorf("closed-now suffix should precede out-of-stock suffix, got:\n%s", out)
	}
}
