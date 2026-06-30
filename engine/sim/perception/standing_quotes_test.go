package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// standing_quotes_test.go — LLM-45. The seller-side "## Offers you've put out"
// cue (buildStandingQuotesFromMe + renderStandingQuotesFromMe): the
// seller/scene_quote mirror of HOME-413's buyer-side standing-offer view. It
// gives a seller cross-tick memory of the wares it offered and to whom, so a weak
// model neither re-posts a standing quote (the already_quoted thrash) nor
// confabulates a queue between two co-present seekers (the John Ellis two-room
// scene: he told Jefferey to wait on Ezekiel while his own room offer to Jefferey
// stood).

// activeQuote builds an active SceneQuote for tests.
func activeQuote(id sim.QuoteID, seller, target sim.ActorID, item sim.ItemKind, qty, amount int) *sim.SceneQuote {
	return &sim.SceneQuote{
		ID:          id,
		SellerID:    seller,
		TargetBuyer: target,
		Lines:       []sim.QuoteLine{{ItemKind: item, Qty: qty}},
		Amount:      amount,
		State:       sim.SceneQuoteStateActive,
	}
}

// quoteSnap mirrors offerSnap: John Ellis (seller) and Jefferey (buyer), with the
// seller acquainted with the buyer so descriptorLabel yields the plain name.
func quoteSnap(quotes map[sim.QuoteID]*sim.SceneQuote) *sim.Snapshot {
	return &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"john": {DisplayName: "John Ellis", Role: "tavernkeeper", Kind: sim.KindNPCStateful,
				Needs: map[sim.NeedKey]int{}, Acquaintances: map[string]sim.Acquaintance{"Jefferey": {}}},
			"jefferey": {DisplayName: "Jefferey", Role: "traveler", Kind: sim.KindNPCShared, Needs: map[sim.NeedKey]int{}},
		},
		Quotes:     quotes,
		PayLedger:  map[sim.LedgerID]*sim.PayLedgerEntry{},
		Scenes:     map[sim.SceneID]*sim.Scene{},
		Huddles:    map[sim.HuddleID]*sim.Huddle{},
		Structures: map[sim.StructureID]*sim.Structure{},
	}
}

// A seller's own active quotes surface — targeted (with the buyer's name) and
// public (untargeted) — sorted by QuoteID ascending.
func TestBuildStandingQuotesFromMe_TargetedAndPublic(t *testing.T) {
	snap := quoteSnap(map[sim.QuoteID]*sim.SceneQuote{
		2: activeQuote(2, "john", "", "nights_stay", 1, 4),         // public
		1: activeQuote(1, "john", "jefferey", "nights_stay", 1, 4), // targeted
	})
	views := buildStandingQuotesFromMe(snap, "john", snap.Actors["john"])
	if len(views) != 2 {
		t.Fatalf("views = %d, want 2", len(views))
	}
	if views[0].QuoteID != 1 || views[0].BuyerName != "Jefferey" {
		t.Errorf("views[0] = %+v, want QuoteID 1 BuyerName Jefferey (targeted, acquainted)", views[0])
	}
	if views[1].QuoteID != 2 || views[1].BuyerName != "" {
		t.Errorf("views[1] = %+v, want QuoteID 2 empty BuyerName (public)", views[1])
	}
}

// Foreign-seller quotes, terminal quotes, and the buyer-subject view are all
// excluded — the scan returns only the subject's OWN active quotes.
func TestBuildStandingQuotesFromMe_FiltersForeignTerminalAndBuyerSubject(t *testing.T) {
	expired := activeQuote(5, "john", "jefferey", "nights_stay", 1, 4)
	expired.State = sim.SceneQuoteStateExpired
	snap := quoteSnap(map[sim.QuoteID]*sim.SceneQuote{
		3: activeQuote(3, "elizabeth", "jefferey", "stew", 1, 4), // another seller
		5: expired,                                               // terminal
	})
	if got := buildStandingQuotesFromMe(snap, "john", snap.Actors["john"]); got != nil {
		t.Errorf("got %v, want nil (foreign + expired filtered)", got)
	}
	one := quoteSnap(map[sim.QuoteID]*sim.SceneQuote{1: activeQuote(1, "john", "jefferey", "nights_stay", 1, 4)})
	if got := buildStandingQuotesFromMe(one, "jefferey", one.Actors["jefferey"]); got != nil {
		t.Errorf("buyer subject got %v, want nil (quote is theirs to take, not posted by them)", got)
	}
}

