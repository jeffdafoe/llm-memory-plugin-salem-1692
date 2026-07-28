package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// trade_reversal_golden_test.go — LLM-555. The perception half of the trade-churn
// fix: a buyer who SOLD an item to a supplier a few hours ago must not be routed
// back to that supplier to buy the same good.
//
// Modelled on a live pair. Hannah Boggs and Josiah Thorne reversed firewood three
// times on 2026-07-19, at gaps of 7.7, 60.6 and 174.3 minutes, each leg a fresh
// conversation. The fixture is the moment before the fourth: she is low on
// firewood, she sold him firewood an hour ago, and there is another woodcutter in
// the village she has no such history with.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "keeper_restock_drops_supplier_she_sold_to",
			summary: "LLM-555 sold-to drop: Hannah Boggs (innkeeper) is low on firewood and has TWO firewood suppliers — " +
				"Josiah's General Store (the distributor), whom she SOLD 6 firewood an hour ago, and Martha's Copse (an " +
				"untagged forager) she has no such history with. The golden pins that the '## Restocking' cue lists ONLY " +
				"the Copse as the walk-to target: the store is dropped with the sold-to reason and its own coda ('what you " +
				"sold is theirs now'), so she is never routed back to buy her own firewood off the man she sold it to. " +
				"This is the cue half of the churn fix — the reverse-pay gate refuses the offer, this stops it ever being " +
				"proposed. Both suppliers are untagged for wholesale, so the fixture isolates the sold-to drop rather than " +
				"the LLM-223 wholesale gate. Keeps TestGoldensRestockNeverTargetsSupplierSoldTo non-vacuous.",
			build: keeperRestockDropsSupplierSheSoldTo,
		},
	)
}

func keeperRestockDropsSupplierSheSoldTo() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		hannahID = sim.ActorID("hannah")
		josiahID = sim.ActorID("josiah")
		marthaID = sim.ActorID("martha")
		inn      = sim.StructureID("the_inn")
		store    = sim.StructureID("general_store")
		copse    = sim.StructureID("marthas_copse")
	)
	start, end := 360, 1080 // 06:00-18:00
	now := 720              // 12:00 — on shift, at the inn
	published := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	hannah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Hannah Boggs",
		Role:              "innkeeper",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 10},
		WorkStructureID:   inn,
		InsideStructureID: inn,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             30,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"firewood": 2},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "firewood", Source: sim.RestockSourceBuy, Max: 12},
		}},
		// She sold Josiah firewood an hour ago. Person-keyed and item-keyed, with no
		// structure: the memory is about the man, which is what lets it work on a
		// visiting trader who has no workplace at all.
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{PeerID: josiahID, ItemKind: "firewood", Condition: sim.ObservedSoldToPeer}: published.Add(-time.Hour),
		}),
	}
	josiah := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Josiah Thorne",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 400, Y: 400},
		WorkStructureID: store,
		Inventory:       map[sim.ItemKind]int{"firewood": 30},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "firewood", Source: sim.RestockSourceBuy, Max: 40},
		}},
	}
	martha := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Martha Reed",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 420, Y: 420},
		WorkStructureID: copse,
		Inventory:       map[sim.ItemKind]int{"firewood": 40},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "firewood", Source: sim.RestockSourceForage, Max: 40},
		}},
	}
	// Buyer-side price history: ~2 coins/firewood at each, affordable on 30 coins, so
	// the means-to-pay gate cannot be what drops either supplier.
	josiahBuys := sim.NewRingBuffer[sim.PriceObservation](8)
	josiahBuys.Push(sim.PriceObservation{BuyerID: hannahID, Amount: 2, Qty: 1, Consumers: 1, At: published.Add(-2 * 24 * time.Hour)})
	marthaBuys := sim.NewRingBuffer[sim.PriceObservation](8)
	marthaBuys.Push(sim.PriceObservation{BuyerID: hannahID, Amount: 2, Qty: 1, Consumers: 1, At: published.Add(-2 * 24 * time.Hour)})
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			hannahID: hannah, josiahID: josiah, marthaID: martha,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			inn:   plainStructure(inn, "The Inn"),
			store: plainStructure(store, "General Store"),
			copse: plainStructure(copse, "Martha's Copse"),
		},
		// The store carries the distributor tag so Josiah is a lawful firewood
		// supplier for a reseller; the copse is untagged, so neither supplier is
		// touched by the LLM-223 wholesale gate and only the sold-to memory can
		// separate them.
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(store): {ID: sim.VillageObjectID(store), OwnerActorID: josiahID, Tags: []string{sim.TagDistributor}},
			sim.VillageObjectID(copse): {ID: sim.VillageObjectID(copse), OwnerActorID: marthaID},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"firewood": {Name: "firewood", Capabilities: []string{"portable"}, DisplayLabel: "firewood", Category: sim.ItemCategoryMaterial},
		},
		RestockReorderPct: 25,
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: josiahID, Item: "firewood"}: josiahBuys,
			{SellerID: marthaID, Item: "firewood"}: marthaBuys,
		},
	}
	return snap, hannahID, nil
}

