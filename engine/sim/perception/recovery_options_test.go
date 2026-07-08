package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// recovery_options_test.go — ZBBS-HOME-297. Covers the firing gate (tired /
// homeless / neither), the free-rest + inn gather, price-book vs ask-the-
// keeper cost, and the render.

func tirednessObject(id sim.VillageObjectID, name string, x, y float64, amount int) *sim.VillageObject {
	return &sim.VillageObject{
		ID: id, DisplayName: name, Pos: sim.WorldPos{X: x, Y: y},
		Refreshes: []*sim.ObjectRefresh{{Attribute: "tiredness", Amount: amount}},
	}
}

func innStructure(id sim.StructureID, name string) *sim.Structure {
	return &sim.Structure{
		ID: id, DisplayName: name,
		Rooms: []*sim.Room{
			{ID: 1, StructureID: id, Kind: sim.RoomKindCommon, Name: "common"},
			{ID: 2, StructureID: id, Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
		},
	}
}

// --- firing gate ---

func TestBuildRecoveryOptions_NotTiredWithHome_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": 5}, HomeStructureID: "cottage"}
	snap := &sim.Snapshot{
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{"oak": tirednessObject("oak", "the old oak", 96, 0, -12)},
	}
	if v := buildRecoveryOptions(snap, "ezekiel", subj); v != nil {
		t.Errorf("want nil when neither tired nor homeless, got %+v", v)
	}
}

func TestBuildRecoveryOptions_HomelessFiresWhenRested(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": 1}, HomeStructureID: ""}
	snap := &sim.Snapshot{
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{"oak": tirednessObject("oak", "the old oak", 96, 0, -12)},
	}
	v := buildRecoveryOptions(snap, "ezekiel", subj)
	if v == nil || len(v.Options) == 0 {
		t.Fatal("homeless actor must get recovery options every tick (the bootstrap cue), got nil/empty")
	}
}

// TestBuildRecoveryOptions_RentedRoomSuppressesBootstrap — ZBBS-WORK-415. An
// actor with no owned home (HomeStructureID == "") but holding an active lodging
// grant is NOT homeless for the bootstrap cue — it already has a room. The rest
// section stays closed when the actor is also rested, even with a free rest spot
// in range (which the homeless arm would otherwise surface).
func TestBuildRecoveryOptions_RentedRoomSuppressesBootstrap(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Needs:           map[sim.NeedKey]int{"tiredness": 1}, // rested
		HomeStructureID: "",                                  // no owned home
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceLedger}: ledgerAccess(2, 72*time.Hour),
		},
	}
	snap := &sim.Snapshot{
		PublishedAt:    lodgingNow, // ledgerAccess expiry is relative to lodgingNow
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{"oak": tirednessObject("oak", "the old oak", 96, 0, -12)},
	}
	if v := buildRecoveryOptions(snap, "ezekiel", subj); v != nil {
		t.Fatalf("rented-room holder (rested) must not get the homeless bootstrap, got %+v", v)
	}

	// Sanity: the SAME actor, once genuinely tired, still gets the rest section.
	subj.Needs["tiredness"] = sim.DefaultTirednessRedThreshold
	if v := buildRecoveryOptions(snap, "ezekiel", subj); v == nil {
		t.Fatal("a tired rented-room holder must still get recovery options")
	}
}

func TestBuildRecoveryOptions_TiredWithHomeFires(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": sim.DefaultTirednessRedThreshold}, HomeStructureID: "cottage"}
	snap := &sim.Snapshot{
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{"oak": tirednessObject("oak", "the old oak", 96, 0, -12)},
	}
	if v := buildRecoveryOptions(snap, "ezekiel", subj); v == nil {
		t.Fatal("tired actor (tiredness at red threshold) must get recovery options")
	}
}

// --- free rest spots ---

func TestBuildRecoveryOptions_FreeRestSpot(t *testing.T) {
	// Actor coords are padded internal-grid tiles, so express the actor's
	// position through WorldToTile too (world origin) — the object at pixel
	// (96,0) is then 3 tiles due east in the SAME space. Subtracting raw
	// pixels from tiles would be the HOME-297 unit bug.
	origin := sim.WorldToTile(0, 0)
	subj := &sim.ActorSnapshot{Pos: origin, Needs: map[sim.NeedKey]int{"tiredness": 22}, HomeStructureID: "cottage"}
	snap := &sim.Snapshot{
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{"oak": tirednessObject("oak", "the old oak", 96, 0, -12)},
	}
	v := buildRecoveryOptions(snap, "ezekiel", subj)
	if v == nil || len(v.Options) != 1 {
		t.Fatalf("want 1 option, got %+v", v)
	}
	o := v.Options[0]
	if o.Kind != "rest" || o.Label != "the old oak" || o.Magnitude != 12 || o.CostText != "free" {
		t.Errorf("unexpected rest option: %+v", o)
	}
	// 96px = 3 tiles east → "right nearby" (<5 tiles), bearing east. Wrong
	// units would land in a different bucket / direction.
	if o.Distance != "right nearby" || o.Direction != "east" {
		t.Errorf("want 3-tiles-east (right nearby / east), got dist=%q dir=%q", o.Distance, o.Direction)
	}
}

