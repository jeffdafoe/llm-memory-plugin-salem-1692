package perception

import (
	"strings"
	"testing"
	"time"

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
	t.Run("on shift, at work -> atPost stabilizer (ZBBS-WORK-431)", func(t *testing.T) {
		v := dutySteer(dutySnap(1100, 420, 1140), agentSched("tavern"), dutyAnchors)
		if v == nil || !v.AtPost || v.ToWork {
			t.Fatalf("want atPost stabilizer at post, got %+v", v)
		}
		// LLM-40: the stabilizer carries the effective close time (schedule end).
		if v.ShiftEndMin == nil || *v.ShiftEndMin != 180 {
			t.Fatalf("want ShiftEndMin=180 (03:00 schedule end), got %v", v.ShiftEndMin)
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
	t.Run("nil actor -> nil (no panic)", func(t *testing.T) {
		// Pins the guard-ordering fix: the a.Kind deref must not run before the
		// nil check (code_review, HOME-400 Option B).
		if v := dutySteer(dutySnap(1100, 420, 1140), nil, dutyAnchors); v != nil {
			t.Fatalf("want nil (nil actor), got %+v", v)
		}
	})
	t.Run("nil snapshot -> nil", func(t *testing.T) {
		if v := dutySteer(nil, agentSched("general_store"), dutyAnchors); v != nil {
			t.Fatalf("want nil (nil snapshot), got %+v", v)
		}
	})
	t.Run("nil anchors -> nil", func(t *testing.T) {
		if v := dutySteer(dutySnap(1100, 420, 1140), agentSched("general_store"), nil); v != nil {
			t.Fatalf("want nil (no anchors), got %+v", v)
		}
	})
}

// TestBuildDutyPending — ZBBS-HOME-442. The pre-suppression "to-work duty
// applies" predicate the noop-skip gate consumes. The load-bearing case is the
// divergence — the to-work STEER is nil but duty-pending stays true — which keeps
// the gate from skip-locking an off-post NPC (the Josiah HOME-441 failure). Since
// ZBBS-HOME-463 that divergence is driven by a RED need (or a restock errand /
// pending offer), not a mild need: a mild need now renders the steer, so steer and
// duty-pending agree there.
func TestBuildDutyPending(t *testing.T) {
	agentSched := func(inside sim.StructureID) *sim.ActorSnapshot {
		return &sim.ActorSnapshot{
			Kind:              sim.KindNPCStateful,
			ScheduleStartMin:  dutyMinPtr(540),  // 9:00
			ScheduleEndMin:    dutyMinPtr(1260), // 21:00
			InsideStructureID: inside,
		}
	}

	t.Run("off-post on-shift -> pending", func(t *testing.T) {
		if !buildDutyPending(dutySnap(600, 420, 1140), agentSched(""), dutyAnchors) {
			t.Fatal("want duty-pending for an off-post on-shift agent")
		}
	})
	t.Run("mild need: steer renders AND duty pending (no divergence since HOME-463)", func(t *testing.T) {
		// HOME-463 removed the Option B mild-need steer suppression, so at mild the
		// to-work steer renders — steer and duty-pending now agree (no divergence).
		snap := dutySnap(600, 420, 1140)
		snap.NeedThresholds = sim.DefaultNeedThresholds()
		a := agentSched("")
		a.Needs = map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold - 4} // mild band
		if v := buildDutySteer(snap, "", a, dutyAnchors, false); v == nil || !v.ToWork {
			t.Fatalf("a mild need must NOT nil the to-work steer since HOME-463, got %+v", v)
		}
		if !buildDutyPending(snap, a, dutyAnchors) {
			t.Fatal("want duty-pending for an off-post on-shift agent")
		}
	})
	t.Run("red need: steer suppressed, duty still pending", func(t *testing.T) {
		// The red-need gate (HOME-362) is deliberately NOT mirrored here —
		// duty-pending is a pure "duty applies" predicate. Harmless to the
		// gate: a red need already opens it via the needs condition.
		snap := dutySnap(600, 420, 1140)
		snap.NeedThresholds = sim.DefaultNeedThresholds()
		a := agentSched("")
		a.Needs = map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold + 2}
		if v := buildDutySteer(snap, "", a, dutyAnchors, false); v != nil {
			t.Fatalf("precondition: red need must nil the steer, got %+v", v)
		}
		if !buildDutyPending(snap, a, dutyAnchors) {
			t.Fatal("want duty-pending at red (pure predicate)")
		}
	})
	t.Run("at post -> not pending", func(t *testing.T) {
		if buildDutyPending(dutySnap(600, 420, 1140), agentSched("tavern"), dutyAnchors) {
			t.Fatal("want no duty-pending at post")
		}
	})
	t.Run("off shift -> not pending", func(t *testing.T) {
		if buildDutyPending(dutySnap(300, 420, 1140), agentSched(""), dutyAnchors) {
			t.Fatal("want no duty-pending off shift (5:00 before a 9:00 start)")
		}
	})
	t.Run("unscheduled NPC uses dawn/dusk window", func(t *testing.T) {
		a := &sim.ActorSnapshot{Kind: sim.KindNPCShared, InsideStructureID: ""}
		if !buildDutyPending(dutySnap(600, 420, 1140), a, dutyAnchors) {
			t.Fatal("want duty-pending via dawn/dusk fallback")
		}
	})
	t.Run("unscheduled + unknown window -> not pending", func(t *testing.T) {
		a := &sim.ActorSnapshot{Kind: sim.KindNPCShared, InsideStructureID: ""}
		if buildDutyPending(dutySnap(600, 0, 0), a, dutyAnchors) {
			t.Fatal("want no duty-pending without a usable window")
		}
	})
	t.Run("no work anchor -> not pending", func(t *testing.T) {
		anchors := &AnchorsView{HomeID: "cottage", HomeLabel: "Ellis Cottage"}
		if buildDutyPending(dutySnap(600, 420, 1140), agentSched(""), anchors) {
			t.Fatal("want no duty-pending without a work anchor")
		}
	})
	t.Run("PC -> not pending", func(t *testing.T) {
		a := agentSched("")
		a.Kind = sim.KindPC
		if buildDutyPending(dutySnap(600, 420, 1140), a, dutyAnchors) {
			t.Fatal("want no duty-pending for a PC")
		}
	})
	t.Run("nil clock / nil actor / nil snap / nil anchors -> not pending", func(t *testing.T) {
		noClock := &sim.Snapshot{DawnMinute: 420, DuskMinute: 1140, DawnDuskMinuteOK: true}
		if buildDutyPending(noClock, agentSched(""), dutyAnchors) {
			t.Fatal("want no duty-pending with a nil clock")
		}
		if buildDutyPending(dutySnap(600, 420, 1140), nil, dutyAnchors) {
			t.Fatal("want no duty-pending with a nil actor")
		}
		if buildDutyPending(nil, agentSched(""), dutyAnchors) {
			t.Fatal("want no duty-pending with a nil snapshot")
		}
		if buildDutyPending(dutySnap(600, 420, 1140), agentSched(""), nil) {
			t.Fatal("want no duty-pending with nil anchors")
		}
	})
}

// TestBuildDutySteer_MidMealSuppressesGoHome — ZBBS-WORK-386. The off-shift
// "head home now" cue yields while the NPC holds a live item-source dwell credit
// (mid-meal), so it isn't prompted to abandon the meal mid-dwell (the Prudence
// stew walk-away). Object-source dwell (resting at a tree/well) is out of scope
// and does not suppress.
func TestBuildDutySteer_MidMealSuppressesGoHome(t *testing.T) {
	// Off shift (now=10:00 vs schedule 16:00-03:00 wrap), away from home → go-home arm.
	base := func() *sim.ActorSnapshot {
		return &sim.ActorSnapshot{
			Kind:              sim.KindNPCStateful,
			ScheduleStartMin:  dutyMinPtr(960),
			ScheduleEndMin:    dutyMinPtr(180),
			InsideStructureID: "tavern",
		}
	}

	// Precondition: with no dwell, the go-home cue fires.
	if v := dutySteer(dutySnap(600, 420, 1140), base(), dutyAnchors); v == nil || v.ToWork || v.TargetID != "cottage" {
		t.Fatalf("precondition: want home=cottage, got %+v", v)
	}

	// A live item-source dwell credit (mid-meal) suppresses the go-home cue.
	eating := base()
	eating.DwellCredits = map[sim.DwellCreditKey]*sim.DwellCredit{
		{ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem}: {
			ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem,
		},
	}
	if v := dutySteer(dutySnap(600, 420, 1140), eating, dutyAnchors); v != nil {
		t.Errorf("mid-meal should suppress the go-home cue, got %+v", v)
	}

	// An object-source dwell (resting) is out of scope — the go-home cue still fires.
	resting := base()
	resting.DwellCredits = map[sim.DwellCreditKey]*sim.DwellCredit{
		{ObjectID: "well", Attribute: "thirst", Source: sim.DwellSourceObject}: {
			ObjectID: "well", Attribute: "thirst", Source: sim.DwellSourceObject,
		},
	}
	if v := dutySteer(dutySnap(600, 420, 1140), resting, dutyAnchors); v == nil || v.ToWork {
		t.Errorf("object-source dwell should NOT suppress the go-home cue, got %+v", v)
	}
}

// TestBuildDutySteer_OptionBSuppression — ZBBS-HOME-400, amended by ZBBS-HOME-463.
// The to-work cue is suppressed while the agent is mid-business — an active
// restock errand or a pending outgoing offer — matching the shift-duty warrant. A
// RED need suppresses both arms via the separate gate above; a merely MILD need no
// longer suppresses the commute (HOME-463). The go-home arm is never suppressed by
// these signals.
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
	t.Run("mild (sub-red) need does NOT suppress toWork", func(t *testing.T) {
		snap, a := onShiftAway()
		// hunger 10 with the default red threshold (20) is MILD ([8,20)). Since
		// HOME-463 only a RED need defers the commute, so a peckish NPC still
		// clocks in (the mild gate that stranded chronically-needy NPCs is gone).
		a.Needs = map[sim.NeedKey]int{"hunger": 10}
		if v := buildDutySteer(snap, "moses", a, dutyAnchors, false); v == nil || !v.ToWork {
			t.Fatalf("want toWork (mild need must NOT suppress since HOME-463), got %+v", v)
		}
	})
	t.Run("red need suppresses toWork", func(t *testing.T) {
		snap, a := onShiftAway()
		// hunger 22 >= the red threshold (20) → caught by the red-need gate
		// (HOME-362) above the switch, which suppresses both duty arms.
		a.Needs = map[sim.NeedKey]int{"hunger": 22}
		if v := buildDutySteer(snap, "moses", a, dutyAnchors, false); v != nil {
			t.Fatalf("want nil (red need suppresses to-work), got %+v", v)
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
	// ZBBS-WORK-431: a seller's active scene_quote addressed to the buyer is an
	// in-progress purchase — the buyer-side complement to the pending-outgoing-offer
	// suppressor — so the to-work yank holds off rather than dragging the buyer out
	// of the deal (the Prudence shop↔General-Store bounce).
	t.Run("offered quote to buyer suppresses toWork", func(t *testing.T) {
		snap, a := onShiftAway()
		snap.Quotes = map[sim.QuoteID]*sim.SceneQuote{
			2: {ID: 2, SellerID: "josiah", TargetBuyer: "moses", State: sim.SceneQuoteStateActive},
		}
		if v := buildDutySteer(snap, "moses", a, dutyAnchors, false); v != nil {
			t.Fatalf("want nil (an offered quote suppresses to-work), got %+v", v)
		}
	})
	t.Run("a quote to SOMEONE ELSE does not suppress", func(t *testing.T) {
		snap, a := onShiftAway()
		snap.Quotes = map[sim.QuoteID]*sim.SceneQuote{
			2: {ID: 2, SellerID: "josiah", TargetBuyer: "hannah", State: sim.SceneQuoteStateActive},
		}
		if v := buildDutySteer(snap, "moses", a, dutyAnchors, false); v == nil || !v.ToWork {
			t.Fatalf("want toWork (another buyer's quote is irrelevant), got %+v", v)
		}
	})
	t.Run("a public (untargeted) quote does not suppress", func(t *testing.T) {
		snap, a := onShiftAway()
		snap.Quotes = map[sim.QuoteID]*sim.SceneQuote{
			2: {ID: 2, SellerID: "josiah", TargetBuyer: "", State: sim.SceneQuoteStateActive},
		}
		if v := buildDutySteer(snap, "moses", a, dutyAnchors, false); v == nil || !v.ToWork {
			t.Fatalf("want toWork (a public quote pins no particular buyer), got %+v", v)
		}
	})
	t.Run("a terminal (expired) quote does not suppress", func(t *testing.T) {
		snap, a := onShiftAway()
		snap.Quotes = map[sim.QuoteID]*sim.SceneQuote{
			2: {ID: 2, SellerID: "josiah", TargetBuyer: "moses", State: sim.SceneQuoteStateExpired},
		}
		if v := buildDutySteer(snap, "moses", a, dutyAnchors, false); v == nil || !v.ToWork {
			t.Fatalf("want toWork (an expired quote is irrelevant), got %+v", v)
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
	t.Run("own NON-pending (accepted) offer does not suppress", func(t *testing.T) {
		// Only a Pending offer signals "waiting for the seller's accept_pay"; a
		// terminal entry must not keep suppressing the cue (code_review).
		snap, a := onShiftAway()
		snap.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{
			1: {BuyerID: "moses", State: sim.PayLedgerStateAccepted},
		}
		if v := buildDutySteer(snap, "moses", a, dutyAnchors, false); v == nil || !v.ToWork {
			t.Fatalf("want toWork (own terminal offer is irrelevant), got %+v", v)
		}
	})
	t.Run("a silent (sub-mild) need does not suppress", func(t *testing.T) {
		// hunger 5 is below the silent floor (8). Since HOME-463 no sub-red need
		// suppresses the to-work commute; this remains a valid lower-boundary case.
		snap, a := onShiftAway()
		a.Needs = map[sim.NeedKey]int{"hunger": 5}
		if v := buildDutySteer(snap, "moses", a, dutyAnchors, false); v == nil || !v.ToWork {
			t.Fatalf("want toWork (sub-red need does not suppress), got %+v", v)
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

	// ZBBS-WORK-431: the at-post stabilizer is placeless (the actor is already
	// there) and tells it to stay put rather than wander.
	atPost := render(&DutySteerView{AtPost: true})
	if !strings.Contains(atPost, "at your post") || !strings.Contains(atPost, "wandering off") {
		t.Errorf("atPost prose missing pieces, got %q", atPost)
	}
	if strings.Contains(atPost, "structure_id") {
		t.Errorf("atPost prose should be placeless (no structure_id), got %q", atPost)
	}
	if strings.Contains(atPost, "you close at") {
		t.Errorf("atPost without ShiftEndMin should omit the close clause, got %q", atPost)
	}

	// LLM-40: with the effective close time set, the stabilizer states it
	// (period-voiced) so "stay open later" is a bounded decision.
	atPostClose := render(&DutySteerView{AtPost: true, ShiftEndMin: dutyMinPtr(1260)})
	if !strings.Contains(atPostClose, "you close at 9 in the evening") {
		t.Errorf("atPost with ShiftEndMin should state the close time, got %q", atPostClose)
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

// TestBuildDutySteer_OpenUntilSuppression — ZBBS-WORK-387. A stay_open "open
// until" commitment suppresses the off-shift wind-down cue, but yields to peak
// exhaustion (the needs floor wins) and is inert once lapsed.
func TestBuildDutySteer_OpenUntilSuppression(t *testing.T) {
	now := lodgingNow
	mkSnap := func() *sim.Snapshot {
		m := 600 // 10:00 — off the 16:00–03:00 schedule below
		return &sim.Snapshot{LocalMinuteOfDay: &m, PublishedAt: now, NeedThresholds: sim.DefaultNeedThresholds()}
	}
	base := func() *sim.ActorSnapshot {
		return &sim.ActorSnapshot{
			Kind:              sim.KindNPCStateful,
			ScheduleStartMin:  dutyMinPtr(960),
			ScheduleEndMin:    dutyMinPtr(180),
			InsideStructureID: "tavern", // off-shift, away from home (cottage) → wind-down
			Needs:             map[sim.NeedKey]int{"tiredness": 0},
		}
	}

	// Precondition: no commitment → wind-down fires (home=cottage).
	if v := buildDutySteer(mkSnap(), "ez", base(), dutyAnchors, false); v == nil || v.ToWork || v.TargetID != "cottage" {
		t.Fatalf("precondition: want home=cottage, got %+v", v)
	}

	// Unlapsed OpenUntil, not peak → suppressed.
	committed := base()
	committed.OpenUntil = ptrTime(now.Add(2 * time.Hour))
	if v := buildDutySteer(mkSnap(), "ez", committed, dutyAnchors, false); v != nil {
		t.Errorf("OpenUntil (not peak) should suppress the wind-down, got %+v", v)
	}

	// At peak exhaustion the wind-down CUE is silenced by the red-need gate
	// (hasRedNeed, HOME-362) regardless of OpenUntil — peak is a strict subset of
	// red. The "peak overrides the commitment" property lives on the engine side
	// (the force-bed MarchHome), asserted in sim/stay_open_test.go; the cue layer
	// just goes quiet here.
	peak := base()
	peak.OpenUntil = ptrTime(now.Add(2 * time.Hour))
	peak.Needs["tiredness"] = 24
	if v := buildDutySteer(mkSnap(), "ez", peak, dutyAnchors, false); v != nil {
		t.Errorf("at peak the wind-down cue should be silent (red-need gate), got %+v", v)
	}

	// Lapsed OpenUntil → inert.
	lapsed := base()
	lapsed.OpenUntil = ptrTime(now.Add(-time.Hour))
	if v := buildDutySteer(mkSnap(), "ez", lapsed, dutyAnchors, false); v == nil || v.TargetID != "cottage" {
		t.Errorf("lapsed OpenUntil should not suppress, got %+v", v)
	}
}

// TestBuildDutySteer_LodgerWindsDownToInn — ZBBS-WORK-387. A lodger (no home,
// active ledger grant) winds down toward the inn it rents, with Lodging set.
func TestBuildDutySteer_LodgerWindsDownToInn(t *testing.T) {
	m := 600                                                                 // off-shift
	lodgerAnchors := &AnchorsView{WorkID: "smithy", WorkLabel: "The Smithy"} // no home
	subj := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		ScheduleStartMin:  dutyMinPtr(960),
		ScheduleEndMin:    dutyMinPtr(180),
		InsideStructureID: "smithy", // away from the inn
		WorkStructureID:   "smithy",
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 72*time.Hour),
		},
	}
	structs := map[sim.StructureID]*sim.Structure{"inn": innStructureN("inn", "Hannah's Inn", 1)}
	snap := &sim.Snapshot{LocalMinuteOfDay: &m, PublishedAt: lodgingNow, Structures: structs, NeedThresholds: sim.DefaultNeedThresholds()}

	v := buildDutySteer(snap, "ezekiel", subj, lodgerAnchors, false)
	if v == nil || v.ToWork || v.TargetID != "inn" || !v.Lodging {
		t.Fatalf("want lodger wind-down to inn (Lodging=true), got %+v", v)
	}

	// Already at the inn → nil.
	subj.InsideStructureID = "inn"
	if v := buildDutySteer(snap, "ezekiel", subj, lodgerAnchors, false); v != nil {
		t.Errorf("lodger at the inn should have no wind-down cue, got %+v", v)
	}
}

// TestBuildDutySteer_HomelessNudgeAtPost — ZBBS-WORK-387. A homeless keeper (no
// home, no grant) gets a placeless wind-down nudge (TargetID == "") only while
// still at its work post; off the post it gets nothing (recovery_options + the
// homeless rest floor take over).
func TestBuildDutySteer_HomelessNudgeAtPost(t *testing.T) {
	m := 600
	anchors := &AnchorsView{WorkID: "smithy", WorkLabel: "The Smithy"} // no home
	snap := &sim.Snapshot{LocalMinuteOfDay: &m, PublishedAt: lodgingNow, NeedThresholds: sim.DefaultNeedThresholds()}

	atPost := &sim.ActorSnapshot{
		Kind: sim.KindNPCStateful, ScheduleStartMin: dutyMinPtr(960), ScheduleEndMin: dutyMinPtr(180),
		WorkStructureID: "smithy", InsideStructureID: "smithy",
	}
	if v := buildDutySteer(snap, "vagrant", atPost, anchors, false); v == nil || v.ToWork || v.TargetID != "" || v.Lodging {
		t.Fatalf("want placeless homeless wind-down (empty TargetID), got %+v", v)
	}

	offPost := &sim.ActorSnapshot{
		Kind: sim.KindNPCStateful, ScheduleStartMin: dutyMinPtr(960), ScheduleEndMin: dutyMinPtr(180),
		WorkStructureID: "smithy", InsideStructureID: "market",
	}
	if v := buildDutySteer(snap, "vagrant", offPost, anchors, false); v != nil {
		t.Errorf("homeless off the post should get no wind-down cue, got %+v", v)
	}
}

func TestStayOpenReason(t *testing.T) {
	if r := stayOpenReason(true, false, false); !strings.Contains(r, "orders") {
		t.Errorf("owed-orders reason, got %q", r)
	}
	if r := stayOpenReason(false, true, false); !strings.Contains(r, "customer") {
		t.Errorf("co-present-buyer reason, got %q", r)
	}
	if r := stayOpenReason(false, false, true); !strings.Contains(r, "offer") {
		t.Errorf("pending-offer reason, got %q", r)
	}
	if r := stayOpenReason(false, false, false); r != "" {
		t.Errorf("no signal → empty, got %q", r)
	}
	if r := stayOpenReason(true, true, true); !strings.Contains(r, "orders") {
		t.Errorf("owed orders should take precedence, got %q", r)
	}
}

// TestRenderDutySteer_WindDownVariants covers the ZBBS-WORK-387 prose: lodger,
// homeless (placeless), and the stay_open clause (encouraged + discretionary,
// both naming until_hour).
func TestRenderDutySteer_WindDownVariants(t *testing.T) {
	render := func(v *DutySteerView) string {
		var b strings.Builder
		renderDutySteer(&b, v)
		return b.String()
	}

	lodger := render(&DutySteerView{TargetID: "inn", TargetLabel: "Hannah's Inn", Lodging: true})
	if !strings.Contains(lodger, "rented room at Hannah's Inn") || !strings.Contains(lodger, "structure_id: inn") {
		t.Errorf("lodger prose missing pieces, got %q", lodger)
	}

	homeless := render(&DutySteerView{})
	if !strings.Contains(homeless, "find yourself a place to rest") || strings.Contains(homeless, "structure_id") {
		t.Errorf("homeless prose should be placeless, got %q", homeless)
	}

	enc := render(&DutySteerView{TargetID: "cottage", TargetLabel: "Ellis Cottage", OfferStayOpen: true, StayOpenReason: "you still have orders to deliver"})
	if !strings.Contains(enc, "stay_open") || !strings.Contains(enc, "until_hour") || !strings.Contains(enc, "orders to deliver") {
		t.Errorf("encouraged stay-open prose missing pieces, got %q", enc)
	}

	disc := render(&DutySteerView{TargetID: "cottage", TargetLabel: "Ellis Cottage", OfferStayOpen: true})
	if !strings.Contains(disc, "stay_open") || !strings.Contains(disc, "until_hour") {
		t.Errorf("discretionary stay-open prose missing pieces, got %q", disc)
	}
}
