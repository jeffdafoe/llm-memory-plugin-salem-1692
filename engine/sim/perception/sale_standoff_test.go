package perception

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// sale_standoff_test.go — LLM-525. A buy directory must not send the buyer back to a
// counter whose answer she already has.
//
// The live failure (Elizabeth Ellis, 2026-07-25): she needed 5 nails to mend Ellis
// Farm and carried none; Ezekiel Crane held 2 and was keeping them for his own worn
// forge. Standing with him, the co-present standoff (LLM-297/LLM-510) softened her cue
// to "hold off and come back later". But that classification is derived per-tick from
// the pay ledger scoped to the CURRENT huddle, so the moment she stepped away it was
// gone — and the off-post errand cue re-named the Blacksmith as a move_to destination
// on the very next tick. She turned around. Four round trips in twelve minutes, the
// buy imperative and the return-to-post duty steer pulling opposite ways.
//
// The fix gives the refusal a memory (sim.ObservedSaleStandoff, the LLM-198 shape):
// once the standoff trips it is stamped into the buyer's observed store, and
// findItemVendors drops that (structure, item) for SaleStandoffMemoryTTL. With the
// destination gone the errand cue has no buy path at all, so it renders nothing and
// the return-to-post steer wins.
//
// ownerOffPostShortNailsStandoffRemembered reproduces the live shape and is the
// non-vacuous anchor for the cross-scenario invariant below. Its foil is the existing
// owner_off_post_short_nails_walking (same owner, same shortfall, same supplier, no
// standoff memory → the walk-to destination correctly stands).
func ownerOffPostShortNailsStandoffRemembered() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, actorID, warrants := ellisFarmWorn(sim.WorldPos{X: 1000, Y: 1000}, "", "", 0)
	// Observed.Active needs a clock baseline to decay against; ellisFarmWorn leaves
	// PublishedAt zero (no scenario needed it before). Fixed so the harness re-render
	// is byte-identical.
	published := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	snap.PublishedAt = published
	elizabeth := snap.Actors[actorID]
	// Stamped half an hour ago — well inside the 4h TTL, and old enough that this is
	// plainly a remembered conversation rather than one still in progress.
	elizabeth.Observed = sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
		{StructureID: "blacksmith", ItemKind: "nail", Condition: sim.ObservedSaleStandoff}: published.Add(-30 * time.Minute),
	})
	return snap, actorID, warrants
}

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "owner_off_post_short_nails_standoff_remembered",
			summary: "LLM-525 (the live 2026-07-25 failure): Elizabeth Ellis is off her worn Ellis Farm with 0 of the 5 " +
				"nails a repair takes, and Ezekiel Crane is a resolvable nail supplier of record — but she carries a live " +
				"ObservedSaleStandoff memory for (Blacksmith, nail) from a conversation half an hour ago that went nowhere. " +
				"findItemVendors drops that supplier, so she has no buy path at all and the '## Nails to mend your " +
				"business' cue is ABSENT — and with no errand to defer to, the return-to-post to-work steer correctly " +
				"fires and sends her back to the farm. Before the fix the cue re-issued 'buy from Blacksmith (destination: " +
				"blacksmith)' one step after the co-present soften told her to come back later, and she oscillated " +
				"farm↔blacksmith. Foil of owner_off_post_short_nails_walking (identical, minus the memory → the walk-to " +
				"destination correctly stands).",
			build: ownerOffPostShortNailsStandoffRemembered,
		},
	)
}

