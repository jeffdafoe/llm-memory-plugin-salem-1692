package sim_test

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// item_kind_commands_test.go — sim-level coverage of SetItemKind (LLM-200): the
// in-memory item_kind upsert. A fresh name installs a new catalog entry; an
// existing name updates the definitional fields while PRESERVING the per-need
// satiation (item/set never carries it), and stores a clone rather than mutating
// the def a concurrent snapshot reader may hold. The durable item_kind write is
// covered in repo/pg; the end-to-end route in httpapi.

func buildItemKindTestWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		// stew carries a full dwell triple so the preserve-on-update case is real.
		"stew": {Name: "stew", DisplayLabel: "Hearty Stew", Category: sim.ItemCategoryFood, SortOrder: 5,
			Satisfies: []sim.ItemSatisfaction{
				{Attribute: "hunger", Immediate: 10, DwellAmount: 2, DwellPeriodMinutes: 15, DwellTotalTicks: 4},
			}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	return w, func() { cancel(); <-done }
}

func TestSetItemKind_InsertsNewKind(t *testing.T) {
	w, stop := buildItemKindTestWorld(t)
	defer stop()

	def := sim.ItemKindDef{
		Name: "shovel", DisplayLabel: "Shovel", Category: sim.ItemCategory("tool"),
		SortOrder: 0, Capabilities: []string{"portable"},
		DisplayLabelSingular: "shovel", DisplayLabelPlural: "shovels",
	}
	got, err := w.Send(sim.SetItemKind(def))
	if err != nil {
		t.Fatalf("SetItemKind: %v", err)
	}
	stored, ok := got.(sim.ItemKindDef)
	if !ok {
		t.Fatalf("result type %T, want sim.ItemKindDef", got)
	}
	if stored.Name != "shovel" || stored.DisplayLabel != "Shovel" || stored.Category != "tool" {
		t.Errorf("stored def fields: %+v", stored)
	}
	if len(stored.Satisfies) != 0 {
		t.Errorf("a new kind should have no satiation, got %+v", stored.Satisfies)
	}
	// It lives in the live catalog with its capability + counting nouns intact.
	res, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.ItemKinds["shovel"], nil
	}})
	live, _ := res.(*sim.ItemKindDef)
	if live == nil || !live.HasCapability("portable") || live.Plural() != "shovels" {
		t.Errorf("live shovel def = %+v", res)
	}
}

func TestSetItemKind_UpdatePreservesSatisfies(t *testing.T) {
	w, stop := buildItemKindTestWorld(t)
	defer stop()

	// Edit stew's definition (relabel + recategorize-as sort). The request carries
	// no satiation, so the existing hunger entry + dwell triple must survive.
	got, err := w.Send(sim.SetItemKind(sim.ItemKindDef{
		Name: "stew", DisplayLabel: "Beef Stew", Category: sim.ItemCategoryFood, SortOrder: 7,
	}))
	if err != nil {
		t.Fatalf("SetItemKind: %v", err)
	}
	stored := got.(sim.ItemKindDef)
	if stored.DisplayLabel != "Beef Stew" || stored.SortOrder != 7 {
		t.Errorf("definitional fields not updated: %+v", stored)
	}
	if len(stored.Satisfies) != 1 {
		t.Fatalf("satiation dropped on update: %+v", stored.Satisfies)
	}
	st := stored.Satisfies[0]
	if st.Attribute != "hunger" || st.Immediate != 10 || st.DwellAmount != 2 ||
		st.DwellPeriodMinutes != 15 || st.DwellTotalTicks != 4 {
		t.Errorf("preserved satiation corrupted: %+v", st)
	}
}

func TestSetItemKind_ClonesDoesNotMutateOld(t *testing.T) {
	w, stop := buildItemKindTestWorld(t)
	defer stop()

	before, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.ItemKinds["stew"], nil
	}})
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	oldDef := before.(*sim.ItemKindDef)

	if _, err := w.Send(sim.SetItemKind(sim.ItemKindDef{
		Name: "stew", DisplayLabel: "Beef Stew", Category: sim.ItemCategoryFood,
	})); err != nil {
		t.Fatalf("set: %v", err)
	}

	// The map now points at a fresh def (read-only-once-in-map invariant) and the
	// previously-held def is untouched — a concurrent snapshot reader holding the
	// old pointer never sees a torn def.
	after, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.ItemKinds["stew"], nil
	}})
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	newDef := after.(*sim.ItemKindDef)
	if newDef == oldDef {
		t.Fatal("expected a fresh def pointer after the edit (clone, not in-place mutation)")
	}
	if oldDef.DisplayLabel != "Hearty Stew" {
		t.Fatalf("old def mutated: label=%q, want Hearty Stew", oldDef.DisplayLabel)
	}
}
