package pg

// Real-pg integration test for the asset_refresh_default write-through + load
// (LLM-363). Runs against embedded Postgres with the full prod-baseline schema +
// all migrations applied (so the LLM-363 table exists); skipped under -short.
//
// Exercises the substrate facts worth checking against real pg: the wholesale
// DELETE+INSERT replace transaction, the nullable attribute / gather_item round-trip
// (need row keeps its attribute + null gather; yield row keeps its gather + null
// attribute), and that LoadAll's attachRefreshDefaults reassembles the template onto
// Asset.RefreshDefaults.

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

const assetUUIDSage = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

func TestIntegration_AssetRefreshDefaults_WriteLoadReplace(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewAssetsRepo(f.Pool)

	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO asset (id, name, category) VALUES ($1, 'Sage Bush', 'nature')`, assetUUIDSage); err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	pi := func(v int) *int { return &v }

	// Two rows: a yield-only sage source (null attribute, gather set) and a
	// finite need-bearing drink row (attribute set, null gather).
	rows := []*sim.ObjectRefresh{
		{Amount: 0, GatherItem: "sage",
			AvailableQuantity: pi(10), MaxQuantity: pi(10),
			RefreshMode: sim.RefreshModeContinuous, RefreshPeriodHours: pi(24)},
		{Attribute: "thirst", Amount: -5,
			AvailableQuantity: pi(20), MaxQuantity: pi(20),
			RefreshMode: sim.RefreshModeContinuous, RefreshPeriodHours: pi(12)},
	}
	if err := repo.UpdateAssetRefreshDefaults(ctx, assetUUIDSage, rows); err != nil {
		t.Fatalf("write defaults: %v", err)
	}

	assets, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	a := assets[sim.AssetID(assetUUIDSage)]
	if a == nil || len(a.RefreshDefaults) != 2 {
		t.Fatalf("RefreshDefaults = %+v, want 2 rows", a)
	}

	var yield, need *sim.ObjectRefresh
	for _, r := range a.RefreshDefaults {
		switch {
		case r.GatherItem == "sage":
			yield = r
		case r.Attribute == "thirst":
			need = r
		}
	}
	if yield == nil || yield.Attribute != "" || yield.Amount != 0 ||
		yield.AvailableQuantity == nil || *yield.AvailableQuantity != 10 {
		t.Errorf("yield row = %+v, want sage/amount0/available10 with empty attribute", yield)
	}
	if need == nil || need.GatherItem != "" || need.Amount != -5 ||
		need.RefreshPeriodHours == nil || *need.RefreshPeriodHours != 12 {
		t.Errorf("need row = %+v, want thirst/amount-5/period12 with empty gather", need)
	}

	// Wholesale replace: a single new row must leave exactly one default (the old
	// two gone), proving the DELETE-then-INSERT transaction replaces, not appends.
	replacement := []*sim.ObjectRefresh{
		{Amount: 0, GatherItem: "rosemary",
			AvailableQuantity: pi(5), MaxQuantity: pi(5),
			RefreshMode: sim.RefreshModePeriodic, RefreshPeriodHours: pi(6)},
	}
	if err := repo.UpdateAssetRefreshDefaults(ctx, assetUUIDSage, replacement); err != nil {
		t.Fatalf("replace defaults: %v", err)
	}
	assets, err = repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll after replace: %v", err)
	}
	a = assets[sim.AssetID(assetUUIDSage)]
	if a == nil || len(a.RefreshDefaults) != 1 || a.RefreshDefaults[0].GatherItem != "rosemary" {
		t.Fatalf("after replace RefreshDefaults = %+v, want exactly [rosemary]", a)
	}

	// Clear: an empty set removes the asset's defaults entirely.
	if err := repo.UpdateAssetRefreshDefaults(ctx, assetUUIDSage, nil); err != nil {
		t.Fatalf("clear defaults: %v", err)
	}
	assets, err = repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll after clear: %v", err)
	}
	if a := assets[sim.AssetID(assetUUIDSage)]; a != nil && len(a.RefreshDefaults) != 0 {
		t.Errorf("after clear RefreshDefaults = %+v, want none", a.RefreshDefaults)
	}
}
