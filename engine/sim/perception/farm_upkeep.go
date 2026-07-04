package perception

import (
	"fmt"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// farm_upkeep.go — LLM-215 owner audience. The "## Farm upkeep" section: a standing
// reminder to a farm owner that the season has worn out their tools and they owe
// fresh shovels, bought from the smith (the coin conduit farm→smith, LLM-83).
// Stock-based, so it derives the obligation live from the owner's coins
// (sim.FarmUpkeepObligation) — no per-object accumulator — and shows only the
// shortfall over what they already carry. A nil view renders nothing (the common
// case: not a farm owner, or nothing owed). Unlike the at-the-stall repair cue this
// is NOT co-location-gated — the owner buys elsewhere (at the blacksmith), so it
// rides any tick. This standing cue is the primary driver; it re-surfaces on the
// owner's normal ticks after the once-a-day wake warrant (renderWarrantLine) is
// consumed.

// FarmUpkeepView is the farm owner's upkeep-buy cue. Non-nil only when the actor
// owns a farm AND owes more upkeep shovels than they currently carry.
type FarmUpkeepView struct {
	ShovelsOwed   int             // obligation derived from coins held above the floor
	ShovelsHeld   int             // shovels the owner currently carries
	ShovelsShort  int             // ShovelsOwed - ShovelsHeld (> 0 whenever the cue shows)
	ShovelVendors []RestockVendor // where to buy the shovels (LLM-274); the move_to destination(s)
}

// buildFarmUpkeep returns the owner's upkeep-buy cue, or nil. Pure over the snapshot.
// Gated on: the actor owns a farm, and their coin balance implies more upkeep shovels
// than they hold (a non-positive FarmUpkeepCoinsPerShovel makes the obligation 0, so
// the feature's off-switch also silences the cue).
func buildFarmUpkeep(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *FarmUpkeepView {
	if snap == nil || actorSnap == nil {
		return nil
	}
	if sim.OwnedFarm(snap.VillageObjects, actorID) == nil {
		return nil
	}
	owed := sim.FarmUpkeepObligation(actorSnap.Coins, snap.FarmUpkeepFloor, snap.FarmUpkeepCoinsPerShovel)
	held := actorSnap.Inventory[sim.ShovelItemKind]
	// DYNAMIC target, not a fixed daily debt: `owed` is re-derived from CURRENT coins
	// every build, so as the owner buys shovels (coins fall, held rises) the shortfall
	// shrinks and clears at a fixed point (held == owed) — self-limiting, so a farm
	// whose balance drops mid-cycle is never over-taxed. This is the greenlit stock-
	// based, no-accumulator design; the coin actually collected is <= the boundary
	// assessment, by intent.
	if owed <= held {
		return nil // nothing owed beyond what they already carry
	}
	return &FarmUpkeepView{
		ShovelsOwed:  owed,
		ShovelsHeld:  held,
		ShovelsShort: owed - held,
		// LLM-274: resolve the shovel supplier(s) so the cue names a move_to destination
		// instead of the dead-end "the blacksmith". Same restock-directory path the
		// stall-repair nail cue uses — findItemVendors names only first-hand producers
		// (the smith produces shovels, LLM-200), drops remembered-shut/unaffordable, and
		// dedupes by workplace. Empty → render keeps the generic "from the blacksmith".
		ShovelVendors: findItemVendors(snap, actorID, actorSnap, sim.ShovelItemKind),
	}
}

// renderFarmUpkeep writes the "## Farm upkeep" section. Content-gated: a nil view
// writes nothing. Symmetrical awareness — states the worn-tools problem AND the way
// out (buy shovels from the blacksmith) in one place, and names the shortfall so the
// owner knows how many to buy.
func renderFarmUpkeep(b *strings.Builder, v *FarmUpkeepView) {
	if v == nil {
		return
	}
	b.WriteString("## Farm upkeep\n")
	b.WriteString("The season's work has worn your farm tools. ")
	if v.ShovelsHeld > 0 {
		fmt.Fprintf(b, "Upkeep calls for %d shovels and you carry %d. ", v.ShovelsOwed, v.ShovelsHeld)
	}
	shovels := "a fresh shovel"
	if v.ShovelsShort != 1 {
		shovels = fmt.Sprintf("%d fresh shovels", v.ShovelsShort)
	}
	if len(v.ShovelVendors) > 0 {
		// LLM-274: name the actual shovel supplier(s) — workplace + structure_id — in the
		// model-proven format so the weak model walks the errand. No "come back" hop: the
		// daily assessment consumes the held shovels, so buying them IS the whole action
		// (unlike stall repair, which mends on site).
		fmt.Fprintf(b, "Buy %s to set the farm right for the season. Use move_to to reach a supplier, then pay_with_item once you arrive:\n", shovels)
		renderWalkToVendors(b, v.ShovelVendors)
	} else {
		// No shovel supplier resolves (none produce them, or all shut/unaffordable) —
		// keep the generic sentence rather than a dead-end target (LLM-216 posture).
		fmt.Fprintf(b, "Buy %s from the blacksmith to set the farm right for the season.\n", shovels)
	}
}
