package perception

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// satiation.go — ZBBS-HOME-304. The "## What you can eat or drink" perception
// section: surfaces, to a hungry or thirsty NPC, how to resolve the pressing
// need — the satisfying items it ALREADY CARRIES (consume-first), free public
// sources it can walk to (a well, a fruit tree), and nearby vendors selling a
// satisfier. Port of v1's engine/satiation.go, adapted to v2: vendor cues run
// through the shared findVendorConsumables finder (see consumable_vendors.go),
// the same structural-vendorship + workplace surface the recovery-options
// remedy arm uses.
//
// Free-source arm (ZBBS-HOME-359): hunger/thirst had no analogue to the
// recovery section's free rest spots (gatherFreeRestSpots), so a thirsty NPC
// away from the well never saw it — the only path was the gather cue, which
// fires only once already standing at the source. This scans VillageObjects
// for free need-easing placements the same way rest spots are surfaced, and
// carries each source's object id so move_to can walk there (the id / name
// paths now resolve a bare refresh source to an object visit — sim/move_to.go).
//
// Why the own-stock line is the load-bearing half (v1's ZBBS-123 diagnosis): a
// herbalist hungry on shift while holding 50 berries walked to a store instead
// of eating them, because nothing connected "you're hungry" to the consume
// tool — the inventory read framed the berries as merchandise. The own-stock
// line states the connection outright: "you have berries — consume to eat."
//
// Scope is hunger + thirst only. Tiredness's resolution is spatial (rest spot /
// home / inn / brew), so it lives in recovery_options.go's unified list, not
// here — same split v1 settled on (ZBBS-HOME-206).

// satiationNeeds is the fixed render order — hunger before thirst, matching the
// order pressing needs appear elsewhere in the prompt.
var satiationNeeds = []sim.NeedKey{"hunger", "thirst"}

// ZBBS-HOME-465: morning breakfast cue. The food menu below is normally gated to
// the RED need threshold (the same boundary the need-threshold warrant fires on),
// so a mildly-hungry NPC isn't shown where to eat until it is nearly starving.
// During the morning that gate is relaxed for HUNGER, so a waking, peckish NPC is
// prompted with what is available (the Inn's porridge — an open breakfast while
// the night-shift Tavern is shut) and can choose to break its fast. This renders
// the moment legibly rather than forcing a scheduled meal. The window matches the
// "morning" time-of-day band (timeOfDayProse, 07:00–12:00); the floor matches the
// perception silent floor (sim's unexported needSilentFloor = 8) — at or above it
// the NPC already "feels peckish", so it should also be told where to eat.
const (
	morningBreakfastStartMinute = 420 // 07:00 — start of the "morning" band
	morningBreakfastEndMinute   = 720 // 12:00 — end of the "morning" band
	morningBreakfastHungerFloor = 8   // = sim needSilentFloor: at/over it hunger is felt
)

var satiationHungerNeed = sim.NeedKey("hunger")

// morningBreakfastCue reports whether the morning-breakfast relaxation applies to
// this need: it is hunger, the village clock is within the morning band, and the
// actor is at least mildly hungry (felt — at/over the silent floor). Off when the
// clock isn't established (nil LocalMinuteOfDay); then the normal red gate stands.
func morningBreakfastCue(snap *sim.Snapshot, need sim.NeedKey, value int) bool {
	if need != satiationHungerNeed || snap.LocalMinuteOfDay == nil {
		return false
	}
	m := *snap.LocalMinuteOfDay
	return m >= morningBreakfastStartMinute && m < morningBreakfastEndMinute && value >= morningBreakfastHungerFloor
}

// SatiationView is the content-gated "## What you can eat or drink" section.
// A nil view (or empty Needs) means render omits the section.
type SatiationView struct {
	Needs []SatiationNeedView
}

