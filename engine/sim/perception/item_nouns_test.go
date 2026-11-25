package perception

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// countedNounSections are the offer and order-book sections whose "<n> <goods>"
// counts LLM-1029 moved from the raw kind key to the catalog count noun.
var countedNounSections = []string{
	"## Offers awaiting your decision",
	"## Orders to deliver",
	"## Upcoming bookings",
	"## Orders you're waiting on",
	"## Overdue — paid but not delivered",
	"## Offers you have standing",
	"## Recently settled offers",
}

// sectionBody is the text of the section that starts at header, up to the next
// "## " header. Empty when the section is not rendered.
func sectionBody(text, header string) string {
	start := strings.Index(text, header+"\n")
	if start < 0 {
		return ""
	}
	body := text[start+len(header)+1:]
	if end := strings.Index(body, "\n## "); end >= 0 {
		body = body[:end]
	}
	return body
}

// TestGoldensOfferAndOrderLinesUseCountNouns is the LLM-1029 cross-scenario
// invariant: in the offer and order-book sections, no "<n> <kind key>" appears
// where the catalog has a different phrase for that count. "5 nail" and
// "1 fried_meat" fail; "1 nail" passes, because the catalog's singular for nail
// is "nail". It checks counted goods only; a one-item order line that names the
// goods without a numeral is pinned by TestOrderLinesNamePerRecipientNoun.
func TestGoldensOfferAndOrderLinesUseCountNouns(t *testing.T) {
	for _, sc := range perceptionScenarios {
		snap, actorID, warrants := sc.build()
		text := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))
		for kind, def := range snap.ItemKinds {
			if def == nil {
				continue
			}
			// \b on both sides, so "5 nails" never reads as "5 nail".
			re := regexp.MustCompile(`\b(\d+) ` + regexp.QuoteMeta(string(kind)) + `\b`)
			for _, header := range countedNounSections {
				body := sectionBody(text, header)
				for _, m := range re.FindAllStringSubmatchIndex(body, -1) {
					qty, err := strconv.Atoi(body[m[2]:m[3]])
					if err != nil {
						continue
					}
					want := def.CountNoun(qty)
					if want == "" || want == string(kind) {
						continue
					}
					// The right noun can start with the key ("3 fish fillets" for
					// key fish); that is the right text, not the key.
					if strings.HasPrefix(body[m[0]:], strconv.Itoa(qty)+" "+want) {
						continue
					}
					t.Errorf("%s: %q in %q names the kind key; the catalog says %q", sc.name, body[m[0]:m[1]], header, strconv.Itoa(qty)+" "+want)
				}
			}
		}
	}
}

// TestOrderLinesNamePerRecipientNoun pins the per-recipient noun against the
// order-total noun: one shovel each for two people is "shovel" on the order
// book's per-recipient line, while the order's total noun stays "shovels".
func TestOrderLinesNamePerRecipientNoun(t *testing.T) {
	snap := orderSnap(map[sim.OrderID]*sim.Order{
		7: orderOf(7, "hannah", "jefferey", "shovel", 1, []sim.ActorID{"jefferey", "mary"}, time.Hour),
	})
	snap.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
		"shovel": {Name: "shovel", DisplayLabelSingular: "shovel", DisplayLabelPlural: "shovels"},
	}
	fromMe, _ := buildPendingOrderViews(snap, "hannah")
	if len(fromMe) != 1 {
		t.Fatalf("fromMe = %v, want the one order", fromMe)
	}
	if got := fromMe[0].QtyNoun; got != "shovel" {
		t.Errorf("QtyNoun = %q, want %q (one per recipient)", got, "shovel")
	}
	if got := fromMe[0].Noun; got != "shovels" {
		t.Errorf("Noun = %q, want %q (two across the order)", got, "shovels")
	}
	var b strings.Builder
	renderOrdersReadyToHandOver(&b, fromMe, time.Now())
	if want := "- #7: shovel for Jefferey"; !strings.Contains(b.String(), want) {
		t.Errorf("seller line missing %q:\n%s", want, b.String())
	}
	_, toMe := buildPendingOrderViews(snap, "jefferey")
	b.Reset()
	renderOrdersWaitingOn(&b, toMe, time.Now())
	if want := "owes you 1 shovel"; !strings.Contains(b.String(), want) {
		t.Errorf("buyer line missing %q:\n%s", want, b.String())
	}

	// Two each: the per-recipient line counts the plural.
	snap.Orders[7].Qty = 2
	fromMe, _ = buildPendingOrderViews(snap, "hannah")
	b.Reset()
	renderOrdersReadyToHandOver(&b, fromMe, time.Now())
	if want := "- #7: 2 shovels for Jefferey"; !strings.Contains(b.String(), want) {
		t.Errorf("seller line missing %q:\n%s", want, b.String())
	}
}

