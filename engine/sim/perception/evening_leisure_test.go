package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// evening_leisure_test.go — LLM-149 (Lever 2 of the living-evening epic). The
// evening "tavern's open" cue, the day-shift evening window it fires on, the
// venue resolver, and the go-home-steer suppression that makes it the single
// voice in-window.

func evMinPtr(n int) *int { return &n }

// eveningAnchors: a homed day-worker — home "cottage", work "blacksmith".
var eveningAnchors = &AnchorsView{
	WorkID: "blacksmith", WorkLabel: "the Blacksmith",
	HomeID: "cottage", HomeLabel: "Ellis Cottage",
}

// eveningSnap carries the village clock, the 22:00 lodger bedtime (the evening
// window's close), and a tavern venue: a VillageObject tagged "tavern" bridged
// to a same-id Structure.
func eveningSnap(nowMin int) *sim.Snapshot {
	m := nowMin
	return &sim.Snapshot{
		LocalMinuteOfDay:     &m,
		LodgingBedtimeMinute: 1320, // 22:00
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"tavern": {Tags: []string{sim.VisitorTagTavern}, Pos: sim.WorldPos{X: 0, Y: 0}},
		},
		Structures: map[sim.StructureID]*sim.Structure{
			"tavern": {ID: "tavern", DisplayName: "the Tavern"},
		},
	}
}

// eveningWorker is a homed day-shift agent (07:00–19:00) standing wherever
// `inside` says, with no pressing needs.
func eveningWorker(inside sim.StructureID) *sim.ActorSnapshot {
	return &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		ScheduleStartMin:  evMinPtr(420),  // 07:00
		ScheduleEndMin:    evMinPtr(1140), // 19:00
		HomeStructureID:   "cottage",
		WorkStructureID:   "blacksmith",
		InsideStructureID: inside,
		Needs:             map[sim.NeedKey]int{},
	}
}

func TestInEveningWindow(t *testing.T) {
	a := eveningWorker("")
	cases := []struct {
		name string
		now  int
		want bool
	}{
		{"inside window 20:30", 1230, true},
		{"at shift-end open (inclusive)", 1140, true},
		{"just before shift-end (still on shift)", 1139, false},
		{"at bedtime (exclusive)", 1320, false},
		{"after bedtime", 1380, false},
	}
	for _, c := range cases {
		if got := inEveningWindow(eveningSnap(c.now), a); got != c.want {
			t.Errorf("%s: inEveningWindow(now=%d) = %v, want %v", c.name, c.now, got, c.want)
		}
	}

	t.Run("unscheduled -> no evening", func(t *testing.T) {
		if inEveningWindow(eveningSnap(1230), &sim.ActorSnapshot{Kind: sim.KindNPCStateful}) {
			t.Error("an unscheduled actor has no evening window")
		}
	})
	t.Run("wrap/night shift -> no evening", func(t *testing.T) {
		wrap := eveningWorker("")
		wrap.ScheduleStartMin = evMinPtr(1320) // 22:00
		wrap.ScheduleEndMin = evMinPtr(360)    // 06:00 (wraps)
		if inEveningWindow(eveningSnap(1230), wrap) {
			t.Error("a wrap/night shift has no simple post-work evening")
		}
	})
	t.Run("shift ends at/after bedtime -> no evening", func(t *testing.T) {
		late := eveningWorker("")
		late.ScheduleEndMin = evMinPtr(1320) // closes exactly at bedtime
		if inEveningWindow(eveningSnap(1325), late) {
			t.Error("a shift ending at bedtime leaves no evening window")
		}
	})
	t.Run("nil clock -> false", func(t *testing.T) {
		s := eveningSnap(1230)
		s.LocalMinuteOfDay = nil
		if inEveningWindow(s, a) {
			t.Error("nil clock yields no window")
		}
	})
}

