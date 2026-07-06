package perception

import (
	"fmt"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// stall_repair.go — LLM-118 owner audience, generalized to all businesses in
// LLM-247, and to hired workers in LLM-271. The repair section: a standing
// reminder, shown whenever the actor stands at a worn business they are
// responsible for — its owner at their own premises ("## Your business"), or a
// worker Working a hired job there ("## The business you're working at") — that it
// needs mending and how. A nil view renders nothing (the common case — not at such
// a business, or it isn't worn yet). The SAME non-nil view gates the `repair`
// tool's advertisement (handlers/tool_gating.go), so the cue and the tool appear
// together. This is the standing-fact surface that keeps reminding after the
// one-shot repair warrant (renderWarrantLine) is consumed.

// StallRepairView is the at-the-business repair cue. Non-nil only when the actor
// is responsible for a wearable business they are standing at AND it has worn to
// the repair threshold — either as its owner, or (LLM-271) as a worker actively
// Working a hired job there. Hired flips the render from "your business" to "the
// business you're working at" so the cue states the true relationship.
type StallRepairView struct {
	Hired          bool            // resolved through a hire (Working for the owner), not ownership (LLM-271)
	Degraded       bool            // worn past the degrade threshold: closed for trade until mended
	NailsNeeded    int             // nails one repair consumes
	NailsHeld      int             // nails the actor currently carries
	HasEnoughNails bool            // NailsHeld >= NailsNeeded
	Name           string          // the business's display name (structure/object); "" → generic noun
	NailVendors    []RestockVendor // owner's buy-nails destinations (LLM-274); populated only when short of nails and NOT hired

	// Conserve (LLM-301): set only when NailVendors came back EMPTY — every supplier
	// dropped by the LLM-216 filters (unaffordable / remembered shut / none exist) —
	// and the working-capital gate says this keeper should hold off buying. Selects
	// the sell-first soften over the plain shortfall statement in the vendor-less
	// fallback. Rides merchantConserve, the same signal "## Restocking" uses, so the
	// two cues can never contradict.
	Conserve bool
}

// buildStallRepair returns the at-the-business repair cue, or nil. Pure over the
// snapshot. Gated on: the actor is responsible for a wearable business (owns it,
// or is Working a hired job there — LLM-271), is standing at/inside it, and it has
// worn to the repair threshold.
func buildStallRepair(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *StallRepairView {
	if snap == nil || actorSnap == nil {
		return nil
	}
	stall, hired := sim.WearableStallToMend(snap.VillageObjects, snap.LaborLedger, actorID)
	if stall == nil {
		return nil
	}
	if !sim.AtBusiness(actorSnap.Pos, actorSnap.InsideStructureID, stall.ID, objectLoiterPin(stall), true) {
		return nil // not standing at or inside the business
	}
	if !sim.StallRepairable(stall, snap.StallWearRepairThreshold, snap.StallWearDegradeThreshold) {
		return nil // not worn enough to bother (degraded counts — a bad threshold config can't hide the cue)
	}
	needed := snap.StallNailsPerRepair
	held := actorSnap.Inventory[sim.NailItemKind]
	view := &StallRepairView{
		Hired:          hired,
		Degraded:       sim.StallDegraded(stall, snap.StallWearDegradeThreshold),
		NailsNeeded:    needed,
		NailsHeld:      held,
		HasEnoughNails: held >= needed,
		Name:           resolveDwellPinLabel(snap, stall.ID),
	}
	// LLM-274: when the owner is short of nails, resolve the nail supplier(s) so the
	// cue can name a concrete move_to destination instead of the dead-end "the smith".
	// findItemVendors inherits the restock directory's filtering (supplier-of-record,
	// remembered-shut drop, affordability drop, workplace dedupe). Skipped for a hired
	// worker — they can't leave the job to shop, so their cue only names the shortfall
	// (renderHiredStallRepair).
	if !view.HasEnoughNails && !hired {
		view.NailVendors = findItemVendors(snap, actorID, actorSnap, sim.NailItemKind)
		// LLM-301: every supplier dropped — the render falls back to a destination-less
		// sentence, so decide its tone here. Computed only on the vendor-less path, so
		// the common healthy case pays nothing for the conserve scan.
		if len(view.NailVendors) == 0 {
			view.Conserve = merchantConserve(snap, actorID, actorSnap).Active
		}
	}
	return view
}

// renderStallRepair writes the "## Your business" section. Content-gated: a nil view
// writes nothing. Symmetrical awareness — it states the problem AND the way out
// (the smith sells the nails) in one place, and tells the owner whether they can
// mend now or must buy nails first (the two-step buy->repair, mirroring
// gather->consume).
func renderStallRepair(b *strings.Builder, v *StallRepairView) {
	if v == nil {
		return
	}
	name := v.Name
	if name == "" {
		name = "place of business"
	}
	if v.Hired {
		renderHiredStallRepair(b, v, name)
		return
	}
	b.WriteString("## Your business\n")
	if v.Degraded {
		fmt.Fprintf(b, "Your %s is too worn to trade — it stays shut until you mend it. ", name)
	} else {
		fmt.Fprintf(b, "Your %s is showing hard use and needs mending. ", name)
	}
	if v.HasEnoughNails {
		fmt.Fprintf(b, "You carry enough nails (%d) to mend it — use the repair tool now to fix it, hammer in hand, on site (it takes a short while).\n", v.NailsHeld)
	} else if len(v.NailVendors) > 0 {
		// LLM-274: name the actual nail supplier(s) — workplace + structure_id, resolved
		// via findItemVendors — in the model-proven Restocking format, plus the repair's
		// second hop (come back and mend). The old destination-less "buy from the smith"
		// left llama-3.3-70b narrating the errand ("I must visit the smith") and never
		// issuing move_to.
		fmt.Fprintf(b, "Mending takes %d nails and you have %d — buy them, then come back here and repair. Use move_to to reach a supplier, then pay_with_item once you arrive:\n", v.NailsNeeded, v.NailsHeld)
		renderWalkToVendors(b, v.NailVendors)
	} else if v.Conserve {
		// LLM-301: no reachable supplier AND the working-capital gate says hold off —
		// state the way out (sell, recover, then buy and mend) instead of goading a
		// buy the purse can't close, harmonizing with the "## Restocking" hold-off.
		fmt.Fprintf(b, "Mending takes %d nails and you have %d — but your purse can't take on nails just now. Sell what you can and let your coins recover, then buy nails and come back to mend it.\n", v.NailsNeeded, v.NailsHeld)
	} else {
		// No reachable, open, affordable nail supplier on record — state the shortfall
		// without a target rather than a dead-end errand (mirrors the Restocking
		// actionability posture, LLM-216); the cue self-heals when a supplier opens or
		// the purse covers one. "From the smith" is deliberately NOT said: a person-
		// shaped target with no move_to destination reads as an errand, and the weak
		// model invents one — the live Josiah case hallucinated "the Smithy" and burned
		// his whole turn retrying it (LLM-301).
		fmt.Fprintf(b, "Mending takes %d nails and you have %d — you'll need to buy more before you can mend it.\n", v.NailsNeeded, v.NailsHeld)
	}
}

// renderHiredStallRepair writes the repair cue for a worker hired to labor at the
// business (LLM-271): the same actionable mend, framed as the premises they were
// taken on to help with rather than their own ("## The business you're working
// at"), so the engine states the situation truthfully instead of calling a hired
// hand's workplace "your" shop. It still tells them whether they can mend now or
// are short of nails — the buy-then-mend steer is softened (they can't leave a job
// to shop), so it just names the shortfall.
func renderHiredStallRepair(b *strings.Builder, v *StallRepairView, name string) {
	b.WriteString("## The business you're working at\n")
	if v.Degraded {
		fmt.Fprintf(b, "The %s you're working at is too worn to trade — it stays shut until it's mended. ", name)
	} else {
		fmt.Fprintf(b, "The %s you're working at is showing hard use and needs mending. ", name)
	}
	if v.HasEnoughNails {
		fmt.Fprintf(b, "You carry enough nails (%d) to mend it — use the repair tool now to fix it, hammer in hand, on site (it takes a short while).\n", v.NailsHeld)
	} else {
		fmt.Fprintf(b, "Mending takes %d nails and you have %d — you'd need more from the smith first.\n", v.NailsNeeded, v.NailsHeld)
	}
}

// StallConditionView is the co-present world-fact line for an actor — NOT the
// owner — standing at a worn business. Social texture a passerby can remark
// on (and the perception twin of the worn sprite, which the art pass defers).
// nil when the actor isn't at a worn business, or when they ARE its owner (they
// get the richer "## Your business" cue instead).
type StallConditionView struct {
	Degraded bool
	Name     string // the business's display name (structure/object); "" → generic noun
}

// buildStallCondition returns the co-present worn-business line for a non-owner,
// or nil. Pure over the snapshot. Surfaces the FIRST worn business the actor
// stands at that isn't their own.
func buildStallCondition(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *StallConditionView {
	if snap == nil || actorSnap == nil {
		return nil
	}
	// The business the actor is responsible for mending — their own, or (LLM-271)
	// the one they are Working a hired job at — gets the richer repair cue instead,
	// so skip it here: a mender shouldn't ALSO be handed the bystander "looks worn"
	// texture line for the very stall they've been cued to fix.
	responsible, _ := sim.WearableStallToMend(snap.VillageObjects, snap.LaborLedger, actorID)
	for _, obj := range snap.VillageObjects {
		if !sim.IsWearableStall(obj) || obj.OwnerActorID == actorID {
			continue // non-business/unowned, or my own (## Your business covers me)
		}
		if responsible != nil && obj.ID == responsible.ID {
			continue // I'm hired to mend this one — "## The business you're working at" covers me
		}
		if !sim.AtBusiness(actorSnap.Pos, actorSnap.InsideStructureID, obj.ID, objectLoiterPin(obj), true) {
			continue
		}
		if !sim.StallNeedsRepair(obj, snap.StallWearRepairThreshold) {
			continue
		}
		return &StallConditionView{
			Degraded: sim.StallDegraded(obj, snap.StallWearDegradeThreshold),
			Name:     resolveDwellPinLabel(snap, obj.ID),
		}
	}
	return nil
}

// renderStallCondition writes the co-present worn-business atmosphere line. A nil
// view writes nothing. A bare standing fact (no header) — it's environmental
// texture, not an actionable cue for this actor.
func renderStallCondition(b *strings.Builder, v *StallConditionView) {
	if v == nil {
		return
	}
	name := v.Name
	if name == "" {
		name = "business"
	}
	if v.Degraded {
		fmt.Fprintf(b, "The %s here is battered and clearly unfit for trade.\n", name)
	} else {
		fmt.Fprintf(b, "The %s here looks worn and run-down from hard use.\n", name)
	}
}

// StallRepairBuyView is the OFF-POST half of the repair errand (LLM-277): the
// standing "go buy nails to mend your worn business" cue an owner carries once she
// steps AWAY from that business, still short of the nails a repair takes. The
// "## Your business" cue (buildStallRepair) covers the buy while she stands AT the
// business; this covers it everywhere else, so the errand persists across the walk
// to the smith instead of vanishing the moment she leaves the farm (the LLM-274
// half named the destination but the cue disappeared off-post). It names concrete
// move_to destinations while away and flips to a co-present pay_with_item imperative
// once she shares the smith's huddle — the same walk-to → buy-here progression the
// "## Restocking" reseller cue uses. It does NOT gate the `repair` tool: mending
// happens only on site, so buildStallRepair remains the sole gate for that tool.
type StallRepairBuyView struct {
	Name            string          // the worn business's display name (structure/object); "" → generic noun
	NailsNeeded     int             // nails one repair consumes
	NailsHeld       int             // nails the actor currently carries
	NailsShort      int             // NailsNeeded - NailsHeld (> 0 whenever the cue shows)
	Vendors         []RestockVendor // where to buy nails while away — the move_to destination(s)
	CoPresentSeller string          // a nail seller sharing the actor's huddle right now; "" when none
	PendingOffer    bool            // a still-pending nail offer already stands with CoPresentSeller (bide, don't re-offer)

	// LLM-297: co-present-buy situation awareness. SellerStock is the nails the
	// CoPresentSeller holds right now (0 when none present); Block selects whether
	// renderStallRepairBuy issues the "Buy it now" imperative or softens to a
	// walk-away. Both are set only when a seller is co-present and no offer is
	// already standing (the PendingOffer bide steer wins first).
	SellerStock int
	Block       stallBuyBlock
}

// stallBuyBlock decides how renderStallRepairBuy treats a co-present nail seller
// (LLM-297). The zero value goads the buy; the blocked variants replace the
// imperative with a walk-away so the cue stops pushing the model to re-offer into
// an empty forge, an unaffordable / already-declined negotiation, or against the
// working-capital gate's own hold-off advice.
type stallBuyBlock int

const (
	stallBuyOK             stallBuyBlock = iota // seller has stock and a deal is plausible — goad the buy
	stallBuyBlockedNoStock                      // seller holds nothing right now (defensive — coPresentSellerForItem's qty>0 gate normally prevents it)
	stallBuyBlockedCoin                         // conserve mode or an empty purse — coins must recover before this buy can close
	stallBuyBlockedTerms                        // repeated declines / unfilled offers this huddle — the deal isn't meeting; try later
)

// buildStallRepairBuy returns the off-post nail-buy errand cue, or nil. Pure over the
// snapshot. Non-nil only when the actor OWNS a repairable worn business (not a hired
// hand — a hire can't leave the job to shop, the same carve-out buildStallRepair
// makes for its NailVendors), is NOT standing at that business (buildStallRepair owns
// the at-business buy), is short of the nails a repair needs, and has at least one
// actionable buy path — a co-present nail seller or a surviving walk-to supplier (the
// LLM-216 dead-end drop, mirroring buildRestocking so a broke or supplier-less owner
// isn't handed an unactionable errand it would tour).
func buildStallRepairBuy(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *StallRepairBuyView {
	if snap == nil || actorSnap == nil {
		return nil
	}
	stall, hired := sim.WearableStallToMend(snap.VillageObjects, snap.LaborLedger, actorID)
	if stall == nil || hired {
		return nil // not responsible, or a hire who can't leave the job to shop
	}
	if sim.AtBusiness(actorSnap.Pos, actorSnap.InsideStructureID, stall.ID, objectLoiterPin(stall), true) {
		return nil // at the business — "## Your business" (buildStallRepair) owns the buy here
	}
	if !sim.StallRepairable(stall, snap.StallWearRepairThreshold, snap.StallWearDegradeThreshold) {
		return nil // not worn enough to warrant mending
	}
	needed := snap.StallNailsPerRepair
	if needed <= 0 {
		return nil // a repair costs no nails (feature off / misconfig) — nothing to buy
	}
	held := actorSnap.Inventory[sim.NailItemKind]
	if held >= needed {
		return nil // already carrying enough to mend — no buy errand
	}
	coName, coID := coPresentSellerForItem(snap, actorID, actorSnap, sim.NailItemKind)
	vendors := findItemVendors(snap, actorID, actorSnap, sim.NailItemKind)
	if coName == "" && len(vendors) == 0 {
		return nil // LLM-216: no actionable buy path — don't surface a dead-end errand
	}
	view := &StallRepairBuyView{
		Name:            resolveDwellPinLabel(snap, stall.ID),
		NailsNeeded:     needed,
		NailsHeld:       held,
		NailsShort:      needed - held,
		Vendors:         vendors,
		CoPresentSeller: coName,
		PendingOffer:    coID != "" && hasPendingOfferTo(snap, actorID, coID, sim.NailItemKind),
	}
	// LLM-297: a co-present seller carries the "Buy it now" imperative — make it
	// situation-aware. Read his live nail stock to cap the suggested qty at what he can
	// actually deliver, then decide whether the buy is worth goading at all:
	//   - no stock (defensive; coPresentSellerForItem's qty>0 gate normally keeps this >=1),
	//   - the working-capital gate is telling this keeper to hold off (merchantConserve —
	//     the same signal the "## Restocking" hold-off rides, so the two cues can never
	//     contradict), which reads as a coin block,
	//   - or a recent negotiation with him has dead-ended (nailBuyStandoff → coin/terms).
	// The PendingOffer bide steer wins first, so skip the block scan under it.
	if coID != "" && !view.PendingOffer {
		if seller := snap.Actors[coID]; seller != nil {
			view.SellerStock = seller.Inventory[sim.NailItemKind]
		}
		switch {
		case view.SellerStock <= 0:
			view.Block = stallBuyBlockedNoStock
		case merchantConserve(snap, actorID, actorSnap).Active:
			view.Block = stallBuyBlockedCoin
		default:
			view.Block = nailBuyStandoff(snap, actorID, coID, actorSnap.CurrentHuddleID)
		}
	}
	return view
}

// nailStandoffDeclineThreshold is how many seller declines / unfilled-offer failures to
// the same co-present nail seller in the current huddle mark the negotiation as stuck
// (LLM-297). One decline is ordinary haggling; a second means the terms aren't going to
// meet. An engine-hard insufficient-funds rejection short-circuits to a coin block on the
// first occurrence — an empty purse won't clear by re-offering the same coins.
const nailStandoffDeclineThreshold = 2

// nailBuyStandoff classifies how the owner's recent nail offers to the co-present seller
// have dead-ended in this huddle: stallBuyBlockedCoin when the purse couldn't cover one
// (a single failed_insufficient_funds is definitive), stallBuyBlockedTerms when he has
// declined / couldn't fill at least nailStandoffDeclineThreshold offers, or stallBuyOK
// when neither. Mirrors hasPendingOfferTo's ledger walk (buyer, seller, item, huddle) but
// keys on the negative terminal states, and — like the buyer's own "## Recently settled
// offers" view (buildRecentlyResolvedOffersFromMe) — counts only offers resolved within
// recentlyResolvedOfferWindow of snap.PublishedAt, so a stale decline from earlier (terminal
// entries linger up to the 1h reap window) can't keep suppressing the buy after coins or
// stock have since recovered. An empty huddle disables the scan — co-presence with no
// shared huddle can't scope "this negotiation".
func nailBuyStandoff(snap *sim.Snapshot, buyer, seller sim.ActorID, huddle sim.HuddleID) stallBuyBlock {
	if seller == "" || huddle == "" {
		return stallBuyOK
	}
	declines := 0
	for _, e := range snap.PayLedger {
		if e == nil || e.BuyerID != buyer || e.SellerID != seller || e.ItemKind != sim.NailItemKind || e.HuddleID != huddle {
			continue
		}
		if e.ResolvedAt.IsZero() || snap.PublishedAt.Sub(e.ResolvedAt) > recentlyResolvedOfferWindow {
			continue // stale or mid-construction — count only offers the buyer still sees as recently settled
		}
		switch e.State {
		case sim.PayLedgerStateFailedInsufficientFunds:
			return stallBuyBlockedCoin
		case sim.PayLedgerStateDeclined,
			sim.PayLedgerStateFailedInsufficientStock,
			sim.PayLedgerStateFailedInsufficientGoods:
			declines++
		}
	}
	if declines >= nailStandoffDeclineThreshold {
		return stallBuyBlockedTerms
	}
	return stallBuyOK
}

// renderStallRepairBuy writes the "## Nails to mend your business" section — the
// off-post half of the repair errand (LLM-277). Content-gated: a nil view writes
// nothing. When a nail seller shares the huddle it issues the concrete buy-here
// imperative (renderCoPresentBuy); otherwise it names the walk-to destination(s)
// (renderWalkToVendors), the same progression "## Restocking" uses. When a nail
// offer already stands with the co-present seller it renders a bide steer instead,
// so the owner waits for the answer rather than re-offering and churning (the
// LLM-64 co-present-offer guard).
func renderStallRepairBuy(b *strings.Builder, v *StallRepairBuyView) {
	if v == nil {
		return
	}
	name := v.Name
	if name == "" {
		name = "place of business"
	}
	b.WriteString("## Nails to mend your business\n")
	fmt.Fprintf(b, "Your %s is worn and needs mending, but you carry only %d of the %d nails a repair takes. ", name, v.NailsHeld, v.NailsNeeded)
	if v.CoPresentSeller != "" {
		seller := sanitizeInline(v.CoPresentSeller)
		switch {
		case v.PendingOffer:
			// A nail offer already stands with the co-present seller — bide, don't
			// re-offer (the LLM-64 co-present-offer guard).
			fmt.Fprintf(b, "%s is here with you and your nail offer is still with them — wait here for their answer; do not re-offer or leave.\n", seller)
		case v.Block == stallBuyBlockedNoStock:
			// LLM-297 (defensive): a co-present seller normally holds >=1, but if his stock
			// has emptied, say so instead of goading a buy he can't fill.
			fmt.Fprintf(b, "%s is here with you but has no nails to spare just now — he's still forging them. Come back once he's made more rather than pressing him for stock he hasn't got.\n", seller)
		case v.Block == stallBuyBlockedCoin:
			// LLM-297: the purse can't take this on (conserve mode, or an offer already
			// failed for want of coin) — soften to a hold-off that harmonizes with the
			// "## Restocking" advice instead of goading a re-offer it can't afford.
			fmt.Fprintf(b, "%s is here with you, but your purse can't take on nails just now — hold off buying and let your coins recover before you come back for them.\n", seller)
		case v.Block == stallBuyBlockedTerms:
			// LLM-297: repeated offers to him have gone nowhere this huddle — the terms
			// aren't meeting, so stop pressing and come back later rather than re-offering
			// into the same no.
			fmt.Fprintf(b, "%s is here with you, but your offers for nails aren't finding a deal right now — hold off and come back later rather than pressing them into a no.\n", seller)
		case v.SellerStock > 0 && v.SellerStock < v.NailsShort:
			// LLM-297: he can't cover the whole shortfall — cap the ask at what he holds
			// and say so, so the "qty up to N" never exceeds his stock (the live case:
			// the buyer needed 5 nails, the smith held only 1).
			fmt.Fprintf(b, "%s can spare only %d just now, so buy what he has and come back for the rest once he's forged more. ", seller, v.SellerStock)
			renderCoPresentBuy(b, v.CoPresentSeller, "nails", sim.NailItemKind, v.SellerStock)
		default:
			b.WriteString("Buy the rest, then return to mend it. ")
			renderCoPresentBuy(b, v.CoPresentSeller, "nails", sim.NailItemKind, v.NailsShort)
		}
		return
	}
	b.WriteString("Buy the rest, then return to mend it. Use move_to to reach a supplier, then pay_with_item once you arrive:\n")
	renderWalkToVendors(b, v.Vendors)
}
