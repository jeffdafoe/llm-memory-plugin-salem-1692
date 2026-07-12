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
			summary: "LLM-373: a peddler on its daytime circuit stands inside the Blacksmith with the smith " +
				"co-present. The prompt carries '## On your rounds' — the social framing that turns the visit into " +
				"a greeting + trade beat rather than a mute stop.",
			build: travelerMakingRoundsScenario,
		},
		perceptionScenario{
			name: "traveler_seeking_bed_at_inn",
			summary: "LLM-373: a homeless peddler at the inn of an evening, the innkeeper co-present. The prompt " +
				"carries '## A bed for the night' — the booking cue that names pay_with_item for a nights_stay — " +
				"so the traveler books through the real lodging flow.",
			build: travelerSeekingBedScenario,
		},
	)
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
		DisplayName:       "Elias Drum the peddler",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 10},
		InsideStructureID: blacksmith,
		CurrentHuddleID:   "h1",
		Coins:             40,
		Inventory:         map[sim.ItemKind]int{"cheese": 4, "iron": 2},
		Needs:             map[sim.NeedKey]int{},
		VisitorState: &sim.VisitorState{
			Archetype:   "peddler",
			Origin:      "Boston",
			Disposition: "weary",
			Phase:       sim.VisitorPhaseMakingRounds,
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
