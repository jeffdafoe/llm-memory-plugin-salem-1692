package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// traveler_dayplan_golden_test.go — golden scenarios + cross-scenario invariants
// for the LLM-373 traveler day-plan cues (## On your rounds, ## A bed for the
// night). Registered into perceptionScenarios via init() so the whole-prompt golden
// + determinism harness (TestPerceptionGoldens) covers them alongside the rest.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "traveler_making_rounds_at_shop",
			summary: "LLM-455: a nail-buyer stands inside the Blacksmith — his errand counterparty — with the smith " +
				"co-present. '## Your rounds' cues the trade-here moment: buy the good he came for, naming pay_with_item " +
				"with the exact item kind. His commerce is confined to this one keeper.",
			build: travelerMakingRoundsScenario,
		},
		perceptionScenario{
			name: "traveler_seeking_bed_at_inn",
			summary: "LLM-373: a homeless peddler at the inn of an evening, the innkeeper co-present. The prompt " +
				"carries '## A bed for the night' — the booking cue that names pay_with_item for a nights_stay — " +
				"so the traveler books through the real lodging flow.",
			build: travelerSeekingBedScenario,
		},
		perceptionScenario{
			name: "traveler_between_legs_navigates",
			summary: "LLM-455: a nail-buyer between legs of his rounds — out in the open, not in any shop. '## Your " +
				"rounds' points him at his errand counterparty (the Smithy) with a bearing, casts the other open shop " +
				"(the Weaver's) as a talk-only social call, and shows the failing light — never a single 'go here " +
				"next' target. He navigates with move_to; his commerce is confined to the Smithy.",
			build: travelerBetweenLegsScenario,
		},
		perceptionScenario{
			name: "traveler_errand_settled_winds_down",
			summary: "LLM-455/508: a nail-buyer whose purchase has settled, with the light going — his errand is " +
				"done and it is late enough for bed. '## Your rounds' turns to the wind-down (his business is done, " +
				"the tavern's the place now for supper and a bed) instead of pressing his rounds — the legible " +
				"'business concluded' state that kills the loop.",
			build: travelerErrandSettledScenario,
		},
		perceptionScenario{
			name: "traveler_errand_settled_midday",
			summary: "LLM-507/508: the same settled nail-buyer, but at midday — hours of daylight left. The settled " +
				"lead gives him the day for social calls instead of pitching supper-and-bed (which had him announcing " +
				"goodnight all afternoon), and the nightfall line renders the social-circuit variant (visit the other " +
				"businesses) instead of 'for your trade' — neither line may contradict 'your business is done'.",
			build: travelerErrandSettledMiddayScenario,
		},
	)
}

// travelerErrandSettledMiddayScenario is the settled wind-down scenario with the clock
// pulled back to midday, so minutes-to-dusk lands above the bed-pressure boundary: the
// settled lead renders its rest-of-the-day social variant (LLM-508) and the nightfall
// line its social-circuit variant (LLM-507).
func travelerErrandSettledMiddayScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, id, warrants := travelerErrandSettledScenario()
	midday := 780 // 13:00 — five hours to dusk (1080), but his trade is behind him
	snap.LocalMinuteOfDay = &midday
	return snap, id, warrants
}

func travelerErrandSettledScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		buyerID = sim.ActorID("vstr-0000abcd")
		smithID = sim.ActorID("ezekiel")
		smithy  = sim.StructureID("smithy")
	)
	now := 1050 // 17:30 — half an hour to dusk (1080): the light going, bed-pressure time (LLM-508)
	buyer := &sim.ActorSnapshot{
		Kind:        sim.KindNPCShared,
		DisplayName: "Elias Drum the nail-buyer",
		State:       sim.StateIdle,
		Pos:         sim.TilePos{X: 80, Y: 120},
		Coins:       78,
		Inventory:   map[sim.ItemKind]int{"nail": 6},
		Needs:       map[sim.NeedKey]int{},
		VisitorState: &sim.VisitorState{
			Archetype:         "nail-buyer",
			Origin:            "Boston",
			Disposition:       "weary",
			Phase:             sim.VisitorPhaseMakingRounds,
			VisitedBusinesses: []sim.StructureID{smithy},
			Trade:             &sim.TradeErrand{Direction: sim.TradeDirectionBuy, Good: "nail", Counterparty: smithy, Settled: true},
		},
	}
	smith := &sim.ActorSnapshot{
		Kind:               sim.KindNPCStateful,
		DisplayName:        "Ezekiel Crane",
		Role:               "blacksmith",
		State:              sim.StateIdle,
		Pos:                sim.TilePos{X: 80, Y: 112},
		WorkStructureID:    smithy,
		InsideStructureID:  smithy,
		Needs:              map[sim.NeedKey]int{},
		BusinessownerState: &sim.BusinessownerState{Flavor: "smith"},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		DawnMinute:       360,
		DuskMinute:       1080,
		DawnDuskMinuteOK: true,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{buyerID: buyer, smithID: smith},
		Structures:       map[sim.StructureID]*sim.Structure{smithy: plainStructure(smithy, "Smithy")},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(smithy): {ID: sim.VillageObjectID(smithy), Pos: sim.WorldPos{X: 640, Y: 0}},
		},
	}
	return snap, buyerID, nil
}

func travelerBetweenLegsScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		peddlerID = sim.ActorID("vstr-0000abcd")
		smithID   = sim.ActorID("ezekiel")
		weaverID  = sim.ActorID("goodwife-mary")
		smithy    = sim.StructureID("smithy")
		weaver    = sim.StructureID("weaver")
		cooper    = sim.StructureID("cooper") // already called at
	)
	now := 960 // 16:00 — the afternoon wearing on (dusk 18:00)
	peddler := &sim.ActorSnapshot{
		Kind:        sim.KindNPCShared,
		DisplayName: "Elias Drum the nail-buyer",
		State:       sim.StateIdle,
		Pos:         sim.TilePos{X: 80, Y: 120}, // out in the open, no shop, no huddle (padded tile)
		Coins:       90,
		Needs:       map[sim.NeedKey]int{},
		VisitorState: &sim.VisitorState{
			Archetype:         "nail-buyer",
			Origin:            "Boston",
			Disposition:       "weary",
			Phase:             sim.VisitorPhaseMakingRounds,
			VisitedBusinesses: []sim.StructureID{cooper},
			// His errand: buy nails from the Smithy (his must-hit counterparty). The weaver is a
			// talk-only social call.
			Trade: &sim.TradeErrand{Direction: sim.TradeDirectionBuy, Good: "nail", Counterparty: smithy},
		},
	}
	// Two shops still open, at distinct bearings from the peddler; the smith is nearer
	// (north), the weaver a short way east.
	smith := &sim.ActorSnapshot{
		Kind:               sim.KindNPCStateful,
		DisplayName:        "Ezekiel Crane",
		Role:               "blacksmith",
		State:              sim.StateIdle,
		Pos:                sim.TilePos{X: 80, Y: 112}, // north of the peddler
		WorkStructureID:    smithy,
		InsideStructureID:  smithy,
		Needs:              map[sim.NeedKey]int{},
		BusinessownerState: &sim.BusinessownerState{Flavor: "smith"},
	}
	weav := &sim.ActorSnapshot{
		Kind:               sim.KindNPCStateful,
		DisplayName:        "Goodwife Mary",
		Role:               "weaver",
		State:              sim.StateIdle,
		Pos:                sim.TilePos{X: 95, Y: 120}, // a short way east of the peddler
		WorkStructureID:    weaver,
		InsideStructureID:  weaver,
		Needs:              map[sim.NeedKey]int{},
		BusinessownerState: &sim.BusinessownerState{Flavor: "weaver"},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		DawnMinute:       360,
		DuskMinute:       1080,
		DawnDuskMinuteOK: true,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			peddlerID: peddler, smithID: smith, weaverID: weav,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			smithy: plainStructure(smithy, "Smithy"),
			weaver: plainStructure(weaver, "Weaver's"),
			cooper: plainStructure(cooper, "Cooper's"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			// WorldPos.Tile() adds PadX=60/PadY=112: smithy → tile {80,112} (due north,
			// ~8 tiles), weaver → tile {95,120} (east, ~15 tiles).
			sim.VillageObjectID(smithy): {ID: sim.VillageObjectID(smithy), Pos: sim.WorldPos{X: 640, Y: 0}},
			sim.VillageObjectID(weaver): {ID: sim.VillageObjectID(weaver), Pos: sim.WorldPos{X: 1120, Y: 256}},
		},
	}
	return snap, peddlerID, nil
}

func travelerMakingRoundsScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		peddlerID  = sim.ActorID("vstr-0000abcd")
		smithID    = sim.ActorID("ezekiel")
		blacksmith = sim.StructureID("blacksmith")
	)
	now := 540 // 09:00 — daytime
	peddler := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Elias Drum the nail-buyer",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 10},
		InsideStructureID: blacksmith,
		CurrentHuddleID:   "h1",
		Coins:             90,
		Needs:             map[sim.NeedKey]int{},
		VisitorState: &sim.VisitorState{
			Archetype:   "nail-buyer",
			Origin:      "Boston",
			Disposition: "weary",
			Phase:       sim.VisitorPhaseMakingRounds,
			// The Blacksmith IS his errand counterparty — he stands with the smith, the trade-here moment.
			Trade: &sim.TradeErrand{Direction: sim.TradeDirectionBuy, Good: "nail", Counterparty: blacksmith},
		},
	}
	smith := &sim.ActorSnapshot{
		Kind:               sim.KindNPCStateful,
		DisplayName:        "Ezekiel Crane",
		Role:               "blacksmith",
		State:              sim.StateIdle,
		Pos:                sim.TilePos{X: 11, Y: 10},
		WorkStructureID:    blacksmith,
		InsideStructureID:  blacksmith,
		CurrentHuddleID:    "h1",
		Coins:              12,
		Needs:              map[sim.NeedKey]int{},
		BusinessownerState: &sim.BusinessownerState{Flavor: "smith"},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{peddlerID: peddler, smithID: smith},
		Structures:       map[sim.StructureID]*sim.Structure{blacksmith: plainStructure(blacksmith, "Blacksmith")},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {Members: map[sim.ActorID]struct{}{peddlerID: {}, smithID: {}}},
		},
	}
	return snap, peddlerID, nil
}

func travelerSeekingBedScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		peddlerID = sim.ActorID("vstr-0000abcd")
		keeperID  = sim.ActorID("hannah")
		inn       = sim.StructureID("inn")
	)
	now := 1170 // 19:30 — evening (past dusk 18:00, before bedtime 22:00)
	peddler := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Elias Drum the peddler",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 20, Y: 20},
		InsideStructureID: inn,
		CurrentHuddleID:   "h1",
		Coins:             40,
		Inventory:         map[sim.ItemKind]int{"cheese": 4, "iron": 2},
		Needs:             map[sim.NeedKey]int{},
		VisitorState: &sim.VisitorState{
			Archetype:   "peddler",
			Origin:      "Boston",
			Disposition: "weary",
			Phase:       sim.VisitorPhaseLodging,
		},
	}
	keeper := &sim.ActorSnapshot{
		Kind:               sim.KindNPCStateful,
		DisplayName:        "Goodwife Hannah",
		Role:               "innkeeper",
		State:              sim.StateIdle,
		Pos:                sim.TilePos{X: 21, Y: 20},
		WorkStructureID:    inn,
		InsideStructureID:  inn,
		CurrentHuddleID:    "h1",
		Coins:              25,
		Needs:              map[sim.NeedKey]int{},
		BusinessownerState: &sim.BusinessownerState{Flavor: "innkeeper"},
	}
	snap := &sim.Snapshot{
		PublishedAt:          time.Date(2026, 7, 12, 19, 30, 0, 0, time.UTC),
		LocalMinuteOfDay:     &now,
		DawnMinute:           360,
		DuskMinute:           1080,
		DawnDuskMinuteOK:     true,
		LodgingBedtimeMinute: 1320,
		NeedThresholds:       sim.NeedThresholds{},
		Actors:               map[sim.ActorID]*sim.ActorSnapshot{peddlerID: peddler, keeperID: keeper},
		Structures:           map[sim.StructureID]*sim.Structure{inn: plainStructure(inn, "Hannah's Inn")},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(inn): {
				ID:   sim.VillageObjectID(inn),
				Pos:  sim.WorldPos{X: 320, Y: 320},
				Tags: []string{sim.VisitorTagTavern, "lodging"},
			},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {Members: map[sim.ActorID]struct{}{peddlerID: {}, keeperID: {}}},
		},
	}
	return snap, peddlerID, nil
}

