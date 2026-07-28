package perception

import (
	"strings"
	"testing"
	"time"

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

// --- LLM-551: the buyer-side twin, "## Offers made to you" ---------------

// buyerQuoteSnap is quoteSnap with the two actors in a shared huddle. The
// buyer-side section is ACTIONABLE — its lines end in a pay_with_item call — and
// pay_with_item resolves its seller among the caller's huddle peers, so every
// buyer-side fixture must put them in conversation or the offer isn't takeable.
func buyerQuoteSnap(quotes map[sim.QuoteID]*sim.SceneQuote) *sim.Snapshot {
	snap := quoteSnap(quotes)
	const huddle = sim.HuddleID("h1")
	snap.Actors["john"].CurrentHuddleID = huddle
	snap.Actors["jefferey"].CurrentHuddleID = huddle
	snap.Huddles = map[sim.HuddleID]*sim.Huddle{
		huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{"john": {}, "jefferey": {}}},
	}
	return snap
}

// The buyer of a targeted quote sees it, with the quote_id pay_with_item needs.
// The seller of that same quote does NOT see it in this section — it is his, and
// he already has "## Offers you've put out".
func TestBuildStandingQuotesToMe_TargetedOnly(t *testing.T) {
	snap := buyerQuoteSnap(map[sim.QuoteID]*sim.SceneQuote{
		1: activeQuote(1, "john", "jefferey", "bread", 2, 4),
		2: activeQuote(2, "john", "", "bread", 2, 4), // public — an ad to the room
	})
	views := buildStandingQuotesToMe(snap, "jefferey", snap.Actors["jefferey"], nil, nil)
	if len(views) != 1 {
		t.Fatalf("views = %d, want 1 (the targeted quote only)", len(views))
	}
	if views[0].QuoteID != 1 {
		t.Errorf("QuoteID = %d, want 1", views[0].QuoteID)
	}
	// Jefferey isn't acquainted with John in the fixture, so the seller reads as
	// a role descriptor rather than a name — the same gating the seller side uses.
	if views[0].SellerName == "" {
		t.Error("SellerName empty — the buyer must be told who is offering")
	}
	if len(buildStandingQuotesToMe(snap, "john", snap.Actors["john"], nil, nil)) != 0 {
		t.Error("the seller sees his own quote in the buyer-side section")
	}
}

// A quote announced by THIS tick's warrant is left to the warrant line — the
// section exists for every LATER tick, once that one-shot is spent.
func TestBuildStandingQuotesToMe_SkipsQuoteAnnouncedThisTick(t *testing.T) {
	snap := buyerQuoteSnap(map[sim.QuoteID]*sim.SceneQuote{
		1: activeQuote(1, "john", "jefferey", "bread", 2, 4),
	})
	warrants := []sim.WarrantMeta{{
		TriggerActorID: "john",
		Reason: sim.SceneQuoteTargetedWarrantReason{
			QuoteID: 1, SellerID: "john",
			Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 2}}, Amount: 4,
		},
		SourceEventID: 1,
	}}
	if got := buildStandingQuotesToMe(snap, "jefferey", snap.Actors["jefferey"], nil, warrants); len(got) != 0 {
		t.Errorf("views = %d, want 0 — the warrant line already carries this quote", len(got))
	}
	// Same quote, next tick: the warrant is spent and the section takes over.
	if got := buildStandingQuotesToMe(snap, "jefferey", snap.Actors["jefferey"], nil, nil); len(got) != 1 {
		t.Errorf("views = %d, want 1 once the warrant is spent — this is the defect", len(got))
	}
}

// An unswept expired quote still reads Active in the map. Rendering it would
// hand the buyer a take-instruction the fast path then refuses — a cue at war
// with its gate, which is the whole bug class this ticket is about.
func TestBuildStandingQuotesToMe_ExpiredQuoteExcluded(t *testing.T) {
	now := time.Date(2026, 7, 28, 19, 16, 0, 0, time.UTC)
	q := activeQuote(1, "john", "jefferey", "bread", 2, 4)
	q.ExpiresAt = now.Add(-time.Second) // lapsed; the sweep hasn't run yet
	snap := buyerQuoteSnap(map[sim.QuoteID]*sim.SceneQuote{1: q})
	snap.PublishedAt = now
	if got := buildStandingQuotesToMe(snap, "jefferey", snap.Actors["jefferey"], nil, nil); len(got) != 0 {
		t.Errorf("views = %d, want 0 — an expired quote is not takeable", len(got))
	}
	q.ExpiresAt = now.Add(time.Minute) // still live
	if got := buildStandingQuotesToMe(snap, "jefferey", snap.Actors["jefferey"], nil, nil); len(got) != 1 {
		t.Errorf("views = %d, want 1 for an unexpired quote", len(got))
	}
}

