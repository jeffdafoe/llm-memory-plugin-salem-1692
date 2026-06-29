package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_offer_pending_test.go — ZBBS-HOME-413 + ZBBS-HOME-453. Three surfaces:
//   - the buyer-side "## Offers you have standing" cue (buildPendingOffersFromMe +
//     renderPendingOffersFromMe), the cross-tick repeat-offer-storm fix;
//   - filterStalePayOfferWarrants, which drops a seller's pay-offer warrant
//     once its ledger entry has resolved (so a dead offer doesn't render a
//     stale "what just happened" line); and
//   - the seller-side standing offer view (buildPayOffersForMe), the
//     cross-tick "## Offers awaiting your decision" + response-tool source —
//     the seller deadlock fix.
//
// The fixture mirrors the live incident: Prudence Ward (buyer) staking
// pay-with-item offers against Elizabeth Ellis (seller).

// offerEntry builds a PayLedgerEntry in the given state for tests.
func offerEntry(id sim.LedgerID, buyer, seller sim.ActorID, item sim.ItemKind, qty, amount int, state sim.PayLedgerState) *sim.PayLedgerEntry {
	return &sim.PayLedgerEntry{
		ID:       id,
		BuyerID:  buyer,
		SellerID: seller,
		ItemKind: item,
		Qty:      qty,
		Amount:   amount,
		State:    state,
	}
}

// offerSnap builds a minimal *sim.Snapshot with the buyer/seller/bystander
// actors and the supplied PayLedger. prudence (buyer) knows nobody by default,
// so a seller name resolves to the role descriptor until Acquaintances is set.
func offerSnap(ledger map[sim.LedgerID]*sim.PayLedgerEntry) *sim.Snapshot {
	return &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"prudence":  {DisplayName: "Prudence Ward", Role: "apothecary", Kind: sim.KindNPCStateful, Needs: map[sim.NeedKey]int{}},
			"elizabeth": {DisplayName: "Elizabeth Ellis", Role: "dairykeeper", Kind: sim.KindNPCShared, Needs: map[sim.NeedKey]int{}},
			"mary":      {DisplayName: "Mary", Role: "weaver", Kind: sim.KindNPCStateful, Needs: map[sim.NeedKey]int{}},
		},
		PayLedger:  ledger,
		Scenes:     map[sim.SceneID]*sim.Scene{},
		Huddles:    map[sim.HuddleID]*sim.Huddle{},
		Structures: map[sim.StructureID]*sim.Structure{},
	}
}

// --- Part 1: buyer-side pending-offer build -------------------------------

func TestBuildPendingOffersFromMe_NilAndEmpty(t *testing.T) {
	if got := buildPendingOffersFromMe(nil, "prudence", nil); got != nil {
		t.Errorf("nil snap: got %v, want nil", got)
	}
	snap := offerSnap(nil)
	if got := buildPendingOffersFromMe(snap, "prudence", snap.Actors["prudence"]); got != nil {
		t.Errorf("empty ledger: got %v, want nil", got)
	}
}

// The buyer sees an offer they staked, with the load-bearing ledger id, the
// goods, the payment, and a role-gated seller name (Prudence doesn't yet know
// Elizabeth → "the dairykeeper").
func TestBuildPendingOffersFromMe_BuyerSeesOwnPendingOffer(t *testing.T) {
	snap := offerSnap(map[sim.LedgerID]*sim.PayLedgerEntry{
		236: offerEntry(236, "prudence", "elizabeth", "meat", 10, 48, sim.PayLedgerStatePending),
	})
	views := buildPendingOffersFromMe(snap, "prudence", snap.Actors["prudence"])
	if len(views) != 1 {
		t.Fatalf("views = %v, want one pending offer", views)
	}
	v := views[0]
	if v.LedgerID != 236 || v.Item != "meat" || v.Qty != 10 || v.Amount != 48 {
		t.Errorf("view fields = %+v, want ledger 236 / meat / qty 10 / amount 48", v)
	}
	if v.SellerName != "the dairykeeper" {
		t.Errorf("SellerName = %q, want role-gated %q (Prudence doesn't know Elizabeth)", v.SellerName, "the dairykeeper")
	}
}

