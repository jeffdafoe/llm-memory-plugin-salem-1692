package perception

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// work_clothes.go — LLM-589. The "## Your clothes" section: a standing reminder
// to a WORKING actor that what they labour in has worn through, and where a
// replacement can be had.
//
// WHY THIS EXISTS. LLM-422 wears garments by worked MINUTE — labour, not weather
// — but the only clothing demand surface in the engine was the cold self-line's
// vendor nudge, which sits behind `Storm && !Indoors` (perception/hearth.go). So
// the mechanic and the cue disagreed about what clothing is for: a smith at a
// forge wears his shirt out faster than anyone in the village and was never once
// told about it. In a clear-weather village the demand side never spoke at all,
// and the clothing economy LLM-422 built has turned over exactly once in the
// world's life.
//
// SCOPE — this cue owns WORKING garments, the cold self-line keeps OUTERWEAR.
// sim.IsWorkGarment splits them on the `warms` capability, so linens/woolens/homespun
// belong here and coat/cloak stay with cold. The split is derived from the
// catalog rather than a kind list, so a garment added later joins the right cue
// on its own. Nothing about who may wear what is modelled: an actor needs SOME
// sound working garment, and the engine carries no sex to gate that on.
//
// NOT co-location-gated, like the farm-upkeep cue it is modelled on (and unlike
// the at-the-stall repair cue): the replacement is bought elsewhere, so it rides
// any tick the wearer draws.

// WorkClothesView is the working-clothes cue. Non-nil only when the actor is a
// garment-wearer whose best working garment is threadbare or absent.
type WorkClothesView struct {
	// Tier is sim.WarmGarmentThreadbare (worn thin) or sim.WarmGarmentNone (nothing
	// fit to work in). A sound tier returns a nil view — silence is a valid volume.
	Tier sim.WarmGarmentTier

	// Item is the working garment kind the cue steers toward, chosen as the first
	// kind with a resolvable supplier. Empty when no supplier exists anywhere, in
	// which case the cue still states the scene and simply names no destination.
	Item sim.ItemKind

	// ItemLabel is Item's PLURAL counting noun ("sets of woolens"), resolved at
	// build time so render stays pure over the view like every other renderer here.
	//
	// The plural counting noun rather than DisplayLabel: the catalog's display
	// labels are title-case proper names for an operator's item list ("Breeches",
	// "Shovel"), and dropping one mid-sentence renders "sells Breeches". The
	// counting nouns are the in-world prose forms (LLM-113) and are what belongs in
	// a scene. ItemSingular carries the qty-1 form for the same reason.
	ItemLabel string

	// ItemSingular is Item's singular counting noun ("set of woolens"), used
	// where the cue names the one garment being bought.
	ItemSingular string

	// Vendors are the workplaces selling Item — the move_to destination(s).
	Vendors []RestockVendor

	// CoPresentSeller / PendingOffer / SellerStock / Block mirror the farm-upkeep
	// and nail repair-buy errands: the buy-here fast path when a seller already
	// shares the huddle, the bide steer when an offer stands, and the situation
	// classifier that softens the goad when he has no stock or the purse can't
	// take it on.
	CoPresentSeller string
	PendingOffer    bool
	SellerStock     int
	Block           copresentBuyBlock

	// Mend arm (LLM-625). Set only on the THREADBARE tier — the wearer still
	// owns the garment, worn thin, which is exactly what a mender restores —
	// and only when a live mender (a keeper of a TagMending structure holding
	// thread) exists. When set, render steers to the MEND instead of a
	// replacement: it is the cheaper resolution and conserves the garment. The
	// NONE tier never mends (nothing left to mend) and keeps the replace steer.
	MendItem         sim.ItemKind    // the catalog's mending service kind
	MendVendors      []RestockVendor // walk-to mending shops, the replace steer's vendor shape
	CoPresentMender  string          // the mender's display name when co-present, "" otherwise
	MendPendingOffer bool            // a mending offer already stands with the co-present mender
	MendBlock        copresentBuyBlock
}

// snapActorWearsGarments defers to sim.SnapshotWearsGarments — the audience is
// exactly the set whose garments actually wear, so the cue can never address
// someone the wear sweep does not touch. Trade-errand visitors and the
// distributor are excluded there because their held garments are stock rather
// than clothing; without that the distributor would be told to buy the linens
// already on his own shelf.
//
// This started as a hand-copy of sim.actorWearsGarments and got the visitor arm
// wrong — it dropped EVERY visitor, where the sweep exempts only a visitor with a
// bound trade errand, so a plain passer-through wore his clothes out in silence.
// Hence the shared predicate rather than a second spelling of it (code_review).
//
// Gating on the working posture (rather than rendering to anyone, always) is
// deliberate: you notice a worn-through sleeve at the work, not over supper.
func snapActorWearsGarments(snap *sim.Snapshot, a *sim.ActorSnapshot) bool {
	return sim.SnapshotWearsGarments(snap, a)
}