func TestBuildRecoveryOptions_SkipsNonTirednessAndDepleted(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": 22}, HomeStructureID: "cottage"}
	well := &sim.VillageObject{ID: "well", DisplayName: "the well", Pos: sim.WorldPos{X: 50, Y: 0},
		Refreshes: []*sim.ObjectRefresh{{Attribute: "thirst", Amount: -10}}}
	zero := 0
	depleted := &sim.VillageObject{ID: "spring", DisplayName: "dry spring", Pos: sim.WorldPos{X: 60, Y: 0},
		Refreshes: []*sim.ObjectRefresh{{Attribute: "tiredness", Amount: -8, AvailableQuantity: &zero}}}
	snap := &sim.Snapshot{
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{"well": well, "spring": depleted},
	}
	if v := buildRecoveryOptions(snap, "ezekiel", subj); v != nil {
		t.Errorf("a thirst object + a depleted tiredness object should yield no rest options; got %+v", v)
	}
}

func TestBuildRecoveryOptions_NearestRestFirst(t *testing.T) {
	origin := sim.WorldToTile(0, 0)
	subj := &sim.ActorSnapshot{Pos: origin, Needs: map[sim.NeedKey]int{"tiredness": 22}, HomeStructureID: "cottage"}
	near := tirednessObject("near", "near oak", 64, 0, -10) // 2 tiles east
	far := tirednessObject("far", "far oak", 640, 0, -10)   // 20 tiles east
	snap := &sim.Snapshot{
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{"far": far, "near": near},
	}
	v := buildRecoveryOptions(snap, "ezekiel", subj)
	if v == nil || len(v.Options) != 2 {
		t.Fatalf("want 2 options, got %+v", v)
	}
	if v.Options[0].Label != "near oak" {
		t.Errorf("nearest should sort first, got %q then %q", v.Options[0].Label, v.Options[1].Label)
	}
}

// --- inns ---

func TestBuildRecoveryOptions_InnAskTheKeeper(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": 1}, HomeStructureID: ""} // homeless → fires
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"ezekiel": subj,
			"hannah":  {WorkStructureID: "inn"}, // keeper present, but no price history
		},
		Structures: map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")},
	}
	v := buildRecoveryOptions(snap, "ezekiel", subj)
	if v == nil || len(v.Options) != 1 {
		t.Fatalf("want 1 inn option, got %+v", v)
	}
	o := v.Options[0]
	if o.Kind != "inn" || o.Label != "Hannah's Inn" || o.CostText != "ask the keeper" {
		t.Errorf("unexpected inn option (keeper present, no price history → ask the keeper): %+v", o)
	}
}

// An inn with no keeper (no actor works there) is skipped — "rent a room"
// would be unactionable (the booking pays the keeper). (code_review)
func TestBuildRecoveryOptions_KeeperlessInnSkipped(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": 1}, HomeStructureID: ""}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		Structures: map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")},
	}
	if v := buildRecoveryOptions(snap, "ezekiel", subj); v != nil {
		t.Errorf("an inn with no keeper must be skipped; want nil, got %+v", v)
	}
}

// TestBuildRecoveryOptions_InnRememberedShut — LLM-126. An inn the actor remembers
// finding shut (a decaying ObservedClosed memory) is flagged Shut, and the render
// appends the recalled-shut caveat instead of advertising it as freely bookable —
// the experiential replacement for the retired omniscient keeper-asleep marker. The
// keeper is awake here: the cue is driven by the memory, not the keeper's state.
func TestBuildRecoveryOptions_InnRememberedShut(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
		Needs:           map[sim.NeedKey]int{"tiredness": 1},
		HomeStructureID: "", // homeless → inns fire
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "inn", Condition: sim.ObservedClosed}: now.Add(-time.Hour),
		}),
	}
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"ezekiel": subj,
			"hannah":  {WorkStructureID: "inn", State: sim.StateIdle},
		},
		Structures: map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")},
	}
	v := buildRecoveryOptions(snap, "ezekiel", subj)
	if v == nil || len(v.Options) != 1 || !v.Options[0].Shut {
		t.Fatalf("want 1 inn option flagged Shut, got %+v", v)
	}
	var b strings.Builder
	renderRecoveryOptions(&b, v)
	if !strings.Contains(b.String(), closedBusinessAnnotation) {
		t.Errorf("rendered rest section missing the experiential shut caveat:\n%s", b.String())
	}
}