// SatiationNeedView is one pressing consumable need's resolution surface.
type SatiationNeedView struct {
	Need sim.NeedKey // "hunger" | "thirst"
	Verb string      // "eat" | "drink"

	// Level is the actor's CURRENT level of this need at build time, feeding
	// the per-unit sufficiency clause (ZBBS-WORK-392): when one unit of an
	// offered item would fully zero it, the render says so ("a single one
	// would fully satisfy your hunger") — the informed-buying fact whose
	// absence let a starving buyer accept a seller-pitched 10-meat bundle.
	Level int

	OwnStock []OwnStockItem // satisfiers the actor already carries (shared shape)

	// CoPresentPeers are huddle peers standing with the subject RIGHT NOW who
	// carry an item that eases this need — the immediately-actionable
	// buy-from-the-person-beside-you affordance (ZBBS-HOME-342). pay_with_item
	// resolves the seller as any co-present huddle peer holding the goods (it is
	// NOT vendor-gated), so the transaction substrate already supports this; the
	// only gap was that perception never named the co-present holder. Rendered
	// AHEAD of Vendors and carries NO structure_id — they're already here, so
	// nothing should tempt a move_to.
	CoPresentPeers []SatiationPeerOffer

	// FreeSources are free, public refresh placements the actor can walk to and
	// consume in place at no cost — a well (thirst), a fruit tree (hunger). The
	// hunger/thirst analogue of the recovery section's free rest spots. Rendered
	// AHEAD of Vendors (free beats paid) and AFTER CoPresentPeers (a walk is more
	// than buying from someone already beside you). ZBBS-HOME-359.
	FreeSources []SatiationFreeSource

	Vendors []SatiationVendor // nearby places selling a satisfier (walk to)
}

// SatiationFreeSource is one free, public refresh source for the need — a well
// (thirst), a fruit tree (hunger) — the actor can walk to and consume at in
// place. The object-keyed analogue of the recovery section's free rest spots
// (RecoveryOption Kind "rest"). ObjectID is the move_to handle: a bare refresh
// source resolves by id or name to an object visit (sim/move_to.go), so the cue
// surfaces it as a structure_id the same way every other actionable cue does.
// ZBBS-HOME-359.
type SatiationFreeSource struct {
	Label     string              // "Well" — the source's display name
	ObjectID  sim.VillageObjectID // move_to handle (rendered as structure_id)
	Magnitude int                 // immediate need eased on arrival (positive)
	Distance  string              // qualitative ("a short walk"); "" when unknown
	Direction string              // cardinal ("northeast"); "" when coincident

	// sortKey is the actor→source tile distance used to order bullets (nearest
	// first). Unexported — never rendered.
	sortKey float64
	// sourceKey is the object id — a stable final tie-break so equal-distance,
	// equal-label sources order deterministically (VillageObjects is a map).
	// Unexported — never rendered.
	sourceKey string
}

// SatiationPeerOffer is one co-present huddle peer who carries a satisfier for
// the need — a buy-it-now-from-them affordance with no walk. PeerLabel is the
// acquaintance-gated name (descriptorLabel), never a raw UUID.
type SatiationPeerOffer struct {
	PeerLabel string       // "Hannah" | "the herbalist" | "a stranger"
	ItemLabel string       // "stew"
	Magnitude int          // immediate need eased per unit (positive)
	peerID    sim.ActorID  // sort key only — never rendered (huddle order is by ID)
	itemKind  sim.ItemKind // sort tie-break only — never rendered
}

// SatiationVendor is one (workplace, item) buy opportunity.
type SatiationVendor struct {
	StructureLabel string          // "PW Apothecary" — where the buyer walks to
	StructureID    sim.StructureID // the workplace's key — passed to move_to to walk there
	ItemLabel      string          // "stew"
	Magnitude      int
	CostText       string // "~3 coins" | "ask the seller"

	// Shut is true when the subject has a live experiential memory of finding
	// this business shut (no keeper) within the decay window — render annotates
	// the line so the model deprioritizes the trip. ZBBS-HOME-353.
	Shut bool

	// OutOfStock is true when the subject has a live experiential memory of
	// trying to buy THIS item here and finding it out of stock within the decay
	// window — render annotates the line so the model deprioritizes the trip.
	// ZBBS-HOME-363.
	OutOfStock bool

	// ClosedNow is true when the vendor backing this offer is asleep at snapshot
	// time (an off-shift night-shift keeper abed at the counter) — render
	// annotates the line present-tense so the buyer redirects to an open source
	// instead of petitioning a sleeping keeper who can't transact. Distinct from
	// the experiential Shut memory: this is a LIVE state read, so it takes
	// precedence over Shut in render. ZBBS-HOME-387.
	ClosedNow bool

	// EatHere is true when the item always settles eat-here (consumable,
	// neither service nor portable — ItemKindDef.EatHereOnly). Render states
	// the fact on the line so the buyer plans a sit-down, not a carry-out
	// the WORK-405 clamp would quietly rewrite. ZBBS-WORK-405.
	EatHere bool
}

