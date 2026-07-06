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

// eveningPricedSnap is eveningSnap plus a tavern keeper who sells "ale" at retail
// price alePrice (0 = unpriced/free), so canAffordLeisure (LLM-205) has a real floor
// to read off the venue.
func eveningPricedSnap(nowMin, alePrice int) *sim.Snapshot {
	s := eveningSnap(nowMin)
	s.Actors = map[sim.ActorID]*sim.ActorSnapshot{
		"keeper": {
			Kind:              sim.KindNPCShared,
			WorkStructureID:   "tavern",
			InsideStructureID: "tavern",
			Inventory:         map[sim.ItemKind]int{"ale": 5},
			Needs:             map[sim.NeedKey]int{},
		},
	}
	s.Recipes = map[sim.ItemKind]*sim.ItemRecipe{
		"ale": {OutputItem: "ale", OutputQty: 1, RetailPrice: alePrice},
	}
	return s
}

func eveningWorkerCoins(inside sim.StructureID, coins int) *sim.ActorSnapshot {
	w := eveningWorker(inside)
	w.Coins = coins
	return w
}

// TestCanAffordLeisure pins the coin floor (LLM-205): a night out costs at least the
// cheapest PRICED drink the tavern sells; a free item or an unplaced tavern is no
// barrier.
func TestCanAffordLeisure(t *testing.T) {
	cases := []struct {
		name     string
		alePrice int
		coins    int
		noTavern bool
		want     bool
	}{
		{"affords the drink", 3, 5, false, true},
		{"exactly the drink price", 3, 3, false, true},
		{"too broke for the drink", 3, 2, false, false},
		{"free drink is no barrier", 0, 0, false, true},
		{"no tavern placed is no barrier", 3, 0, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := eveningPricedSnap(1230, c.alePrice)
			if c.noTavern {
				s.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{}
			}
			if got := canAffordLeisure(s, eveningWorkerCoins("", c.coins)); got != c.want {
				t.Errorf("canAffordLeisure(coins=%d, alePrice=%d, noTavern=%v) = %v, want %v",
					c.coins, c.alePrice, c.noTavern, got, c.want)
			}
		})
	}

	// The floor is the CHEAPEST priced item, not just any one: the keeper sells ale
	// (retail 5) and stew (retail 3), so 3 coins suffices and 2 does not.
	t.Run("cheapest priced item controls the floor", func(t *testing.T) {
		s := eveningPricedSnap(1230, 5)
		s.Actors["keeper"].Inventory = map[sim.ItemKind]int{"ale": 5, "stew": 3}
		s.Recipes["stew"] = &sim.ItemRecipe{OutputItem: "stew", OutputQty: 1, RetailPrice: 3}
		if !canAffordLeisure(s, eveningWorkerCoins("", 3)) {
			t.Error("coins == cheapest item (stew=3) should afford the evening")
		}
		if canAffordLeisure(s, eveningWorkerCoins("", 2)) {
			t.Error("coins below the cheapest item (stew=3) should not afford the evening")
		}
	})

	// Nil Recipes (vendor + inventory present, nothing priced): no floor resolves, so
	// there is no affordability barrier — and the nil-map read must not panic.
	t.Run("nil recipes is no barrier", func(t *testing.T) {
		s := eveningPricedSnap(1230, 5)
		s.Recipes = nil
		if !canAffordLeisure(s, eveningWorkerCoins("", 0)) {
			t.Error("with no recipes there is no priced floor, so no affordability barrier")
		}
	})
}

// TestInEveningLeisure pins the composite gate (LLM-205): homed AND in the evening
// window AND able to afford a night out.
func TestInEveningLeisure(t *testing.T) {
	t.Run("homed + in window + affords -> true", func(t *testing.T) {
		if !inEveningLeisure(eveningPricedSnap(1230, 3), eveningWorkerCoins("", 5)) {
			t.Error("a homed, flush, in-window worker is in evening leisure")
		}
	})
	t.Run("too broke -> false", func(t *testing.T) {
		if inEveningLeisure(eveningPricedSnap(1230, 3), eveningWorkerCoins("", 2)) {
			t.Error("a worker too broke for the tavern's cheapest drink is not in evening leisure")
		}
	})
	t.Run("outside the window (on shift) -> false", func(t *testing.T) {
		if inEveningLeisure(eveningPricedSnap(1000, 3), eveningWorkerCoins("", 50)) {
			t.Error("an on-shift worker is not in evening leisure")
		}
	})
	t.Run("homeless (no room grant) -> false", func(t *testing.T) {
		a := eveningWorkerCoins("", 50)
		a.HomeStructureID = ""
		if inEveningLeisure(eveningPricedSnap(1230, 3), a) {
			t.Error("a homeless worker with no room grant has no night-place, so no evening")
		}
	})
	t.Run("lodger with a paid room + affords -> true (LLM-311)", func(t *testing.T) {
		a := eveningLodger("blacksmith")
		a.Coins = 5
		if !inEveningLeisure(withLodgerInn(eveningPricedSnap(1230, 3)), a) {
			t.Error("a lodging, flush, in-window agent has an evening the same as a homed one")
		}
	})
	t.Run("lodger too broke -> false (LLM-311)", func(t *testing.T) {
		a := eveningLodger("blacksmith")
		a.Coins = 2
		if inEveningLeisure(withLodgerInn(eveningPricedSnap(1230, 3)), a) {
			t.Error("a lodger too broke for the tavern's cheapest drink is not in evening leisure")
		}
	})
}

// TestEveningLeisureSuppressesSolicit is the LLM-205 rule-2 cross-scenario invariant
// (the GUIDELINES growth loop): wherever the evening "tavern's open" cue appears in
// the matrix, the solicit-work affordance must NOT — an agent enjoying an affordable
// evening is off the clock and doesn't hustle for work. homed_workers_evening_commons
// _no_solicit makes it non-vacuous (cue present, a solicitable peer present, affordance
// suppressed).
func TestEveningLeisureSuppressesSolicit(t *testing.T) {
	for _, sc := range perceptionScenarios {
		out := renderScenario(sc)
		if strings.Contains(out, "the tavern is open of an evening") &&
			strings.Contains(out, "offer your labor with solicit_work") {
			t.Errorf("scenario %q shows the evening cue AND the solicit-work affordance; evening leisure must suppress soliciting (LLM-205)", sc.name)
		}
	}
}

// TestCanSolicitWork_SuppressedInEveningLeisure proves the rule-2 suppression is the
// evening leisure, NOT the absence of a solicitable audience: the same fixture (a
// flush homed worker with a solicitable peer present) yields CanSolicitWork=false in
// the evening and CanSolicitWork=true when shifted on-shift, where the audience is
// identical. Without this contrast the golden could pass vacuously (affordance gone
// for the wrong reason).
func TestCanSolicitWork_SuppressedInEveningLeisure(t *testing.T) {
	evening, id, _ := homedWorkersEveningCommonsNoSolicit()
	if Build(evening, id, nil).CanSolicitWork {
		t.Error("a worker in affordable evening leisure must not be offered solicit_work")
	}
	// Same fixture, clock moved to 10:00 (on shift): the peer is still solicitable, so
	// the only thing that was suppressing the affordance is the evening leisure gate.
	onShift, id2, _ := homedWorkersEveningCommonsNoSolicit()
	*onShift.LocalMinuteOfDay = 600
	if !Build(onShift, id2, nil).CanSolicitWork {
		t.Error("on-shift, the same solicitable audience must yield CanSolicitWork — proving the evening gate suppressed it, not a missing audience")
	}
}