// TestSaleStandoff_DropsSupplierFromBuyDirectory is the focused counterpart to the
// cross-scenario invariant: it pins each link of the chain on the live shape — the
// memory drops the supplier out of findItemVendors, names it in the blocked list with
// the standoff reason, and leaves the off-post errand with no buy path at all so
// buildStallRepairBuy returns nil.
func TestSaleStandoff_DropsSupplierFromBuyDirectory(t *testing.T) {
	snap, actorID, _ := ownerOffPostShortNailsStandoffRemembered()
	elizabeth := snap.Actors[actorID]

	vendors, blocked := findItemVendors(snap, actorID, elizabeth, sim.NailItemKind)
	if len(vendors) != 0 {
		t.Errorf("findItemVendors returned %d nail vendor(s), want 0 — a remembered standoff must drop the supplier: %+v", len(vendors), vendors)
	}
	if len(blocked) != 1 {
		t.Fatalf("blocked suppliers = %d, want 1 (the dropped supplier must still be NAMED so the restock cue can state why): %+v", len(blocked), blocked)
	}
	if blocked[0].Reason != restockBlockStandoff {
		t.Errorf("blocked reason = %v, want restockBlockStandoff", blocked[0].Reason)
	}
	if blocked[0].StructureLabel != "Blacksmith" {
		t.Errorf("blocked supplier label = %q, want %q", blocked[0].StructureLabel, "Blacksmith")
	}

	// With the sole supplier dropped and no co-present seller, the off-post errand has
	// no actionable buy path — the LLM-216 dead-end drop — so it renders nothing at
	// all. That absence is what lets the return-to-post steer take the tick.
	if v := buildStallRepairBuy(snap, actorID, elizabeth); v != nil {
		t.Errorf("buildStallRepairBuy = %+v, want nil (no buy path left once the standoff supplier is dropped)", v)
	}

	out := renderScenario(perceptionScenario{name: "owner_off_post_short_nails_standoff_remembered", build: ownerOffPostShortNailsStandoffRemembered})
	if strings.Contains(out, "## Nails to mend your business") {
		t.Errorf("prompt still carries the nail-buy errand cue after the standoff drop:\n%s", out)
	}
	if strings.Contains(out, "(destination: blacksmith)") {
		t.Errorf("prompt still names the Blacksmith as a move_to destination — the whole point is that it stops being one:\n%s", out)
	}
}

// TestSaleStandoff_MemoryIsPerItem pins the (structure, item) scoping: a standoff over
// nails at the smith must not suppress a DIFFERENT good the same smith supplies. The
// buyer's quarrel is with one deal, not with the shop.
func TestSaleStandoff_MemoryIsPerItem(t *testing.T) {
	snap, actorID, _ := ownerOffPostShortNailsStandoffRemembered()
	elizabeth := snap.Actors[actorID]
	ezekiel := snap.Actors["ezekiel"]
	// Give the smith a second good to sell, and the buyer a reason to want it.
	ezekiel.Inventory["shovel"] = 4
	ezekiel.RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{
		{Item: "nail", Source: sim.RestockSourceProduce, Max: 40},
		{Item: "shovel", Source: sim.RestockSourceProduce, Max: 40},
	}}

	if vendors, _ := findItemVendors(snap, actorID, elizabeth, sim.NailItemKind); len(vendors) != 0 {
		t.Errorf("nail vendors = %d, want 0 (the remembered standoff is over nails)", len(vendors))
	}
	shovelVendors, _ := findItemVendors(snap, actorID, elizabeth, sim.ShovelItemKind)
	if len(shovelVendors) != 1 {
		t.Fatalf("shovel vendors = %d, want 1 — a nail standoff must not suppress the smith's other goods: %+v", len(shovelVendors), shovelVendors)
	}
	if shovelVendors[0].StructureID != "blacksmith" {
		t.Errorf("shovel vendor structure = %q, want %q", shovelVendors[0].StructureID, "blacksmith")
	}
}

// TestSaleStandoff_ExpiredMemoryRestoresSupplier pins the self-healing half: past the
// TTL the supplier returns to the directory on its own, so "come back later" really is
// later rather than never. Without this the fix would trade an oscillation for a
// permanently unreachable good.
func TestSaleStandoff_ExpiredMemoryRestoresSupplier(t *testing.T) {
	snap, actorID, _ := ownerOffPostShortNailsStandoffRemembered()
	elizabeth := snap.Actors[actorID]
	elizabeth.Observed = sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
		{StructureID: "blacksmith", ItemKind: "nail", Condition: sim.ObservedSaleStandoff}: snap.PublishedAt.Add(-sim.SaleStandoffMemoryTTL - time.Minute),
	})
	vendors, blocked := findItemVendors(snap, actorID, elizabeth, sim.NailItemKind)
	if len(vendors) != 1 {
		t.Fatalf("nail vendors = %d, want 1 once the standoff memory has aged past its TTL: %+v", len(vendors), vendors)
	}
	if len(blocked) != 0 {
		t.Errorf("blocked = %+v, want empty — an expired memory blocks nothing", blocked)
	}
}

// TestSaleStandoff_InFlightWalkIsNotDiverted pins the LLM-366 / ZBBS-HOME-405 guard:
// while the buyer is actually walking to the shop, the memory must not read as live
// and yank her off the destination she just committed to. Arrival re-opens the
// question — a half-finished walk that turns around at the door is its own
// oscillation, which is what this whole ticket is about.
func TestSaleStandoff_InFlightWalkIsNotDiverted(t *testing.T) {
	snap, actorID, _ := ownerOffPostShortNailsStandoffRemembered()
	elizabeth := snap.Actors[actorID]
	elizabeth.MoveDestStructureID = "blacksmith"
	elizabeth.MoveDestKind = sim.MoveDestinationStructureVisit
	if vendors, _ := findItemVendors(snap, actorID, elizabeth, sim.NailItemKind); len(vendors) != 1 {
		t.Errorf("nail vendors = %d, want 1 — a standoff memory must not divert a walk already under way", len(vendors))
	}
}