// TestPayOfferPartialStockNamesBothCounts pins the held count and the asked
// count to their own nouns: holding one shovel against an ask for three reads
// "for 3 shovels … you hold only 1 shovel", and the barter goods are named by
// their noun too.
func TestPayOfferPartialStockNamesBothCounts(t *testing.T) {
	nouns := buildItemNouns(&sim.Snapshot{ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
		"shovel": {Name: "shovel", DisplayLabelSingular: "shovel", DisplayLabelPlural: "shovels"},
		"wheat":  {Name: "wheat", DisplayLabelSingular: "sheaf of wheat", DisplayLabelPlural: "sheaves of wheat"},
	}})
	offers := []sim.PayOfferWarrantReason{{
		LedgerID: 41, Buyer: "jefferey", Item: "shovel", Qty: 3,
		PayItems: []sim.ItemKindQty{{Kind: "wheat", Qty: 6}},
	}}
	shortfalls := map[sim.LedgerID]StockShortfall{41: {Held: 1, Noun: "shovels"}}
	nameOf := func(sim.ActorID) string { return "Jefferey" }
	var b strings.Builder
	renderPayOffers(&b, offers, nameOf, shortfalls, nil, nil, nouns)
	if want := "Jefferey offers 6 sheaves of wheat for 3 shovels to keep (offer id 41) — you hold only 1 shovel"; !strings.Contains(b.String(), want) {
		t.Errorf("offer line missing %q:\n%s", want, b.String())
	}
}

func TestItemNounsCount(t *testing.T) {
	nouns := buildItemNouns(&sim.Snapshot{ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
		"flour":      {Name: "flour", DisplayLabelSingular: "sack of flour", DisplayLabelPlural: "sacks of flour"},
		"sheep":      {Name: "sheep", DisplayLabelSingular: "sheep", DisplayLabelPlural: "sheep"},
		"fried_meat": {Name: "fried_meat", DisplayLabel: "fried meat"},
		"unlabeled":  {Name: "unlabeled"},
	}})
	cases := []struct {
		kind sim.ItemKind
		qty  int
		want string
	}{
		{"flour", 1, "sack of flour"},
		{"flour", 7, "sacks of flour"},
		{"flour", 0, "sacks of flour"},
		{"sheep", 5, "sheep"},
		// Only a DisplayLabel: Singular and Plural both fall back to it.
		{"fried_meat", 1, "fried meat"},
		{"fried_meat", 2, "fried meat"},
		// No phrase at all, and a kind not in the catalog: the raw key.
		{"unlabeled", 3, "unlabeled"},
		{"ghost", 3, "ghost"},
	}
	for _, c := range cases {
		if got := nouns.count(c.kind, c.qty); got != c.want {
			t.Errorf("count(%s, %d) = %q, want %q", c.kind, c.qty, got, c.want)
		}
	}
	// A nil table (a hand-built payload) names every kind by its key.
	var none itemNouns
	if got := none.counted("shovel", 3); got != "3 shovel" {
		t.Errorf("nil table counted = %q, want %q", got, "3 shovel")
	}
}
