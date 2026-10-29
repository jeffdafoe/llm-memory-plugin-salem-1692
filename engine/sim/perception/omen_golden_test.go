package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// omen_golden_test.go — LLM-843 golden scenarios + a cross-scenario invariant.
// An omen reaches a villager in two ways: the witness reads it at the moment
// (the completion beat) and carries it first-hand, worded to them when it was
// their own; everyone else hears it as word going round, which — unlike other
// gossip — is spoken even with its subject standing there.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "omen_witness_sour_milk",
			summary: "LLM-843: Elizabeth Ellis just finished milking Cow 2 and the milk turned in the pail. The " +
				"completion beat is the omen's witness line in place of the yield line, and '## Word about the " +
				"village' carries it first-hand, worded to her: 'You saw it yourself: your milk turned sour in the pail.'",
			build: omenWitnessScenario,
		},
		perceptionScenario{
			name: "omen_word_spoken_with_subject_present",
			summary: "LLM-843: John Ellis heard that Elizabeth Ellis's milk spoils as soon as it is drawn, and she is " +
				"in his conversation now. Other gossip is held back in its subject's presence; an omen is not, so the " +
				"word renders: 'Word has it that Elizabeth Ellis's milk spoils as soon as it is drawn.'",
			build: omenWordWithSubjectPresentScenario,
		},
	)
}

const (
	omenElizabethID = sim.ActorID("elizabeth")
	omenJohnID      = sim.ActorID("john")
)

// omenFarmSnapshot: Elizabeth and John Ellis outdoors at the farm on a summer
// morning, in one conversation when huddled is set.
func omenFarmSnapshot(huddled bool) *sim.Snapshot {
	now := 9 * 60
	published := time.Date(2026, 7, 14, 13, 0, 0, 0, time.UTC)
	huddle := sim.HuddleID("")
	if huddled {
		huddle = "h-farm"
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		PublishedAt:      published,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			omenElizabethID: {
				Kind: sim.KindNPCShared, DisplayName: "Elizabeth Ellis", Role: "farmer", State: sim.StateIdle,
				Pos: sim.TilePos{X: 40, Y: 40}, CurrentHuddleID: huddle, Needs: map[sim.NeedKey]int{},
				Acquaintances: map[string]sim.Acquaintance{"John Ellis": {}},
			},
			omenJohnID: {
				Kind: sim.KindNPCShared, DisplayName: "John Ellis", Role: "farmer", State: sim.StateIdle,
				Pos: sim.TilePos{X: 41, Y: 40}, CurrentHuddleID: huddle, Needs: map[sim.NeedKey]int{},
				Acquaintances: map[string]sim.Acquaintance{"Elizabeth Ellis": {}},
			},
		},
	}
	if huddled {
		snap.Huddles = map[sim.HuddleID]*sim.Huddle{
			huddle: {Members: map[sim.ActorID]struct{}{omenElizabethID: {}, omenJohnID: {}}},
		}
	}
	return snap
}

func omenWitnessScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap := omenFarmSnapshot(false)
	snap.Actors[omenElizabethID].Rumors = []sim.KnownRumor{{
		Topic: sim.RumorTopicOmenSourMilk, SubjectID: omenElizabethID, SubjectName: "Elizabeth Ellis",
		HeardAt: snap.PublishedAt.Add(-time.Minute), FirstHand: true,
	}}
	warrants := []sim.WarrantMeta{{
		TriggerActorID: omenElizabethID,
		Reason: sim.SourceActivityCompletedWarrantReason{
			ActivityKind: sim.SourceActivityMilk, Item: "milk", SourceName: "Cow 2",
			NarrationText: sim.OmenWitnessLine(sim.OmenSourMilk, "Cow 2"),
		},
	}}
	return snap, omenElizabethID, warrants
}

func omenWordWithSubjectPresentScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap := omenFarmSnapshot(true)
	snap.Actors[omenJohnID].Rumors = []sim.KnownRumor{{
		Topic: sim.RumorTopicOmenSourMilk, SubjectID: omenElizabethID, SubjectName: "Elizabeth Ellis",
		Rung: 1, HeardAt: snap.PublishedAt.Add(-30 * time.Minute),
	}}
	return snap, omenJohnID, nil
}

// TestGoldensNoEngineTextSaysWitch — across the whole matrix, no prompt carries
// the word "witch" (LLM-841: the engine never names witchcraft; the village may).
// Every scenario's prompt is engine-written text, so this is the place to pin it.
func TestGoldensNoEngineTextSaysWitch(t *testing.T) {
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			if strings.Contains(strings.ToLower(renderScenario(sc)), "witch") {
				t.Errorf("scenario %q: the prompt names witchcraft", sc.name)
			}
		})
	}
}

// TestBuildVillageWordOmenExceptions — the omen topics relax both content
// filters: the subject's own omen shows, worded to them, and an omen shows with
// its subject present. short_on_coin about the same person stays hidden.
func TestBuildVillageWordOmenExceptions(t *testing.T) {
	now := villageWordTestNow()
	a := &sim.ActorSnapshot{
		Kind: sim.KindNPCShared,
		Rumors: []sim.KnownRumor{
			{Topic: sim.RumorTopicOmenColdHearth, SubjectID: "self", SubjectName: "Hannah Boggs", HeardAt: now, FirstHand: true},
			{Topic: sim.RumorTopicOmenFlatBread, SubjectID: "present", SubjectName: "Silence Walker", Rung: 2, HeardAt: now.Add(-time.Minute)},
			vwRumor("present", "Silence Walker", 0, now.Add(-2*time.Minute), false),
		},
	}
	s := SurroundingsView{HuddleMembers: []HuddleMember{{ID: "present"}}}
	got := buildVillageWord("self", a, s, now)
	if len(got) != 2 {
		t.Fatalf("got %d views, want the two omens: %+v", len(got), got)
	}
	if got[0].Clause != "your hearth fire would not stay lit" || !got[0].FirstHand {
		t.Errorf("own omen view = %+v", got[0])
	}
	if got[1].Clause != "nothing will rise in Silence Walker's oven" || got[1].FirstHand {
		t.Errorf("present-subject omen view = %+v", got[1])
	}
}