// buildSatiation builds the eat/drink view for actorSnap, or nil when no
// consumable need is pressing or no satisfier exists. Pure over the snapshot.
func buildSatiation(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *SatiationView {
	if snap == nil || actorSnap == nil {
		return nil
	}
	var needs []SatiationNeedView
	for _, need := range satiationNeeds {
		// Pressing = at/over the configured red threshold, the same boundary the
		// need-threshold producer's warrant fires on (NeedThresholds.Get falls
		// back to the registry default when unset). ZBBS-HOME-465 also opens the
		// menu for morning hunger at the milder felt floor (morningBreakfastCue),
		// so a waking NPC is prompted with breakfast before it is starving.
		if actorSnap.Needs[need] < snap.NeedThresholds.Get(need) && !morningBreakfastCue(snap, need, actorSnap.Needs[need]) {
			continue
		}
		own := gatherOwnStock(snap, actorSnap, need)
		peers := gatherCoPresentPeerOffers(snap, actorID, actorSnap, need)
		free := gatherFreeSatiationSources(snap, actorSnap, need)
		vendors := gatherSatiationVendors(snap, actorID, actorSnap, need)
		if len(own) == 0 && len(peers) == 0 && len(free) == 0 && len(vendors) == 0 {
			continue
		}
		needs = append(needs, SatiationNeedView{
			Need:           need,
			Verb:           satiationVerb(need),
			Level:          actorSnap.Needs[need],
			OwnStock:       own,
			CoPresentPeers: peers,
			FreeSources:    free,
			Vendors:        vendors,
		})
	}
	if len(needs) == 0 {
		return nil
	}
	return &SatiationView{Needs: needs}
}

// maxSatiationVendors caps the buy menu to the N nearest vendor structures per
// need (ZBBS-HOME-363 altitude). The live prompt had ~20 undifferentiated
// options — the Tavern repeated once per item it sells, no distance bound — so a
// hungry NPC re-sampled a sprawling menu each tick instead of picking the
// closest place to eat. The cap + dedup-by-structure + nearest-first sort below
// turn that into a short, ordered list.
const maxSatiationVendors = 4

// satiationVendorCandidate is one structure's chosen representative offer plus
// the keys the altitude pass orders + dedups on.
type satiationVendorCandidate struct {
	sv         SatiationVendor
	distTiles  float64
	outOfStock bool
	magnitude  int
	itemKind   sim.ItemKind
}

// gatherSatiationVendors maps the shared vendor-consumable finder into the
// satiation bullet shape and applies ALTITUDE (ZBBS-HOME-363): exclude the
// buyer's own workplace, DEDUP to one representative item per vendor structure
// (so the Tavern stops repeating per item), order the structures NEAREST-FIRST,
// and cap to maxSatiationVendors. The per-structure representative prefers an
// item NOT remembered out of stock, then the strongest satisfier — so the menu
// shows something the NPC can actually buy when possible.
func gatherSatiationVendors(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot, need sim.NeedKey) []SatiationVendor {
	byStructure := make(map[sim.StructureID]satiationVendorCandidate)
	for _, vc := range findVendorConsumables(snap, actorID, need, "ask the seller") {
		// No resolvable workplace → no structure_id for move_to (unactionable);
		// own workplace excluded so a hungry vendor doesn't get steered to buy
		// from itself (the Josiah-buys-at-his-own-General-Store case).
		if vc.StructureID == "" || vc.StructureID == actorSnap.WorkStructureID {
			continue
		}
		outOfStock := businessRememberedOutOfStock(snap, actorSnap, vc.StructureID, vc.ItemKind)
		cand := satiationVendorCandidate{
			sv: SatiationVendor{
				StructureLabel: vc.StructureLabel,
				StructureID:    vc.StructureID,
				ItemLabel:      vc.ItemLabel,
				Magnitude:      vc.Magnitude,
				CostText:       vc.CostText,
				Shut:           businessRememberedShut(snap, actorSnap, vc.StructureID),
				OutOfStock:     outOfStock,
				ClosedNow:      vendorKeeperAsleep(snap, vc.VendorID),
				EatHere:        snap.ItemKinds[vc.ItemKind].EatHereOnly(),
			},
			distTiles:  vendorStructureDistanceTiles(snap, actorSnap, vc.StructureID),
			outOfStock: outOfStock,
			magnitude:  vc.Magnitude,
			itemKind:   vc.ItemKind,
		}
		if existing, ok := byStructure[vc.StructureID]; !ok || betterSatiationRepresentative(cand, existing) {
			byStructure[vc.StructureID] = cand
		}
	}
	if len(byStructure) == 0 {
		return nil
	}
	cands := make([]satiationVendorCandidate, 0, len(byStructure))
	for _, c := range byStructure {
		cands = append(cands, c)
	}
	// Nearest first; ties by label then structure_id for deterministic output.
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].distTiles != cands[j].distTiles {
			return cands[i].distTiles < cands[j].distTiles
		}
		if cands[i].sv.StructureLabel != cands[j].sv.StructureLabel {
			return cands[i].sv.StructureLabel < cands[j].sv.StructureLabel
		}
		return cands[i].sv.StructureID < cands[j].sv.StructureID
	})
	if len(cands) > maxSatiationVendors {
		cands = cands[:maxSatiationVendors]
	}
	out := make([]SatiationVendor, len(cands))
	for i, c := range cands {
		out[i] = c.sv
	}
	return out
}