// Once the buyer is acquainted with the seller, the display name is used.
func TestBuildPendingOffersFromMe_AcquaintedUsesDisplayName(t *testing.T) {
	snap := offerSnap(map[sim.LedgerID]*sim.PayLedgerEntry{
		236: offerEntry(236, "prudence", "elizabeth", "meat", 10, 48, sim.PayLedgerStatePending),
	})
	snap.Actors["prudence"].Acquaintances = map[string]sim.Acquaintance{"Elizabeth Ellis": {}}
	views := buildPendingOffersFromMe(snap, "prudence", snap.Actors["prudence"])
	if len(views) != 1 || views[0].SellerName != "Elizabeth Ellis" {
		t.Errorf("SellerName = %q, want %q once acquainted", views[0].SellerName, "Elizabeth Ellis")
	}
}

// The SELLER an offer is staked against does NOT get a "your pending offers"
// view — those are offers staked by the buyer. The seller learns of them via
// the standing seller-side view (buildPayOffersForMe → renderPayOffers), not
// this one.
func TestBuildPendingOffersFromMe_SellerSubjectGetsNone(t *testing.T) {
	snap := offerSnap(map[sim.LedgerID]*sim.PayLedgerEntry{
		236: offerEntry(236, "prudence", "elizabeth", "meat", 10, 48, sim.PayLedgerStatePending),
	})
	if got := buildPendingOffersFromMe(snap, "elizabeth", snap.Actors["elizabeth"]); got != nil {
		t.Errorf("seller subject got %v, want nil (offer is staked against them, not by them)", got)
	}
}

// Only Pending entries surface — every terminal state is filtered out.
func TestBuildPendingOffersFromMe_TerminalFiltered(t *testing.T) {
	terminals := []sim.PayLedgerState{
		sim.PayLedgerStateAccepted,
		sim.PayLedgerStateDeclined,
		sim.PayLedgerStateExpired,
		sim.PayLedgerStateFailedInsufficientFunds,
		sim.PayLedgerStateWithdrawnByBuyer,
	}
	for _, st := range terminals {
		snap := offerSnap(map[sim.LedgerID]*sim.PayLedgerEntry{
			236: offerEntry(236, "prudence", "elizabeth", "meat", 10, 48, st),
		})
		if got := buildPendingOffersFromMe(snap, "prudence", snap.Actors["prudence"]); got != nil {
			t.Errorf("state %q: got %v, want nil (terminal filtered)", st, got)
		}
	}
}

// Multiple pending offers sort by LedgerID ascending (deterministic).
func TestBuildPendingOffersFromMe_DeterministicOrder(t *testing.T) {
	snap := offerSnap(map[sim.LedgerID]*sim.PayLedgerEntry{
		237: offerEntry(237, "prudence", "elizabeth", "meat", 10, 48, sim.PayLedgerStatePending),
		235: offerEntry(235, "prudence", "elizabeth", "meat", 10, 48, sim.PayLedgerStatePending),
		236: offerEntry(236, "prudence", "elizabeth", "meat", 10, 48, sim.PayLedgerStatePending),
	})
	views := buildPendingOffersFromMe(snap, "prudence", snap.Actors["prudence"])
	if len(views) != 3 {
		t.Fatalf("views count = %d, want 3", len(views))
	}
	for i, want := range []sim.LedgerID{235, 236, 237} {
		if views[i].LedgerID != want {
			t.Errorf("views[%d].LedgerID = %d, want %d", i, views[i].LedgerID, want)
		}
	}
}

// --- Part 1: buyer-side pending-offer render ------------------------------

func TestRenderPendingOffersFromMe_HappyPath(t *testing.T) {
	var b strings.Builder
	renderPendingOffersFromMe(&b, []PendingOfferView{
		{LedgerID: 236, SellerName: "Elizabeth Ellis", Item: "meat", Qty: 10, Amount: 48},
	})
	out := b.String()
	for _, must := range []string{
		"## Offers you have standing",
		"offer id 236",
		"48 coins",
		"10 meat",
		"Elizabeth Ellis",
		"make no second offer",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("missing %q\n--- output ---\n%s", must, out)
		}
	}
}

