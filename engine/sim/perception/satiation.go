package perception

import (
	"fmt"
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
	Need     sim.NeedKey     // "hunger" | "thirst"
	Verb     string          // "eat" | "drink"
	OwnStock []OwnStockItem  // satisfiers the actor already carries (shared shape)
	Vendors  []SatiationVendor // nearby places selling a satisfier
}

// SatiationVendor is one (workplace, item) buy opportunity.
type SatiationVendor struct {
	StructureLabel string // "PW Apothecary" — where the buyer walks to
	ItemLabel      string // "stew"
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
		vendors := gatherSatiationVendors(snap, actorID, need)
		if len(own) == 0 && len(vendors) == 0 {
			continue
		}
		needs = append(needs, SatiationNeedView{
			Need:     need,
			Verb:     satiationVerb(need),
			OwnStock: own,
			Vendors:  vendors,
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
		out = append(out, SatiationVendor{
			StructureLabel: vc.StructureLabel,
			ItemLabel:      vc.ItemLabel,
			Magnitude:      vc.Magnitude,
			CostText:       vc.CostText,
		})
	}
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
			fmt.Fprintf(b, "You have %s on hand — consume to %s.\n", renderOwnStockLine(n.OwnStock), n.Verb)
		}
		if len(n.Vendors) > 0 {
			fmt.Fprintf(b, "Nearby to buy (%s):\n", string(n.Need))
			for _, vd := range n.Vendors {
				b.WriteString("- ")
				b.WriteString(sanitizeInline(vd.StructureLabel))
				fmt.Fprintf(b, " — buy %s", sanitizeInline(vd.ItemLabel))
				if vd.Magnitude > 0 {
					fmt.Fprintf(b, ", eases %s (~%d)", string(n.Need), vd.Magnitude)
				}
				if vd.CostText != "" {
					fmt.Fprintf(b, ", %s", vd.CostText)
				}
				b.WriteString("\n")
			}
		}
	}
	b.WriteString("\n")
}