// LLM-189: a quote taken via the fast path flips to the terminal
// SceneQuoteStateTaken, so it drops out of "## Offers you've put out" while a
// separate still-active quote stays. Pins the live Prudence regression — a
// just-sold quote kept rendering as a phantom standing offer ("they have yet
// to answer"), luring the seller into firing the buyer verb at her own customer.
func TestBuildStandingQuotesFromMe_TakenQuoteExcluded(t *testing.T) {
	taken := activeQuote(7, "john", "jefferey", "stew", 1, 4)
	taken.State = sim.SceneQuoteStateTaken
	snap := quoteSnap(map[sim.QuoteID]*sim.SceneQuote{
		1: activeQuote(1, "john", "jefferey", "nights_stay", 1, 4), // still standing
		7: taken,                                                   // just sold
	})
	views := buildStandingQuotesFromMe(snap, "john", snap.Actors["john"])
	if len(views) != 1 || views[0].QuoteID != 1 {
		t.Fatalf("views = %+v, want only the active quote 1 (taken quote 7 excluded)", views)
	}
}

// An unacquainted targeted buyer renders as a role descriptor, not a name.
func TestBuildStandingQuotesFromMe_UnacquaintedBuyerGetsDescriptor(t *testing.T) {
	snap := quoteSnap(map[sim.QuoteID]*sim.SceneQuote{
		1: activeQuote(1, "john", "stranger", "nights_stay", 1, 4),
	})
	snap.Actors["stranger"] = &sim.ActorSnapshot{DisplayName: "Goodman Stranger", Role: "blacksmith",
		Kind: sim.KindNPCShared, Needs: map[sim.NeedKey]int{}}
	views := buildStandingQuotesFromMe(snap, "john", snap.Actors["john"])
	if len(views) != 1 || views[0].BuyerName != "the blacksmith" {
		t.Fatalf("views = %+v, want one with BuyerName 'the blacksmith' (unacquainted)", views)
	}
}

// A nil quote entry is skipped, and a targeted buyer missing from the snapshot
// falls back to a generic descriptor rather than leaking the raw actor id.
func TestBuildStandingQuotesFromMe_NilQuoteAndMissingBuyerSafe(t *testing.T) {
	snap := quoteSnap(map[sim.QuoteID]*sim.SceneQuote{
		1: nil,
		2: activeQuote(2, "john", "missing", "nights_stay", 1, 4),
	})
	views := buildStandingQuotesFromMe(snap, "john", snap.Actors["john"])
	if len(views) != 1 {
		t.Fatalf("views = %d, want 1 (nil entry skipped)", len(views))
	}
	if views[0].BuyerName == "missing" {
		t.Fatalf("leaked raw actor id: %+v", views[0])
	}
	if views[0].BuyerName != "someone" {
		t.Errorf("missing buyer BuyerName = %q, want \"someone\"", views[0].BuyerName)
	}
}

func TestRenderStandingQuotesFromMe_TargetedLine(t *testing.T) {
	var b strings.Builder
	renderStandingQuotesFromMe(&b, []StandingQuoteView{
		{QuoteID: 1, BuyerName: "Jefferey", Lines: []sim.QuoteLine{{ItemKind: "nights_stay", Qty: 1}}, Amount: 4},
	})
	out := b.String()
	for _, must := range []string{
		"## Offers you've put out",
		"You have offered Jefferey nights_stay for 4 coins",
		"they have yet to answer",
		"do not post it again",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("missing %q\n--- output ---\n%s", must, out)
		}
	}
}

func TestRenderStandingQuotesFromMe_PublicLine(t *testing.T) {
	var b strings.Builder
	renderStandingQuotesFromMe(&b, []StandingQuoteView{
		{QuoteID: 2, BuyerName: "", Lines: []sim.QuoteLine{{ItemKind: "nights_stay", Qty: 1}}, Amount: 4},
	})
	out := b.String()
	if !strings.Contains(out, "nights_stay for 4 coins to anyone here") {
		t.Errorf("public quote line wrong\n%s", out)
	}
}

func TestRenderStandingQuotesFromMe_EmptyGated(t *testing.T) {
	var b strings.Builder
	renderStandingQuotesFromMe(&b, nil)
	if b.Len() != 0 {
		t.Errorf("empty list produced output: %q", b.String())
	}
}

// End-to-end: a seller with an active quote shows the section in the full prompt.
func TestRender_SellerStandingQuoteSection(t *testing.T) {
	snap := quoteSnap(map[sim.QuoteID]*sim.SceneQuote{
		1: activeQuote(1, "john", "jefferey", "nights_stay", 1, 4),
	})
	p := Build(snap, "john", nil)
	out := combinedPrompt(Render(p, DefaultRenderConfig()))
	if !strings.Contains(out, "## Offers you've put out") || !strings.Contains(out, "You have offered Jefferey nights_stay") {
		t.Errorf("seller standing-quote cue missing from full prompt\n%s", out)
	}
}
