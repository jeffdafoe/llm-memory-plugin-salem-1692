package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// turn_in_gate_test.go — LLM-447. The voluntary bed-down affordance gate
// (buildTurnInChoice) and its cue (renderTurnInChoice).
//
// Reuses the evening_leisure_test dawn/dusk snapshot (07:00 dawn / 19:00 dusk /
// 22:00 lodger bedtime). The window that matters here is [dusk 19:00, dawn 07:00)
// — deliberately WIDER at the front than the auto-bed's [22:00, 07:00), because
// the three hours between them are the empty evening the Walker "Long Goodnight"
// filled with 26 goodnights and no one going to bed.

// turnInCompany is a stand-in huddle peer — the household member there to bid
// goodnight to. Only its presence matters to the gate.
var turnInCompany = []HuddleMember{{ID: "housemate", DisplayName: "Patience", Acquainted: true}}

func TestBuildTurnInChoice_HomeArm(t *testing.T) {
	// The core case: the Walker shape — an unscheduled day-worker home in the
	// evening, hours before the auto-bed hour would take her.
	t.Run("at home after dusk -> offered", func(t *testing.T) {
		v := buildTurnInChoice(eveningSnap(21*60), eveningUnscheduledWorker("cottage"), turnInCompany)
		if v == nil {
			t.Fatal("at home at 21:00 with company: got nil, want the bed-down affordance")
		}
		if v.Lodging {
			t.Error("home arm should not read as lodging")
		}
		if !v.HasCompany {
			t.Error("HasCompany = false with a huddle peer present")
		}
		if v.PlaceName != "Ellis Cottage" {
			t.Errorf("PlaceName = %q, want the home's label", v.PlaceName)
		}
	})

	// The widening itself. At 19:30 the auto-bed window ([22:00, 07:00)) has not
	// opened, so nothing else in the engine would put this actor to bed for
	// another two and a half hours — which is exactly the stretch the loop lived
	// in. If this case ever regresses to nil, the ticket's whole premise is gone.
	t.Run("just after dusk, long before the auto-bed hour -> offered", func(t *testing.T) {
		if v := buildTurnInChoice(eveningSnap(19*60+30), eveningUnscheduledWorker("cottage"), turnInCompany); v == nil {
			t.Error("at home at 19:30: got nil, want offered — the dusk widening is the feature")
		}
	})

	t.Run("alone at home -> offered, without company", func(t *testing.T) {
		v := buildTurnInChoice(eveningSnap(21*60), eveningUnscheduledWorker("cottage"), nil)
		if v == nil {
			t.Fatal("alone at home: got nil, want offered — an NPC alone may still end its day")
		}
		if v.HasCompany {
			t.Error("HasCompany = true with no huddle peers")
		}
	})

	t.Run("daytime -> not offered", func(t *testing.T) {
		if v := buildTurnInChoice(eveningSnap(14*60), eveningUnscheduledWorker("cottage"), turnInCompany); v != nil {
			t.Errorf("at home at 14:00: got %+v, want nil — turning in is an evening act", v)
		}
	})

	t.Run("at dusk exactly -> offered (window opens inclusive)", func(t *testing.T) {
		if v := buildTurnInChoice(eveningSnap(19*60), eveningUnscheduledWorker("cottage"), turnInCompany); v == nil {
			t.Error("at 19:00 (dusk): got nil, want offered — [dusk, dawn) opens inclusive")
		}
	})

	t.Run("one minute before dusk -> not offered", func(t *testing.T) {
		if v := buildTurnInChoice(eveningSnap(19*60-1), eveningUnscheduledWorker("cottage"), turnInCompany); v != nil {
			t.Errorf("at 18:59: got %+v, want nil — the window has not opened", v)
		}
	})

	t.Run("away from home -> not offered", func(t *testing.T) {
		if v := buildTurnInChoice(eveningSnap(21*60), eveningUnscheduledWorker("tavern"), turnInCompany); v != nil {
			t.Errorf("in the tavern at 21:00: got %+v, want nil — no bed there", v)
		}
	})

	t.Run("out in the open -> not offered", func(t *testing.T) {
		if v := buildTurnInChoice(eveningSnap(21*60), eveningUnscheduledWorker(""), turnInCompany); v != nil {
			t.Errorf("outdoors at 21:00: got %+v, want nil", v)
		}
	})

	t.Run("already asleep -> not offered", func(t *testing.T) {
		a := eveningUnscheduledWorker("cottage")
		a.State = sim.StateSleeping
		if v := buildTurnInChoice(eveningSnap(21*60), a, turnInCompany); v != nil {
			t.Errorf("already abed: got %+v, want nil", v)
		}
	})

	t.Run("a PC -> not offered", func(t *testing.T) {
		a := eveningUnscheduledWorker("cottage")
		a.Kind = sim.KindPC
		if v := buildTurnInChoice(eveningSnap(21*60), a, turnInCompany); v != nil {
			t.Errorf("a PC: got %+v, want nil — players have the pc_sleep_* surface", v)
		}
	})

	t.Run("no usable dawn/dusk clock -> not offered", func(t *testing.T) {
		snap := eveningSnap(21 * 60)
		snap.DawnDuskMinuteOK = false
		if v := buildTurnInChoice(snap, eveningUnscheduledWorker("cottage"), turnInCompany); v != nil {
			t.Errorf("unusable clock: got %+v, want nil rather than an unbounded window", v)
		}
	})
}