// TestBuildRecoveryOptions_RememberedShutInnNotDemoted — LLM-126, decision 1(a). An
// inn the actor remembers finding shut keeps its natural (alphabetical) position
// rather than being sunk below an open one: the omniscient open-before-closed sink
// was retired with ClosedNow, so a remembered-shut inn is annotated, not demoted.
func TestBuildRecoveryOptions_RememberedShutInnNotDemoted(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
		Needs:           map[sim.NeedKey]int{"tiredness": 1},
		HomeStructureID: "", // homeless → inns fire
		// He remembers the Anchor (alphabetically first) shut; the Boggs he does not.
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "anchor", Condition: sim.ObservedClosed}: now.Add(-time.Hour),
		}),
	}
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"ezekiel": subj,
			"amos":    {WorkStructureID: "anchor", State: sim.StateIdle},
			"boggs":   {WorkStructureID: "boggs", State: sim.StateIdle},
		},
		Structures: map[sim.StructureID]*sim.Structure{
			"anchor": innStructure("anchor", "Anchor Inn"),
			"boggs":  innStructure("boggs", "Boggs Inn"),
		},
	}
	v := buildRecoveryOptions(snap, "ezekiel", subj)
	if v == nil || len(v.Options) != 2 {
		t.Fatalf("want 2 inn options, got %+v", v)
	}
	// Alphabetical by label (Anchor < Boggs); the remembered-shut Anchor is NOT sunk.
	if v.Options[0].StructureID != "anchor" || !v.Options[0].Shut {
		t.Errorf("remembered-shut Anchor must keep its alphabetical lead (not demoted), got first = %+v", v.Options[0])
	}
	if v.Options[1].StructureID != "boggs" || v.Options[1].Shut {
		t.Errorf("the un-remembered Boggs must follow and not be flagged shut, got second = %+v", v.Options[1])
	}
}

// keeperOf picks the lexicographically-smallest worker ID so cost text is
// deterministic when several actors work at the inn. (code_review)
func TestKeeperOf_DeterministicLowestID(t *testing.T) {
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{
		"zeb":   {WorkStructureID: "inn"},
		"alice": {WorkStructureID: "inn"},
		"mara":  {WorkStructureID: "inn"},
	}}
	for i := 0; i < 20; i++ {
		if got := keeperOf(snap, "inn"); got != "alice" {
			t.Fatalf("keeperOf = %q, want deterministic 'alice' (lowest id)", got)
		}
	}
}

// cardinalDirection: world pixels are +x east, +y south. (code_review)
func TestCardinalDirection_Compass(t *testing.T) {
	cases := []struct {
		name     string
		toX, toY float64
		want     string
	}{
		{"north (smaller Y)", 0, -10, "north"},
		{"south (larger Y)", 0, 10, "south"},
		{"east", 10, 0, "east"},
		{"west", -10, 0, "west"},
		{"coincident", 0, 0, ""},
	}
	for _, c := range cases {
		if got := cardinalDirection(0, 0, c.toX, c.toY); got != c.want {
			t.Errorf("%s: cardinalDirection(0,0,%g,%g) = %q, want %q", c.name, c.toX, c.toY, got, c.want)
		}
	}
}

func TestBuildRecoveryOptions_InnPriceFromPriceBook(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": 1}, HomeStructureID: ""}
	keeper := &sim.ActorSnapshot{WorkStructureID: "inn"}
	pb := sim.NewRingBuffer[sim.PriceObservation](4)
	pb.Push(sim.PriceObservation{BuyerID: "ezekiel", Amount: 28, Qty: 1, Consumers: 1, At: time.Now().UTC()})
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "hannah": keeper},
		Structures: map[sim.StructureID]*sim.Structure{"inn": innStructure("inn", "Hannah's Inn")},
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "hannah", Item: "nights_stay"}: pb,
		},
	}
	v := buildRecoveryOptions(snap, "ezekiel", subj)
	if v == nil || len(v.Options) != 1 {
		t.Fatalf("want 1 inn option, got %+v", v)
	}
	if v.Options[0].CostText != "~28 coins" {
		t.Errorf("CostText = %q, want '~28 coins' (last-paid from the price book)", v.Options[0].CostText)
	}
}

