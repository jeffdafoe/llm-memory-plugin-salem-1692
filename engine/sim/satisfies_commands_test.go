package sim_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// satisfies_commands_test.go — sim-level coverage of the item-satiation edit
// helpers (LLM-119): ResolveSatisfaction (catalog-reference validation +
// canonicalization) and SetItemSatisfaction (in-memory catalog upsert, dwell-
// preserving, clone-not-mutate). The durable item_satisfies write is covered in
// repo/pg; the end-to-end route in httpapi.

func buildSatisfiesTestWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		// stew carries a full dwell triple so the edit-preserves-dwell case is real.
		"stew": {Name: "stew", DisplayLabel: "Hearty Stew", Category: sim.ItemCategoryFood,
			Satisfies: []sim.ItemSatisfaction{
				{Attribute: "hunger", Immediate: 10, DwellAmount: 2, DwellPeriodMinutes: 15, DwellTotalTicks: 4},
			}},
		"berry": {Name: "berry", DisplayLabel: "Berry", Category: sim.ItemCategoryFood,
			Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 2}}},
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

func TestResolveSatisfaction_CanonicalizesAndValidates(t *testing.T) {
	w, stop := buildSatisfiesTestWorld(t)
	defer stop()

	// A label-cased name resolves to the canonical catalog key.
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.ResolveSatisfaction(world, "Berry")
	}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := res.(sim.ItemKind); got != "berry" {
		t.Fatalf("canonicalized = %q, want berry", got)
	}

	// Unknown item wraps ErrUnknownItemKind.
	_, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.ResolveSatisfaction(world, "dragonfruit")
	}})
	if !errors.Is(err, sim.ErrUnknownItemKind) {
		t.Fatalf("unknown item err = %v, want ErrUnknownItemKind", err)
	}
}

func TestSetItemSatisfaction_EditExistingPreservesDwell(t *testing.T) {
	w, stop := buildSatisfiesTestWorld(t)
	defer stop()

	res, err := w.Send(sim.SetItemSatisfaction("stew", "hunger", 12))
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	applied := res.(sim.ItemSatisfaction)
	if applied.Attribute != "hunger" || applied.Immediate != 12 {
		t.Fatalf("applied = %+v, want hunger/12", applied)
	}
	// The dwell triple is preserved (only the immediate amount changed) and no
	// second hunger entry was appended.
	got, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.ItemKinds["stew"].Satisfies, nil
	}})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	sats := got.([]sim.ItemSatisfaction)
	if len(sats) != 1 {
		t.Fatalf("stew satisfies len=%d, want 1 (edit, not append)", len(sats))
	}
	s := sats[0]
	if s.Immediate != 12 || s.DwellAmount != 2 || s.DwellPeriodMinutes != 15 || s.DwellTotalTicks != 4 {
		t.Fatalf("stew entry = %+v, want immediate 12 with dwell triple intact", s)
	}
}

func TestSetItemSatisfaction_AppendsNewAttribute(t *testing.T) {
	w, stop := buildSatisfiesTestWorld(t)
	defer stop()

	if _, err := w.Send(sim.SetItemSatisfaction("berry", "thirst", 3)); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.ItemKinds["berry"].Satisfies, nil
	}})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	sats := got.([]sim.ItemSatisfaction)
	if len(sats) != 2 {
		t.Fatalf("berry satisfies len=%d, want 2 (hunger + new thirst)", len(sats))
	}
	byAttr := map[sim.NeedKey]int{}
	for _, s := range sats {
		byAttr[s.Attribute] = s.Immediate
	}
	if byAttr["hunger"] != 2 || byAttr["thirst"] != 3 {
		t.Fatalf("berry satisfies = %+v, want hunger:2 thirst:3", byAttr)
	}
}

func TestSetItemSatisfaction_ClonesDefDoesNotMutateOld(t *testing.T) {
	w, stop := buildSatisfiesTestWorld(t)
	defer stop()

	// Capture the def pointer + its entry as it stands before the edit.
	before, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.ItemKinds["berry"], nil
	}})
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	oldDef := before.(*sim.ItemKindDef)
	oldImmediate := oldDef.Satisfies[0].Immediate

	if _, err := w.Send(sim.SetItemSatisfaction("berry", "hunger", 9)); err != nil {
		t.Fatalf("set: %v", err)
	}

	// The map now points at a fresh def (read-only-once-in-map invariant) and the
	// previously-held def is untouched — a concurrent snapshot reader holding the
	// old pointer never sees a torn slice.
	after, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.ItemKinds["berry"], nil
	}})
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	newDef := after.(*sim.ItemKindDef)
	if newDef == oldDef {
		t.Fatal("expected a fresh def pointer after the edit (clone, not in-place mutation)")
	}
	if oldDef.Satisfies[0].Immediate != oldImmediate {
		t.Fatalf("old def mutated: immediate=%d, want %d", oldDef.Satisfies[0].Immediate, oldImmediate)
	}
	if newDef.Satisfies[0].Immediate != 9 {
		t.Fatalf("new def immediate=%d, want 9", newDef.Satisfies[0].Immediate)
	}
}

func TestSetItemSatisfaction_UnknownKind(t *testing.T) {
	w, stop := buildSatisfiesTestWorld(t)
	defer stop()

	_, err := w.Send(sim.SetItemSatisfaction("dragonfruit", "hunger", 5))
	if !errors.Is(err, sim.ErrUnknownItemKind) {
		t.Fatalf("unknown kind err = %v, want ErrUnknownItemKind", err)
	}
}