// betterSatiationRepresentative reports whether candidate a should replace b as
// a structure's single buy-menu entry: prefer an item NOT remembered out of
// stock (show something buyable), then the stronger satisfier, then a stable
// ItemKind tie-break (Inventory iteration is unordered).
func betterSatiationRepresentative(a, b satiationVendorCandidate) bool {
	if a.outOfStock != b.outOfStock {
		return !a.outOfStock
	}
	if a.magnitude != b.magnitude {
		return a.magnitude > b.magnitude
	}
	return a.itemKind < b.itemKind
}

// vendorStructureDistanceTiles is the actor→vendor-workplace distance in
// internal-grid tiles, for nearest-first ordering. Structures share their id
// with their village_object placement, so the structure tile comes from
// snap.VillageObjects[id].Pos.Tile() (world pixels → tile), same conversion the
// free-source scan uses. An unplaced structure sorts last (+Inf).
func vendorStructureDistanceTiles(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot, structureID sim.StructureID) float64 {
	obj := snap.VillageObjects[sim.VillageObjectID(structureID)]
	if obj == nil {
		return math.Inf(1)
	}
	t := obj.Pos.Tile()
	dx := float64(t.X - actorSnap.Pos.X)
	dy := float64(t.Y - actorSnap.Pos.Y)
	return math.Sqrt(dx*dx + dy*dy)
}