// TestRoundsSettledNoClockStaysSocial — on an unusable dawn/dusk clock the settled
// wind-down renders its social variant and the nightfall line is suppressed entirely
// (LLM-508): a bedtime claim needs a clock to stand on, and MinutesToDusk's zero
// value must not read as dusk.
func TestRoundsSettledNoClockStaysSocial(t *testing.T) {
	var b strings.Builder
	renderTravelerRounds(&b, &TravelerRoundsView{
		Errand: &RoundsErrand{Buy: true, GoodLabel: "nail", Settled: true},
	})
	out := b.String()
	for _, phrase := range []string{"supper and a bed", "see about a bed", "light has all but gone"} {
		if strings.Contains(out, phrase) {
			t.Errorf("no-clock settled rounds rendered bedtime copy %q:\n%s", phrase, out)
		}
	}
	if !strings.Contains(out, "the rest of the day is yours") {
		t.Errorf("no-clock settled rounds missing the social wind-down lead:\n%s", out)
	}
}

// TestGoldensNoDaylightBedContradiction — a prompt that says there is plenty of
// light left must not, anywhere, press toward supper and a bed (LLM-508): the
// settled wind-down and the nightfall line key on the same roundsBedPressureMins
// boundary precisely so this pair can never co-occur. Cross-scenario invariant
// over the whole matrix; the positive cases are pinned by the settled goldens.
func TestGoldensNoDaylightBedContradiction(t *testing.T) {
	daylight := []string{"plenty of daylight left", "plenty of light left"}
	bed := []string{"supper and a bed", "see about a bed"}
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			out := renderScenario(sc)
			for _, d := range daylight {
				if !strings.Contains(out, d) {
					continue
				}
				for _, b := range bed {
					if strings.Contains(out, b) {
						t.Errorf("scenario %q: prompt claims %q yet presses %q — the daylight and bed-pressure copy contradict (LLM-508)", sc.name, d, b)
					}
				}
			}
		})
	}
}

// TestGoldensRoundsCueOnlyForTraveler — the "## On your rounds" section may render
// only for a transient-traveler subject; it must never leak into a persistent NPC /
// PC prompt. One-directional matrix guard (the positive case is pinned by the
// traveler_making_rounds_at_shop golden).
func TestGoldensRoundsCueOnlyForTraveler(t *testing.T) {
	const marker = "## On your rounds"
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			if !strings.Contains(renderScenario(sc), marker) {
				return
			}
			if a := snap.Actors[actorID]; a == nil || a.VisitorState == nil {
				t.Errorf("scenario %q: %q rendered for a non-traveler subject — the rounds cue must be traveler-only (LLM-373)", sc.name, marker)
			}
		})
	}
}

// TestGoldensSeekBedCueTravelerOnlyAndNamesTool — the "## A bed for the night"
// section may render only for a traveler subject, and whenever it renders it must
// name the pay_with_item / nights_stay call (cue-tool lockstep): a booking cue with
// no tool named is a dead end for the weak model.
func TestGoldensSeekBedCueTravelerOnlyAndNamesTool(t *testing.T) {
	const marker = "## A bed for the night"
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			out := renderScenario(sc)
			if !strings.Contains(out, marker) {
				return
			}
			snap, actorID, _ := sc.build()
			if a := snap.Actors[actorID]; a == nil || a.VisitorState == nil {
				t.Errorf("scenario %q: %q rendered for a non-traveler subject — the seek-a-bed cue must be traveler-only (LLM-373)", sc.name, marker)
			}
			if !strings.Contains(out, "pay_with_item") || !strings.Contains(out, "nights_stay") {
				t.Errorf("scenario %q: %q rendered without naming pay_with_item / nights_stay — the booking cue must name its tool (LLM-373)", sc.name, marker)
			}
		})
	}
}
