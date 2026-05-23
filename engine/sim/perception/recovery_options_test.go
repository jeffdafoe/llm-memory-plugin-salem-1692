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
		ID: id, DisplayName: name, X: x, Y: y,
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
	// 96px = 3 tiles east → "a short walk" (3–8 tiles), bearing east. Wrong
	// units would land in a different bucket / direction.
	if o.Distance != "a short walk" || o.Direction != "east" {
		t.Errorf("want 3-tiles-east (a short walk / east), got dist=%q dir=%q", o.Distance, o.Direction)
	}
}

func TestBuildRecoveryOptions_SkipsNonTirednessAndDepleted(t *testing.T) {
	subj := &sim.ActorSnapshot{Needs: map[sim.NeedKey]int{"tiredness": 22}, HomeStructureID: "cottage"}
	well := &sim.VillageObject{ID: "well", DisplayName: "the well", X: 50, Y: 0,
		Refreshes: []*sim.ObjectRefresh{{Attribute: "thirst", Amount: -10}}}
	zero := 0
	depleted := &sim.VillageObject{ID: "spring", DisplayName: "dry spring", X: 60, Y: 0,
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
	if !strings.Contains(out, "the old oak — eases tiredness (~12), free, a short walk east") {
		t.Errorf("rest bullet wrong: %q", out)
	}
	if !strings.Contains(out, "Hannah's Inn — rent a room, ask the keeper") {
		t.Errorf("inn bullet wrong: %q", out)
	}
}