func TestNearestTaggedVenue(t *testing.T) {
	near := sim.WorldPos{X: 50, Y: 50}
	far := sim.WorldPos{X: 5000, Y: 5000}
	snap := &sim.Snapshot{
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"tavern_far":  {Tags: []string{sim.VisitorTagTavern}, Pos: far},
			"tavern_near": {Tags: []string{sim.VisitorTagTavern}, Pos: near},
			"untagged":    {Pos: near}, // present but not a tavern
		},
		Structures: map[sim.StructureID]*sim.Structure{
			"tavern_far":  {ID: "tavern_far", DisplayName: "Far Tavern"},
			"tavern_near": {ID: "tavern_near", DisplayName: "Near Tavern"},
		},
	}
	a := &sim.ActorSnapshot{Pos: near.Tile()}
	id, label, ok := nearestTaggedVenue(snap, a, sim.VisitorTagTavern)
	if !ok || id != "tavern_near" || label != "Near Tavern" {
		t.Fatalf("want nearest = tavern_near/\"Near Tavern\", got ok=%v id=%q label=%q", ok, id, label)
	}
}

func TestNearestTaggedVenue_TaggedButNoStructure_Skipped(t *testing.T) {
	// A tavern-tagged object with no backing Structure is a decorative, not a
	// venue we can steer a move_to at — it must not resolve.
	snap := &sim.Snapshot{
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"tavern": {Tags: []string{sim.VisitorTagTavern}, Pos: sim.WorldPos{}},
		},
		Structures: map[sim.StructureID]*sim.Structure{},
	}
	if _, _, ok := nearestTaggedVenue(snap, &sim.ActorSnapshot{}, sim.VisitorTagTavern); ok {
		t.Error("a tagged object with no backing Structure must not resolve as a venue")
	}
}

func TestNearestTaggedVenue_NonePlaced(t *testing.T) {
	snap := &sim.Snapshot{
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{},
		Structures:     map[sim.StructureID]*sim.Structure{},
	}
	if _, _, ok := nearestTaggedVenue(snap, &sim.ActorSnapshot{}, sim.VisitorTagTavern); ok {
		t.Error("no tavern placed -> ok must be false")
	}
}

func TestBuildEveningLeisure(t *testing.T) {
	t.Run("fires off-shift homed at workplace in window", func(t *testing.T) {
		v := buildEveningLeisure(eveningSnap(1230), eveningWorker("blacksmith"), eveningAnchors)
		if v == nil {
			t.Fatal("want evening cue, got nil")
		}
		if v.VenueID != "tavern" || v.VenueLabel != "the Tavern" {
			t.Errorf("venue: got id=%q label=%q", v.VenueID, v.VenueLabel)
		}
		if v.HomeID != "cottage" || v.HomeLabel != "Ellis Cottage" {
			t.Errorf("home: got id=%q label=%q", v.HomeID, v.HomeLabel)
		}
	})
	t.Run("fires outdoors in window", func(t *testing.T) {
		if v := buildEveningLeisure(eveningSnap(1230), eveningWorker(""), eveningAnchors); v == nil {
			t.Fatal("want evening cue while outdoors in window, got nil")
		}
	})
	t.Run("nil settled at home", func(t *testing.T) {
		if v := buildEveningLeisure(eveningSnap(1230), eveningWorker("cottage"), eveningAnchors); v != nil {
			t.Fatalf("want nil at home (chose to stay in), got %+v", v)
		}
	})
	t.Run("nil already at the tavern", func(t *testing.T) {
		if v := buildEveningLeisure(eveningSnap(1230), eveningWorker("tavern"), eveningAnchors); v != nil {
			t.Fatalf("want nil at the venue (acted on), got %+v", v)
		}
	})
	t.Run("nil walking to the tavern", func(t *testing.T) {
		a := eveningWorker("")
		a.MoveDestKind = sim.MoveDestinationStructureEnter
		a.MoveDestStructureID = "tavern"
		if v := buildEveningLeisure(eveningSnap(1230), a, eveningAnchors); v != nil {
			t.Fatalf("want nil while walking to the venue, got %+v", v)
		}
	})
	t.Run("nil before the window (still on shift)", func(t *testing.T) {
		if v := buildEveningLeisure(eveningSnap(1000), eveningWorker("blacksmith"), eveningAnchors); v != nil {
			t.Fatalf("want nil pre-window, got %+v", v)
		}
	})
	t.Run("nil after bedtime", func(t *testing.T) {
		if v := buildEveningLeisure(eveningSnap(1330), eveningWorker("blacksmith"), eveningAnchors); v != nil {
			t.Fatalf("want nil past bedtime, got %+v", v)
		}
	})
	t.Run("nil for a red need", func(t *testing.T) {
		a := eveningWorker("blacksmith")
		a.Needs = map[sim.NeedKey]int{recoveryTirednessNeed: 24} // red (default floor 16)
		if v := buildEveningLeisure(eveningSnap(1230), a, eveningAnchors); v != nil {
			t.Fatalf("want nil with a red need, got %+v", v)
		}
	})
	t.Run("nil when unhomed (lodger/homeless out of scope)", func(t *testing.T) {
		a := eveningWorker("blacksmith")
		a.HomeStructureID = ""
		noHome := &AnchorsView{WorkID: "blacksmith", WorkLabel: "the Blacksmith"}
		if v := buildEveningLeisure(eveningSnap(1230), a, noHome); v != nil {
			t.Fatalf("want nil unhomed, got %+v", v)
		}
	})
	t.Run("nil when unscheduled", func(t *testing.T) {
		a := eveningWorker("blacksmith")
		a.ScheduleStartMin = nil
		a.ScheduleEndMin = nil
		if v := buildEveningLeisure(eveningSnap(1230), a, eveningAnchors); v != nil {
			t.Fatalf("want nil unscheduled, got %+v", v)
		}
	})
	t.Run("nil when no tavern placed", func(t *testing.T) {
		s := eveningSnap(1230)
		s.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{}
		if v := buildEveningLeisure(s, eveningWorker("blacksmith"), eveningAnchors); v != nil {
			t.Fatalf("want nil with no venue, got %+v", v)
		}
	})
	t.Run("nil for a PC", func(t *testing.T) {
		a := eveningWorker("blacksmith")
		a.Kind = sim.KindPC
		if v := buildEveningLeisure(eveningSnap(1230), a, eveningAnchors); v != nil {
			t.Fatalf("want nil for a PC, got %+v", v)
		}
	})
}