// gatherFreeSatiationSources returns a free-source bullet for each village
// object that eases `need` on arrival (a well's thirst refresh, a fruit tree's
// hunger refresh), skipping objects whose finite supply is exhausted. The
// hunger/thirst analogue of recovery_options' gatherFreeRestSpots — same
// distance/direction derivation in INTERNAL-GRID TILE space (actor Pos is a
// padded tile; an object's Pos is world pixels, converted via obj.Pos.Tile()
// before measuring) and same nearest-first ordering. Each source carries its
// object id so the rendered cue is actionable through move_to. ZBBS-HOME-359.
func gatherFreeSatiationSources(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot, need sim.NeedKey) []SatiationFreeSource {
	if snap == nil || actorSnap == nil {
		return nil
	}
	ax := float64(actorSnap.Pos.X)
	ay := float64(actorSnap.Pos.Y)
	var out []SatiationFreeSource
	for id, obj := range snap.VillageObjects {
		if obj == nil {
			continue
		}
		mag := objectRefreshMagnitude(obj, need)
		if mag <= 0 {
			continue
		}
		objTile := obj.Pos.Tile()
		tx := float64(objTile.X)
		ty := float64(objTile.Y)
		dx := tx - ax
		dy := ty - ay
		distTiles := math.Sqrt(dx*dx + dy*dy)
		label := obj.DisplayName
		if label == "" {
			label = "a nearby source"
		}
		out = append(out, SatiationFreeSource{
			Label:     label,
			ObjectID:  id,
			Magnitude: mag,
			Distance:  qualitativeDistance(distTiles),
			Direction: cardinalDirection(ax, ay, tx, ty),
			sortKey:   distTiles,
			sourceKey: string(id),
		})
	}
	// Nearest first; ties broken by label then object id for deterministic output
	// (VillageObjects is a map). Mirrors gatherFreeRestSpots' ordering.
	sort.Slice(out, func(i, j int) bool {
		if out[i].sortKey != out[j].sortKey {
			return out[i].sortKey < out[j].sortKey
		}
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].sourceKey < out[j].sourceKey
	})
	return out
}

// objectRefreshMagnitude returns the positive amount of `need` eased by
// arriving at obj — the negated arrival decrement plus any dwell delta — or 0
// if obj doesn't ease `need` or its finite supply is exhausted. The shared core
// of the satiation free-source scan and recovery_options' tirednessRefreshMagnitude,
// generalised over the need key so a well (thirst) and a shade tree (tiredness)
// read the same way. ZBBS-HOME-359.
func objectRefreshMagnitude(obj *sim.VillageObject, need sim.NeedKey) int {
	for _, r := range obj.Refreshes {
		if r == nil || r.Attribute != need {
			continue
		}
		if r.IsFinite() && r.AvailableQuantity != nil && *r.AvailableQuantity <= 0 {
			continue
		}
		mag := -r.Amount // Amount is the negative arrival decrement
		if r.DwellDelta != nil {
			mag += -*r.DwellDelta
		}
		if mag < 0 {
			mag = 0
		}
		return mag
	}
	return 0
}