// TestBuildTurnInChoice_NightShiftKeeperMidShift is the load-bearing off-shift
// case, and the reason the gate cannot be "is it dark and am I home".
//
// A tavernkeeper works 16:00–03:00 and lives above the shop, so at 22:00 she is
// standing inside her own home, in the night window, with company — every arm of
// the gate satisfied except the one that matters. Offering her the bed would shut
// the tavern mid-evening. This is the same trap the auto-bed's off-shift arm
// exists to avoid (npcSleepHere), which is why both now read one predicate.
func TestBuildTurnInChoice_NightShiftKeeperMidShift(t *testing.T) {
	keeper := eveningWorker("cottage")
	keeper.ScheduleStartMin = evMinPtr(16 * 60) // 16:00
	keeper.ScheduleEndMin = evMinPtr(3 * 60)    // 03:00, wrapping midnight

	if v := buildTurnInChoice(eveningSnap(22*60), keeper, turnInCompany); v != nil {
		t.Errorf("night-shift keeper at home at 22:00 mid-shift: got %+v, want nil — she is at work", v)
	}
	// 04:00: her shift has ended and it is still before dawn, so now she may.
	if v := buildTurnInChoice(eveningSnap(4*60), keeper, turnInCompany); v == nil {
		t.Error("night-shift keeper at home at 04:00 off-shift: got nil, want offered")
	}
}

// TestBuildTurnInChoice_ScheduledDayWorkerOnShift covers the ordinary on-shift
// refusal: a dawn→dusk worker who steps home mid-shift is not offered its bed.
func TestBuildTurnInChoice_ScheduledDayWorkerOnShift(t *testing.T) {
	// 18:00 — inside the 07:00–19:00 shift, and before dusk anyway.
	if v := buildTurnInChoice(eveningSnap(18*60), eveningWorker("cottage"), turnInCompany); v != nil {
		t.Errorf("day worker home at 18:00 mid-shift: got %+v, want nil", v)
	}
	// 20:00 — shift over, past dusk: offered.
	if v := buildTurnInChoice(eveningSnap(20*60), eveningWorker("cottage"), turnInCompany); v == nil {
		t.Error("day worker home at 20:00 off-shift: got nil, want offered")
	}
}

// turnInLodger is the LLM-36 retireLodger (a boarder standing inside the inn it
// rents, holding an active ledger grant) marked as an agent NPC, so the two
// bedtime surfaces are exercised against the SAME fixture — see
// TestBuildTurnInChoice_SupersedesRetireCue.
func turnInLodger(inside sim.StructureID) *sim.ActorSnapshot {
	a := retireLodger()
	a.Kind = sim.KindNPCStateful
	a.InsideStructureID = inside
	return a
}

func TestBuildTurnInChoice_LodgerArm(t *testing.T) {
	t.Run("lodger inside its inn after dusk -> offered", func(t *testing.T) {
		v := buildTurnInChoice(retireSnap(nil, 21*60, retireStructs()), turnInLodger("inn"), turnInCompany)
		if v == nil {
			t.Fatal("lodger at its inn at 21:00: got nil, want offered")
		}
		if !v.Lodging {
			t.Error("Lodging = false for a boarder — the cue would name the wrong bed")
		}
		if v.PlaceName != "Hannah's Inn" {
			t.Errorf("PlaceName = %q, want the inn's label", v.PlaceName)
		}
	})

	// The lodger arm gets the same widening as the home arm: the LLM-36 retire cue
	// only opens at the 22:00 lodger bedtime, so 20:00 is a two-hour stretch where
	// a boarder previously had no way to end its evening either.
	t.Run("lodger at 20:00, before the lodger bedtime hour -> offered", func(t *testing.T) {
		if v := buildTurnInChoice(retireSnap(nil, 20*60, retireStructs()), turnInLodger("inn"), turnInCompany); v == nil {
			t.Error("lodger at its inn at 20:00: got nil, want offered")
		}
	})

	t.Run("lodger elsewhere -> not offered", func(t *testing.T) {
		if v := buildTurnInChoice(retireSnap(nil, 21*60, retireStructs()), turnInLodger("tavern"), turnInCompany); v != nil {
			t.Errorf("lodger away from its inn: got %+v, want nil", v)
		}
	})

	t.Run("homeless non-lodger -> not offered", func(t *testing.T) {
		// Ezekiel-when-unhoused: no home, no grant. Excluded naturally, by design —
		// never "heal" this into a home.
		a := turnInLodger("inn")
		a.RoomAccess = nil
		if v := buildTurnInChoice(retireSnap(nil, 21*60, retireStructs()), a, turnInCompany); v != nil {
			t.Errorf("homeless non-lodger: got %+v, want nil", v)
		}
	})
}