// A quote whose seller is not in the buyer's conversation is not takeable:
// pay_with_item resolves the seller among huddle peers and rejects otherwise.
func TestBuildStandingQuotesToMe_RequiresSellerCoPresence(t *testing.T) {
	snap := buyerQuoteSnap(map[sim.QuoteID]*sim.SceneQuote{
		1: activeQuote(1, "john", "jefferey", "bread", 2, 4),
	})
	if got := buildStandingQuotesToMe(snap, "jefferey", snap.Actors["jefferey"], nil, nil); len(got) != 1 {
		t.Fatalf("co-present seller views = %d, want 1", len(got))
	}
	// John wanders off to another conversation — the offer may still be on his
	// books, but she cannot pay him from here.
	snap.Actors["john"].CurrentHuddleID = "h2"
	if got := buildStandingQuotesToMe(snap, "jefferey", snap.Actors["jefferey"], nil, nil); len(got) != 0 {
		t.Errorf("views = %d, want 0 — the seller is in another conversation", len(got))
	}
	// And a buyer in no conversation at all can take nothing.
	snap.Actors["john"].CurrentHuddleID = "h1"
	alone := *snap.Actors["jefferey"]
	alone.CurrentHuddleID = ""
	if got := buildStandingQuotesToMe(snap, "jefferey", &alone, nil, nil); len(got) != 0 {
		t.Errorf("views = %d, want 0 — the buyer is in no conversation", len(got))
	}
}

// A self-targeted quote cannot arise from scene_quote, which rejects it; the
// builder excludes it anyway rather than resting on another component's checks.
func TestBuildStandingQuotesToMe_SelfTargetedExcluded(t *testing.T) {
	snap := buyerQuoteSnap(map[sim.QuoteID]*sim.SceneQuote{
		1: activeQuote(1, "jefferey", "jefferey", "bread", 2, 4),
	})
	if got := buildStandingQuotesToMe(snap, "jefferey", snap.Actors["jefferey"], nil, nil); len(got) != 0 {
		t.Errorf("views = %d, want 0 — nobody is offered his own wares", len(got))
	}
}

// A homed subject is spared a lodging quote she structurally can't take
// (LLM-182/208); a homeless one still gets it.
func TestBuildStandingQuotesToMe_HomedLodgingSuppressed(t *testing.T) {
	snap := buyerQuoteSnap(map[sim.QuoteID]*sim.SceneQuote{
		1: activeQuote(1, "john", "jefferey", "nights_stay", 1, 4),
	})
	snap.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
		"nights_stay": {Name: "nights_stay", Capabilities: []string{"lodging"}},
	}
	if got := buildStandingQuotesToMe(snap, "jefferey", snap.Actors["jefferey"], nil, nil); len(got) != 1 {
		t.Fatalf("homeless seeker views = %d, want 1", len(got))
	}
	homed := *snap.Actors["jefferey"]
	homed.HomeStructureID = "ward_residence"
	if got := buildStandingQuotesToMe(snap, "jefferey", &homed, nil, nil); len(got) != 0 {
		t.Errorf("views = %d, want 0 — a homed subject can't take a room", len(got))
	}
}

// End-to-end: the buyer's full prompt carries the section and the take token.
func TestRender_BuyerStandingQuoteSection(t *testing.T) {
	snap := buyerQuoteSnap(map[sim.QuoteID]*sim.SceneQuote{
		7: activeQuote(7, "john", "jefferey", "bread", 2, 4),
	})
	out := combinedPrompt(Render(Build(snap, "jefferey", nil), DefaultRenderConfig()))
	if !strings.Contains(out, "## Offers made to you") {
		t.Errorf("buyer standing-quote section missing from full prompt\n%s", out)
	}
	if !strings.Contains(out, "quote_id 7") {
		t.Errorf("take-instruction lacks the quote_id — the buyer can only cross the quote without it\n%s", out)
	}
}