func TestBuildRecoveryOptions_NonLodgingStructureNotAnInn(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": 1}, HomeStructureID: ""}
	smithy := &sim.Structure{ID: "smithy", DisplayName: "The Smithy",
		Rooms: []*sim.Room{{ID: 1, StructureID: "smithy", Kind: sim.RoomKindCommon, Name: "common"}}}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		Structures: map[sim.StructureID]*sim.Structure{"smithy": smithy},
	}
	if v := buildRecoveryOptions(snap, "ezekiel", subj); v != nil {
		t.Errorf("a structure with no private bedroom is not an inn; want nil, got %+v", v)
	}
}

// --- consumable remedies (ZBBS-HOME-299) ---

// tirednessCatalog returns an item catalog where coca_tea eases tiredness +12
// immediate and bread eases hunger (a non-tiredness control).
func tirednessCatalog() map[sim.ItemKind]*sim.ItemKindDef {
	return map[sim.ItemKind]*sim.ItemKindDef{
		"coca_tea": {
			Name: "coca_tea", DisplayLabel: "coca tea", Category: sim.ItemCategoryDrink,
			Satisfies: []sim.ItemSatisfaction{{Attribute: "tiredness", Immediate: 12}},
		},
		"bread": {
			Name: "bread", DisplayLabel: "bread", Category: sim.ItemCategoryFood,
			Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 8}},
		},
	}
}

// plainStructure is a structure with no private bedroom — a workplace that is
// NOT an inn, so it isolates the remedy arm from the inn arm.
func plainStructure(id sim.StructureID, name string) *sim.Structure {
	return &sim.Structure{ID: id, DisplayName: name,
		Rooms: []*sim.Room{{ID: 1, StructureID: id, Kind: sim.RoomKindCommon, Name: "common"}}}
}

func TestBuildRecoveryOptions_RemedyVendorSurfaced(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": sim.DefaultTirednessRedThreshold}, HomeStructureID: "cottage"}
	prudence := &sim.ActorSnapshot{WorkStructureID: "apothecary", Inventory: map[sim.ItemKind]int{"coca_tea": 13}}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "prudence": prudence},
		Structures: map[sim.StructureID]*sim.Structure{"apothecary": plainStructure("apothecary", "PW Apothecary")},
		ItemKinds:  tirednessCatalog(),
	}
	v := buildRecoveryOptions(snap, "ezekiel", subj)
	if v == nil || len(v.Options) != 1 {
		t.Fatalf("want 1 remedy option, got %+v", v)
	}
	o := v.Options[0]
	if o.Kind != "remedy" || o.Label != "PW Apothecary" || o.ItemLabel != "coca tea" || o.Magnitude != 12 || o.CostText != "ask the seller" {
		t.Errorf("unexpected remedy option (no price history → ask the seller): %+v", o)
	}
	if o.StructureID != "apothecary" {
		t.Errorf("remedy StructureID = %q, want 'apothecary' (the move_to target)", o.StructureID)
	}
}

// Two tiredness items at the same workplace share the parked sortKey AND the
// Label, so determinism rests entirely on the sourceKey ("vendorID:itemKind")
// tie-break. Exercise it directly (prior inn code had map-iteration
// nondeterminism, so this is worth pinning down). (code_review)
func TestBuildRecoveryOptions_RemedyDeterministicTieBreak(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": sim.DefaultTirednessRedThreshold}, HomeStructureID: "cottage"}
	prudence := &sim.ActorSnapshot{WorkStructureID: "apothecary", Inventory: map[sim.ItemKind]int{"coca_tea": 5, "willow_bark": 3}}
	cat := tirednessCatalog()
	cat["willow_bark"] = &sim.ItemKindDef{Name: "willow_bark", DisplayLabel: "willow bark", Category: sim.ItemCategoryDrink,
		Satisfies: []sim.ItemSatisfaction{{Attribute: "tiredness", Immediate: 6}}}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "prudence": prudence},
		Structures: map[sim.StructureID]*sim.Structure{"apothecary": plainStructure("apothecary", "PW Apothecary")},
		ItemKinds:  cat,
	}
	// Build repeatedly; order must be stable across runs (map iteration is not).
	var first []string
	for i := 0; i < 25; i++ {
		v := buildRecoveryOptions(snap, "ezekiel", subj)
		if v == nil || len(v.Options) != 2 {
			t.Fatalf("want 2 remedy options, got %+v", v)
		}
		got := []string{v.Options[0].ItemLabel, v.Options[1].ItemLabel}
		if first == nil {
			first = got
			continue
		}
		if got[0] != first[0] || got[1] != first[1] {
			t.Fatalf("nondeterministic remedy order: first=%v now=%v", first, got)
		}
	}
	// sourceKey is "prudence:coca_tea" < "prudence:willow_bark", so coca tea first.
	if first[0] != "coca tea" || first[1] != "willow bark" {
		t.Errorf("tie-break order = %v, want [coca tea, willow bark] (sourceKey ascending)", first)
	}
}