// TestBuildDutySteer_EveningWindow_SuppressesGoHome: inside the evening window
// the off-shift go-home wind-down steer is suppressed so the evening cue is the
// single voice (LLM-149); outside the window it resumes.
func TestBuildDutySteer_EveningWindow_SuppressesGoHome(t *testing.T) {
	a := eveningWorker("blacksmith") // 07:00–19:00, away from home (at its post)

	// 20:30 — inside [19:00, 22:00): the go-home steer is suppressed.
	if v := buildDutySteer(eveningSnap(1230), "ezekiel", a, eveningAnchors, false, false); v != nil {
		t.Fatalf("want nil go-home steer inside the evening window, got %+v", v)
	}

	// 22:30 — past bedtime, no longer the evening: the go-home steer resumes.
	v := buildDutySteer(eveningSnap(1350), "ezekiel", a, eveningAnchors, false, false)
	if v == nil || v.ToWork || v.TargetID != "cottage" {
		t.Fatalf("want a go-home steer to cottage past bedtime, got %+v", v)
	}
}

func TestRenderEveningLeisure(t *testing.T) {
	var b strings.Builder
	renderEveningLeisure(&b, &EveningLeisureView{
		VenueID: "tavern", VenueLabel: "the Tavern",
		HomeID: "cottage", HomeLabel: "Ellis Cottage",
	})
	out := b.String()
	for _, want := range []string{
		"the tavern is open of an evening",
		"(structure_id: tavern)",  // the venue move_to token
		"(structure_id: cottage)", // the co-equal stay-home token
		"turn in for the night",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered cue missing %q; got:\n%s", want, out)
		}
	}

	var nb strings.Builder
	renderEveningLeisure(&nb, nil)
	if nb.String() != "" {
		t.Errorf("nil view should render nothing, got %q", nb.String())
	}
}

// TestEveningCueReplacesGoHomeSteer is a cross-scenario invariant (the GUIDELINES
// growth-loop): wherever the evening "tavern's open" cue appears in the golden
// matrix, the off-shift go-home wind-down steer ("Your working hours are over …")
// must NOT — the cue REPLACES that turn-in pressure for the evening window
// (LLM-149); the two never stack. renderScenario + perceptionScenarios live in
// golden_test.go (same package).
func TestEveningCueReplacesGoHomeSteer(t *testing.T) {
	for _, sc := range perceptionScenarios {
		out := renderScenario(sc)
		if strings.Contains(out, "the tavern is open of an evening") &&
			strings.Contains(out, "Your working hours are over") {
			t.Errorf("scenario %q shows the evening cue AND the go-home wind-down steer; the cue must replace it, not stack on it", sc.name)
		}
	}
}
