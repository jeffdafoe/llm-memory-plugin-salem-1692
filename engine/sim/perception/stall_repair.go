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
	Degraded       bool            // worn past the degrade threshold: shut for restock buying, production slowed, still sells on-hand stock (LLM-304, LLM-446)
	ProduceBlocked bool            // Degraded AND StallDegradedProducePct == 0: the legacy full production block (LLM-446) — selects the "can't make more" wording over "work goes slowly"
	NailsNeeded    int             // nails one repair consumes
	NailsHeld      int             // nails the actor currently carries
	HasEnoughNails bool            // NailsHeld >= NailsNeeded
	Name           string          // the business's display name (structure/object); "" → generic noun
	NailVendors    []RestockVendor // owner's buy-nails destinations (LLM-274); populated only when short of nails and NOT hired

	// Conserve (LLM-301): the working-capital gate says this keeper should hold off
	// buying. Set whenever the owner is short of nails (never for a hired worker).
	// Rides merchantConserve, the same signal "## Restocking" uses, and WINS over the
	// vendor-list branch in the render — even with a resolvable supplier, the cue
	// must not goad a buy while "## Restocking" says hold off (the LLM-297 posture);
	// findItemVendors' affordability drop is a different, narrower filter than the
	// working-capital floor, so the two can disagree.
	Conserve bool

	// MakesNails (LLM-446): the short-of-nails owner PRODUCES nails themselves —
	// the smith's own case. Wins over every buy branch in the render: for the
	// village's sole nail producer, "buy them" is an errand to a supplier who
	// doesn't exist (findItemVendors returns nothing — he IS the supplier of
	// record), and the deadlock's whole exit is that he forges his own. Making
	// costs no coin, so it also wins over Conserve.
	MakesNails bool
}

// ownerBusinessDegraded reports whether the actor owns a wearable business worn
// past the degrade threshold — shut for restock buying, and production slowed
// (or blocked at pct 0), until mended (LLM-304, LLM-446). The snapshot-side twin
// of sim.ownerStallDegraded: the buy-side "## Restocking" cue suppresses on it
// unconditionally so it can't steer a buy the degraded shop can't turn into
// stock. nil-safe via sim.StallDegraded (an actor owning no wearable stall is
// never degraded).
func ownerBusinessDegraded(snap *sim.Snapshot, actorID sim.ActorID) bool {
	return sim.StallDegraded(sim.OwnedWearableStall(snap.VillageObjects, actorID), snap.StallWearDegradeThreshold)
}

// ownerBusinessProduceBlocked reports whether degrade FULLY blocks the actor's
// production — degraded AND StallDegradedProducePct dialed to 0 (the legacy
// LLM-304 block). The snapshot-side twin of sim.degradedProduceBlocked: the
// production-side cues ("## Keeping up production", the forge/production
// choice) suppress on THIS, not on ownerBusinessDegraded, so at a positive pct
// a degraded keeper is still invited to produce — slowed production is the
// whole way out of the sole-nail-producer self-repair deadlock (LLM-446).
func ownerBusinessProduceBlocked(snap *sim.Snapshot, actorID sim.ActorID) bool {
	return snap.StallDegradedProducePct <= 0 && ownerBusinessDegraded(snap, actorID)
}