func TestBuildRecoveryOptions_RemedyPriceFromPriceBook(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": sim.DefaultTirednessRedThreshold}, HomeStructureID: "cottage"}
	prudence := &sim.ActorSnapshot{WorkStructureID: "apothecary", Inventory: map[sim.ItemKind]int{"coca_tea": 13}}
	pb := sim.NewRingBuffer[sim.PriceObservation](4)
	pb.Push(sim.PriceObservation{BuyerID: "ezekiel", Amount: 2, Qty: 1, Consumers: 1, At: time.Now().UTC()})
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "prudence": prudence},
		Structures: map[sim.StructureID]*sim.Structure{"apothecary": plainStructure("apothecary", "PW Apothecary")},
		ItemKinds:  tirednessCatalog(),
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "prudence", Item: "coca_tea"}: pb,
		},
	}
	v := buildRecoveryOptions(snap, "ezekiel", subj)
	if v == nil || len(v.Options) != 1 {
		t.Fatalf("want 1 remedy option, got %+v", v)
	}
	if v.Options[0].CostText != "~2 coins" {
		t.Errorf("CostText = %q, want '~2 coins' (last-paid from the price book)", v.Options[0].CostText)
	}
}

// The consumable arm is tiredness-gated, not homeless-gated: a homeless actor
// who is not yet tired sees shelter cues but NOT remedy-vendor prompts.
func TestBuildRecoveryOptions_RemedyTirednessGatedOff(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": 1}, HomeStructureID: ""} // homeless → fires, but rested
	prudence := &sim.ActorSnapshot{WorkStructureID: "apothecary", Inventory: map[sim.ItemKind]int{"coca_tea": 13}}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "prudence": prudence},
		Structures: map[sim.StructureID]*sim.Structure{"apothecary": plainStructure("apothecary", "PW Apothecary")},
		ItemKinds:  tirednessCatalog(),
	}
	// Homeless fires the section, but with no shelter options and remedies
	// gated off by low tiredness, there's nothing to surface.
	if v := buildRecoveryOptions(snap, "ezekiel", subj); v != nil {
		t.Errorf("remedies must stay tiredness-gated for a rested homeless actor; got %+v", v)
	}
}

func TestBuildRecoveryOptions_RemedyExcludesPCVendor(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": sim.DefaultTirednessRedThreshold}, HomeStructureID: "cottage"}
	pcHolder := &sim.ActorSnapshot{Kind: sim.KindPC, WorkStructureID: "apothecary", Inventory: map[sim.ItemKind]int{"coca_tea": 1}}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "wendy": pcHolder},
		Structures: map[sim.StructureID]*sim.Structure{"apothecary": plainStructure("apothecary", "PW Apothecary")},
		ItemKinds:  tirednessCatalog(),
	}
	if v := buildRecoveryOptions(snap, "ezekiel", subj); v != nil {
		t.Errorf("a PC holding tea is not a vendor; want nil, got %+v", v)
	}
}

func TestBuildRecoveryOptions_RemedyExcludesNoWorkplaceAndUnresolvedStructure(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": sim.DefaultTirednessRedThreshold}, HomeStructureID: "cottage"}
	noWork := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"coca_tea": 5}}                                // holds tea, no workplace
	ghostWork := &sim.ActorSnapshot{WorkStructureID: "missing", Inventory: map[sim.ItemKind]int{"coca_tea": 5}} // workplace not in snapshot
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "wanderer": noWork, "ghost": ghostWork},
		ItemKinds: tirednessCatalog(),
	}
	if v := buildRecoveryOptions(snap, "ezekiel", subj); v != nil {
		t.Errorf("a tea-holder with no resolvable workplace must not surface a remedy; got %+v", v)
	}
}

func TestBuildRecoveryOptions_RemedyIgnoresNonTirednessAndEmptyStock(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": sim.DefaultTirednessRedThreshold}, HomeStructureID: "cottage"}
	// Holds bread (hunger, not tiredness) and zero-qty tea and an unknown kind.
	baker := &sim.ActorSnapshot{WorkStructureID: "bakery", Inventory: map[sim.ItemKind]int{"bread": 9, "coca_tea": 0, "mystery": 3}}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj, "baker": baker},
		Structures: map[sim.StructureID]*sim.Structure{"bakery": plainStructure("bakery", "The Bakery")},
		ItemKinds:  tirednessCatalog(),
	}
	if v := buildRecoveryOptions(snap, "ezekiel", subj); v != nil {
		t.Errorf("non-tiredness items, zero-qty stock, and unknown kinds must not surface a remedy; got %+v", v)
	}
}

// --- home-bed option + tiredness own-stock (ZBBS-HOME-305) ---

