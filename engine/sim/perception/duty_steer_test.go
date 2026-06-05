package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func dutyMinPtr(n int) *int { return &n }

// dutySnap builds a minimal snapshot carrying the village clock + dawn/dusk
// window the duty-steer cue reads.
func dutySnap(nowMin, dawn, dusk int) *sim.Snapshot {
	m := nowMin
	return &sim.Snapshot{LocalMinuteOfDay: &m, DawnMinute: dawn, DuskMinute: dusk, DawnDuskMinuteOK: true}
}

var dutyAnchors = &AnchorsView{
	WorkID: "tavern", WorkLabel: "the Tavern",
	HomeID: "cottage", HomeLabel: "Ellis Cottage",
}

// dutySteer wraps buildDutySteer with the no-suppression defaults (no actor id,
// no restock errand) for the pre-Option-B decision-table tests; the ZBBS-HOME-400
// suppression signals get their own dedicated test below.
func dutySteer(snap *sim.Snapshot, a *sim.ActorSnapshot, anchors *AnchorsView) *DutySteerView {
	return buildDutySteer(snap, "", a, anchors, false)
}

// TestMinuteInWindow covers the half-open window check incl. wrap-midnight and
// the empty (start==end) window. ZBBS-HOME-352.
func TestMinuteInWindow(t *testing.T) {
	cases := []struct {
		start, end, now int
		want            bool
	}{
		{420, 1140, 420, true},   // start inclusive
		{420, 1140, 1139, true},  // before end
		{420, 1140, 1140, false}, // end exclusive
		{420, 1140, 419, false},  // before start
		{960, 180, 1000, true},   // wrap: evening on-shift
		{960, 180, 60, true},     // wrap: after midnight, still on
		{960, 180, 960, true},    // wrap: at start
		{960, 180, 180, false},   // wrap: end exclusive
		{960, 180, 500, false},   // wrap: midday off
		{600, 600, 600, false},   // empty window is never on
	}
	for _, c := range cases {
		if got := minuteInWindow(c.start, c.end, c.now); got != c.want {
			t.Errorf("minuteInWindow(%d,%d,%d) = %v, want %v", c.start, c.end, c.now, got, c.want)
		}
	}
}

