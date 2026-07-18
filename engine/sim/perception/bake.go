package perception

import (
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// bake.go — LLM-454. The daytime bake affordance: the cue + tool-gate signal for a
// resident who could bake the household's bread at home during the day, before dusk.
// The presence of BakeChoiceView is the SINGLE gate both the bake tool
// (handlers/tool_gating.go) and the cue (renderBakeChoice) read, so they can't drift
// (tool-cue lockstep, discussion-109). It fills the home-idle daytime stretch where
// the homebodies otherwise loop "let's make bread" without doing it — the thing they
// keep narrating, made a real, invokable task.

// bakeMinWindowMinutes mirrors sim.MinBakeWindow (30 min) — the least of the day that
// must remain before dusk for a bake to be worth offering.
const bakeMinWindowMinutes = 30

// BakeChoiceView is the daytime bake affordance for a resident at home. nil when
// baking isn't on the table (not home, not daytime, on shift, busy, a pressing need,
// or neither the flour to start nor a household bake to join).
type BakeChoiceView struct {
	// Joining is true when a household bake is already in progress here — the actor
	// lends a hand at the same batch and needs no flour of its own; false when it
	// would START the batch (and provides the flour).
	Joining bool
}

// buildBakeChoice returns the daytime bake affordance for a resident at home, or nil.
// Conditions (the OffersBake gate, mirrored by the sim-side StartOrJoinBake checks):
// an awake agent NPC, inside its own home (a home is implicitly a kitchen — no hearth
// tag, LLM-454), in the at-home daytime window (before dusk, not on a scheduled shift)
// with enough of the day left before dusk, nothing pressing, not already busy, and
// either holding the flour to start OR a household bake already going here to join.
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
	// The at-home daytime window: before dusk and NOT on an explicit scheduled shift.
	// An unscheduled labor vendor's day-active window is not a binding post, so it
	// qualifies — that's the looping homebodies this fills; a scheduled keeper on its
	// shift belongs at its post and is turned away (see inDaytimeHomeWindow).
	if !inDaytimeHomeWindow(snap, a) {
		return nil
	}
	// Enough of the day left before dusk to bother.
	if *snap.LocalMinuteOfDay >= snap.DuskMinute-bakeMinWindowMinutes {
		return nil
	}
	// A pressing (red) need or an in-flight activity outranks an idle day's bake.
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
		b.WriteString("The bread is already going in the kitchen — you could lend a hand and see it through the afternoon. It is a good few hours' work, and the loaves will be ready by dusk. Call bake to join in.\n\n")
		return
	}
	b.WriteString("The house is quiet and the household is about. There is flour in the crock — you could get the bread on for the week, an afternoon's work at the hearth that leaves fresh loaves by dusk. Call bake to start.\n\n")
}