// A barter offer (goods, or goods + coins) renders the goods being offered.
func TestRenderPendingOffersFromMe_Barter(t *testing.T) {
	var b strings.Builder
	renderPendingOffersFromMe(&b, []PendingOfferView{
		{LedgerID: 9, SellerName: "the dairykeeper", Item: "stew", Qty: 1, Amount: 0,
			PayItems: []sim.ItemKindQty{{Kind: "nail", Qty: 5}}},
	})
	out := b.String()
	if !strings.Contains(out, "for 1 stew, 5 nail offered") {
		t.Errorf("barter offer line missing the goods offered\n%s", out)
	}
	if !strings.Contains(out, "offer id 9") {
		t.Errorf("ledger id missing on barter offer\n%s", out)
	}
}

// Content-gated: an empty list produces no section at all.
func TestRenderPendingOffersFromMe_EmptyGated(t *testing.T) {
	var b strings.Builder
	renderPendingOffersFromMe(&b, nil)
	if b.Len() != 0 {
		t.Errorf("empty list produced output: %q", b.String())
	}
}

// The cue appears end-to-end through Build+Render for a buyer with a pending
// offer.
func TestRender_BuyerPendingOfferSection(t *testing.T) {
	snap := offerSnap(map[sim.LedgerID]*sim.PayLedgerEntry{
		236: offerEntry(236, "prudence", "elizabeth", "meat", 10, 48, sim.PayLedgerStatePending),
	})
	p := Build(snap, "prudence", nil)
	out := combinedPrompt(Render(p, DefaultRenderConfig()))
	if !strings.Contains(out, "## Offers you have standing") || !strings.Contains(out, "offer id 236") {
		t.Errorf("buyer pending-offer cue missing from full prompt\n%s", out)
	}
}

// --- Part 3: stale seller-warrant filter ----------------------------------

// A pay-offer warrant whose ledger entry has resolved (terminal) or vanished is
// dropped; a still-pending one and any non-pay warrant pass through.
func TestFilterStalePayOfferWarrants_DropsResolvedAndMissing(t *testing.T) {
	snap := offerSnap(map[sim.LedgerID]*sim.PayLedgerEntry{
		235: offerEntry(235, "prudence", "elizabeth", "meat", 10, 48, sim.PayLedgerStateExpired),
		236: offerEntry(236, "prudence", "elizabeth", "meat", 10, 48, sim.PayLedgerStatePending),
		// 237 intentionally absent from the ledger (reaped) → its warrant is stale.
	})
	warrants := []sim.WarrantMeta{
		payOfferWarrant(235, "prudence", "meat", 10, 48, false), // terminal → drop
		payOfferWarrant(236, "prudence", "meat", 10, 48, false), // pending → keep
		payOfferWarrant(237, "prudence", "meat", 10, 48, false), // missing → drop
		speechWarrant(40, "s1", "mary", "good evening"),         // non-pay → keep
	}
	got := filterStalePayOfferWarrants(warrants, snap)
	if len(got) != 2 {
		t.Fatalf("filtered len = %d, want 2 (pending offer + speech)\n%+v", len(got), got)
	}
	offers := 0
	for _, w := range got {
		if r, ok := w.Reason.(sim.PayOfferWarrantReason); ok {
			offers++
			if r.LedgerID != 236 {
				t.Errorf("surviving pay-offer warrant ledger = %d, want only 236", r.LedgerID)
			}
		}
	}
	if offers != 1 {
		t.Errorf("surviving pay-offer warrants = %d, want 1", offers)
	}
}

// When nothing is stale, the original slice is returned unchanged (no alloc).
func TestFilterStalePayOfferWarrants_NoStaleReturnsInput(t *testing.T) {
	snap := offerSnap(map[sim.LedgerID]*sim.PayLedgerEntry{
		236: offerEntry(236, "prudence", "elizabeth", "meat", 10, 48, sim.PayLedgerStatePending),
	})
	warrants := []sim.WarrantMeta{
		payOfferWarrant(236, "prudence", "meat", 10, 48, false),
		speechWarrant(40, "s1", "mary", "good evening"),
	}
	got := filterStalePayOfferWarrants(warrants, snap)
	if len(got) != len(warrants) {
		t.Fatalf("filtered len = %d, want %d (nothing stale)", len(got), len(warrants))
	}
}