func TestBuildRecoveryOptions_HomeBedSurfaced(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": sim.DefaultTirednessRedThreshold}, HomeStructureID: "cottage"}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		Structures: map[sim.StructureID]*sim.Structure{"cottage": plainStructure("cottage", "Thorne Cottage")},
	}
	v := buildRecoveryOptions(snap, "ezekiel", subj)
	if v == nil {
		t.Fatal("tired homed actor must get a home rest option")
	}
	var home *RecoveryOption
	for i := range v.Options {
		if v.Options[i].Kind == "home" {
			home = &v.Options[i]
		}
	}
	if home == nil {
		t.Fatalf("want a 'home' option, got %+v", v.Options)
	}
	if home.Label != "Thorne Cottage" || home.CostText != "free" {
		t.Errorf("unexpected home option: %+v", home)
	}
	if home.AfterShiftOnly {
		t.Errorf("unscheduled actor (always off-shift) must not be AfterShiftOnly, got %+v", home)
	}
}

// findHomeOption pulls the single "home" rest option out of a view, failing the
// test if the view is nil or carries no home option.
func findHomeOption(t *testing.T, v *RecoveryOptionsView) *RecoveryOption {
	t.Helper()
	if v == nil {
		t.Fatal("want a recovery-options view, got nil")
	}
	for i := range v.Options {
		if v.Options[i].Kind == "home" {
			return &v.Options[i]
		}
	}
	t.Fatalf("want a 'home' option, got %+v", v.Options)
	return nil
}

// On shift, the home-bed option is marked AfterShiftOnly: the engine refuses to
// bed a homed NPC at home while it is on shift (npcSleepHere → !isActorOnShift),
// so offering "sleep in your own bed" as a now-action mid-shift was a false
// affordance that drove the home↔post oscillation. LLM-62.
func TestBuildRecoveryOptions_HomeBedAfterShiftOnlyOnShift(t *testing.T) {
	start, end, onShiftMin := 360, 1140, 600 // shift 06:00–19:00; now 10:00 → on shift
	subj := &sim.ActorSnapshot{
		Needs:            map[sim.NeedKey]int{"tiredness": sim.DefaultTirednessRedThreshold},
		HomeStructureID:  "cottage",
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
	}
	snap := &sim.Snapshot{
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		Structures:       map[sim.StructureID]*sim.Structure{"cottage": plainStructure("cottage", "Thorne Cottage")},
		LocalMinuteOfDay: &onShiftMin,
	}
	home := findHomeOption(t, buildRecoveryOptions(snap, "ezekiel", subj))
	if !home.AfterShiftOnly {
		t.Errorf("on-shift home bed must be marked AfterShiftOnly, got %+v", home)
	}
}

// Off shift, the home bed is a real now-action and stays unmarked. LLM-62.
func TestBuildRecoveryOptions_HomeBedNotAfterShiftOffShift(t *testing.T) {
	start, end, offShiftMin := 360, 1140, 1300 // shift 06:00–19:00; now 21:40 → off shift
	subj := &sim.ActorSnapshot{
		Needs:            map[sim.NeedKey]int{"tiredness": sim.DefaultTirednessRedThreshold},
		HomeStructureID:  "cottage",
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
	}
	snap := &sim.Snapshot{
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		Structures:       map[sim.StructureID]*sim.Structure{"cottage": plainStructure("cottage", "Thorne Cottage")},
		LocalMinuteOfDay: &offShiftMin,
	}
	home := findHomeOption(t, buildRecoveryOptions(snap, "ezekiel", subj))
	if home.AfterShiftOnly {
		t.Errorf("off-shift home bed must not be marked AfterShiftOnly, got %+v", home)
	}
}

// An overnight (wraparound) shift marks AfterShiftOnly correctly: OnShiftAtMinute
// → minuteInShiftWindow handles start > end (on shift = now >= start OR now < end),
// so a night-shift actor at 01:00 is on shift and must be marked, while a point in
// the daytime gap is off shift and unmarked. Guards the wraparound path the
// snapshot clock can produce. LLM-62.
func TestBuildRecoveryOptions_HomeBedAfterShiftOnlyWraparoundShift(t *testing.T) {
	start, end := 1320, 360 // overnight shift 22:00–06:00
	homeAtMinute := func(nowMin int) *RecoveryOption {
		subj := &sim.ActorSnapshot{
			Needs:            map[sim.NeedKey]int{"tiredness": sim.DefaultTirednessRedThreshold},
			HomeStructureID:  "cottage",
			ScheduleStartMin: &start,
			ScheduleEndMin:   &end,
		}
		snap := &sim.Snapshot{
			Actors:           map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
			Structures:       map[sim.StructureID]*sim.Structure{"cottage": plainStructure("cottage", "Thorne Cottage")},
			LocalMinuteOfDay: &nowMin,
		}
		return findHomeOption(t, buildRecoveryOptions(snap, "ezekiel", subj))
	}
	if h := homeAtMinute(60); !h.AfterShiftOnly { // 01:00 → inside the overnight window
		t.Errorf("overnight on-shift (01:00) home bed must be marked AfterShiftOnly, got %+v", h)
	}
	if h := homeAtMinute(720); h.AfterShiftOnly { // 12:00 → daytime gap of an overnight shift
		t.Errorf("overnight off-shift (12:00) home bed must not be marked AfterShiftOnly, got %+v", h)
	}
}