// TestGoldensSaleStandoffSupplierNeverADestination is the LLM-525 cross-scenario
// invariant: across the whole matrix, no (structure, item) the subject carries a LIVE
// ObservedSaleStandoff memory for may ever come back out of findItemVendors as a
// destination. findItemVendors is the single directory every buy steer draws its
// move_to targets from — the nail repair-buy, the shovel farm-upkeep buy, the restock
// cue — so holding the property there holds it for all of them, and no future cue can
// reintroduce the oscillation by resolving suppliers its own way.
//
// owner_off_post_short_nails_standoff_remembered is the non-vacuous anchor. Item
// candidates are gathered from the snapshot's kind catalog plus every actor's
// inventory, so a fixture that skips ItemKinds is still covered.
func TestGoldensSaleStandoffSupplierNeverADestination(t *testing.T) {
	var exercised bool
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			subject := snap.Actors[actorID]
			if subject == nil || subject.Observed.Len() == 0 {
				return // no experiential memory at all — invariant N/A
			}
			for _, structureID := range sortedStructureIDs(snap) {
				for _, item := range sortedItemKinds(snap) {
					if !businessRememberedSaleStandoff(snap, subject, structureID, item) {
						continue
					}
					exercised = true
					vendors, _ := findItemVendors(snap, actorID, subject, item)
					for _, v := range vendors {
						if v.StructureID == structureID {
							t.Errorf("scenario %q: %s is a walk-to destination for %q even though the subject remembers a standoff there — the buy directory must drop it (LLM-525)", sc.name, structureID, item)
						}
					}
				}
			}
		})
	}
	if !exercised {
		t.Error("matrix must exercise a subject carrying a live sale-standoff memory for a supplier of a good (LLM-525)")
	}
}

// TestSaleStandoff_EveryBuySteerConsumerHonoursTheDrop covers the blast radius
// (code_review LLM-525): findItemVendors feeds several cues, and the invariant above
// only proves the directory primitive drops the supplier — not that each consumer
// actually stops naming a destination. Each case takes that cue's own existing golden
// fixture, resolves the supplier the cue would send the subject to, stamps a standoff
// on it, and asserts the destination is gone from the rendered prompt. The
// no-standoff render is asserted to carry it first, so a fixture that stopped
// exercising its cue would fail loudly rather than pass vacuously.
func TestSaleStandoff_EveryBuySteerConsumerHonoursTheDrop(t *testing.T) {
	cases := []struct {
		name  string
		build func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta)
		item  sim.ItemKind
	}{
		{"stall_repair_nails", ownerOffPostShortNailsWalking, sim.NailItemKind},
		{"farm_upkeep_shovels", farmOwnerOwesUpkeepWithShovelSupplier, sim.ShovelItemKind},
		{"hearth_firewood", keeperLowHearthShortWoodWithSupplier, sim.FirewoodItemKind},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Baseline: the cue names the supplier as a move_to destination.
			snap, actorID, warrants := tc.build()
			vendors, _ := findItemVendors(snap, actorID, snap.Actors[actorID], tc.item)
			if len(vendors) == 0 {
				t.Fatalf("fixture no longer resolves a %q supplier — this case can't prove anything", tc.item)
			}
			before := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))
			for _, v := range vendors {
				if !strings.Contains(before, "(destination: "+string(v.StructureID)+")") {
					t.Fatalf("baseline prompt does not name %s as a destination — the fixture no longer exercises this cue:\n%s", v.StructureID, before)
				}
			}

			// Same fixture, with the subject remembering a standoff at each supplier.
			snap, actorID, warrants = tc.build()
			subject := snap.Actors[actorID]
			if snap.PublishedAt.IsZero() {
				snap.PublishedAt = time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
			}
			stamps := map[sim.ObservedStateKey]time.Time{}
			for _, v := range vendors {
				stamps[sim.ObservedStateKey{StructureID: v.StructureID, ItemKind: tc.item, Condition: sim.ObservedSaleStandoff}] = snap.PublishedAt.Add(-30 * time.Minute)
			}
			subject.Observed = sim.NewObservedStates(stamps)

			after := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))
			for _, v := range vendors {
				if strings.Contains(after, "(destination: "+string(v.StructureID)+")") {
					t.Errorf("cue still sends the subject to %s for %q despite a remembered standoff there (LLM-525):\n%s", v.StructureID, tc.item, after)
				}
			}
		})
	}
}