// TestBuildDutySteer exercises the cue's decision table. ZBBS-HOME-352.
func TestBuildDutySteer(t *testing.T) {
	agentSched := func(inside sim.StructureID) *sim.ActorSnapshot {
		return &sim.ActorSnapshot{
			Kind:              sim.KindNPCStateful,
			ScheduleStartMin:  dutyMinPtr(960), // 16:00
			ScheduleEndMin:    dutyMinPtr(180), // 03:00 (wrap)
			InsideStructureID: inside,
		}
	}

	t.Run("on shift, away from work -> toWork", func(t *testing.T) {
		v := dutySteer(dutySnap(1100, 420, 1140), agentSched("general_store"), dutyAnchors)
		if v == nil || !v.ToWork || v.TargetID != "tavern" || v.TargetLabel != "the Tavern" {
			t.Fatalf("want toWork=tavern, got %+v", v)
		}
	})
	t.Run("on shift, at work -> nil", func(t *testing.T) {
		if v := dutySteer(dutySnap(1100, 420, 1140), agentSched("tavern"), dutyAnchors); v != nil {
			t.Fatalf("want nil (at post), got %+v", v)
		}
	})
	t.Run("off shift, away from home -> home", func(t *testing.T) {
		v := dutySteer(dutySnap(600, 420, 1140), agentSched("tavern"), dutyAnchors)
		if v == nil || v.ToWork || v.TargetID != "cottage" {
			t.Fatalf("want home=cottage, got %+v", v)
		}
	})
	t.Run("off shift, at home -> nil", func(t *testing.T) {
		if v := dutySteer(dutySnap(600, 420, 1140), agentSched("cottage"), dutyAnchors); v != nil {
			t.Fatalf("want nil (at home), got %+v", v)
		}
	})
	t.Run("unscheduled NPC uses dawn/dusk window", func(t *testing.T) {
		a := &sim.ActorSnapshot{Kind: sim.KindNPCShared, InsideStructureID: "general_store"}
		v := dutySteer(dutySnap(600, 420, 1140), a, dutyAnchors) // 10:00 in [07:00,19:00)
		if v == nil || !v.ToWork {
			t.Fatalf("want toWork via dawn/dusk fallback, got %+v", v)
		}
	})
	t.Run("unscheduled + unknown window -> nil", func(t *testing.T) {
		a := &sim.ActorSnapshot{Kind: sim.KindNPCShared, InsideStructureID: "general_store"}
		if v := dutySteer(dutySnap(600, 0, 0), a, dutyAnchors); v != nil {
			t.Fatalf("want nil (no schedule, no window), got %+v", v)
		}
	})
	t.Run("degenerate dawn==dusk window -> nil", func(t *testing.T) {
		a := &sim.ActorSnapshot{Kind: sim.KindNPCShared, InsideStructureID: "general_store"}
		if v := dutySteer(dutySnap(600, 720, 720), a, dutyAnchors); v != nil {
			t.Fatalf("want nil (empty dawn==dusk window), got %+v", v)
		}
	})
	t.Run("partial dawn/dusk parse (OK=false) -> nil", func(t *testing.T) {
		a := &sim.ActorSnapshot{Kind: sim.KindNPCShared, InsideStructureID: "general_store"}
		m := 600
		// dawn parsed, dusk failed → OK=false → must not derive a bogus window.
		snap := &sim.Snapshot{LocalMinuteOfDay: &m, DawnMinute: 420, DuskMinute: 0, DawnDuskMinuteOK: false}
		if v := dutySteer(snap, a, dutyAnchors); v != nil {
			t.Fatalf("want nil (partial parse, OK=false), got %+v", v)
		}
	})
	t.Run("partial schedule (end nil) falls back to dawn/dusk", func(t *testing.T) {
		// Only ScheduleStartMin set → not "both bounds" → uses the dawn/dusk
		// window [07:00,19:00); now=10:00 is on-shift → toWork.
		a := &sim.ActorSnapshot{Kind: sim.KindNPCStateful, ScheduleStartMin: dutyMinPtr(960), InsideStructureID: "general_store"}
		if v := dutySteer(dutySnap(600, 420, 1140), a, dutyAnchors); v == nil || !v.ToWork {
			t.Fatalf("want toWork via dawn/dusk fallback (partial schedule), got %+v", v)
		}
	})
	t.Run("PC -> nil", func(t *testing.T) {
		a := agentSched("general_store")
		a.Kind = sim.KindPC
		if v := dutySteer(dutySnap(1100, 420, 1140), a, dutyAnchors); v != nil {
			t.Fatalf("want nil (PC out of scope), got %+v", v)
		}
	})
	t.Run("nil clock -> nil", func(t *testing.T) {
		snap := &sim.Snapshot{DawnMinute: 420, DuskMinute: 1140} // LocalMinuteOfDay nil
		if v := dutySteer(snap, agentSched("general_store"), dutyAnchors); v != nil {
			t.Fatalf("want nil (clock unknown), got %+v", v)
		}
	})
	t.Run("nil anchors -> nil", func(t *testing.T) {
		if v := dutySteer(dutySnap(1100, 420, 1140), agentSched("general_store"), nil); v != nil {
			t.Fatalf("want nil (no anchors), got %+v", v)
		}
	})
}

