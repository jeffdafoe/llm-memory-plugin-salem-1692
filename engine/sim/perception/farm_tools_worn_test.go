package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// farm_tools_worn_test.go — LLM-1005 unit tests beside the goldens: the loss
// line's lifetime, its survival through the evening/summons gates (LossOnly),
// and the upkeep reservation in the wares cue.

func TestFarmToolsWornQty_Window(t *testing.T) {
	snap, actorID, _ := farmUpkeepSnapshot(95, 0, 30, 20)
	snap.PublishedAt = time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)
	a := snap.Actors[actorID]

	if got := farmToolsWornQty(snap, a); got != 0 {
		t.Errorf("no record: %d, want 0", got)
	}
	a.FarmToolsWorn = &sim.FarmToolsWorn{Qty: 3, At: snap.PublishedAt.Add(-14 * time.Hour)}
	if got := farmToolsWornQty(snap, a); got != 3 {
		t.Errorf("14h-old record: %d, want 3", got)
	}
	a.FarmToolsWorn.At = snap.PublishedAt.Add(-sim.FarmToolsWornWindow)
	if got := farmToolsWornQty(snap, a); got != 0 {
		t.Errorf("record at the window edge: %d, want 0", got)
	}
}

// The settled-evening and summons gates drop the shovel errand but keep the loss.
func TestFarmUpkeepLossOnly(t *testing.T) {
	snap, actorID, _ := farmUpkeepSnapshot(95, 0, 30, 20)
	withFarmToolsWorn(snap, actorID, 3)
	v := buildFarmUpkeep(snap, actorID, snap.Actors[actorID])
	if v == nil || v.WornQty != 3 || v.ShovelsShort != 3 {
		t.Fatalf("view = %+v, want worn 3, short 3", v)
	}
	lo := v.LossOnly()
	if lo == nil || lo.WornQty != 3 || lo.ShovelsShort != 0 || lo.HasWalkToSupplier() || lo.CoPresentSeller != "" {
		t.Fatalf("LossOnly = %+v, want the loss alone", lo)
	}
	var b strings.Builder
	renderFarmUpkeep(&b, lo)
	want := "## Farm upkeep\n" + sim.FarmToolsWornNarration(3) + "\n"
	if b.String() != want {
		t.Errorf("LossOnly render = %q, want %q", b.String(), want)
	}

	snap2, id2, _ := farmUpkeepSnapshot(95, 0, 30, 20) // no recorded loss
	if got := buildFarmUpkeep(snap2, id2, snap2.Actors[id2]).LossOnly(); got != nil {
		t.Errorf("LossOnly without a loss = %+v, want nil", got)
	}
}

// Through the full Build: settled in the tavern of an evening, or summoned, the
// shovel errand goes but the loss line stays — and it names no destination.
func TestBuildKeepsFarmLossWhenErrandSilenced(t *testing.T) {
	check := func(name string, snap *sim.Snapshot, actorID sim.ActorID, warrants []sim.WarrantMeta) {
		t.Helper()
		p := Build(snap, actorID, warrants)
		v := p.FarmUpkeep
		if v == nil || v.WornQty != 3 || v.ShovelsShort != 0 || len(v.ShovelVendors) != 0 || v.CoPresentSeller != "" {
			t.Fatalf("%s: FarmUpkeep = %+v, want the loss of 3 alone", name, v)
		}
		out := combinedPrompt(Render(p, DefaultRenderConfig()))
		if !strings.Contains(out, sim.FarmToolsWornNarration(3)) {
			t.Errorf("%s: prompt lacks the loss line", name)
		}
		if strings.Contains(out, "fresh shovel") || strings.Contains(out, "structure_id: blacksmith") {
			t.Errorf("%s: prompt still carries the shovel errand:\n%s", name, out)
		}
	}

	snap, actorID, warrants := farmOwnerSettledInTavernEvening()
	withFarmToolsWorn(snap, actorID, 3)
	check("settled in the tavern", snap, actorID, warrants)

	snap, actorID, warrants = farmOwnerOwesUpkeepWithShovelSupplier()
	withFarmToolsWorn(snap, actorID, 3)
	snap.Actors[actorID].PendingSummon = &sim.PendingSummon{SummonerName: "Josiah Thorne", Place: "the Tavern", At: snap.PublishedAt}
	check("summoned", snap, actorID, warrants)
}

// A farm owner's held upkeep shovels are reserved, not priced as wares; the
// surplus beyond the obligation keeps its price with a keep-back clause.
func TestTradeValue_UpkeepShovelsNotWares(t *testing.T) {
	cases := []struct {
		name      string
		held      int
		reserve   int
		wantLine  string
		forbidden string
	}{
		{"all owed", 2, 2, "- Shovel: the 2 you carry are for the farm's upkeep, not wares.", "your shop lives on the difference"},
		{"surplus", 5, 3, "Keep 3 back for the farm's upkeep; only the 2 beyond that are yours to sell.", "the 5 you carry are for the farm's upkeep"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap, actorID, _ := farmUpkeepSnapshot(95, c.held, 30, 20)
			snap.Recipes = map[sim.ItemKind]*sim.ItemRecipe{
				sim.ShovelItemKind: {WholesalePrice: 6, RetailPrice: 9},
			}
			snap.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
				sim.ShovelItemKind: {Name: sim.ShovelItemKind, DisplayLabel: "Shovel"},
			}
			a := snap.Actors[actorID]
			a.RestockPolicy = producePolicy("wheat", 20) // a working farm, like the live Elizabeth
			a.FarmUpkeepReserve = c.reserve
			v := buildTradeValue(snap, actorID, a, true)
			var b strings.Builder
			renderTradeValue(&b, v)
			got := b.String()
			if !strings.Contains(got, c.wantLine) {
				t.Errorf("wares cue lacks %q:\n%s", c.wantLine, got)
			}
			if strings.Contains(got, c.forbidden) {
				t.Errorf("wares cue carries %q:\n%s", c.forbidden, got)
			}
		})
	}
}