// TestGoldensRestockNeverTargetsSupplierSoldTo is the cross-scenario invariant for
// LLM-555: across the WHOLE golden matrix, no rendered restock destination may name
// a supplier whose representative keeper the subject remembers selling that item to.
// The property the fix asserts is general — it is not about firewood or about
// Hannah — so it is pinned across every scenario rather than in one golden, the
// same posture the shut-supplier and wholesale invariants take.
//
// A scenario with no such memory passes vacuously; keeper_restock_drops_supplier_
// she_sold_to is what keeps the test honest.
func TestGoldensRestockNeverTargetsSupplierSoldTo(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			actorSnap := snap.Actors[actorID]
			if actorSnap == nil {
				return
			}
			if actorSnap.RestockPolicy == nil {
				return
			}
			// Asserted against findItemVendors rather than the rendered view: the
			// directory is the unit that decides destinations, and it is keyed by
			// ItemKind, which the view carries only as a display label.
			for _, e := range actorSnap.RestockPolicy.Restock {
				vendors, _ := findItemVendors(snap, actorID, actorSnap, e.Item)
				for _, v := range vendors {
					if v.StructureID == "" {
						continue
					}
					for peerID, peer := range snap.Actors {
						if peer == nil || peer.WorkStructureID != v.StructureID {
							continue
						}
						if peerRememberedSoldTo(snap, actorSnap, peerID, e.Item) {
							t.Errorf("scenario %q: restock names %s as a %s destination for %s, but they remember selling %s to its keeper %s",
								sc.name, v.StructureLabel, e.Item, actorSnap.DisplayName, e.Item, peer.DisplayName)
						}
					}
				}
			}
		})
	}
}

// TestSoldToDropIsScoped pins that the drop takes the supplier she sold to and
// leaves the one she did not, so the cue still gives her somewhere to go. A drop
// that emptied the directory would trade the churn for the absorbing state LLM-406
// exists to prevent.
func TestSoldToDropIsScoped(t *testing.T) {
	snap, actorID, warrants := keeperRestockDropsSupplierSheSoldTo()
	out := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))

	if !strings.Contains(out, "Martha's Copse") {
		t.Errorf("the supplier she never sold to must remain a destination; got:\n%s", out)
	}
	if strings.Contains(out, "General Store") {
		t.Errorf("the supplier she sold firewood to must not be named as a firewood destination; got:\n%s", out)
	}
}

// TestSoldToSoleSupplierSaysWhy pins the render arm that only fires when the
// sold-to supplier is the buyer's ONLY one. Blocked suppliers are rendered solely
// for an item with no actionable buy path left (LLM-406), so the two-supplier
// golden never reaches this prose — and a new restockBlockReason that nobody
// renders falls through renderBlockedItem's default arm and tells the actor she
// "called there and found it shut", which she did not.
//
// The case is not hypothetical: the live pair this fixture is drawn from traded
// firewood with each other because Josiah was where Hannah's firewood came from.
func TestSoldToSoleSupplierSaysWhy(t *testing.T) {
	snap, actorID, warrants := keeperRestockDropsSupplierSheSoldTo()
	delete(snap.Actors, sim.ActorID("martha")) // leave the sold-to store as her only firewood supplier
	out := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))

	if strings.Contains(out, "found it shut") {
		t.Errorf("a sold-to block must not render as a shut business; got:\n%s", out)
	}
	// Suppression may remove the destination; it must never remove the reason
	// (LLM-406). She is told where her firewood went.
	if !strings.Contains(out, "you sold it to them yourself") {
		t.Errorf("the sold-to block must say so in the scene; got:\n%s", out)
	}
	// And the way out must not be "go and buy it back", which is the round trip.
	if !strings.Contains(out, "what you sold is theirs now") {
		t.Errorf("the sold-to coda must point away from the buy-back; got:\n%s", out)
	}
}

// TestPeerRememberedSoldToIsDirectionalAndItemScoped pins the property that keeps
// the distributor whole. The memory answers "did I sell kind to P" and nothing
// else: it must not fire for a different item, for a different person, or once it
// has aged past its TTL. Josiah restocked flour from the mill twice in thirty-five
// minutes on the live afternoon this was diagnosed — a same-direction repeat buy
// that this fix must leave completely alone.
func TestPeerRememberedSoldToIsDirectionalAndItemScoped(t *testing.T) {
	const (
		subjectID = sim.ActorID("subject")
		peerID    = sim.ActorID("peer")
		otherID   = sim.ActorID("other")
	)
	published := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	subject := &sim.ActorSnapshot{
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{PeerID: peerID, ItemKind: "firewood", Condition: sim.ObservedSoldToPeer}: published.Add(-time.Hour),
		}),
	}
	snap := &sim.Snapshot{PublishedAt: published}

	if !peerRememberedSoldTo(snap, subject, peerID, "firewood") {
		t.Error("the remembered (peer, item) must read active within its TTL")
	}
	if peerRememberedSoldTo(snap, subject, peerID, "flour") {
		t.Error("a DIFFERENT item from the same peer must not be suppressed — this is what keeps an ordinary restock working")
	}
	if peerRememberedSoldTo(snap, subject, otherID, "firewood") {
		t.Error("the same item from a DIFFERENT peer must not be suppressed — the defect is the round trip with one partner, not the resale")
	}

	// Past the TTL the pair may deal in that direction again: the memory bounds the
	// churn without permanently severing a trading relationship.
	lapsed := &sim.Snapshot{PublishedAt: published.Add(sim.SoldToPeerMemoryTTL + time.Minute)}
	if peerRememberedSoldTo(lapsed, subject, peerID, "firewood") {
		t.Error("the memory must lapse at SoldToPeerMemoryTTL")
	}
}