// TestRenderBlockedItem_StandoffProse covers the render half of the new blocked reason
// (code_review LLM-525): the standoff sub-bullet and its own resolution, alone and
// mixed with each other reason, so no combination emits a contradictory coda or drops
// a supplier's way out. The pre-existing shut/no-means rows are included to pin that
// their prose is unchanged.
func TestRenderBlockedItem_StandoffProse(t *testing.T) {
	const (
		standoffBullet = "The Blacksmith sells nails, but you pressed them for it not long ago and could not come to terms."
		shutBullet     = "Thorne's General Store sells nails, but you called there and found it shut."
		noMeansBullet  = "Ellis Farm sells nails, but you have neither the coin for it nor a good you can spare to put up in trade."
		standoffCoda   = "Let that one rest and ask again later in the day"
		shutCoda       = "Look in again another day"
		noMeansCoda    = "Keep your shop and take what trade comes to you"
	)
	blocked := func(reasons ...restockBlockReason) []RestockBlockedSupplier {
		out := make([]RestockBlockedSupplier, 0, len(reasons))
		for _, r := range reasons {
			label := "The Blacksmith"
			switch r {
			case restockBlockShut:
				label = "Thorne's General Store"
			case restockBlockNoMeans:
				label = "Ellis Farm"
			}
			out = append(out, RestockBlockedSupplier{StructureLabel: label, Reason: r})
		}
		return out
	}
	cases := []struct {
		name    string
		blocked []RestockBlockedSupplier
		want    []string
		absent  []string
	}{
		{"standoff_only", blocked(restockBlockStandoff),
			[]string{standoffBullet, standoffCoda}, []string{shutCoda, noMeansCoda}},
		{"shut_only", blocked(restockBlockShut),
			[]string{shutBullet, shutCoda}, []string{standoffCoda, noMeansCoda}},
		{"no_means_only", blocked(restockBlockNoMeans),
			[]string{noMeansBullet, noMeansCoda}, []string{standoffCoda, shutCoda}},
		{"standoff_and_shut", blocked(restockBlockStandoff, restockBlockShut),
			[]string{standoffBullet, shutBullet, standoffCoda, shutCoda}, []string{noMeansCoda}},
		{"standoff_and_no_means", blocked(restockBlockStandoff, restockBlockNoMeans),
			[]string{standoffBullet, noMeansBullet, standoffCoda, noMeansCoda}, []string{shutCoda}},
		{"all_three", blocked(restockBlockStandoff, restockBlockShut, restockBlockNoMeans),
			[]string{standoffBullet, shutBullet, noMeansBullet, standoffCoda,
				"once you have coin or goods to trade with you can restock, and the shut one is worth looking in on another day"}, nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			renderBlockedItem(&b, RestockItemView{CurrentQty: 0, ItemLabel: "nails", Blocked: tc.blocked})
			out := b.String()
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("blocked-item prose missing %q:\n%s", want, out)
				}
			}
			for _, bad := range tc.absent {
				if strings.Contains(out, bad) {
					t.Errorf("blocked-item prose carries an inapplicable resolution %q:\n%s", bad, out)
				}
			}
			// Every blocked item must end with SOME way out — a bare want with no
			// resolution is the vacuum the weak model fills by inventing an errand
			// (LLM-298).
			if !strings.Contains(out, standoffCoda) && !strings.Contains(out, shutCoda) && !strings.Contains(out, noMeansCoda) {
				t.Errorf("blocked-item prose leaves the want dangling with no resolution:\n%s", out)
			}
		})
	}
}

// sortedStructureIDs / sortedItemKinds give the invariant a deterministic sweep over a
// fixture's places and goods. Item candidates come from the kind catalog AND every
// actor's inventory, because plenty of fixtures carry goods without declaring an
// ItemKinds catalog.
func sortedStructureIDs(snap *sim.Snapshot) []sim.StructureID {
	out := make([]sim.StructureID, 0, len(snap.Structures))
	for id := range snap.Structures {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortedItemKinds(snap *sim.Snapshot) []sim.ItemKind {
	seen := map[sim.ItemKind]struct{}{}
	for kind := range snap.ItemKinds {
		seen[kind] = struct{}{}
	}
	for _, a := range snap.Actors {
		for kind := range a.Inventory {
			seen[kind] = struct{}{}
		}
	}
	out := make([]sim.ItemKind, 0, len(seen))
	for kind := range seen {
		out = append(out, kind)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
