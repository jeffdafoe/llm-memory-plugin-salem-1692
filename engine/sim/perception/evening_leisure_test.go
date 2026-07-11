package perception

import (
	"strings"
	"testing"
	"time"

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
		// Dawn/dusk day window (07:00–19:00) so shiftWindowBounds can supply the
		// day-active fallback for an UNSCHEDULED worker (LLM-137/LLM-352). A
		// scheduled actor ignores it; a non-worker never reaches it.
		DawnMinute:       420,  // 07:00
		DuskMinute:       1140, // 19:00
		DawnDuskMinuteOK: true,
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"tavern": {Tags: []string{sim.VisitorTagTavern}, Pos: sim.WorldPos{X: 0, Y: 0}},
		},
		Structures: map[sim.StructureID]*sim.Structure{
			"tavern": {ID: "tavern", DisplayName: "the Tavern"},
			// The homed worker's home must exist in the world so the snapshot-derived
			// homed check (subjectIsHomed, behind inEveningLeisure) resolves it.
			"cottage": {ID: "cottage", DisplayName: "Ellis Cottage"},
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

// eveningUnscheduledWorker is a homed labor vendor (the Walkers): no schedule row,
// no fixed workplace, carries AttrWorker. It is day-active on the world dawn/dusk
// window (LLM-137), so its evening is [dusk, bedtime) exactly like a
// dawn→dusk-scheduled worker's (LLM-352). Pair the snapshot with eveningSnap,
// which supplies the dawn/dusk day window shiftWindowBounds falls back to.
func eveningUnscheduledWorker(inside sim.StructureID) *sim.ActorSnapshot {
	a := eveningWorker(inside)
	a.Kind = sim.KindNPCShared // the Walkers run on the shared salem-vendor VA
	a.ScheduleStartMin = nil
	a.ScheduleEndMin = nil
	a.WorkStructureID = ""
	a.AttributeSlugs = []string{sim.AttrWorker}
	return a
}

// eveningInnRoom is the room the lodger fixtures hold a ledger grant at. Distinct
// from the room id plainStructure/eveningSnap structures carry, so structureForRoom
// resolves it uniquely to the Inn.
const eveningInnRoom sim.RoomID = 42

// withLodgerInn adds an Inn (a lodging house distinct from the tavern venue) holding
// the room the lodger fixtures rent, plus the publish clock the grant is measured
// against, and returns the same snapshot. Pair with eveningLodger.
func withLodgerInn(s *sim.Snapshot) *sim.Snapshot {
	s.PublishedAt = time.Date(2026, 7, 6, 20, 30, 0, 0, time.UTC)
	s.Structures["inn"] = &sim.Structure{
		ID: "inn", DisplayName: "the Inn",
		Rooms: []*sim.Room{{ID: eveningInnRoom, StructureID: "inn", Name: "private_1"}},
	}
	return s
}

// eveningLodger is a homeless-by-design agent (home NULL) holding an active ledger
// room grant at the Inn — the canonical rent-a-room NPC (Ezekiel), same 07:00–19:00
// day shift as eveningWorker. Pair the snapshot with withLodgerInn.
func eveningLodger(inside sim.StructureID) *sim.ActorSnapshot {
	a := eveningWorker(inside)
	a.HomeStructureID = ""
	expires := time.Date(2026, 7, 7, 3, 0, 0, 0, time.UTC) // after withLodgerInn's PublishedAt
	a.RoomAccess = map[sim.RoomAccessKey]*sim.RoomAccess{
		{RoomID: eveningInnRoom, Source: sim.AccessSourceLedger}: {
			RoomID: eveningInnRoom, Source: sim.AccessSourceLedger, Active: true, ExpiresAt: &expires,
		},
	}
	return a
}

// eveningLodgerAnchors: a lodger's anchors carry a workplace but no home — buildAnchors
// only sets HomeID from HomeStructureID, which is empty for a lodger.
var eveningLodgerAnchors = &AnchorsView{WorkID: "blacksmith", WorkLabel: "the Blacksmith"}

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

	t.Run("unscheduled non-worker -> no evening", func(t *testing.T) {
		// No schedule AND no worker marker: home is its resting state (HOME-204), no
		// post-work evening (LLM-352 preserves this — the HOME-204 tension).
		if inEveningWindow(eveningSnap(1230), &sim.ActorSnapshot{Kind: sim.KindNPCStateful}) {
			t.Error("an unscheduled non-worker has no evening window")
		}
	})
	t.Run("partial schedule (worker) -> no evening", func(t *testing.T) {
		// Exactly one bound set is a malformed schedule row — no evening even for a
		// worker (matches the pre-LLM-352 reject; don't silently fall to dawn/dusk).
		partial := eveningUnscheduledWorker("")
		partial.ScheduleStartMin = evMinPtr(420) // start set, end still nil
		if inEveningWindow(eveningSnap(1230), partial) {
			t.Error("a partial (one-bound) schedule is malformed and has no evening window")
		}
	})
	t.Run("unscheduled worker -> evening via dawn/dusk fallback", func(t *testing.T) {
		// LLM-352: the Walkers. Day-active on dawn/dusk (07:00–19:00), so 20:30 is in
		// their [dusk, bedtime) evening even with no schedule row.
		if !inEveningWindow(eveningSnap(1230), eveningUnscheduledWorker("")) {
			t.Error("an unscheduled worker is day-active and has an evening [dusk, bedtime)")
		}
		// ...but not during the workday, and not after bedtime.
		if inEveningWindow(eveningSnap(720), eveningUnscheduledWorker("")) {
			t.Error("an unscheduled worker at noon is on-shift, not in the evening")
		}
		if inEveningWindow(eveningSnap(1380), eveningUnscheduledWorker("")) {
			t.Error("an unscheduled worker after bedtime is past the evening window")
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
	t.Run("fires for an unscheduled worker in window (LLM-352)", func(t *testing.T) {
		// The Walkers: no schedule, day-active on dawn/dusk, so they get the same
		// tavern invitation a scheduled day-worker does instead of being bedded at dusk.
		if v := buildEveningLeisure(eveningSnap(1230), eveningUnscheduledWorker(""), eveningAnchors); v == nil {
			t.Fatal("want evening cue for an unscheduled worker in its [dusk, bedtime) window, got nil")
		}
	})
	t.Run("nil settled at home", func(t *testing.T) {
		if v := buildEveningLeisure(eveningSnap(1230), eveningWorker("cottage"), eveningAnchors); v != nil {
			t.Fatalf("want nil at home (chose to stay in), got %+v", v)
		}
	})
	t.Run("settled-in tier at the tavern (LLM-345)", func(t *testing.T) {
		// The invitation is acted on, but the evening does not evaporate at the
		// threshold: the view switches to the destination-free settled-in scene.
		v := buildEveningLeisure(eveningSnap(1230), eveningWorker("tavern"), eveningAnchors)
		if v == nil {
			t.Fatal("want the settled-in view inside the venue, got nil")
		}
		if !v.SettledIn {
			t.Errorf("want SettledIn inside the venue, got %+v", v)
		}
		if v.VenueLabel != "the Tavern" {
			t.Errorf("venue label: got %q, want %q", v.VenueLabel, "the Tavern")
		}
		// No destinations: the settled-in scene must never re-offer a place to walk to.
		if v.VenueID != "" || v.HomeID != "" {
			t.Errorf("settled-in view must carry no move targets, got venue=%q home=%q", v.VenueID, v.HomeID)
		}
		if v.Invitation() {
			t.Error("settled-in view must not count as the invitation (it would force idle ticks)")
		}
	})
	t.Run("settled-in reads the venue the actor is IN, not the nearest one", func(t *testing.T) {
		// Two taverns: the actor stands in the farther. The old nearest-venue identity
		// check would have missed it and re-pumped an invitation to the near one.
		s := eveningSnap(1230)
		s.VillageObjects["tavern_far"] = &sim.VillageObject{Tags: []string{sim.VisitorTagTavern}, Pos: sim.WorldPos{X: 4096, Y: 4096}}
		s.Structures["tavern_far"] = &sim.Structure{ID: "tavern_far", DisplayName: "the Ship Inn"}
		v := buildEveningLeisure(s, eveningWorker("tavern_far"), eveningAnchors)
		if v == nil || !v.SettledIn {
			t.Fatalf("want the settled-in view in the farther tavern, got %+v", v)
		}
		if v.VenueLabel != "the Ship Inn" {
			t.Errorf("venue label: got %q, want the tavern the actor stands in", v.VenueLabel)
		}
	})
	t.Run("nil leaving the tavern (don't argue with the choice)", func(t *testing.T) {
		// Inside the venue but already walking out: the decision to leave is made, so the
		// room must not be re-pumped at the agent's back. InsideStructureID tracks the
		// CURRENT TILE, so this state persists for every tick spent crossing the tavern
		// floor — it is not a one-tick transient. Any destination counts, not just home
		// (code_review): the model may walk out to the smith or its own workplace.
		for _, dest := range []struct {
			name string
			kind sim.MoveDestinationKind
			id   sim.StructureID
		}{
			{"home (the co-equal stay-in option)", sim.MoveDestinationStructureEnter, "cottage"},
			{"the blacksmith (an errand)", sim.MoveDestinationStructureEnter, "blacksmith"},
			{"a visitor slot outside the tavern itself", sim.MoveDestinationStructureVisit, "tavern"},
		} {
			a := eveningWorker("tavern")
			a.MoveDestKind = dest.kind
			a.MoveDestStructureID = dest.id
			if v := buildEveningLeisure(eveningSnap(1230), a, eveningAnchors); v != nil {
				t.Errorf("want nil while walking out of the venue to %s, got %+v", dest.name, v)
			}
		}
	})
	t.Run("settled-in survives a stale enter-intent aimed at the venue itself", func(t *testing.T) {
		// A StructureEnter targeting the venue the actor already stands in is an arrival
		// that just reconciled, not a departure — the room must still render.
		a := eveningWorker("tavern")
		a.MoveDestKind = sim.MoveDestinationStructureEnter
		a.MoveDestStructureID = "tavern"
		v := buildEveningLeisure(eveningSnap(1230), a, eveningAnchors)
		if v == nil || !v.SettledIn {
			t.Fatalf("want the settled-in view for an arrival intent at the venue, got %+v", v)
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
	t.Run("nil walking home (chose the stay-home option)", func(t *testing.T) {
		// The cue offers home as an actionable move_to token; a model that takes it
		// must not be re-pumped the same invitation the whole walk home (code_review).
		a := eveningWorker("")
		a.MoveDestKind = sim.MoveDestinationStructureEnter
		a.MoveDestStructureID = "cottage"
		if v := buildEveningLeisure(eveningSnap(1230), a, eveningAnchors); v != nil {
			t.Fatalf("want nil while walking home, got %+v", v)
		}
	})
	t.Run("nil while sleeping", func(t *testing.T) {
		a := eveningWorker("")
		a.State = sim.StateSleeping
		if v := buildEveningLeisure(eveningSnap(1230), a, eveningAnchors); v != nil {
			t.Fatalf("want nil for a sleeping actor (awake-only), got %+v", v)
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
	t.Run("nil when homeless (no home, no room grant)", func(t *testing.T) {
		a := eveningWorker("blacksmith")
		a.HomeStructureID = ""
		noHome := &AnchorsView{WorkID: "blacksmith", WorkLabel: "the Blacksmith"}
		if v := buildEveningLeisure(eveningSnap(1230), a, noHome); v != nil {
			t.Fatalf("want nil for the genuinely homeless (no night-place), got %+v", v)
		}
	})
	t.Run("fires for a lodger with a paid room (night-place = the inn, LLM-311)", func(t *testing.T) {
		v := buildEveningLeisure(withLodgerInn(eveningSnap(1230)), eveningLodger("blacksmith"), eveningLodgerAnchors)
		if v == nil {
			t.Fatal("want the evening cue for an off-shift lodger in-window, got nil")
		}
		if v.VenueID != "tavern" {
			t.Errorf("venue: got id=%q, want tavern", v.VenueID)
		}
		// The co-equal "stay in" destination is the rented inn, not an empty token.
		if v.HomeID != "inn" || v.HomeLabel != "the Inn" {
			t.Errorf("night-place: got id=%q label=%q, want inn/\"the Inn\"", v.HomeID, v.HomeLabel)
		}
	})
	t.Run("nil for a lodger settled in its rented inn (stay-in chosen)", func(t *testing.T) {
		if v := buildEveningLeisure(withLodgerInn(eveningSnap(1230)), eveningLodger("inn"), eveningLodgerAnchors); v != nil {
			t.Fatalf("want nil for a lodger already at its inn, got %+v", v)
		}
	})
	t.Run("nil for a lodger walking to its rented inn (don't re-pump)", func(t *testing.T) {
		a := eveningLodger("")
		a.MoveDestKind = sim.MoveDestinationStructureEnter
		a.MoveDestStructureID = "inn"
		if v := buildEveningLeisure(withLodgerInn(eveningSnap(1230)), a, eveningLodgerAnchors); v != nil {
			t.Fatalf("want nil while the lodger walks to its inn, got %+v", v)
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
	if v := buildDutySteer(eveningSnap(1230), "ezekiel", a, eveningAnchors, false, false, false); v != nil {
		t.Fatalf("want nil go-home steer inside the evening window, got %+v", v)
	}

	// 22:30 — past bedtime, no longer the evening: the go-home steer resumes.
	v := buildDutySteer(eveningSnap(1350), "ezekiel", a, eveningAnchors, false, false, false)
	if v == nil || v.ToWork || v.TargetID != "cottage" {
		t.Fatalf("want a go-home steer to cottage past bedtime, got %+v", v)
	}
}

// TestBuildDutySteer_EveningWindow_SuppressesLodgerWindDown is the LLM-311 companion
// to the homed suppression above: inside the evening window a LODGER's premature
// wind-down to its rented inn is suppressed too (mirroring the homed arm), so the
// evening cue is the single voice; past bedtime it resumes toward the inn.
func TestBuildDutySteer_EveningWindow_SuppressesLodgerWindDown(t *testing.T) {
	a := eveningLodger("blacksmith") // off-shift at its post; home NULL; grant at the Inn

	// 20:30 — inside [19:00, 22:00): the lodger wind-down steer is suppressed.
	if v := buildDutySteer(withLodgerInn(eveningSnap(1230)), "ezekiel", a, eveningLodgerAnchors, false, false, false); v != nil {
		t.Fatalf("want nil lodger wind-down steer inside the evening window, got %+v", v)
	}

	// 22:30 — past bedtime: the lodger wind-down steer resumes toward the inn.
	v := buildDutySteer(withLodgerInn(eveningSnap(1350)), "ezekiel", a, eveningLodgerAnchors, false, false, false)
	if v == nil || v.ToWork || v.TargetID != "inn" || !v.Lodging {
		t.Fatalf("want a lodging wind-down steer to the inn past bedtime, got %+v", v)
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
		"(destination: tavern)",  // the venue move_to token
		"(destination: cottage)", // the co-equal stay-home token
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

// TestRenderEveningLeisure_SettledIn: the LLM-345 settled-in tier renders the room
// and nothing to walk to. "Scenes, not stats" — the room is the argument, so the line
// carries no imperative and no move_to token.
func TestRenderEveningLeisure_SettledIn(t *testing.T) {
	var b strings.Builder
	renderEveningLeisure(&b, &EveningLeisureView{SettledIn: true, VenueLabel: "the Tavern"})
	out := b.String()

	for _, want := range []string{
		"inside the Tavern of an evening",
		"can wait for the morning",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("settled-in scene missing %q; got:\n%s", want, out)
		}
	}
	// The invitation's tokens and its three-way choice must be gone: the agent is
	// already here, and re-offering destinations is the re-pump this tier removes.
	for _, unwanted := range []string{"destination:", "make your way to", "turn in for the night"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("settled-in scene must not carry %q; got:\n%s", unwanted, out)
		}
	}

	// Unnamed venue falls back rather than rendering an empty place.
	var fb strings.Builder
	renderEveningLeisure(&fb, &EveningLeisureView{SettledIn: true})
	if !strings.Contains(fb.String(), "inside the tavern of an evening") {
		t.Errorf("settled-in scene should fall back to \"the tavern\"; got:\n%s", fb.String())
	}
}

// TestInsideLeisureVenue covers the tag-based occupancy predicate behind both LLM-345
// levers — it reads the tag off the structure the actor is IN (via the shared-identity
// VillageObject), never the nearest venue.
func TestInsideLeisureVenue(t *testing.T) {
	snap := eveningSnap(1230)

	if !insideLeisureVenue(snap, eveningWorker("tavern")) {
		t.Error("want true inside the tavern")
	}
	if insideLeisureVenue(snap, eveningWorker("cottage")) {
		t.Error("want false inside a home (no venue tag)")
	}
	if insideLeisureVenue(snap, eveningWorker("")) {
		t.Error("want false outdoors")
	}
	if insideLeisureVenue(nil, eveningWorker("tavern")) || insideLeisureVenue(snap, nil) {
		t.Error("want false for nil snapshot / nil actor")
	}
	// A structure with no backing VillageObject (so no tags) is not a venue.
	snap.Structures["barn"] = &sim.Structure{ID: "barn", DisplayName: "the Barn"}
	if insideLeisureVenue(snap, eveningWorker("barn")) {
		t.Error("want false inside an untagged structure")
	}
}

// TestLeavingLeisureVenue: from inside a venue, ANY active move intent is a departure —
// except a StructureEnter aimed at the venue already occupied, which is an arrival that
// has just reconciled. A StructureVisit at the same venue IS a departure: visitor slots
// stand outside the walls.
func TestLeavingLeisureVenue(t *testing.T) {
	idle := eveningWorker("tavern")
	if leavingLeisureVenue(idle) {
		t.Error("want false: no move intent")
	}
	if leavingLeisureVenue(nil) {
		t.Error("want false for a nil actor")
	}
	// Total, not precondition-bound: an actor standing outdoors is leaving nothing, even
	// with a live move intent.
	outdoors := eveningWorker("")
	outdoors.MoveDestKind = sim.MoveDestinationStructureEnter
	outdoors.MoveDestStructureID = "blacksmith"
	if leavingLeisureVenue(outdoors) {
		t.Error("want false outdoors: you cannot leave a structure you are not in")
	}

	arriving := eveningWorker("tavern")
	arriving.MoveDestKind = sim.MoveDestinationStructureEnter
	arriving.MoveDestStructureID = "tavern"
	if leavingLeisureVenue(arriving) {
		t.Error("want false: an enter-intent at the venue already occupied is an arrival")
	}

	for name, mutate := range map[string]func(*sim.ActorSnapshot){
		"enter another structure": func(a *sim.ActorSnapshot) {
			a.MoveDestKind = sim.MoveDestinationStructureEnter
			a.MoveDestStructureID = "blacksmith"
		},
		"visit a slot outside this venue": func(a *sim.ActorSnapshot) {
			a.MoveDestKind = sim.MoveDestinationStructureVisit
			a.MoveDestStructureID = "tavern"
		},
		"walk to a bare tile": func(a *sim.ActorSnapshot) {
			a.MoveDestKind = sim.MoveDestinationPosition
		},
		"visit a village object": func(a *sim.ActorSnapshot) {
			a.MoveDestKind = sim.MoveDestinationObjectVisit
			a.MoveDestObjectID = "well"
		},
	} {
		a := eveningWorker("tavern")
		mutate(a)
		if !leavingLeisureVenue(a) {
			t.Errorf("want true: %s is a departure from the venue", name)
		}
	}
}

// TestSettledAtLeisureVenue: the Lever-B gate is a conjunction, so it is false the moment
// any half lapses — most importantly for the tavernkeeper, whose wrap schedule never opens
// an evening window, so his own wares/restock cues survive in his own tavern.
func TestSettledAtLeisureVenue(t *testing.T) {
	if !settledAtLeisureVenue(eveningSnap(1230), eveningWorker("tavern")) {
		t.Error("want true: off-shift day-worker settled inside the tavern in-window")
	}
	if settledAtLeisureVenue(eveningSnap(1000), eveningWorker("tavern")) {
		t.Error("want false: still on shift, even standing in the tavern")
	}
	if settledAtLeisureVenue(eveningSnap(1230), eveningWorker("blacksmith")) {
		t.Error("want false: in-window but not inside a venue")
	}
	leaving := eveningWorker("tavern")
	leaving.MoveDestKind = sim.MoveDestinationStructureEnter
	leaving.MoveDestStructureID = "blacksmith"
	if settledAtLeisureVenue(eveningSnap(1230), leaving) {
		t.Error("want false: inside the venue but already walking out — not settled")
	}
	keeper := eveningWorker("tavern")
	keeper.ScheduleStartMin, keeper.ScheduleEndMin = evMinPtr(960), evMinPtr(180) // 16:00–03:00 wrap
	if settledAtLeisureVenue(eveningSnap(1230), keeper) {
		t.Error("want false for the tavernkeeper: a wrap schedule has no evening window")
	}
	broke := eveningWorker("tavern")
	broke.Coins = 0
	// LLM-353: coin no longer gates the evening, so a purse-empty worker settled inside
	// the tavern in-window is settled all the same.
	if !settledAtLeisureVenue(eveningSnap(1230), broke) {
		t.Error("want true: a broke worker settled inside the tavern is settled (coin no longer gates the evening)")
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

// TestBuildSuppressesErrandCuesInLeisureVenue pins Lever B at the Build level, where the
// golden can only speak for the cues its fixture happens to raise: inside the venue on
// the evening, every walk-away errand view is dropped, and the moment either half of the
// gate lapses they all stand again. The on-shift control is the load-bearing half —
// Lever B must not silence a farmer's upkeep errand for the rest of her life just
// because she once walked through a pub.
func TestBuildSuppressesErrandCuesInLeisureVenue(t *testing.T) {
	// The live case: Elizabeth owes 3 shovels and is standing in the Tavern at 19:40.
	snap, actorID, warrants := farmOwnerSettledInTavernEvening()
	p := Build(snap, actorID, warrants)

	if p.FarmUpkeep != nil {
		t.Errorf("farm-upkeep errand must yield inside a leisure venue on the evening, got %+v", p.FarmUpkeep)
	}
	if p.Restocking != nil {
		t.Errorf("restock errand must yield inside a leisure venue on the evening, got %+v", p.Restocking)
	}
	if p.StallRepairBuy != nil {
		t.Errorf("repair-buy errand must yield inside a leisure venue on the evening, got %+v", p.StallRepairBuy)
	}
	if p.Forage != nil {
		t.Errorf("forage errand must yield inside a leisure venue on the evening, got %+v", p.Forage)
	}
	if p.EveningLeisure == nil || !p.EveningLeisure.SettledIn {
		t.Fatalf("want the settled-in scene in the errands' place, got %+v", p.EveningLeisure)
	}

	// Control 1 — the SAME farmer, same tavern, but still on shift at 10:00. The upkeep
	// errand is hers to run and must survive: the gate is the evening, not the building.
	onShift, actorID, warrants := farmOwnerSettledInTavernEvening()
	morning := 600
	onShift.LocalMinuteOfDay = &morning
	if v := Build(onShift, actorID, warrants).FarmUpkeep; v == nil {
		t.Error("on-shift inside the tavern: the upkeep errand must still render (Lever B gates on the evening, not the venue alone)")
	}

	// Control 2 — the same evening, but she has stepped outside. An agent that chooses to
	// run an errand on its way home is not the bug this ticket fixes.
	outdoors, actorID, warrants := farmOwnerSettledInTavernEvening()
	outdoors.Actors[actorID].InsideStructureID = ""
	if v := Build(outdoors, actorID, warrants).FarmUpkeep; v == nil {
		t.Error("outdoors on the evening: the upkeep errand must still render (Lever B is scoped to the venue interior)")
	}

	// Control 3 — still on tavern tiles, but she has ALREADY committed to the walk to buy
	// her shovels. Suppressing the errand now would leave the prompt unable to explain the
	// move it is in the middle of, and the room would be re-argued at her back (code_review).
	leaving, actorID, warrants := farmOwnerSettledInTavernEvening()
	la := leaving.Actors[actorID]
	la.MoveDestKind = sim.MoveDestinationStructureEnter
	la.MoveDestStructureID = "blacksmith"
	lp := Build(leaving, actorID, warrants)
	if lp.FarmUpkeep == nil {
		t.Error("walking out on the errand: the upkeep cue must survive to explain the in-flight move")
	}
	if lp.EveningLeisure != nil {
		t.Errorf("walking out of the venue: the room must not be re-pumped at her back, got %+v", lp.EveningLeisure)
	}
}

// TestGoldensNoWorkErrandCuesInsideLeisureVenue is the LLM-345 cross-scenario invariant
// (the GUIDELINES growth-loop): in ANY situation where the subject is passing its
// affordable post-work evening inside a leisure venue, the prompt must carry the
// settled-in room and must NOT carry a walk-away work-errand cue. Each errand below
// tells the agent to leave and go buy or gather something, and each renders under the
// coda that ranks obligations above idle matters — which is precisely how a farm ledger
// beat a tavern and emptied the room (Elizabeth Ellis, live, 2026-07-09). Running it
// over the whole matrix means a future cue can't reintroduce the pull for some other
// situation that nobody thought to pin a golden for.
//
// The wares cue is deliberately NOT in the forbidden set: it names no destination and
// carries no leave-imperative, and it is what lets a trade happen across the tavern
// table (LLM-125). Silencing it would put invented prices back in the room.
func TestGoldensNoWorkErrandCuesInsideLeisureVenue(t *testing.T) {
	// The section headers of the walk-away errand class, in the order Render writes them.
	errandSections := []string{
		"## Nails to mend your business", // stall repair buy — go to the smith
		"## Farm upkeep",                 // upkeep shovels — go to the smith
		"## Restocking",                  // low stock — go to a supplier
		"## Your bushes to harvest",      // forage — go to your bushes
	}
	settled, rooms := 0, 0
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, warrants := sc.build()
			out := renderScenario(sc)

			// Half one — errand absence. Holds for ANY settled subject, whatever else its
			// situation carries (a red need clears the room but must not restore the errands).
			if settledAtLeisureVenue(snap, snap.Actors[actorID]) {
				settled++
				for _, section := range errandSections {
					if strings.Contains(out, section) {
						t.Errorf("scenario %q: subject is settled in a leisure venue on the evening but the prompt still carries %q — the errand argues it back out the door (LLM-345)", sc.name, section)
					}
				}
			}

			// Half two — the room reaches the page. Whenever Build decides the subject is
			// settled in, Render must say so; this is the Lever-A half, and it catches a
			// render branch silently dropped out from under a correct build.
			if v := Build(snap, actorID, warrants).EveningLeisure; v != nil && v.SettledIn {
				rooms++
				if !strings.Contains(out, "of an evening — the fire lit, the room warm") {
					t.Errorf("scenario %q: the payload carries the settled-in view but the rendered prompt has no room — the evening must not evaporate at the threshold (LLM-345)", sc.name)
				}
			}
		})
	}
	// A matrix with no qualifying scenario would pass either half vacuously, which is
	// exactly the hole the ticket was written against.
	if settled == 0 || rooms == 0 {
		t.Fatalf("this invariant is vacuous — no scenario is settled in a leisure venue (settled=%d) or carries the room (rooms=%d); add one", settled, rooms)
	}
}

func eveningWorkerCoins(inside sim.StructureID, coins int) *sim.ActorSnapshot {
	w := eveningWorker(inside)
	w.Coins = coins
	return w
}

// TestInEveningLeisure pins the composite gate after LLM-353 removed the coin floor:
// a night-place (homed, or a lodger holding a room grant) AND the evening window. Coin
// no longer decides it — a broke agent has the same evening as a flush one.
func TestInEveningLeisure(t *testing.T) {
	t.Run("homed + in window -> true", func(t *testing.T) {
		if !inEveningLeisure(eveningSnap(1230), eveningWorker("")) {
			t.Error("a homed, in-window worker is in evening leisure")
		}
	})
	t.Run("broke is no barrier (LLM-353) -> true", func(t *testing.T) {
		if !inEveningLeisure(eveningSnap(1230), eveningWorkerCoins("", 0)) {
			t.Error("a purse-empty homed worker still has an evening — coin no longer gates it")
		}
	})
	t.Run("outside the window (on shift) -> false", func(t *testing.T) {
		if inEveningLeisure(eveningSnap(1000), eveningWorker("")) {
			t.Error("an on-shift worker is not in evening leisure")
		}
	})
	t.Run("homeless (no room grant) -> false", func(t *testing.T) {
		a := eveningWorker("")
		a.HomeStructureID = ""
		if inEveningLeisure(eveningSnap(1230), a) {
			t.Error("a homeless worker with no room grant has no night-place, so no evening")
		}
	})
	t.Run("lodger with a paid room -> true (LLM-311)", func(t *testing.T) {
		a := eveningLodger("")
		a.Coins = 0 // broke — still has an evening
		if !inEveningLeisure(withLodgerInn(eveningSnap(1230)), a) {
			t.Error("a lodging, in-window agent has an evening the same as a homed one, coin or no coin")
		}
	})
}

// TestGoldensSeekWorkSurvivesEveningUntilTavern is the LLM-353 cross-scenario invariant
// (the ticket's stated DoD, GUIDELINES growth loop). The two work-seeking suppressions key
// on tookEveningLeisure — having actually gone to the tavern — not on the evening window or
// on affluence (the deleted coin gate). So an off-shift worker in the evening window who has
// NOT taken the evening still job-hunts; only once he is at (or walking to) the pub does the
// solicit affordance yield. This guards the re-key from silently regressing into the old
// affluence proxy that silenced anyone who could afford a drink. Non-vacuity: at least one
// scenario must show an in-window worker in the road still offered solicit_work.
func TestGoldensSeekWorkSurvivesEveningUntilTavern(t *testing.T) {
	survived := 0
	for _, sc := range perceptionScenarios {
		snap, actorID, _ := sc.build()
		a := snap.Actors[actorID]
		if a == nil || !subjectIsWorker(a) || !inEveningWindow(snap, a) {
			continue
		}
		solicits := strings.Contains(renderScenario(sc), "offer your labor with solicit_work")
		if tookEveningLeisure(snap, a) {
			if solicits {
				t.Errorf("scenario %q: the worker has taken the evening at the tavern, so the solicit affordance must yield (LLM-353)", sc.name)
			}
			continue
		}
		if solicits {
			survived++ // an in-window worker still in the road, still hustling
		}
	}
	if survived == 0 {
		t.Fatal("vacuous: no scenario shows an in-window worker who has NOT taken the evening still offered solicit_work — the re-key could be silently over-suppressing (add/repair a scenario)")
	}
}

// TestCanSolicitWork_ReKeyedToTookEvening proves the LLM-353 re-key: the solicit-work
// suppression keys on tookEveningLeisure — having actually gone to the tavern — NOT on the
// evening window or affluence. The same flush homed worker with a solicitable peer, in the
// evening window, is:
//   - still offered solicit_work while standing at the Commons (has NOT taken the evening),
//   - NOT offered it once he commits to the pub (walking in), even with the peer still present,
//   - still offered it on-shift (control — the suppressor is the tavern, not a missing audience).
//
// The Commons case is the guard against silently regressing into the old affluence proxy: a
// flush worker in the road must still hustle.
func TestCanSolicitWork_ReKeyedToTookEvening(t *testing.T) {
	atCommons, id, _ := homedWorkersEveningCommonsStillSolicits()
	if !Build(atCommons, id, nil).CanSolicitWork {
		t.Error("evening, at the Commons (not the tavern): a worker who has not taken the evening must still be offered solicit_work — the re-key must not silence the road-stander")
	}

	// Same fixture, subject now walking IN to the tavern (headingToLeisureVenue) — he has
	// committed to the evening. The peer is still co-present at the Commons, so the only
	// change is that he has taken the evening.
	heading, id2, _ := homedWorkersEveningCommonsStillSolicits()
	heading.Actors[id2].MoveDestKind = sim.MoveDestinationStructureEnter
	heading.Actors[id2].MoveDestStructureID = "tavern"
	if Build(heading, id2, nil).CanSolicitWork {
		t.Error("evening, walking in to the tavern: a worker who has committed to the pub must not be offered solicit_work")
	}

	// Same fixture, clock moved to 10:00 (on shift): the peer is still solicitable, so the
	// only thing that ever suppressed the affordance is having gone to the tavern.
	onShift, id3, _ := homedWorkersEveningCommonsStillSolicits()
	*onShift.LocalMinuteOfDay = 600
	if !Build(onShift, id3, nil).CanSolicitWork {
		t.Error("on-shift, the same solicitable audience must yield CanSolicitWork — proving the suppressor was the evening-at-tavern, not a missing audience")
	}

	// No-night-place is intentional: unlike inEveningLeisure, tookEveningLeisure does not
	// require a home/room grant — physical presence at the pub is the signal. A homeless
	// worker in the road at dusk has NOT taken the evening and still hustles; one walking in
	// HAS, and does not. (Ezekiel is scheduled, so inEveningWindow holds regardless of home.)
	homelessCommons, id4, _ := homedWorkersEveningCommonsStillSolicits()
	homelessCommons.Actors[id4].HomeStructureID = ""
	if !Build(homelessCommons, id4, nil).CanSolicitWork {
		t.Error("evening, homeless at the Commons: has not taken the evening, so must still be offered solicit_work (tookEveningLeisure doesn't require a night-place)")
	}
	homelessHeading, id5, _ := homedWorkersEveningCommonsStillSolicits()
	homelessHeading.Actors[id5].HomeStructureID = ""
	homelessHeading.Actors[id5].MoveDestKind = sim.MoveDestinationStructureEnter
	homelessHeading.Actors[id5].MoveDestStructureID = "tavern"
	if Build(homelessHeading, id5, nil).CanSolicitWork {
		t.Error("evening, homeless walking in to the tavern: has taken the evening, so must not be offered solicit_work")
	}
}