// Wired into Build: a seller carrying a standing pay-offer warrant whose entry
// has resolved no longer sees it via PendingPayOffers (so renderPayOffers and
// the tool gate both stop treating the dead offer as actionable).
func TestBuild_DropsResolvedSellerOfferWarrant(t *testing.T) {
	snap := offerSnap(map[sim.LedgerID]*sim.PayLedgerEntry{
		236: offerEntry(236, "prudence", "elizabeth", "meat", 10, 48, sim.PayLedgerStateAccepted),
	})
	warrants := []sim.WarrantMeta{payOfferWarrant(236, "prudence", "meat", 10, 48, false)}
	p := Build(snap, "elizabeth", warrants)
	if got := PendingPayOffers(p); len(got) != 0 {
		t.Errorf("PendingPayOffers = %d, want 0 (resolved offer absent from the ledger scan)", len(got))
	}
	out := combinedPrompt(Render(p, DefaultRenderConfig()))
	if strings.Contains(out, "## Offers awaiting your decision") {
		t.Errorf("resolved offer should not render as awaiting decision\n%s", out)
	}
}

// LLM-173: an offer still PENDING in the snapshot but already answered THIS tick
// is withheld from the seller cue when Build is given WithResolvedPayOffers. The
// within-tick self-state refresh (LLM-88) re-perceives from the turn-start
// snapshot, which still shows the just-accepted offer as pending — so without
// this the cue re-invites a settlement that already happened and the weak model
// burns its remaining rounds re-accepting. The turn-start Build (no option)
// renders the same pending offer normally.
func TestBuild_WithholdsResolvedThisTickPayOffer(t *testing.T) {
	snap := offerSnap(map[sim.LedgerID]*sim.PayLedgerEntry{
		236: offerEntry(236, "prudence", "elizabeth", "meat", 10, 48, sim.PayLedgerStatePending),
	})
	warrants := []sim.WarrantMeta{payOfferWarrant(236, "prudence", "meat", 10, 48, false)}

	// Turn-start Build: the pending offer drives the cue (the option is absent).
	full := Build(snap, "elizabeth", warrants)
	if got := PendingPayOffers(full); len(got) != 1 {
		t.Fatalf("turn-start PendingPayOffers = %d, want 1 (pending offer present)", len(got))
	}
	if out := combinedPrompt(Render(full, DefaultRenderConfig())); !strings.Contains(out, "## Offers awaiting your decision") {
		t.Fatalf("turn-start render must show the cue, got:\n%s", out)
	}

	// Mid-tick re-render: ledger 236 was answered this tick → withheld from both
	// the offer list and the rendered cue, though the snapshot still shows it pending.
	narrowed := Build(snap, "elizabeth", warrants, WithResolvedPayOffers(map[sim.LedgerID]struct{}{236: {}}))
	if got := PendingPayOffers(narrowed); len(got) != 0 {
		t.Errorf("PendingPayOffers = %d, want 0 (resolved-this-tick offer withheld)", len(got))
	}
	if out := combinedPrompt(Render(narrowed, DefaultRenderConfig())); strings.Contains(out, "## Offers awaiting your decision") {
		t.Errorf("re-render must withhold the resolved-this-tick offer's cue\n%s", out)
	}
}

// The pending counterpart survives Build and still drives the seller section.
func TestBuild_KeepsPendingSellerOfferWarrant(t *testing.T) {
	snap := offerSnap(map[sim.LedgerID]*sim.PayLedgerEntry{
		236: offerEntry(236, "prudence", "elizabeth", "meat", 10, 48, sim.PayLedgerStatePending),
	})
	warrants := []sim.WarrantMeta{payOfferWarrant(236, "prudence", "meat", 10, 48, false)}
	p := Build(snap, "elizabeth", warrants)
	if got := PendingPayOffers(p); len(got) != 1 {
		t.Fatalf("PendingPayOffers = %d, want 1 (pending offer retained)", len(got))
	}
}