// gatherCoPresentPeerOffers scans the subject's CURRENT HUDDLE PEERS for any who
// carry an item that eases `need` on the immediate hit, returning one entry per
// (peer, item). This is the buy-from-the-person-beside-you affordance: the
// peers are co-present, so pay_with_item can resolve them as the seller right
// now with no walk (it resolves any co-present huddle peer holding the goods —
// it is NOT vendor-gated). Distinct from the workplace-vendor scan: this is a
// peer-INVENTORY read over the huddle set, not a structural-vendorship scan, so
// a peer surfaces here whether or not they're also a structural vendor (they're
// different affordances — the same peer can appear in both lists).
//
// The peer name is acquaintance-gated via descriptorLabel — the same name-vs-
// descriptor treatment huddle members get in "Around you" — so an unacquainted
// peer reads as "the <role>" / "a stranger", never their DisplayName.
//
// Excluded: the subject themselves, PCs (they don't sell through the NPC
// commerce path — same exclusion the vendor scan applies), and any actor not in
// the subject's huddle. Output is sorted strongest-offer-first (magnitude desc,
// then PeerLabel, ItemLabel, peerID, ItemKind), mirroring the own-stock / vendor
// finders' "usefulness before identity" ordering with fully deterministic
// tie-breaks (huddle membership + Inventory are maps). Empty when not huddled or
// no co-present peer carries a satisfier.
func gatherCoPresentPeerOffers(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot, need sim.NeedKey) []SatiationPeerOffer {
	if snap == nil || actorSnap == nil || actorSnap.CurrentHuddleID == "" {
		return nil
	}
	h := snap.Huddles[actorSnap.CurrentHuddleID]
	if h == nil {
		return nil
	}
	var out []SatiationPeerOffer
	for peerID := range h.Members {
		if peerID == actorID {
			continue
		}
		peer := snap.Actors[peerID]
		if peer == nil || peer.Kind == sim.KindPC {
			continue
		}
		// Acquaintance gating mirrors buildSurroundings: the subject knows the
		// peer iff their DisplayName is in the subject's Acquaintances map.
		acquainted := false
		if peer.DisplayName != "" {
			_, acquainted = actorSnap.Acquaintances[peer.DisplayName]
		}
		label := descriptorLabel(peer.DisplayName, peer.Role, acquainted)
		for kind, qty := range peer.Inventory {
			if qty <= 0 {
				continue
			}
			mag := itemNeedMagnitude(snap, kind, need)
			if mag <= 0 {
				continue
			}
			// A purchase already in flight must not be re-urged: the cross-tick
			// duplicate gate (ZBBS-WORK-391) rejects exactly the pay_with_item
			// this line cues, so rendering it puts the prompt at war with the
			// gate — the model obeys the cue, collects [error: already_offered],
			// and loops (the hud-6c849d… churn, ZBBS-HOME-424). The "## Your
			// pending offers" section already says to wait; other peers/items
			// stay listed as legitimate alternatives.
			if hasPendingOfferTo(snap, actorID, peerID, kind) {
				continue
			}
			out = append(out, SatiationPeerOffer{
				PeerLabel: label,
				ItemLabel: itemDisplayLabel(snap, kind),
				Magnitude: mag,
				peerID:    peerID,
				itemKind:  kind,
			})
		}
	}
	// Strongest offer first — matches the own-stock / vendor finders' "usefulness
	// before identity" ordering, so the most-satisfying buy reads at the top of
	// the prompt. PeerLabel then ItemLabel give a stable human-meaningful order
	// within a magnitude tier; peerID + itemKind are the final deterministic
	// tie-breaks (huddle membership + Inventory are maps, so the comparator must
	// fully order equal-magnitude/label entries).
	sort.Slice(out, func(i, j int) bool {
		if out[i].Magnitude != out[j].Magnitude {
			return out[i].Magnitude > out[j].Magnitude
		}
		if out[i].PeerLabel != out[j].PeerLabel {
			return out[i].PeerLabel < out[j].PeerLabel
		}
		if out[i].ItemLabel != out[j].ItemLabel {
			return out[i].ItemLabel < out[j].ItemLabel
		}
		if out[i].peerID != out[j].peerID {
			return out[i].peerID < out[j].peerID
		}
		return out[i].itemKind < out[j].itemKind
	})
	return out
}

// hasPendingOfferTo reports whether `buyer` already has a still-pending
// pay-ledger offer to `seller` for `kind`, regardless of disposition or terms.
// Deliberately BROADER than the duplicate gate's (buyer, seller, item,
// disposition) key: any live offer for the same goods to the same peer means
// "wait", whatever the disposition — the cue's job is to surface NEW options,
// not re-urge an in-flight one. Expired-but-unswept entries don't suppress
// (mirrors the gate's expiry skip). ZBBS-HOME-424.
func hasPendingOfferTo(snap *sim.Snapshot, buyer, seller sim.ActorID, kind sim.ItemKind) bool {
	now := snap.PublishedAt
	for _, e := range snap.PayLedger {
		if e == nil || e.State != sim.PayLedgerStatePending {
			continue
		}
		if e.BuyerID != buyer || e.SellerID != seller || e.ItemKind != kind {
			continue
		}
		if !e.ExpiresAt.IsZero() && !now.Before(e.ExpiresAt) {
			continue
		}
		return true
	}
	return false
}

// satiationVerb is the consume verb for the need's dominant modality.
func satiationVerb(need sim.NeedKey) string {
	if need == "thirst" {
		return "drink"
	}
	return "eat"
}