// A home structure that doesn't resolve in the snapshot is skipped — the "sleep
// in your own bed" cue would name an unactionable destination.
func TestBuildRecoveryOptions_HomeBedUnresolvedSkipped(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": sim.DefaultTirednessRedThreshold}, HomeStructureID: "ghost"}
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj}}
	// No structures, no rest spots, no inns, no stock → nothing to surface.
	if v := buildRecoveryOptions(snap, "ezekiel", subj); v != nil {
		t.Errorf("an unresolved home + no other options must yield nil, got %+v", v)
	}
}

func TestBuildRecoveryOptions_TirednessOwnStock(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Needs:           map[sim.NeedKey]int{"tiredness": sim.DefaultTirednessRedThreshold},
		HomeStructureID: "cottage",
		Inventory:       map[sim.ItemKind]int{"coca_tea": 2, "bread": 4}, // bread is hunger — must not appear
	}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		Structures: map[sim.StructureID]*sim.Structure{"cottage": plainStructure("cottage", "Thorne Cottage")},
		ItemKinds:  foodDrinkCatalog(), // coca_tea (tiredness 12) + bread (hunger) etc.
	}
	v := buildRecoveryOptions(snap, "ezekiel", subj)
	if v == nil {
		t.Fatal("want a view")
	}
	if len(v.OwnStock) != 1 {
		t.Fatalf("want 1 tiredness own-stock item (coca tea; bread excluded), got %+v", v.OwnStock)
	}
	if v.OwnStock[0].Label != "coca tea" || v.OwnStock[0].Magnitude != 12 {
		t.Errorf("unexpected own-stock item: %+v", v.OwnStock[0])
	}
}

// Tiredness own-stock is maintenance-gated like remedies: a homeless-but-rested
// actor carrying tea (section fires via the homeless arm) gets no own-stock line.
func TestBuildRecoveryOptions_OwnStockTirednessGatedOff(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Needs:           map[sim.NeedKey]int{"tiredness": 1},
		HomeStructureID: "", // homeless → section fires every tick
		Inventory:       map[sim.ItemKind]int{"coca_tea": 2},
	}
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		ItemKinds: foodDrinkCatalog(),
	}
	// Homeless fires, but rested → own-stock gated off, and no spots/inns → nil.
	if v := buildRecoveryOptions(snap, "ezekiel", subj); v != nil {
		t.Errorf("tiredness own-stock must stay tiredness-gated for a rested actor, got %+v", v)
	}
}

// --- render ---

func TestRenderRecoveryOptions_NilAndEmpty(t *testing.T) {
	var b strings.Builder
	renderRecoveryOptions(&b, nil)
	renderRecoveryOptions(&b, &RecoveryOptionsView{})
	if b.String() != "" {
		t.Errorf("nil/empty view should render nothing, got %q", b.String())
	}
}

func TestRenderRecoveryOptions_Bullets(t *testing.T) {
	var b strings.Builder
	renderRecoveryOptions(&b, &RecoveryOptionsView{Options: []RecoveryOption{
		{Kind: "rest", Label: "the old oak", Magnitude: 12, CostText: "free", Distance: "a short walk", Direction: "east"},
		{Kind: "inn", Label: "Hannah's Inn", CostText: "ask the keeper"},
	}})
	out := b.String()
	if !strings.Contains(out, "## How you can rest") {
		t.Errorf("missing section header: %q", out)
	}
	if !strings.Contains(out, "the old oak — a thorough waking, free, a short walk east") {
		t.Errorf("rest bullet wrong: %q", out)
	}
	if !strings.Contains(out, "Hannah's Inn — rent a room for a full night's proper rest, ask the keeper") {
		t.Errorf("inn bullet wrong: %q", out)
	}
}

