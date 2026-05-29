package perception

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// satiation.go — ZBBS-HOME-304. The "## What you can eat or drink" perception
// section: surfaces, to a hungry or thirsty NPC, how to resolve the pressing
// need — the satisfying items it ALREADY CARRIES (consume-first) and nearby
// vendors selling a satisfier. Port of v1's engine/satiation.go, adapted to v2:
// vendor cues run through the shared findVendorConsumables finder (see
// consumable_vendors.go), the same structural-vendorship + workplace surface
// the recovery-options remedy arm uses.
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

// SatiationView is the content-gated "## What you can eat or drink" section.
// A nil view (or empty Needs) means render omits the section.
type SatiationView struct {
	Needs []SatiationNeedView
}

// SatiationNeedView is one pressing consumable need's resolution surface.
type SatiationNeedView struct {
	Need sim.NeedKey // "hunger" | "thirst"
	Verb string      // "eat" | "drink"

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

	Vendors []SatiationVendor // nearby places selling a satisfier (walk to)
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
		// back to the registry default when unset).
		if actorSnap.Needs[need] < snap.NeedThresholds.Get(need) {
			continue
		}
		own := gatherOwnStock(snap, actorSnap, need)
		peers := gatherCoPresentPeerOffers(snap, actorID, actorSnap, need)
		vendors := gatherSatiationVendors(snap, actorID, need)
		if len(own) == 0 && len(peers) == 0 && len(vendors) == 0 {
			continue
		}
		needs = append(needs, SatiationNeedView{
			Need:           need,
			Verb:           satiationVerb(need),
			OwnStock:       own,
			CoPresentPeers: peers,
			Vendors:        vendors,
		})
	}
	if len(needs) == 0 {
		return nil
	}
	return &SatiationView{Needs: needs}
}

// gatherSatiationVendors maps the shared vendor-consumable finder into the
// satiation bullet shape. findVendorConsumables already returns a deterministic
// order, so no re-sort here.
func gatherSatiationVendors(snap *sim.Snapshot, actorID sim.ActorID, need sim.NeedKey) []SatiationVendor {
	var out []SatiationVendor
	for _, vc := range findVendorConsumables(snap, actorID, need, "ask the seller") {
		if vc.StructureID == "" {
			// No resolvable workplace → no structure_id for move_to, so the cue
			// is unactionable and would only tempt a name-based move_to the tool
			// rejects. findVendorConsumables already excludes vendors whose
			// workplace doesn't resolve, so this is a defensive guard, not a
			// live path.
			continue
		}
		out = append(out, SatiationVendor{
			StructureLabel: vc.StructureLabel,
			StructureID:    vc.StructureID,
			ItemLabel:      vc.ItemLabel,
			Magnitude:      vc.Magnitude,
			CostText:       vc.CostText,
		})
	}
	return out
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
// the subject's huddle. Output is sorted deterministically by (peerID, magnitude
// desc, ItemLabel, ItemKind), mirroring the tie-break discipline the vendor /
// own-stock finders use (huddle membership is a map). Empty when not huddled or
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
			out = append(out, SatiationPeerOffer{
				PeerLabel: label,
				ItemLabel: itemDisplayLabel(snap, kind),
				Magnitude: mag,
				peerID:    peerID,
				itemKind:  kind,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].peerID != out[j].peerID {
			return out[i].peerID < out[j].peerID
		}
		if out[i].Magnitude != out[j].Magnitude {
			return out[i].Magnitude > out[j].Magnitude
		}
		if out[i].ItemLabel != out[j].ItemLabel {
			return out[i].ItemLabel < out[j].ItemLabel
		}
		return out[i].itemKind < out[j].itemKind
	})
	return out
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
			fmt.Fprintf(b, "You have %s on hand — consume to %s.\n", renderOwnStockLine(n.OwnStock, n.Need), n.Verb)
		}
		// Co-present peers come BEFORE the walk-to-vendor list: a peer standing
		// with you is immediately actionable (pay_with_item resolves them as the
		// seller now), so it shouldn't be buried under shops you'd have to walk
		// to. NO structure_id on these lines — they're already here, and a
		// structure_id would only tempt a needless move_to (ZBBS-HOME-342).
		for _, pr := range n.CoPresentPeers {
			fmt.Fprintf(b, "%s is here with you, carrying %s (%s) — you could offer to buy it from them now with pay_with_item. No need to walk anywhere.\n",
				sanitizeInline(pr.PeerLabel), sanitizeInline(pr.ItemLabel), itemFeltAmount(pr.Magnitude, n.Need))
		}
		if len(n.Vendors) > 0 {
			fmt.Fprintf(b, "Nearby to buy (%s):\n", string(n.Need))
			for _, vd := range n.Vendors {
				b.WriteString("- ")
				b.WriteString(sanitizeInline(vd.StructureLabel))
				fmt.Fprintf(b, " — buy %s", sanitizeInline(vd.ItemLabel))
				if vd.Magnitude > 0 {
					fmt.Fprintf(b, " (%s)", itemFeltAmount(vd.Magnitude, n.Need))
				}
				if vd.CostText != "" {
					fmt.Fprintf(b, ", %s", vd.CostText)
				}
				// The structure_id is the load-bearing field: move_to(structure_id)
				// is how the buyer actually walks here, and the tool rejects a bare
				// name. Same id-in-perception contract restock + shift_duty use.
				if vd.StructureID != "" {
					fmt.Fprintf(b, " (structure_id: %s)", vd.StructureID)
				}
				b.WriteString("\n")
			}
		}
	}
	b.WriteString("\n")
}