// renderSatiation writes the "## What you can eat or drink" section. Content-
// gated: a nil/empty view writes nothing. Numeric (~N) magnitudes, matching
// the recovery-options style.
func renderSatiation(b *strings.Builder, v *SatiationView) {
	if v == nil || len(v.Needs) == 0 {
		return
	}
	b.WriteString("## What you can eat or drink\n")
	for _, n := range v.Needs {
		if len(n.OwnStock) > 0 {
			fmt.Fprintf(b, "You have %s on hand — consume to %s.\n", renderOwnStockLine(n.OwnStock, n.Need, n.Level), n.Verb)
		}
		// Co-present peers come BEFORE the walk-to-vendor list: a peer standing
		// with you is immediately actionable (pay_with_item resolves them as the
		// seller now), so it shouldn't be buried under shops you'd have to walk
		// to. NO structure_id on these lines — they're already here, and a
		// structure_id would only tempt a needless move_to (ZBBS-HOME-342).
		for _, pr := range n.CoPresentPeers {
			fmt.Fprintf(b, "%s is here with you, carrying %s (%s) — you could offer to buy it from them now with pay_with_item, paying with coins or goods you carry (pay_items). No need to walk anywhere.\n",
				sanitizeInline(pr.PeerLabel), sanitizeInline(pr.ItemLabel), feltAmountWithSufficiency(pr.Magnitude, n.Need, n.Level))
		}
		// Free public sources come BEFORE the walk-to-vendor list: water at a
		// well costs nothing, so it shouldn't read as second to a shop you'd pay
		// at. The object id rides the structure_id field — move_to resolves a
		// bare refresh source by id (or name) to an object visit (ZBBS-HOME-359).
		if len(n.FreeSources) > 0 {
			fmt.Fprintf(b, "Free to %s nearby:\n", n.Verb)
			for _, fs := range n.FreeSources {
				b.WriteString("- ")
				b.WriteString(sanitizeInline(fs.Label))
				if fs.Magnitude > 0 {
					fmt.Fprintf(b, " — %s", itemFeltAmount(fs.Magnitude, n.Need))
				}
				b.WriteString(", free")
				if fs.Distance != "" {
					fmt.Fprintf(b, ", %s", fs.Distance)
					if fs.Direction != "" {
						fmt.Fprintf(b, " %s", fs.Direction)
					}
				}
				if fs.ObjectID != "" {
					fmt.Fprintf(b, " (structure_id: %s)", fs.ObjectID)
				}
				b.WriteString("\n")
			}
		}
		if len(n.Vendors) > 0 {
			fmt.Fprintf(b, "Nearby to buy (%s):\n", string(n.Need))
			for _, vd := range n.Vendors {
				b.WriteString("- ")
				b.WriteString(sanitizeInline(vd.StructureLabel))
				fmt.Fprintf(b, " — buy %s", sanitizeInline(vd.ItemLabel))
				if vd.Magnitude > 0 {
					fmt.Fprintf(b, " (%s)", feltAmountWithSufficiency(vd.Magnitude, n.Need, n.Level))
				}
				if vd.CostText != "" {
					fmt.Fprintf(b, ", %s", vd.CostText)
				}
				// Eat-here disposition fact (ZBBS-WORK-405): this class of
				// goods can't be carried away, so the buyer should plan a
				// sit-down, not a carry-out the clamp would quietly rewrite.
				if vd.EatHere {
					b.WriteString(", to eat there (it can't be carried away)")
				}
				// The structure_id is the load-bearing field: move_to(structure_id)
				// is how the buyer actually walks here, and the tool rejects a bare
				// name. Same id-in-perception contract restock + shift_duty use.
				if vd.StructureID != "" {
					fmt.Fprintf(b, " (structure_id: %s)", vd.StructureID)
				}
				// Live "closed now" (keeper asleep) takes precedence over the
				// stale experiential Shut memory — a present-tense read beats a
				// decaying recollection when both point at the same shop.
				if vd.ClosedNow {
					b.WriteString(closedNowAnnotation)
				} else if vd.Shut {
					b.WriteString(closedBusinessAnnotation)
				}
				if vd.OutOfStock {
					b.WriteString(outOfStockAnnotation)
				}
				b.WriteString("\n")
			}
		}
	}
	b.WriteString("\n")
}
