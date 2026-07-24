package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// order_direction_golden_test.go — LLM-512. A buyer waiting on an undelivered
// order must read as the SELLER owing the buyer the goods, never the reverse.
//
// The live failure: Elizabeth Ellis, waiting on a shovel she had PAID Ezekiel
// Crane for (a Ready made-to-order commission he had not yet forged), saw
// "## Orders you're waiting on / - #N: shovel from Ezekiel Crane" and memorized
// it flipped — "I owe Ezekiel a shovel" — then acted on the false debt for two
// days. The same accept, inside the 3-minute settled-offers window, also told
// her "it's in your pack now. That deal is done", false for goods still on the
// smith's anvil. No golden exercised either buyer-side order line, so it shipped.
//
// buyerAwaitingUndeliveredOrder pins both: the waiting-on line reads "Ezekiel
// Crane owes you 1 shovel", and the settled line reads "it's not in your pack
// yet, Ezekiel Crane will hand it over when it's ready" — even though she is
// carrying 3 shovels of her own (the inventory that fused into the live note).

func buyerAwaitingUndeliveredOrder() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		elizabethID = sim.ActorID("elizabeth")
		ezekielID   = sim.ActorID("ezekiel")
		home        = sim.StructureID("ellis_residence")
		forge       = sim.StructureID("crane_forge")
		ledgerID    = sim.LedgerID(2034)
		orderID     = sim.OrderID(2034)
	)
	// Fixed clock so the order-expiry clause and the settled-offers window render
	// deterministically (RenderedAt = PublishedAt; the harness re-renders and
	// requires byte equality).
	published := time.Date(2026, 7, 19, 12, 51, 0, 0, time.UTC)
	today := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	minute := 771 // 12:51

	elizabeth := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Elizabeth Ellis",
		Role:              "farmer",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 40, Y: 40},
		InsideStructureID: home,
		Coins:             73,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"shovel": 3},
		// Acquainted with the smith so the settled-offers line names him rather
		// than falling back to a role descriptor.
		Acquaintances: map[string]sim.Acquaintance{"Ezekiel Crane": {}},
	}
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 80, Y: 80},
		InsideStructureID: forge,
		Needs:             map[sim.NeedKey]int{},
	}

	// The Ready order the smith owes her, minted from ledger #2034; not yet
	// forged, so it sits in his hands (not her pack) until deliver_order.
	order := &sim.Order{
		ID: orderID, State: sim.OrderStateReady,
		BuyerID: elizabethID, SellerID: ezekielID,
		Item: "shovel", Qty: 1, Amount: 8,
		ConsumerIDs: []sim.ActorID{elizabethID},
		LedgerID:    ledgerID,
		CreatedAt:   published.Add(-2 * time.Minute),
		ReadyBy:     today, // ready today → "waiting on", not overdue
		ExpiresAt:   published.Add(900 * time.Minute),
	}
	// The accepted offer that minted the order — within the settled-offers window
	// (resolved 1 minute ago), so it renders alongside the waiting-on line.
	ledgerEntry := &sim.PayLedgerEntry{
		ID: ledgerID, BuyerID: elizabethID, SellerID: ezekielID,
		ItemKind: "shovel", Qty: 1, Amount: 8,
		State:      sim.PayLedgerStateAccepted,
		ConsumeNow: false,
		ResolvedAt: published.Add(-1 * time.Minute),
	}

	snap := &sim.Snapshot{
		LocalMinuteOfDay: &minute,
		PublishedAt:      published,
		LocalDateUTC:     today,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{elizabethID: elizabeth, ezekielID: ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			home:  plainStructure(home, "Ellis Residence"),
			forge: plainStructure(forge, "Crane Forge"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(home):  {ID: sim.VillageObjectID(home), Pos: sim.WorldPos{X: 640, Y: 640}},
			sim.VillageObjectID(forge): {ID: sim.VillageObjectID(forge), Pos: sim.WorldPos{X: 1280, Y: 1280}},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"shovel": {Name: "shovel", DisplayLabel: "shovel", DisplayLabelSingular: "shovel", DisplayLabelPlural: "shovels", Category: sim.ItemCategory("tool")},
		},
		Orders:    map[sim.OrderID]*sim.Order{orderID: order},
		PayLedger: map[sim.LedgerID]*sim.PayLedgerEntry{ledgerID: ledgerEntry},
	}
	return snap, elizabethID, nil
}

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "buyer_awaiting_undelivered_order",
			summary: "LLM-512: Elizabeth Ellis waiting on a shovel she PAID Ezekiel Crane for (a Ready order, " +
				"not yet forged), inside the settled-offers window, while carrying 3 shovels of her own. The " +
				"buyer-side lines must state the seller owes HER — '## Orders you're waiting on: Ezekiel Crane " +
				"owes you 1 shovel' and settled 'it's not in your pack yet, Ezekiel Crane will hand it over when " +
				"it's ready' — never the flipped 'shovel from Ezekiel' / 'in your pack now' that let the model " +
				"memorize a false debt.",
			build: buyerAwaitingUndeliveredOrder,
		},
	)
}

