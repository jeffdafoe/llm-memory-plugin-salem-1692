package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildVillageContextWorld seeds a world for VillageContext snapshot
// tests: a visitor, a businessowner with produce policy + recipes,
// and a tavern structure to host them.
func buildVillageContextWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "Ingersoll's Ordinary"},
		"smithy": {ID: "smithy", DisplayName: "The Smithy"},
	})
	handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		"ale":   {OutputItem: "ale", RetailPrice: 3},
		"stew":  {OutputItem: "stew", RetailPrice: 5},
		"nails": {OutputItem: "nails", RetailPrice: 2},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:              "hannah",
			DisplayName:     "Hannah Wells",
			Kind:            sim.KindNPCShared,
			WorkStructureID: "tavern",
			RestockPolicy: &sim.RestockPolicy{
				Restock: []sim.RestockEntry{
					{Item: "ale", Source: sim.RestockSourceProduce},
					{Item: "stew", Source: sim.RestockSourceProduce},
				},
			},
		},
		"reeves": {
			ID:              "reeves",
			DisplayName:     "Goodman Reeves",
			Kind:            sim.KindNPCShared,
			WorkStructureID: "smithy",
			RestockPolicy: &sim.RestockPolicy{
				Restock: []sim.RestockEntry{
					{Item: "nails", Source: sim.RestockSourceProduce},
				},
			},
		},
		"babbage": {
			ID:          "babbage",
			DisplayName: "Master Babbage",
			Kind:        sim.KindNPCShared,
			VisitorState: &sim.VisitorState{
				Archetype:   "wandering surgeon",
				Origin:      "Boston",
				Disposition: "withdrawn",
				ExpiresAt:   time.Now().Add(8 * time.Hour),
			},
		},
		"wendell": {
			ID:          "wendell",
			DisplayName: "Caleb Wendell",
			Kind:        sim.KindNPCShared,
			VisitorState: &sim.VisitorState{
				Archetype: "circuit preacher",
				Origin:    "Wenham",
			},
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() { cancel(); <-done }
}

// TestFetchVillageContext_Visitors: actors with VisitorState populate
// the Visitors slice, sorted by DisplayName.
func TestFetchVillageContext_Visitors(t *testing.T) {
	w, stop := buildVillageContextWorld(t)
	defer stop()

	res, err := w.Send(sim.FetchVillageContext(time.Now()))
	if err != nil {
		t.Fatalf("FetchVillageContext: %v", err)
	}
	ctx := res.(sim.VillageContext)
	if len(ctx.Visitors) != 2 {
		t.Fatalf("len(Visitors) = %d, want 2", len(ctx.Visitors))
	}
	// Sorted by DisplayName ascending.
	if ctx.Visitors[0].DisplayName != "Caleb Wendell" {
		t.Errorf("Visitors[0].DisplayName = %q, want Caleb Wendell", ctx.Visitors[0].DisplayName)
	}
	if ctx.Visitors[1].DisplayName != "Master Babbage" {
		t.Errorf("Visitors[1].DisplayName = %q, want Master Babbage", ctx.Visitors[1].DisplayName)
	}
	// VisitorState fields propagate.
	babbage := ctx.Visitors[1]
	if babbage.Archetype != "wandering surgeon" || babbage.Origin != "Boston" || babbage.Disposition != "withdrawn" {
		t.Errorf("Babbage = %+v, want archetype/origin/disposition populated", babbage)
	}
}

// TestFetchVillageContext_BusinessCatalog: actors with WorkStructure +
// RestockPolicy + recipes populate BusinessCatalog with retail prices.
func TestFetchVillageContext_BusinessCatalog(t *testing.T) {
	w, stop := buildVillageContextWorld(t)
	defer stop()

	res, err := w.Send(sim.FetchVillageContext(time.Now()))
	if err != nil {
		t.Fatalf("FetchVillageContext: %v", err)
	}
	ctx := res.(sim.VillageContext)
	if len(ctx.BusinessCatalog) != 2 {
		t.Fatalf("len(BusinessCatalog) = %d, want 2 (hannah + reeves)", len(ctx.BusinessCatalog))
	}
	// Sorted by StructureLabel.
	if ctx.BusinessCatalog[0].StructureLabel != "Ingersoll's Ordinary" {
		t.Errorf("BusinessCatalog[0].StructureLabel = %q, want Ingersoll's Ordinary",
			ctx.BusinessCatalog[0].StructureLabel)
	}
	if ctx.BusinessCatalog[1].StructureLabel != "The Smithy" {
		t.Errorf("BusinessCatalog[1].StructureLabel = %q, want The Smithy",
			ctx.BusinessCatalog[1].StructureLabel)
	}
	// Hannah's items: ale + stew, both with prices.
	hannah := ctx.BusinessCatalog[0]
	if hannah.OwnerDisplayName != "Hannah Wells" {
		t.Errorf("Owner = %q, want Hannah Wells", hannah.OwnerDisplayName)
	}
	if len(hannah.Items) != 2 {
		t.Fatalf("Hannah Items len = %d, want 2", len(hannah.Items))
	}
	// Items sorted by ItemKind — ale before stew alphabetically.
	if hannah.Items[0].Item != "ale" || hannah.Items[0].Price != 3 {
		t.Errorf("Hannah Items[0] = %+v, want ale @ 3", hannah.Items[0])
	}
	if hannah.Items[1].Item != "stew" || hannah.Items[1].Price != 5 {
		t.Errorf("Hannah Items[1] = %+v, want stew @ 5", hannah.Items[1])
	}
}

// TestFetchVillageContext_NoVisitorsNoCatalog: world without visitors
// or business actors returns empty (nil) slices, not crash.
func TestFetchVillageContext_NoVisitorsNoCatalog(t *testing.T) {
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	res, err := w.Send(sim.FetchVillageContext(time.Now()))
	if err != nil {
		t.Fatalf("FetchVillageContext: %v", err)
	}
	snap := res.(sim.VillageContext)
	if len(snap.Visitors) != 0 {
		t.Errorf("Visitors len = %d, want 0 on empty world", len(snap.Visitors))
	}
	if len(snap.BusinessCatalog) != 0 {
		t.Errorf("BusinessCatalog len = %d, want 0 on empty world", len(snap.BusinessCatalog))
	}
}

// TestFetchVillageContext_SkipsBusinessWithoutProduceEntries: actor
// with WorkStructure but no RestockPolicy produce entries is excluded
// from the catalog.
func TestFetchVillageContext_SkipsBusinessWithoutProduceEntries(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"smithy": {ID: "smithy", DisplayName: "The Smithy"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"wanderer": {
			ID:              "wanderer",
			DisplayName:     "Wanderer",
			Kind:            sim.KindNPCShared,
			WorkStructureID: "smithy",
			// No RestockPolicy.
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	res, _ := w.Send(sim.FetchVillageContext(time.Now()))
	snap := res.(sim.VillageContext)
	if len(snap.BusinessCatalog) != 0 {
		t.Errorf("BusinessCatalog = %+v, want empty (no produce entries)", snap.BusinessCatalog)
	}
}