// workGarmentKinds returns the catalog's working-garment kinds in a stable
// order. Sorted because map iteration is unstable and the chosen Item feeds a
// rendered destination — an unsorted pick would make the cue name a different
// shop between two builds of the same world.
func workGarmentKinds(snap *sim.Snapshot) []sim.ItemKind {
	var kinds []sim.ItemKind
	for kind := range snap.ItemKinds {
		if sim.IsWorkGarment(snap.ItemKinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

// buildWorkClothes returns the working-clothes cue, or nil. Pure over the
// snapshot.
func buildWorkClothes(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *WorkClothesView {
	if snap == nil || actorSnap == nil || !snapActorWearsGarments(snap, actorSnap) {
		return nil
	}
	tier := sim.ResolveWorkGarmentTier(snap.ItemKinds, actorSnap.Inventory, actorSnap.GarmentWear, snap.GarmentThreadbareFractionX100)
	if tier == sim.WarmGarmentSound {
		return nil
	}
	view := &WorkClothesView{Tier: tier}
	// Mend arm (LLM-625): on the threadbare tier a live mender resolves the
	// scene without a purchase of cloth at all — the wearer's own garment comes
	// back. Checked before the replace steer so the cheaper, conserving path
	// wins whenever it exists; the replace steer stays the fallback (and the
	// only arm of the NONE tier, which owns nothing a mender could touch).
	if tier == sim.WarmGarmentThreadbare {
		if fillMendArm(snap, actorID, actorSnap, view) {
			return view
		}
	}
	// Steer toward a working kind with a live supplier, preferring one the wearer
	// ALREADY OWNS — replacing like with like. A kind whose sellers are all shut or
	// unaffordable resolves to no vendors and is skipped, so the cue never sends
	// the wearer to a dead end (the LLM-216 posture the farm-upkeep cue takes), and
	// iterating the catalog rather than naming a kind keeps it honest about what
	// can actually be bought today.
	//
	// The like-for-like pass exists because a wearer should replace what he has
	// worn out, not whatever happens to sort first. Without it the cue takes the
	// first purchasable kind outright, so whichever garment the distributor happens
	// to stock is what every worker in the village is sent for, regardless of what
	// they own.
	//
	// It was added for a sharper reason that LLM-596 has since retired: the catalog
	// then named its garments shift, breeches and gown, which are gendered, while
	// the engine models no sex. With the LLM-592 seed the first purchasable kind
	// was a GOWN, so Constable Gideon Marsh — threadbare in a shift and breeches —
	// would have been told a fresh gown would set him right. Preferring what he
	// already wore fixed that without teaching the engine a sex to reason about.
	// The names are unisex layers now (linens, woolens, homespun) so that
	// particular embarrassment cannot recur, but the preference is still the right
	// behaviour on its own merits and stays.
	//
	// The fallback still matters and is not a formality: an actor in the NONE tier
	// owns no working garment to match against, so like-for-like has nothing to say
	// and the cue takes whatever is purchasable — which is right, since anything
	// fit to work in beats nothing.
	pickKind := func(ownedOnly bool) bool {
		for _, kind := range workGarmentKinds(snap) {
			if ownedOnly && actorSnap.Inventory[kind] <= 0 {
				continue
			}
			vendors, _ := findItemVendors(snap, actorID, actorSnap, kind)
			if len(vendors) == 0 {
				continue
			}
			view.Item = kind
			view.ItemLabel = snap.ItemKinds[kind].Plural()
			view.ItemSingular = snap.ItemKinds[kind].Singular()
			view.Vendors = vendors
			return true
		}
		return false
	}
	if !pickKind(true) {
		pickKind(false)
	}
	if view.Item == "" {
		// Nothing purchasable anywhere, so the cue has nothing to say. Silence is a
		// valid volume (GUIDELINES): a standing line that names a problem with no
		// action attached would ride EVERY tick of EVERY working actor in a village
		// with no cloth for sale, which is the noise the whole 'scenes, not stats'
		// register exists to avoid. Unlike the farm-upkeep cue — whose fallback can
		// safely name the blacksmith because the smith definitively produces shovels
		// — clothing is import-only (LLM-410), so "no seller" is a real and ordinary
		// state rather than a resolution failure worth narrating.
		return nil
	}
	coName, coID := coPresentSellerForItem(snap, actorID, actorSnap, view.Item)
	view.CoPresentSeller = coName
	view.PendingOffer = coID != "" && hasPendingOfferTo(snap, actorID, coID, view.Item)
	if coID != "" && !view.PendingOffer {
		view.SellerStock, view.Block = classifyCoPresentBuy(snap, actorID, actorSnap, coID, view.Item)
	}
	return view
}

// HasWalkToSupplier reports whether this cue renders a walk-to destination — the
// off-scene pull the at-post stabilizer has to weigh against. Mirrors
// renderWorkClothes: a co-present seller (or mender) returns before the vendor
// list is reached, so it is not a walk-to.
func (v *WorkClothesView) HasWalkToSupplier() bool {
	if v == nil {
		return false
	}
	if v.MendItem != "" {
		return v.CoPresentMender == "" && len(v.MendVendors) > 0
	}
	if v.CoPresentSeller != "" {
		return false
	}
	return len(v.Vendors) > 0
}

// mendingServiceKind resolves the catalog's mending service kind — the first
// (sorted) kind carrying both the mending and service capabilities. Derived
// from the catalog like IsWorkGarment rather than a hardcoded name, so the
// kind is operator-authorable; requiring `service` alongside keeps a
// misconfigured mending-without-service kind out of the cue the same way the
// delivery path rejects it. "" when the catalog has none (the pre-LLM-625
// world — the whole arm then stays silent).
func mendingServiceKind(snap *sim.Snapshot) sim.ItemKind {
	var kinds []sim.ItemKind
	for kind, def := range snap.ItemKinds {
		if def != nil && def.HasCapability(sim.CapabilityMending) && def.HasCapability("service") {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) == 0 {
		return ""
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds[0]
}

// fillMendArm resolves the mend resolution for a threadbare wearer: the
// catalog's mending kind plus a live mender — a non-PC actor stationed at a
// resolvable, TagMending workplace, holding thread to mend with. Fills the
// view's mend fields and reports whether the arm is live. Mirrors the replace
// steer's vendor discipline: a mender whose shop the wearer remembers shut is
// skipped (the LLM-216 no-dead-end posture), one entry per structure with the
// lowest ActorID as representative, sorted for a stable cue.
func fillMendArm(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot, view *WorkClothesView) bool {
	mendKind := mendingServiceKind(snap)
	if mendKind == "" {
		return false
	}
	type pick struct {
		vendorID  sim.ActorID
		structure *sim.Structure
	}
	best := map[sim.StructureID]pick{}
	sc := buyerCoPresenceScope(snap, actorSnap)
	var coID sim.ActorID
	var coName string
	for menderID, mender := range snap.Actors {
		if mender == nil || menderID == actorID || mender.Kind == sim.KindPC {
			continue
		}
		if mender.Inventory[sim.MendThreadKind] < sim.MendThreadPerMend {
			continue
		}
		if !sim.ActorIsMender(snap.VillageObjects, mender.WorkStructureID) {
			continue
		}
		st := snap.Structures[mender.WorkStructureID]
		if st == nil {
			continue
		}
		if businessRememberedShut(snap, actorSnap, mender.WorkStructureID) {
			continue
		}
		if sc.sellerCoPresent(mender) && (coID == "" || menderID < coID) {
			coID = menderID
			coName = mender.DisplayName
		}
		if cur, ok := best[mender.WorkStructureID]; ok && cur.vendorID <= menderID {
			continue
		}
		best[mender.WorkStructureID] = pick{vendorID: menderID, structure: st}
	}
	if len(best) == 0 {
		return false
	}
	view.MendItem = mendKind
	view.CoPresentMender = coName
	if coID != "" {
		view.MendPendingOffer = hasPendingOfferTo(snap, actorID, coID, mendKind)
		if !view.MendPendingOffer {
			view.MendBlock = classifyCoPresentMend(snap, actorID, actorSnap, coID, mendKind)
		}
		return true
	}
	costFallback := ""
	if r := snap.Recipes[mendKind]; r != nil && r.RetailPrice > 0 {
		costFallback = fmt.Sprintf("~%d coins", r.RetailPrice)
	}
	vendors := make([]RestockVendor, 0, len(best))
	for structureID, p := range best {
		vendors = append(vendors, RestockVendor{
			StructureLabel: vendorStructureLabel(p.structure),
			StructureID:    structureID,
			VendorID:       p.vendorID,
			CostText:       buyerLastPaidText(snap, actorID, p.vendorID, mendKind, costFallback),
		})
	}
	sort.Slice(vendors, func(i, j int) bool {
		if vendors[i].StructureLabel != vendors[j].StructureLabel {
			return vendors[i].StructureLabel < vendors[j].StructureLabel
		}
		return vendors[i].StructureID < vendors[j].StructureID
	})
	view.MendVendors = vendors
	return true
}

// classifyCoPresentMend is classifyCoPresentBuy with the stock read swapped:
// what a mender must hold is THREAD, not units of the service kind (a service
// carries no inventory, so the shared classifier would misread every mender as
// out of stock). The conserve and standoff arms are the shared ones, so the
// mend goad softens in exactly the situations the buy goad would.
func classifyCoPresentMend(snap *sim.Snapshot, buyer sim.ActorID, buyerSnap *sim.ActorSnapshot, mender sim.ActorID, mendKind sim.ItemKind) copresentBuyBlock {
	thread := 0
	if m := snap.Actors[mender]; m != nil {
		thread = m.Inventory[sim.MendThreadKind]
	}
	switch {
	case thread < sim.MendThreadPerMend:
		return copresentBuyBlockedNoStock
	case merchantConserve(snap, buyer, buyerSnap).Active:
		return copresentBuyBlockedCoin
	default:
		return coPresentBuyStandoff(snap, buyer, mender, buyerSnap.CurrentHuddleID, mendKind)
	}
}

// wornPhrase is the tiered scene. Diegetic and escalating, never an imperative —
// the scene is the argument and the model draws the conclusion (GUIDELINES,
// "scenes, not stats").
//
// The NONE wording is deliberately about work clothes rather than about being
// unclothed. Every actor in the village is obviously dressed; what the engine
// models is whether they have a garment fit to labour in, which is an ordinary
// 1692 want. "You have nothing to wear" would read as a retcon.
func wornPhrase(tier sim.WarmGarmentTier) string {
	if tier == sim.WarmGarmentNone {
		return "You have nothing fit to work in — what you stand up in will not take another week of it. "
	}
	return "Your working clothes are worn thin at the elbows, and the cloth is going at the seams. "
}

// renderWorkClothes writes the "## Your clothes" section. Content-gated: a nil
// view writes nothing, which is the common case (a sound garment says nothing).
func renderWorkClothes(b *strings.Builder, v *WorkClothesView) {
	if v == nil {
		return
	}
	b.WriteString("## Your clothes\n")
	b.WriteString(wornPhrase(v.Tier))
	if v.MendItem != "" {
		renderMendArm(b, v)
		return
	}
	if v.Item == "" {
		// No supplier anywhere — the scene alone, with no dead-end target.
		b.WriteString("Nobody in the village has cloth to sell just now.\n")
		return
	}
	label := v.ItemLabel
	if v.CoPresentSeller != "" {
		switch {
		case v.PendingOffer:
			renderCoPresentBuyPending(b, v.CoPresentSeller, label)
		case v.Block == copresentBuyBlockedNoStock, v.Block == copresentBuyBlockedCoin, v.Block == copresentBuyBlockedTerms:
			renderCoPresentBuySoften(b, v.CoPresentSeller, label, v.Block)
		default:
			renderCoPresentBuy(b, v.CoPresentSeller, label, v.Item, 1)
		}
		return
	}
	// Name the garment before the destination. Without it the model arrives at the
	// shop holding a want and no item to put in pay_with_item — the farm-upkeep cue
	// names its shovels for the same reason. The article-less singular reads as
	// prose ("a fresh set of woolens"), which the title-case catalog label
	// ("Breeches") does not.
	fmt.Fprintf(b, "A fresh %s would set you right. Use move_to to reach a seller, then pay_with_item once you arrive:\n", sanitizeInline(v.ItemSingular))
	renderWalkToVendors(b, v.Vendors)
}

// renderMendArm writes the mend resolution of the threadbare scene (LLM-625):
// a co-present mender gets the proven buy-here imperative (or its bide/soften
// arms), an off-scene one the walk-to list. "mending" is both the prose noun
// and the exact pay_with_item item, so the label doubles as the identifier —
// the exact-identifiers rule.
func renderMendArm(b *strings.Builder, v *WorkClothesView) {
	if v.CoPresentMender != "" {
		switch {
		case v.MendPendingOffer:
			renderCoPresentBuyPending(b, v.CoPresentMender, "mending")
		case v.MendBlock == copresentBuyBlockedNoStock:
			fmt.Fprintf(b, "%s takes in mending but has no thread to work with just now — come back once they have some rather than pressing them.\n",
				sanitizeInline(v.CoPresentMender))
		case v.MendBlock == copresentBuyBlockedCoin, v.MendBlock == copresentBuyBlockedTerms:
			renderCoPresentBuySoften(b, v.CoPresentMender, "mending", v.MendBlock)
		default:
			// The lead-in carries the why (your OWN clothes come back, for less);
			// renderCoPresentBuy carries the who-is-here and the proven call shape.
			b.WriteString("A mend would see your own clothes good as new, cheaper than replacing them. ")
			renderCoPresentBuy(b, v.CoPresentMender, "mending", v.MendItem, 1)
		}
		return
	}
	b.WriteString("They could be brought back for a few coins. Use move_to to reach the mender, then pay_with_item once you arrive:\n")
	renderWalkToVendors(b, v.MendVendors)
}