// TestBuildDutySteer_OptionBSuppression — ZBBS-HOME-400. The to-work cue is
// suppressed while the agent is mid-business — a pressing (mild-or-worse) need,
// an active restock errand, or a pending outgoing offer — matching the shift-duty
// warrant. The go-home arm is never suppressed by these signals.
func TestBuildDutySteer_OptionBSuppression(t *testing.T) {
	// On-shift (now 18:20 in [16:00,03:00)), away from work — the baseline that
	// fires the to-work cue when no suppressor is present.
	onShiftAway := func() (*sim.Snapshot, *sim.ActorSnapshot) {
		return dutySnap(1100, 420, 1140), &sim.ActorSnapshot{
			Kind:              sim.KindNPCStateful,
			ScheduleStartMin:  dutyMinPtr(960),
			ScheduleEndMin:    dutyMinPtr(180),
			InsideStructureID: "general_store",
		}
	}

	t.Run("baseline (no suppressor) -> toWork", func(t *testing.T) {
		snap, a := onShiftAway()
		if v := buildDutySteer(snap, "moses", a, dutyAnchors, false); v == nil || !v.ToWork {
			t.Fatalf("want toWork with no suppressor, got %+v", v)
		}
	})
	t.Run("active restock errand suppresses toWork", func(t *testing.T) {
		snap, a := onShiftAway()
		if v := buildDutySteer(snap, "moses", a, dutyAnchors, true); v != nil {
			t.Fatalf("want nil (restock errand suppresses to-work), got %+v", v)
		}
	})
	t.Run("mild (sub-red) need suppresses toWork", func(t *testing.T) {
		snap, a := onShiftAway()
		// hunger 10 with the default red threshold (20) is MILD ([8,20)), so it
		// is NOT caught by the existing red-need whole-steer suppressor — this
		// pins the new mild-or-worse to-work gate specifically.
		a.Needs = map[sim.NeedKey]int{"hunger": 10}
		if v := buildDutySteer(snap, "moses", a, dutyAnchors, false); v != nil {
			t.Fatalf("want nil (mild need suppresses to-work), got %+v", v)
		}
	})
	t.Run("pending outgoing offer suppresses toWork", func(t *testing.T) {
		snap, a := onShiftAway()
		snap.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{
			1: {BuyerID: "moses", State: sim.PayLedgerStatePending},
		}
		if v := buildDutySteer(snap, "moses", a, dutyAnchors, false); v != nil {
			t.Fatalf("want nil (pending offer suppresses to-work), got %+v", v)
		}
	})
	t.Run("a pending offer by SOMEONE ELSE does not suppress", func(t *testing.T) {
		snap, a := onShiftAway()
		snap.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{
			1: {BuyerID: "hannah", State: sim.PayLedgerStatePending},
		}
		if v := buildDutySteer(snap, "moses", a, dutyAnchors, false); v == nil || !v.ToWork {
			t.Fatalf("want toWork (another actor's offer is irrelevant), got %+v", v)
		}
	})
	t.Run("go-home arm is NOT suppressed by these signals", func(t *testing.T) {
		// Off-shift (now 10:00, outside [16:00,03:00)), away from home, WITH a mild
		// need + restock errand + own pending offer → still steers home.
		snap := dutySnap(600, 420, 1140)
		snap.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{
			1: {BuyerID: "moses", State: sim.PayLedgerStatePending},
		}
		a := &sim.ActorSnapshot{
			Kind:              sim.KindNPCStateful,
			ScheduleStartMin:  dutyMinPtr(960),
			ScheduleEndMin:    dutyMinPtr(180),
			InsideStructureID: "tavern",
			Needs:             map[sim.NeedKey]int{"hunger": 10},
		}
		if v := buildDutySteer(snap, "moses", a, dutyAnchors, true); v == nil || v.ToWork || v.TargetID != "cottage" {
			t.Fatalf("want home=cottage (go-home arm not suppressed), got %+v", v)
		}
	})
}

// TestRenderDutySteer covers the prose for both directions, the label fallback,
// and the nil omission. ZBBS-HOME-352.
func TestRenderDutySteer(t *testing.T) {
	render := func(v *DutySteerView) string {
		var b strings.Builder
		renderDutySteer(&b, v)
		return b.String()
	}

	if got := render(nil); got != "" {
		t.Errorf("nil should render nothing, got %q", got)
	}

	work := render(&DutySteerView{ToWork: true, TargetID: "tavern", TargetLabel: "the Tavern"})
	if !strings.Contains(work, "working hours") || !strings.Contains(work, "the Tavern") || !strings.Contains(work, "structure_id: tavern") {
		t.Errorf("toWork prose missing pieces, got %q", work)
	}

	home := render(&DutySteerView{ToWork: false, TargetID: "cottage", TargetLabel: "Ellis Cottage"})
	if !strings.Contains(home, "head home to Ellis Cottage") || !strings.Contains(home, "structure_id: cottage") {
		t.Errorf("home prose missing pieces, got %q", home)
	}

	homeNoLabel := render(&DutySteerView{ToWork: false, TargetID: "cottage"})
	if !strings.Contains(homeNoLabel, "head home (structure_id: cottage)") {
		t.Errorf("home no-label fallback missing id, got %q", homeNoLabel)
	}
}

// TestRender_DutySteerCarriesStructureID is the load-bearing contract test: the
// full rendered prompt must carry the duty target's structure_id so the model
// can act via move_to without depending on another section being present
// (code_review). ZBBS-HOME-352.
func TestRender_DutySteerCarriesStructureID(t *testing.T) {
	p := Payload{
		ActorID:   "moses",
		DutySteer: &DutySteerView{ToWork: true, TargetID: "tavern", TargetLabel: "the Tavern"},
	}
	out := Render(p, DefaultRenderConfig())
	if !strings.Contains(combinedPrompt(out), "structure_id: tavern") {
		t.Errorf("rendered prompt must carry the duty target structure_id, got:\n%s", combinedPrompt(out))
	}
}