// --- Part 4: standing seller-side offer view (ZBBS-HOME-453) ---------------

// buildPayOffersForMe scans the ledger for offers staked AGAINST the subject:
// pending only, seller-match only, LedgerID-ascending, terms + Depth projected.
func TestBuildPayOffersForMe_ScanShape(t *testing.T) {
	ledger := map[sim.LedgerID]*sim.PayLedgerEntry{
		237: offerEntry(237, "prudence", "elizabeth", "meat", 10, 48, sim.PayLedgerStatePending),
		235: offerEntry(235, "mary", "elizabeth", "cheese", 2, 9, sim.PayLedgerStatePending),
		236: offerEntry(236, "prudence", "elizabeth", "meat", 5, 20, sim.PayLedgerStateDeclined), // terminal → out
		238: offerEntry(238, "elizabeth", "prudence", "herbs", 1, 3, sim.PayLedgerStatePending),  // elizabeth is BUYER → out
	}
	ledger[237].Depth = 1
	snap := offerSnap(ledger)

	offers := buildPayOffersForMe(snap, "elizabeth", nil)
	if len(offers) != 2 {
		t.Fatalf("offers = %+v, want the two pending entries staked against elizabeth", offers)
	}
	if offers[0].LedgerID != 235 || offers[1].LedgerID != 237 {
		t.Errorf("ledger ids = %d, %d; want 235, 237 (ascending)", offers[0].LedgerID, offers[1].LedgerID)
	}
	if offers[0].Buyer != "mary" || offers[0].Item != "cheese" || offers[0].Qty != 2 || offers[0].Amount != 9 {
		t.Errorf("offer terms not projected: %+v", offers[0])
	}
	if offers[1].Depth != 1 {
		t.Errorf("Depth = %d, want 1 (the counter-cap tool gate reads it)", offers[1].Depth)
	}

	if got := buildPayOffersForMe(nil, "elizabeth", nil); got != nil {
		t.Errorf("nil snap: got %v, want nil", got)
	}
	if got := buildPayOffersForMe(snap, "mary", nil); got != nil {
		t.Errorf("non-seller subject: got %v, want nil", got)
	}

	// LLM-173: an offer already answered this tick is withheld so the within-tick
	// re-render stops re-inviting a settlement that already happened. 235 is
	// resolved, 237 still stands.
	resolved := map[sim.LedgerID]struct{}{235: {}}
	narrowed := buildPayOffersForMe(snap, "elizabeth", resolved)
	if len(narrowed) != 1 || narrowed[0].LedgerID != 237 {
		t.Errorf("resolved-this-tick filter: got %+v, want only ledger 237", narrowed)
	}
}

// The ZBBS-HOME-453 regression, end-to-end through Build+Render: a seller
// tick with NO pay-offer warrant (it was consumed by an earlier tick the
// model spent speaking — the 06-12 Ellis meat deadlock) still gets the full
// decision section, with the buyer's label resolved off the standing view
// rather than the warrant batch.
func TestBuild_StandingOfferSurvivesConsumedWarrant(t *testing.T) {
	snap := offerSnap(map[sim.LedgerID]*sim.PayLedgerEntry{
		259: offerEntry(259, "prudence", "elizabeth", "meat", 25, 248, sim.PayLedgerStatePending),
	})
	p := Build(snap, "elizabeth", nil) // empty consumed batch — the warrant is gone
	if got := PendingPayOffers(p); len(got) != 1 || got[0].LedgerID != 259 {
		t.Fatalf("PendingPayOffers = %+v, want the standing ledger-259 offer", got)
	}
	out := combinedPrompt(Render(p, DefaultRenderConfig()))
	for _, must := range []string{
		"## Offers awaiting your decision",
		"offer id 259",
		"248 coins",
		"25 meat",
		"the apothecary", // buyer label resolved without a warrant (elizabeth doesn't know prudence)
		"Respond first with accept_pay",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("warrant-less seller tick missing %q\n--- output ---\n%s", must, out)
		}
	}
}