func TestRenderRecoveryOptions_HomeAndOwnStock(t *testing.T) {
	var b strings.Builder
	renderRecoveryOptions(&b, &RecoveryOptionsView{
		Options:  []RecoveryOption{{Kind: "home", Label: "Thorne Cottage", CostText: "free"}},
		OwnStock: []OwnStockItem{{Label: "coca tea", Magnitude: 12}},
	})
	out := b.String()
	if !strings.Contains(out, "Thorne Cottage — sleep in your own bed, free") {
		t.Errorf("home bullet wrong: %q", out)
	}
	if !strings.Contains(out, "You have coca tea (a thorough waking) on hand — consume to recover.") {
		t.Errorf("own-stock line wrong: %q", out)
	}
}

// An on-shift home bed renders with the "once your shift ends" qualifier instead
// of as a now-action. LLM-62.
func TestRenderRecoveryOptions_HomeBedAfterShiftOnly(t *testing.T) {
	var b strings.Builder
	renderRecoveryOptions(&b, &RecoveryOptionsView{
		Options: []RecoveryOption{{Kind: "home", Label: "Thorne Cottage", CostText: "free", StructureID: "cottage", AfterShiftOnly: true}},
	})
	out := b.String()
	if !strings.Contains(out, "Thorne Cottage — sleep in your own bed once your shift ends, free (destination: cottage) — stay at your post until then") {
		t.Errorf("on-shift home bullet wrong: %q", out)
	}
}

func TestRenderRecoveryOptions_RemedyBullet(t *testing.T) {
	var b strings.Builder
	renderRecoveryOptions(&b, &RecoveryOptionsView{Options: []RecoveryOption{
		{Kind: "remedy", Label: "PW Apothecary", ItemLabel: "coca tea", Magnitude: 12, CostText: "~2 coins"},
	}})
	out := b.String()
	if !strings.Contains(out, "PW Apothecary — buy coca tea (a thorough waking), ~2 coins") {
		t.Errorf("remedy bullet wrong: %q", out)
	}
}

// TestRenderRecoveryOptions_StructureIDRendered pins the move_to contract: the
// Every rest-cue kind renders a trailing (destination: …) handle the model
// passes straight to move_to — the tool rejects a bare name, so without it the
// cue is unactionable. The structure-backed kinds (inn / home / remedy) emit
// StructureID; the free-object "rest" kind emits its VillageObjectID, which
// move_to resolves to an object visit (ZBBS-HOME-359). Regression guard for the
// perception gap that left NPCs unable to walk to anything they could see —
// fixed for the structure kinds (HOME-326) + satiation free sources (HOME-359),
// extended to rest free objects here (ZBBS-WORK-365).
func TestRenderRecoveryOptions_StructureIDRendered(t *testing.T) {
	var b strings.Builder
	renderRecoveryOptions(&b, &RecoveryOptionsView{Options: []RecoveryOption{
		{Kind: "rest", ObjectID: "oak", Label: "the old oak", Magnitude: 12, CostText: "free", Distance: "a short walk", Direction: "east"},
		{Kind: "inn", Label: "Hannah's Inn", CostText: "ask the keeper", StructureID: "inn"},
		{Kind: "home", Label: "Thorne Cottage", CostText: "free", StructureID: "cottage"},
		{Kind: "remedy", Label: "PW Apothecary", ItemLabel: "coca tea", Magnitude: 12, CostText: "~2 coins", StructureID: "apothecary"},
	}})
	out := b.String()
	// Exact full-bullet lines — pinning a tool contract, so guard against
	// suffix/shape drift (a strings.Contains check would miss a duplicated id or
	// trailing junk).
	hasLine := func(want string) bool {
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{
		"- Hannah's Inn — rent a room for a full night's proper rest, ask the keeper (destination: inn)",
		"- Thorne Cottage — sleep in your own bed, free (destination: cottage)",
		"- PW Apothecary — buy coca tea (a thorough waking), ~2 coins (destination: apothecary)",
		// The free-object rest kind renders its VillageObjectID on the same
		// structure_id field — move_to falls through to an object visit (HOME-359).
		"- the old oak — a thorough waking, free, a short walk east (destination: oak)",
	} {
		if !hasLine(want) {
			t.Errorf("missing exact bullet %q in:\n%s", want, out)
		}
	}
	// A rest option with no resolvable object id renders no handle (defensive —
	// the non-empty guard in renderRecoveryOptions), mirroring the structure kinds.
	var b2 strings.Builder
	renderRecoveryOptions(&b2, &RecoveryOptionsView{Options: []RecoveryOption{
		{Kind: "rest", Label: "a nameless spot", Magnitude: 12, CostText: "free", Distance: "a short walk", Direction: "east"},
	}})
	for _, line := range strings.Split(b2.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- a nameless spot") && strings.Contains(line, "destination") {
			t.Errorf("rest bullet with empty ObjectID must not render a destination: %q", line)
		}
	}
}