// buildStallRepair returns the at-the-business repair cue, or nil. Pure over the
// snapshot. Gated on: the actor is responsible for a wearable business (owns it,
// or is Working a hired job there — LLM-271), is standing at/inside it, and it has
// worn to the repair threshold.
func buildStallRepair(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *StallRepairView {
	if snap == nil || actorSnap == nil {
		return nil
	}
	if actorMidSourceActivity(actorSnap) {
		return nil // mid a source-activity window — a fresh repair bounces "already busy ... before mending the stall"
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
	degraded := sim.StallDegraded(stall, snap.StallWearDegradeThreshold)
	view := &StallRepairView{
		Hired:          hired,
		Degraded:       degraded,
		ProduceBlocked: degraded && snap.StallDegradedProducePct <= 0,
		NailsNeeded:    needed,
		NailsHeld:      held,
		HasEnoughNails: held >= needed,
		Name:           resolveDwellPinLabel(snap, stall.ID),
	}
	// LLM-446: a nail-producing owner (the smith) is steered to forge their own
	// rather than handed a buy errand — checked before the vendor scan since
	// MakesNails wins every buy branch in the render. NOT set while production
	// is hard-blocked (pct 0): StartProductionCycle rejects there, so the
	// forge-your-own steer would be exactly the unactionable errand this cue
	// exists to avoid (code_review) — the plain shortfall fallback renders
	// instead.
	if !view.HasEnoughNails && !view.ProduceBlocked && !hired && actorSnap.RestockPolicy != nil {
		for _, e := range actorSnap.RestockPolicy.ProduceEntries() {
			if e.Item == sim.NailItemKind {
				view.MakesNails = true
				break
			}
		}
	}
	// LLM-274: when the owner is short of nails, resolve the nail supplier(s) so the
	// cue can name a concrete move_to destination instead of the dead-end "the smith".
	// findItemVendors inherits the restock directory's filtering (supplier-of-record,
	// remembered-shut drop, affordability drop, workplace dedupe). Skipped for a hired
	// worker — they can't leave the job to shop, so their cue only names the shortfall
	// (renderHiredStallRepair).
	if !view.HasEnoughNails && !hired {
		// Only the destinations the owner can actually be sent to; the blocked
		// suppliers findItemVendors also returns are the restock cue's to narrate
		// (LLM-406).
		view.NailVendors, _ = findItemVendors(snap, actorID, actorSnap, sim.NailItemKind)
		// LLM-301: the conserve determination is independent of the vendor drops — a
		// keeper can be under the working-capital floor while a supplier still survives
		// findItemVendors (unknown price, or a remembered price the purse just covers).
		// Computed for every short-of-nails owner so the render can let it win over the
		// vendor list; merchantConserve short-circuits on a healthy purse, so the common
		// case pays nothing for the scan.
		view.Conserve = merchantConserve(snap, actorID, actorSnap).Active
	}
	return view
}

// renderStallRepair writes the "## Your business" section. Content-gated: a nil view
// writes nothing. Symmetrical awareness — it states the problem AND the way out
// (the smith sells the nails) in one place, and tells the owner whether they can
// mend now or must buy nails first (the two-step buy->repair, mirroring
// gather->consume).
// HasWalkToSupplier reports whether this cue will render a walk-to nail supplier —
// an off-scene "(destination: <id>)" for an owner standing at the very post the
// duty stabilizer pins him to (LLM-491). Mirrors renderStallRepair's branch order:
// a hired hand gets the softened no-errand framing, nails in hand mend on site, an
// own-forge owner is pointed at his own bench, and conserve holds off buying —
// only the surviving vendor branch sends him out.
func (v *StallRepairView) HasWalkToSupplier() bool {
	if v == nil || v.Hired || v.HasEnoughNails || v.MakesNails || v.Conserve {
		return false
	}
	return len(v.NailVendors) > 0
}

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
	// Owner path only — the hired-worker branch returned above.
	switch {
	case v.ProduceBlocked:
		// LLM-304 legacy (pct 0): a fully blocked shop sells down what's on hand
		// and reopens the refill on repair. State that plainly so the keeper keeps
		// trading (which earns the coin for the nails) and treats mending as the
		// way to restore the refill, instead of the old "stays shut, earns
		// nothing" framing that trapped a broke keeper.
		fmt.Fprintf(b, "Your %s is too worn to keep stock — you can still sell what's on hand, but you can't restock the shelves or make more until you mend it. ", name)
	case v.Degraded:
		// LLM-446: at a positive pct the degraded shop LIMPS — work continues
		// slowly, so the way out (make/earn the nails) stays visibly open. Only
		// restock buying is shut.
		fmt.Fprintf(b, "Your %s is badly worn — the disrepair drags at every task, and you can't restock the shelves until you mend it. You can still sell what's on hand, and work goes on, though slowly. ", name)
	default:
		fmt.Fprintf(b, "Your %s is showing hard use and needs mending. ", name)
	}
	if v.HasEnoughNails {
		fmt.Fprintf(b, "You carry enough nails (%d) to mend it — use the repair tool now to fix it, hammer in hand, on site (it takes a short while).\n", v.NailsHeld)
	} else if v.MakesNails {
		// LLM-446: the owner forges nails themselves — the sole-producer case,
		// where every buy branch below is an errand to a supplier who doesn't
		// exist. Point the mend at their own bench.
		fmt.Fprintf(b, "Mending takes %d nails and you have %d — but nails are your own work: forge what you're short, then mend it here.\n", v.NailsNeeded, v.NailsHeld)
	} else if v.Conserve {
		// LLM-301: the working-capital gate says hold off buying — the soften wins even
		// over a resolvable supplier (checked BEFORE the vendor list), so this cue can
		// never goad a nail buy while "## Restocking" says hold off (the LLM-297
		// posture). State the way out: sell, recover, then buy and mend.
		fmt.Fprintf(b, "Mending takes %d nails and you have %d — but your purse can't take on nails just now. Sell what you can and let your coins recover, then buy nails and come back to mend it.\n", v.NailsNeeded, v.NailsHeld)
	} else if len(v.NailVendors) > 0 {
		// LLM-274: name the actual nail supplier(s) — workplace + structure_id, resolved
		// via findItemVendors — in the model-proven Restocking format, plus the repair's
		// second hop (come back and mend). The old destination-less "buy from the smith"
		// left llama-3.3-70b narrating the errand ("I must visit the smith") and never
		// issuing move_to.
		fmt.Fprintf(b, "Mending takes %d nails and you have %d — buy them, then come back here and repair. Use move_to to reach a supplier, then pay_with_item once you arrive:\n", v.NailsNeeded, v.NailsHeld)
		renderWalkToVendors(b, v.NailVendors)
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
	switch {
	case v.ProduceBlocked:
		fmt.Fprintf(b, "The %s you're working at is too worn to keep stock — it can still sell what's on hand, but it can't restock or make more until it's mended. ", name)
	case v.Degraded:
		fmt.Fprintf(b, "The %s you're working at is badly worn — the disrepair drags at every task, and it can't restock until it's mended. It can still sell what's on hand, and the work goes on, though slowly. ", name)
	default:
		fmt.Fprintf(b, "The %s you're working at is showing hard use and needs mending. ", name)
	}
	if v.HasEnoughNails {
		fmt.Fprintf(b, "You carry enough nails (%d) to mend it — use the repair tool now to fix it, hammer in hand, on site (it takes a short while).\n", v.NailsHeld)
	} else {
		fmt.Fprintf(b, "Mending takes %d nails and you have %d — you'd need more from the smith first.\n", v.NailsNeeded, v.NailsHeld)
	}
}

// StallConditionView is the co-present world-fact line for an actor — NOT the
// owner — standing at a worn business. When merely worn it's social texture a
// passerby can remark on (the perception twin of the worn sprite, which the art
// pass defers); when DEGRADED (LLM-310) it escalates to the closed-for-restock
// fact, the faithful mirror of the owner cue so the two audiences agree on the
// shop's state. nil when the actor isn't at a worn business, or when they ARE its
// owner (they get the richer "## Your business" cue instead).
type StallConditionView struct {
	Degraded       bool
	ProduceBlocked bool   // Degraded at pct 0 — the legacy full-block wording (LLM-446); mirrors StallRepairView.ProduceBlocked so the two audiences agree
	Name           string // the business's display name (structure/object); "" → generic noun
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
		degraded := sim.StallDegraded(obj, snap.StallWearDegradeThreshold)
		return &StallConditionView{
			Degraded:       degraded,
			ProduceBlocked: degraded && snap.StallDegradedProducePct <= 0,
			Name:           resolveDwellPinLabel(snap, obj.ID),
		}
	}
	return nil
}

// renderStallCondition writes the co-present worn-business line (no header). A nil
// view writes nothing. When merely worn it's a bare atmosphere fact; when degraded
// it states the closed-for-restock condition (LLM-310) — still not a per-actor
// imperative, just the shop's state as it is.
func renderStallCondition(b *strings.Builder, v *StallConditionView) {
	if v == nil {
		return
	}
	name := v.Name
	if name == "" {
		name = "business"
	}
	switch {
	case v.ProduceBlocked:
		// LLM-310: a degraded business is a closed-for-restock fact, not mere texture —
		// state it the same way the owner's "## Your business" cue does (LLM-304: sells
		// on-hand stock, can't refill until mended) so a co-present buyer isn't told the
		// shop is only "run-down" while the owner is told he can still sell. Deliberately
		// NOT "can sell nothing": degrade blocks refill, not selling, so the on-hand stock
		// is still for sale (the eachVendorOffer qty>0 gate drops him once sold empty).
		fmt.Fprintf(b, "The %s here is too worn to restock — its keeper can sell what's on hand, but can't refill the shelves or make more until it's mended.\n", name)
	case v.Degraded:
		// LLM-446 (the positive-pct twin): the keeper still works, slowly — a
		// co-present buyer should expect long waits, not a dead shop.
		fmt.Fprintf(b, "The %s here is badly worn — its keeper can sell what's on hand and still works, though the disrepair makes everything slow, and the shelves can't be restocked until it's mended.\n", name)
	default:
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

	// LLM-297/299: co-present-buy situation awareness, shared with the shovel farm-upkeep
	// buy (copresent_buy.go). SellerStock is the nails the CoPresentSeller holds right now
	// (0 when none present); Block selects whether renderStallRepairBuy issues the "Buy it
	// now" imperative or softens to a hold-off. Both are set only when a seller is co-present
	// and no offer is already standing (the PendingOffer bide steer wins first).
	SellerStock int
	Block       copresentBuyBlock
}

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
	vendors, _ := findItemVendors(snap, actorID, actorSnap, sim.NailItemKind)
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
	// LLM-297/299: a co-present seller carries the "Buy it now" imperative — make it
	// situation-aware via the shared classifier (copresent_buy.go): read his live nail
	// stock to cap the suggested qty at what he can deliver, and decide whether to goad the
	// buy, hold off (no stock / conserve or empty purse / dead-ended negotiation), at all.
	// The PendingOffer bide steer wins first, so skip the block scan under it.
	if coID != "" && !view.PendingOffer {
		view.SellerStock, view.Block = classifyCoPresentBuy(snap, actorID, actorSnap, coID, sim.NailItemKind)
	}
	return view
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
		switch {
		case v.PendingOffer:
			// A nail offer already stands with the co-present seller — bide, don't
			// re-offer (the LLM-64 co-present-offer guard).
			renderCoPresentBuyPending(b, v.CoPresentSeller, "nail")
		case v.Block == copresentBuyBlockedNoStock, v.Block == copresentBuyBlockedCoin, v.Block == copresentBuyBlockedTerms:
			// LLM-297: seller has no stock / the purse can't take it on / the negotiation
			// has dead-ended — soften to a hold-off instead of goading the buy.
			renderCoPresentBuySoften(b, v.CoPresentSeller, "nails", v.Block)
		case v.SellerStock > 0 && v.SellerStock < v.NailsShort:
			// LLM-297: he can't cover the whole shortfall — cap the ask at what he holds
			// so the "qty up to N" never exceeds his stock (the live case: the buyer needed
			// 5 nails, the smith held only 1).
			renderCoPresentBuyCapped(b, v.CoPresentSeller, "nails", sim.NailItemKind, v.SellerStock)
		default:
			b.WriteString("Buy the rest, then return to mend it. ")
			renderCoPresentBuy(b, v.CoPresentSeller, "nails", sim.NailItemKind, v.NailsShort)
		}
		return
	}
	b.WriteString("Buy the rest, then return to mend it. Use move_to to reach a supplier, then pay_with_item once you arrive:\n")
	renderWalkToVendors(b, v.Vendors)
}
