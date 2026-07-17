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