// TestBuildTurnInChoice_IsTheOnlyBedtimeCue pins what replaced the LLM-36 retire
// cue, in the exact situation that cue was built for.
//
// That cue ("## Turn in for the night") predated the sleep verb — its own doc
// comment read "the steer is situational, NOT a tool call" — so it asked a lodger
// to wind down and end its turn and left the engine backstop to bed it. turn_in
// does the same thing with a real tool over a wider window, so the old cue was
// deleted rather than kept as a second, differently-mechanised way to say the
// same thing.
//
// The lodger-at-its-inn-at-bedtime case must therefore still produce exactly one
// bedtime instruction — a tool-backed one. A regression that drops TurnInChoice
// here would leave that lodger with no way to end its evening at all, which is
// worse than the state before either cue existed.
func TestBuildTurnInChoice_IsTheOnlyBedtimeCue(t *testing.T) {
	// 22:30 — the old cue's own window ([bedtime 22:00, dawn)), well inside
	// turn_in's wider [dusk 19:00, dawn).
	subj := turnInLodger("inn")
	snap := retireSnap(subj, 22*60+30, retireStructs())

	p := Build(snap, "ezekiel", nil)
	if p.TurnInChoice == nil {
		t.Fatal("Build dropped the turn_in affordance for a lodger at its inn at 22:30 — the exact " +
			"situation the deleted LLM-36 cue covered, now left with no bedtime cue at all")
	}
	if out := Render(p, DefaultRenderConfig()); strings.Contains(combinedPrompt(out), "## Turn in for the night") {
		t.Error("the deleted LLM-36 retire section rendered — something reintroduced it, and the prompt " +
			"now carries two differently-mechanised bedtime instructions")
	}
}

func TestRenderTurnInChoice(t *testing.T) {
	t.Run("with company names the tool and folds the goodnight into say", func(t *testing.T) {
		var b strings.Builder
		renderTurnInChoice(&b, &TurnInChoiceView{PlaceName: "Ellis Cottage", HasCompany: true})
		out := b.String()
		if !strings.Contains(out, "turn_in") {
			t.Errorf("cue does not name the turn_in tool: %q", out)
		}
		if !strings.Contains(out, "say") {
			t.Errorf("cue does not route the goodnight through say — the model would reach for speak: %q", out)
		}
		if !strings.Contains(out, "your own bed") {
			t.Errorf("home cue should name the actor's own bed: %q", out)
		}
	})

	t.Run("lodger cue names the rented room", func(t *testing.T) {
		var b strings.Builder
		renderTurnInChoice(&b, &TurnInChoiceView{PlaceName: "the Ordinary", Lodging: true})
		out := b.String()
		if !strings.Contains(out, "your room at the Ordinary") {
			t.Errorf("lodger cue should name the rented room: %q", out)
		}
	})

	t.Run("alone -> no goodnight instruction", func(t *testing.T) {
		var b strings.Builder
		renderTurnInChoice(&b, &TurnInChoiceView{PlaceName: "Ellis Cottage"})
		out := b.String()
		if !strings.Contains(out, "turn_in") {
			t.Errorf("solo cue does not name the tool: %q", out)
		}
		if strings.Contains(out, "goodnight") {
			t.Errorf("solo cue should not ask for a goodnight — there is no one to say it to: %q", out)
		}
	})

	t.Run("nil renders nothing", func(t *testing.T) {
		var b strings.Builder
		renderTurnInChoice(&b, nil)
		if b.String() != "" {
			t.Errorf("nil view should render nothing, got %q", b.String())
		}
	})

	// Register check (scenes, not stats): the line is an invitation, never an
	// order. "You must" / "Go to bed now" would make the engine the decider.
	t.Run("register stays diegetic, not imperative", func(t *testing.T) {
		var b strings.Builder
		renderTurnInChoice(&b, &TurnInChoiceView{PlaceName: "Ellis Cottage", HasCompany: true})
		out := strings.ToLower(b.String())
		for _, imperative := range []string{"you must", "you should", "go to bed now"} {
			if strings.Contains(out, imperative) {
				t.Errorf("cue reads as an imperative (%q): %q", imperative, out)
			}
		}
	})
}
