package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// factor_golden_test.go — golden scenarios + cross-scenario invariants for the LLM-410
// wholesale factor cues: the factor's own distributor-only "## Your dealings here" surface
// (the two-way trade at the distributor; the steer to it from afar, suppressing every other
// shop) and the distributor keeper's "## A factor's come to trade" cue. Registered into
// perceptionScenarios so TestPerceptionGoldens covers them alongside the rest.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "factor_at_distributor",
			summary: "LLM-410: a Boston factor stands in the General Store with the distributor co-present. " +
				"'## Your dealings here' cues the two-way deal — sell his cloth, buy the surplus — and names " +
				"pay_with_item for the buy leg. He is steered to no other shop.",
			build: factorAtDistributorScenario,
		},
		perceptionScenario{
			name: "factor_seeks_distributor",
			summary: "LLM-410: a factor between legs, out in the open, with a weaver's shop also open. '## Your " +
				"dealings here' points him ONLY at the distributor's store with a bearing; the ordinary rounds cue " +
				"(which would list the weaver) is suppressed — a factor trades with no one but the distributor.",
			build: factorSeeksDistributorScenario,
		},
		perceptionScenario{
			name: "distributor_views_factor",
			summary: "LLM-410: the distributor's own view with a factor co-present. '## A factor's come to trade' " +
				"tells the keeper who he is and that he deals both ways, and names pay_with_item for the leg the " +
				"keeper drives (buying the factor's cloth).",
			build: distributorViewsFactorScenario,
		},
	)
}

// factorActor builds the standard factor subject snapshot used by the scenarios.
func factorActor(pos sim.TilePos, inside sim.StructureID, huddle sim.HuddleID) *sim.ActorSnapshot {
	return &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Master Whitcombe the factor",
		State:             sim.StateIdle,
		Pos:               pos,
		InsideStructureID: inside,
		CurrentHuddleID:   huddle,
		Coins:             180,
		Inventory:         map[sim.ItemKind]int{"coat": 3, "cloak": 3, "silver_locket": 2},
		Needs:             map[sim.NeedKey]int{},
		VisitorState: &sim.VisitorState{
			Archetype:       sim.FactorArchetype,
			Origin:          sim.FactorOrigin,
			Disposition:     "mercenary",
			Phase:           sim.VisitorPhaseMakingRounds,
			DistributorOnly: true,
		},
	}
}

// distributorKeeper builds Josiah, the distributor keeper, at the distributor-tagged store.
func distributorKeeper(pos sim.TilePos, huddle sim.HuddleID) *sim.ActorSnapshot {
	return &sim.ActorSnapshot{
		Kind:               sim.KindNPCStateful,
		DisplayName:        "Josiah Thorne",
		Role:               "storekeeper",
		State:              sim.StateIdle,
		Pos:                pos,
		WorkStructureID:    "general_store",
		InsideStructureID:  "general_store",
		CurrentHuddleID:    huddle,
		Coins:              60,
		Needs:              map[sim.NeedKey]int{},
		BusinessownerState: &sim.BusinessownerState{Flavor: "storekeeper"},
	}
}

// distributorObjects returns the General Store village-object (distributor-tagged) + structure.
func distributorObjects() (map[sim.VillageObjectID]*sim.VillageObject, map[sim.StructureID]*sim.Structure) {
	return map[sim.VillageObjectID]*sim.VillageObject{
			"general_store": {ID: "general_store", Pos: sim.WorldPos{X: 320, Y: 320}, Tags: []string{sim.TagDistributor}},
		},
		map[sim.StructureID]*sim.Structure{
			"general_store": plainStructure("general_store", "The General Store"),
		}
}

func factorAtDistributorScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const factorID = sim.ActorID("vstr-0000fac0")
	const josiahID = sim.ActorID("josiah")
	now := 540 // 09:00 daytime
	factor := factorActor(sim.TilePos{X: 40, Y: 40}, "general_store", "h1")
	josiah := distributorKeeper(sim.TilePos{X: 41, Y: 40}, "h1")
	vobjs, structs := distributorObjects()
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{factorID: factor, josiahID: josiah},
		Structures:       structs,
		VillageObjects:   vobjs,
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {Members: map[sim.ActorID]struct{}{factorID: {}, josiahID: {}}},
		},
	}
	return snap, factorID, nil
}

func factorSeeksDistributorScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		factorID = sim.ActorID("vstr-0000fac0")
		josiahID = sim.ActorID("josiah")
		weaverID = sim.ActorID("goodwife-mary")
		weaver   = sim.StructureID("weaver")
	)
	now := 600                                                // 10:00 daytime
	factor := factorActor(sim.TilePos{X: 80, Y: 120}, "", "") // out in the open, no huddle
	josiah := distributorKeeper(sim.TilePos{X: 100, Y: 100}, "")
	// A weaver's shop is also open — the ordinary rounds cue WOULD list it, but a factor's
	// cue must not: he trades with no one but the distributor.
	weav := &sim.ActorSnapshot{
		Kind:               sim.KindNPCStateful,
		DisplayName:        "Goodwife Mary",
		Role:               "weaver",
		State:              sim.StateIdle,
		Pos:                sim.TilePos{X: 95, Y: 120},
		WorkStructureID:    weaver,
		InsideStructureID:  weaver,
		Needs:              map[sim.NeedKey]int{},
		BusinessownerState: &sim.BusinessownerState{Flavor: "weaver"},
	}
	vobjs, structs := distributorObjects()
	vobjs[sim.VillageObjectID(weaver)] = &sim.VillageObject{ID: sim.VillageObjectID(weaver), Pos: sim.WorldPos{X: 1120, Y: 256}}
	structs[weaver] = plainStructure(weaver, "Weaver's")
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		DawnMinute:       360,
		DuskMinute:       1080,
		DawnDuskMinuteOK: true,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			factorID: factor, josiahID: josiah, weaverID: weav,
		},
		Structures:     structs,
		VillageObjects: vobjs,
	}
	return snap, factorID, nil
}

func distributorViewsFactorScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const factorID = sim.ActorID("vstr-0000fac0")
	const josiahID = sim.ActorID("josiah")
	now := 540 // 09:00 daytime
	factor := factorActor(sim.TilePos{X: 40, Y: 40}, "general_store", "h1")
	josiah := distributorKeeper(sim.TilePos{X: 41, Y: 40}, "h1")
	vobjs, structs := distributorObjects()
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{factorID: factor, josiahID: josiah},
		Structures:       structs,
		VillageObjects:   vobjs,
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {Members: map[sim.ActorID]struct{}{factorID: {}, josiahID: {}}},
		},
	}
	return snap, josiahID, nil // subject is the DISTRIBUTOR, not the factor
}

// TestGoldensFactorBusinessCueOnlyForFactor — "## Your dealings here" may render only for a
// DistributorOnly factor subject, and a factor on his rounds must NEVER carry the ordinary
// "## Your rounds" cue (the two are mutually exclusive — the factor's steer replaces it, so no
// cue nudges him to trade at a non-distributor shop).
func TestGoldensFactorBusinessCueOnlyForFactor(t *testing.T) {
	const marker = "## Your dealings here"
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			out := renderScenario(sc)
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			isFactor := a != nil && a.VisitorState != nil && a.VisitorState.DistributorOnly
			if strings.Contains(out, marker) && !isFactor {
				t.Errorf("scenario %q: %q rendered for a non-factor subject", sc.name, marker)
			}
			if isFactor && strings.Contains(out, "## Your rounds") {
				t.Errorf("scenario %q: a factor carried the ordinary '## Your rounds' cue — it must be suppressed (LLM-410)", sc.name)
			}
		})
	}
}

// TestGoldensFactorVisitCueOnlyForDistributor — "## A factor's come to trade" may render only
// for a distributor subject (a co-present factor present), and whenever it renders it must name
// pay_with_item so the keeper knows the tool for the leg he drives.
func TestGoldensFactorVisitCueOnlyForDistributor(t *testing.T) {
	const marker = "## A factor's come to trade"
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			out := renderScenario(sc)
			if !strings.Contains(out, marker) {
				return
			}
			snap, actorID, _ := sc.build()
			a := snap.Actors[actorID]
			if a == nil || !sim.ActorIsDistributor(snap.VillageObjects, a.WorkStructureID) {
				t.Errorf("scenario %q: %q rendered for a non-distributor subject", sc.name, marker)
			}
			if !strings.Contains(out, "pay_with_item") {
				t.Errorf("scenario %q: %q rendered without naming pay_with_item (LLM-410)", sc.name, marker)
			}
		})
	}
}
