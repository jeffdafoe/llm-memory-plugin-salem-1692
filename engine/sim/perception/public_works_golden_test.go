package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// public_works_golden_test.go — golden scenarios + a cross-scenario invariant for
// LLM-654 public works: a broken well, the hand's "## The town's works" bounty
// (walk-to, then call repair at the well), the constable's standing fact, the
// empty-chest silence, and a thirsty villager who is never offered the broken
// well. Registered into perceptionScenarios so TestPerceptionGoldens covers them
// and the terminal-verb invariant sweeps their imperatives.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "hand_hears_of_broken_well",
			summary: "LLM-654: the well by the Mill is broken and the chest can pay. Anne, a worker at home across " +
				"the village, gets '## The town's works': the windlass is down, the town pays 12 coins, about an hour " +
				"of work, no nails — and a walk-to by bearing and id. It names no second tool (repair is offered only " +
				"at the well).",
			build: handHearsOfBrokenWellScenario,
		},
		perceptionScenario{
			name: "hand_at_broken_well_offered_repair",
			summary: "LLM-654: Anne stands at the broken well. The cue switches to the act-now line — call repair " +
				"and stay put — the same signal that hands her the repair tool.",
			build: handAtBrokenWellScenario,
		},
		perceptionScenario{
			name: "constable_knows_the_well_is_broken",
			summary: "LLM-654: Gideon the constable hears the standing fact — the well is broken and the town has " +
				"posted 12 coins for the mending, open to any hand. No imperative, no tool: he hires no one.",
			build: constableKnowsWellIsBrokenScenario,
		},
		perceptionScenario{
			name: "hand_broken_well_chest_cannot_pay",
			summary: "LLM-654: the same broken well, but the chest holds less than bounty + reserve. The hand hears " +
				"nothing of it — the town cannot pay, so there is no work on offer.",
			build: handBrokenWellChestLowScenario,
		},
		perceptionScenario{
			name: "thirsty_villager_passes_a_broken_well",
			summary: "LLM-654: thirsty Joseph (not a worker) stands beside the broken well. His drink options list " +
				"only the sound well across the village — the broken one is never offered — and he hears no bounty.",
			build: thirstyVillagerPassesBrokenWellScenario,
		},
	)
}

const (
	pwAnne   = sim.ActorID("anne")
	pwGideon = sim.ActorID("gideon")
	pwJoseph = sim.ActorID("joseph")
)

// publicWorksSnapshot builds the shared fixture: the mill well broken (by the
// Mill), the centre well sound far away, Anne (a worker) at her home, Gideon
// the constable at the Meeting House, Joseph (not a worker) at the Mill. The
// chest holds `chest` against a bounty of 12 and a reserve of 50.
func publicWorksSnapshot(chest int) *sim.Snapshot {
	zero := 0
	now := 600
	start, end := 480, 1080
	broken := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	ip := func(v int) *int { return &v }
	wellRows := func() []*sim.ObjectRefresh {
		return []*sim.ObjectRefresh{
			{Attribute: "thirst", Amount: -8},
			{Amount: 0, GatherItem: "water", AvailableQuantity: ip(20), MaxQuantity: ip(20)},
		}
	}
	actor := func(name string, pos sim.WorldPos, inside sim.StructureID) *sim.ActorSnapshot {
		return &sim.ActorSnapshot{
			Kind:              sim.KindNPCShared,
			DisplayName:       name,
			State:             sim.StateIdle,
			Pos:               pos.Tile(),
			InsideStructureID: inside,
			ScheduleStartMin:  &start,
			ScheduleEndMin:    &end,
			Coins:             20,
			Needs:             map[sim.NeedKey]int{},
			Inventory:         map[sim.ItemKind]int{},
		}
	}
	anne := actor("Anne Walker", sim.WorldPos{X: 3000, Y: 600}, "walker_home")
	anne.AttributeSlugs = []string{sim.AttrWorker}
	anne.HomeStructureID = "walker_home"
	gideon := actor("Constable Gideon Marsh", sim.WorldPos{X: 2000, Y: 2000}, "meeting_house")
	gideon.Kind = sim.KindNPCStateful
	gideon.AttributeSlugs = []string{sim.AttrConstable}
	gideon.WorkStructureID = "meeting_house"
	joseph := actor("Joseph Scott", sim.WorldPos{X: 700, Y: 400}, "")
	joseph.WorkStructureID = "mill"

	return &sim.Snapshot{
		LocalMinuteOfDay:         &now,
		NeedThresholds:           sim.DefaultNeedThresholds(),
		Assets:                   emptyAssetSet,
		Environment:              sim.WorldEnvironment{TownChest: chest},
		PublicWorksBounty:        12,
		PublicWorksChestReserve:  50,
		PublicWorksRepairSeconds: 3600,
		Actors:                   map[sim.ActorID]*sim.ActorSnapshot{pwAnne: anne, pwGideon: gideon, pwJoseph: joseph},
		Structures: map[sim.StructureID]*sim.Structure{
			"mill":          plainStructure("mill", "Mill"),
			"meeting_house": plainStructure("meeting_house", "Meeting House"),
			"walker_home":   plainStructure("walker_home", "Walker Residence"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"mill": {ID: "mill", DisplayName: "Mill", Pos: sim.WorldPos{X: 500, Y: 400},
				LoiterOffsetX: &zero, LoiterOffsetY: &zero},
			"meeting_house": {ID: "meeting_house", DisplayName: "Meeting House", Pos: sim.WorldPos{X: 2000, Y: 2000},
				LoiterOffsetX: &zero, LoiterOffsetY: &zero},
			"walker_home": {ID: "walker_home", DisplayName: "Walker Residence", Pos: sim.WorldPos{X: 3000, Y: 600},
				LoiterOffsetX: &zero, LoiterOffsetY: &zero},
			"mill_well": {ID: "mill_well", DisplayName: "Well", Pos: sim.WorldPos{X: 700, Y: 400},
				Tags: []string{sim.TagWell}, LoiterOffsetX: &zero, LoiterOffsetY: &zero,
				DamagedAt: broken, Refreshes: wellRows()},
			"centre_well": {ID: "centre_well", DisplayName: "Well", Pos: sim.WorldPos{X: 2400, Y: 2400},
				Tags: []string{sim.TagWell}, LoiterOffsetX: &zero, LoiterOffsetY: &zero,
				Refreshes: wellRows()},
		},
	}
}

func handHearsOfBrokenWellScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return publicWorksSnapshot(100), pwAnne, nil
}

func handAtBrokenWellScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap := publicWorksSnapshot(100)
	a := snap.Actors[pwAnne]
	a.Pos = sim.WorldPos{X: 700, Y: 400}.Tile()
	a.InsideStructureID = ""
	return snap, pwAnne, nil
}

func constableKnowsWellIsBrokenScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return publicWorksSnapshot(100), pwGideon, nil
}

func handBrokenWellChestLowScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return publicWorksSnapshot(61), pwAnne, nil
}

func thirstyVillagerPassesBrokenWellScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap := publicWorksSnapshot(100)
	snap.Actors[pwJoseph].Needs = map[sim.NeedKey]int{"thirst": sim.NeedMax * 7 / 10}
	return snap, pwJoseph, nil
}

// TestGoldensTownWorksReachOnlyHandsAndTheConstable — across the whole matrix,
// "## The town's works" renders only for a worker or the constable, and "Call
// repair" only for a worker standing at the broken well (the one signal that
// also hands out the repair tool). Vacuity-guarded on both lines.
func TestGoldensTownWorksReachOnlyHandsAndTheConstable(t *testing.T) {
	sawSection, sawRepair := false, false
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, warrants := sc.build()
			a := snap.Actors[actorID]
			if a == nil {
				return
			}
			p := Build(snap, actorID, warrants)
			out := combinedPrompt(Render(p, DefaultRenderConfig()))
			if strings.Contains(out, "## The town's works") {
				sawSection = true
				if !subjectIsWorker(a) && !isConstableSnapshot(a) {
					t.Errorf("%q is neither a hand nor the constable and hears of the town's works:\n%s", a.DisplayName, out)
				}
			}
			if strings.Contains(out, "Call repair to take the work") {
				sawRepair = true
				if !p.PublicWorks.OffersRepair() {
					t.Errorf("%q is told to call repair but the tool gate would not offer it", a.DisplayName)
				}
			}
		})
	}
	if !sawSection || !sawRepair {
		t.Fatalf("invariant is vacuous: section seen=%v, repair line seen=%v", sawSection, sawRepair)
	}
}

// TestBrokenWellEasesNothing — the shared magnitude core the satiation and
// recovery scans read reports 0 for a broken well, so it is never offered.
func TestBrokenWellEasesNothing(t *testing.T) {
	snap := publicWorksSnapshot(100)
	if got := objectRefreshMagnitude(snap.VillageObjects["mill_well"], "thirst"); got != 0 {
		t.Errorf("broken well eases thirst by %d, want 0", got)
	}
	if got := objectRefreshMagnitude(snap.VillageObjects["centre_well"], "thirst"); got <= 0 {
		t.Errorf("sound well eases thirst by %d, want > 0", got)
	}
}