// TestGoldensBuyerOrderNeverReadsAsBuyerOwes is the LLM-512 cross-scenario
// invariant: in every scenario, each line under "## Orders you're waiting on" or
// "## Overdue — paid but not delivered" must state the seller owes the buyer
// ("owes you") and must never read as the buyer owing the seller ("you owe").
// A buyer's open order is a debt the SELLER carries; the flipped phrasing is what
// corrupted a live NPC's memory into a two-day false obligation.
func TestGoldensBuyerOrderNeverReadsAsBuyerOwes(t *testing.T) {
	target := func(section string) bool {
		return section == "## Orders you're waiting on" || section == "## Overdue — paid but not delivered"
	}
	sawLine := false
	for _, sc := range perceptionScenarios {
		out := renderScenario(sc)
		var section string
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "## ") {
				section = strings.TrimRight(line, " ")
				continue
			}
			if !target(section) {
				continue
			}
			l := strings.TrimSpace(line)
			if !strings.HasPrefix(l, "- #") {
				continue
			}
			sawLine = true
			// Scope the direction check to the line head (before the first " — "
			// clause separator), so a partial-payment balanceClause or the expiry
			// tail can't false-trip the forbidden shapes (code_review, LLM-512).
			head := l
			if i := strings.Index(l, " — "); i >= 0 {
				head = l[:i]
			}
			if !strings.Contains(head, "owes you") {
				t.Errorf("scenario %q: buyer order line lacks the explicit \"owes you\" direction: %q", sc.name, l)
			}
			if strings.Contains(head, " from ") || strings.Contains(head, "you owe") {
				t.Errorf("scenario %q: buyer order line reads as the buyer owing the seller: %q", sc.name, l)
			}
		}
	}
	if !sawLine {
		t.Fatal("no scenario renders a buyer order line — the LLM-512 direction invariant is vacuous")
	}
}

// TestBuyerAwaitingUndeliveredOrderScenario is the focused counterpart to the
// cross-scenario invariant: it asserts the buyer_awaiting_undelivered_order
// scenario actually renders BOTH corrected lines (waiting-on + settled) and none
// of the flipped/false phrasings — so a regression fails here diagnostically, not
// just as a golden-diff.
func TestBuyerAwaitingUndeliveredOrderScenario(t *testing.T) {
	out := renderScenario(perceptionScenario{name: "buyer_awaiting_undelivered_order", build: buyerAwaitingUndeliveredOrder})
	for _, want := range []string{
		"## Orders you're waiting on",
		"Ezekiel Crane owes you 1 shovel",
		"## Recently settled offers",
		"it's not in your pack yet, Ezekiel Crane will hand it over when it's ready",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scenario missing %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"shovel from Ezekiel", "it's in your pack now"} {
		if strings.Contains(out, bad) {
			t.Errorf("scenario still contains the flipped/false phrasing %q:\n%s", bad, out)
		}
	}
}
