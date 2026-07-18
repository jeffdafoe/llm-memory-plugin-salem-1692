package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// bake_gate_test.go — LLM-454. The daytime bake affordance gate (buildBakeChoice) and
// its cue (renderBakeChoice). Reuses the evening_leisure_test dawn/dusk snapshot (07:00
// dawn / 19:00 dusk) with a homed UNSCHEDULED day-worker — the Walker-women shape — in
// the at-home daytime window (before dusk).

// homeBaker is a homed UNSCHEDULED day-worker (no schedule, carries the worker attribute)
// standing at `inside`, carrying `flour`. This is the looping-homebody shape the daytime
// bake targets — distinct from the SCHEDULED eveningWorker, which is on shift all day.
func homeBaker(inside sim.StructureID, flour int) *sim.ActorSnapshot {
	a := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		AttributeSlugs:    []string{sim.AttrWorker}, // unscheduled worker — day-active, no post obligation
		HomeStructureID:   "cottage",
		InsideStructureID: inside,
		Needs:             map[sim.NeedKey]int{},
	}
	if flour > 0 {
		a.Inventory = map[sim.ItemKind]int{sim.BakeFlourItem: flour}
	}
	return a
}

func TestBuildBakeChoice(t *testing.T) {
	const daytime = 16 * 60 // 16:00 — inside [dawn 07:00, dusk 19:00), 3h before dusk

	if v := buildBakeChoice(eveningSnap(daytime), homeBaker("cottage", 2)); v == nil || v.Joining {
		t.Errorf("at-home daytime with flour: got %+v, want START (non-nil, Joining=false)", v)
	}
	if v := buildBakeChoice(eveningSnap(daytime), homeBaker("tavern", 2)); v != nil {
		t.Errorf("away from home: got %+v, want nil", v)
	}
	if v := buildBakeChoice(eveningSnap(20*60), homeBaker("cottage", 2)); v != nil {
		t.Errorf("after dusk (20:00): got %+v, want nil (baking is a daytime task)", v)
	}
	if v := buildBakeChoice(eveningSnap(18*60+45), homeBaker("cottage", 2)); v != nil {
		t.Errorf("too close to dusk (< 30m left): got %+v, want nil", v)
	}
	if v := buildBakeChoice(eveningSnap(daytime), homeBaker("cottage", 0)); v != nil {
		t.Errorf("no flour and no bake going: got %+v, want nil", v)
	}
	// A SCHEDULED actor on its shift belongs at its post, not the hearth — even at home.
	sched := eveningWorker("cottage") // scheduled 07:00–19:00, on shift at 16:00
	sched.Inventory = map[sim.ItemKind]int{sim.BakeFlourItem: 2}
	if v := buildBakeChoice(eveningSnap(daytime), sched); v != nil {
		t.Errorf("scheduled worker on its shift: got %+v, want nil (belongs at post)", v)
	}
	// A household bake already going here → a flourless resident JOINS it.
	snap := eveningSnap(daytime)
	snap.HomeBakesActive = map[sim.StructureID]bool{"cottage": true}
	if v := buildBakeChoice(snap, homeBaker("cottage", 0)); v == nil || !v.Joining {
		t.Errorf("flourless with a bake going: got %+v, want JOIN (non-nil, Joining=true)", v)
	}
}

// TestBuildBakeChoiceRedNeedBlocksStartNotJoin is the LLM-465 case: a pressing (red)
// need bars STARTING a bake but not lending a hand at one already going. Starting is a
// whole afternoon's commitment and a starving villager should see to that first; joining
// costs no flour, mints no batch, and leaves the need fully actionable — gateTools'
// bakingMayMove keeps move_to for a red hunger/thirst, and the reactor's
// hasBreakInterruptingNeedWarrant ticks him through the shelve for it. Live 2026-07-18:
// Lewis Walker was red on hunger while Anne and Patience baked, so the pre-fix gate gave
// him no bake affordance at all, left him unshelved in his own kitchen, and he burned 24
// turns in 70 minutes asking how the loaves were coming — arming bakeReplyDue for BOTH
// bakers with every question.
func TestBuildBakeChoiceRedNeedBlocksStartNotJoin(t *testing.T) {
	const daytime = 16 * 60 // 16:00 — inside [dawn 07:00, dusk 19:00), 3h before dusk

	// Red on hunger at the default threshold (18 — the live line Lewis was over).
	hungry := func(flour int) *sim.ActorSnapshot {
		a := homeBaker("cottage", flour)
		a.Needs = map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold}
		return a
	}

	// Positive control: the SAME actor with no pressing need does get the start cue,
	// so the negative assertion below can't pass because bake broke outright.
	if v := buildBakeChoice(eveningSnap(daytime), homeBaker("cottage", 2)); v == nil || v.Joining {
		t.Fatal("unpressed resident with flour: got no START cue — the control for the red-need " +
			"assertions below is broken, so they prove nothing")
	}

	// Nothing going to join: starting is an afternoon's commitment, so the need wins.
	if v := buildBakeChoice(eveningSnap(daytime), hungry(2)); v != nil {
		t.Errorf("red-need resident with flour and no bake going: got %+v, want nil — starting a "+
			"to-dusk bake does not outrank a pressing need", v)
	}

	// A household bake already going here: the join stays open to him.
	snap := eveningSnap(daytime)
	snap.HomeBakesActive = map[sim.StructureID]bool{"cottage": true}
	if v := buildBakeChoice(snap, hungry(0)); v == nil || !v.Joining {
		t.Errorf("red-need resident with a bake going: got %+v, want JOIN (non-nil, Joining=true) — "+
			"lending a hand costs nothing and he keeps move_to for the need, so refusing him the "+
			"join protects him from nothing and leaves him loose and fully tickable (LLM-465)", v)
	}
	// Holding flour changes nothing while a batch is going — he joins it rather than
	// starting a second one, so the red-need branch is never reached.
	if v := buildBakeChoice(snap, hungry(4)); v == nil || !v.Joining {
		t.Errorf("red-need resident holding flour with a bake going: got %+v, want JOIN", v)
	}
}

func TestRenderBakeChoice(t *testing.T) {
	var b strings.Builder
	renderBakeChoice(&b, &BakeChoiceView{Joining: false})
	if !strings.Contains(b.String(), "Call bake to start") {
		t.Errorf("start cue missing the bake tool: %q", b.String())
	}
	b.Reset()
	renderBakeChoice(&b, &BakeChoiceView{Joining: true})
	if !strings.Contains(b.String(), "Call bake to join") {
		t.Errorf("join cue missing the bake tool: %q", b.String())
	}
	b.Reset()
	renderBakeChoice(&b, nil)
	if b.String() != "" {
		t.Errorf("nil view should render nothing, got %q", b.String())
	}
}
