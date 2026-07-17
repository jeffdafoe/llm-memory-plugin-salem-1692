package perception

import (
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// bake.go — LLM-454. The evening bake affordance: the cue + tool-gate signal for a
// resident who could bake the household's bread at home this evening. The presence
// of BakeChoiceView is the SINGLE gate both the bake tool (handlers/tool_gating.go)
// and the cue (renderBakeChoice) read, so they can't drift (tool-cue lockstep,
// discussion-109). It fills the evening-at-home gap the EveningLeisure cue leaves
// once the actor is settled home — the thing the homebodies keep narrating, made a
// real, invokable task.

// bakeMinWindowMinutes mirrors sim.MinBakeWindow (30 min) — the least evening that
// must remain before bedtime for a bake to be worth offering.
const bakeMinWindowMinutes = 30

// BakeChoiceView is the evening bake affordance for a resident at home. nil when
// baking isn't on the table (not home, not the evening, busy, a pressing need, or
// neither the flour to start nor a household bake to join).
type BakeChoiceView struct {
	// Joining is true when a household bake is already in progress here — the actor
	// lends a hand at the same batch and needs no flour of its own; false when it
	// would START the batch (and provides the flour).
	Joining bool
}

// buildBakeChoice returns the evening bake affordance for a resident at home, or nil.
// Conditions (the OffersBake gate, mirrored by the sim-side StartOrJoinBake checks):
// an awake agent NPC, inside its own home (a home is implicitly a kitchen — no hearth
// tag, LLM-454), in the post-work evening window with enough of it left before
// bedtime, nothing pressing, not already busy, and either holding the flour to start
// OR a household bake already going here to join.
func buildBakeChoice(snap *sim.Snapshot, a *sim.ActorSnapshot) *BakeChoiceView {
	if snap == nil || a == nil || snap.LocalMinuteOfDay == nil {
		return nil
	}
	if a.Kind != sim.KindNPCStateful && a.Kind != sim.KindNPCShared {
		return nil
	}
	if a.State == sim.StateSleeping {
		return nil
	}
	home := a.HomeStructureID
	if home == "" || a.InsideStructureID != home {
		return nil // must be settled inside its own home
	}
	if _, ok := resolveStructureLabel(snap, home); !ok {
		return nil // home must resolve in the snapshot
	}
	// The post-work evening window [shift-end, bedtime); off-shift by construction
	// (worker-gated exactly like the EveningLeisure cue — the looping homebodies are
	// unscheduled labor vendors, so they qualify).
	if !inEveningWindow(snap, a) {
		return nil
	}
	// Enough of the evening left to bother.
	if *snap.LocalMinuteOfDay >= snap.LodgingBedtimeMinute-bakeMinWindowMinutes {
		return nil
	}
	// A pressing (red) need or an in-flight activity outranks an idle evening's bake.
	if hasRedNeed(a, snap) || actorMidSourceActivity(a) {
		return nil
	}
	joining := snap.HomeBakesActive[home]
	if !joining && a.Inventory[sim.BakeFlourItem] < sim.BakeFlourCost {
		return nil // nothing going to join, and no flour to start
	}
	return &BakeChoiceView{Joining: joining}
}

// renderBakeChoice writes the evening bake affordance as a scene (LLM-454): the same
// signal that gates the bake tool, so cue and tool surface together. It names the
// bake tool the way the forge cue names produce, and reads as a warm evening moment
// rather than a stat line (scenes, not stats).
func renderBakeChoice(b *strings.Builder, v *BakeChoiceView) {
	if v == nil {
		return
	}
	if v.Joining {
		b.WriteString("The bread is already going in the kitchen — you could lend a hand and see it through the evening. It is an evening's work, and the loaves will be ready by the time you turn in. Call bake to join in.\n\n")
		return
	}
	b.WriteString("The evening is your own and the household is about. There is flour in the crock — you could get the bread on for the week, an evening's work at the hearth that leaves fresh loaves by the time you turn in. Call bake to start.\n\n")
}
