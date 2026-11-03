package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// departed_member_golden_test.go — LLM-901. A huddle can still list a member
// whose actor has left the world (live: the messenger, removed by visitor
// cleanup while eating at the Inn). Perception must not show that id at all:
// rendered, it read as "a stranger" whom every NPC greeted and pitched to for
// hours, and who never answered. Registered into perceptionScenarios via init()
// so TestPerceptionGoldens pins the whole prompt.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "huddle_departed_member_not_shown",
			summary: "LLM-901: Hannah keeps the tavern, in conversation h1 with Mercy Lewis (acquainted). h1 still " +
				"lists 'roger', a departed visitor with no actor in the snapshot. The golden pins a company line " +
				"and a custom section naming Mercy only: no 'a stranger', no third party to greet or sell to.",
			build: huddleDepartedMemberNotShown,
		},
	)
}

func huddleDepartedMemberNotShown() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		hannahID = sim.ActorID("hannah")
		mercyID  = sim.ActorID("mercy")
		rogerID  = sim.ActorID("roger") // in h1.Members, absent from snap.Actors
		tavern   = sim.StructureID("tavern")
	)
	start, end := 360, 1260 // working hours 06:00–21:00
	now := 450              // 07:30 — the breakfast hour of the live case
	hannah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Hannah Putnam",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 10},
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		CurrentHuddleID:   "h1",
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             20,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances: map[string]sim.Acquaintance{
			"Mercy Lewis": {},
		},
	}
	mercy := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Mercy Lewis",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 11, Y: 10},
		InsideStructureID: tavern,
		CurrentHuddleID:   "h1",
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			hannahID: hannah, mercyID: mercy,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			tavern: plainStructure(tavern, "Tavern"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{
				hannahID: {}, mercyID: {}, rogerID: {},
			}},
		},
	}
	return snap, hannahID, nil
}

// TestGoldensDepartedMemberNeverAStranger pins the scenario's point directly,
// so a golden regenerated carelessly cannot quietly accept the ghost back.
func TestGoldensDepartedMemberNeverAStranger(t *testing.T) {
	// Pin the company line itself: "a stranger" also occurs in ordinary trade
	// wording ("a stranger's purse"), so a whole-prompt search would misfire.
	text := combinedPrompt(Render(Build(huddleDepartedMemberNotShown()), DefaultRenderConfig()))
	const want = "You are inside the Tavern, your workplace, with Mercy Lewis.\n"
	if !strings.Contains(text, want) {
		t.Errorf("company line should name Mercy Lewis only (%q):\n%s", want, text)
	}
	if strings.Contains(text, ", a stranger") || strings.Contains(text, "and a stranger") {
		t.Errorf("a departed huddle member rendered as \"a stranger\":\n%s", text)
	}
}

// TestGoldensCoPresentMembersExistInSnapshot is the LLM-901 cross-scenario
// invariant: across the whole matrix, every co-present or huddle member the
// payload carries has an actor in the snapshot. A member without one is a
// departed actor, and showing it is how the village came to greet a ghost.
func TestGoldensCoPresentMembersExistInSnapshot(t *testing.T) {
	for _, sc := range perceptionScenarios {
		snap, actorID, warrants := sc.build()
		s := Build(snap, actorID, warrants).Surroundings
		groups := map[string][]HuddleMember{
			"HuddleMembers":    s.HuddleMembers,
			"HuddleAway":       s.HuddleAway,
			"CoPresent":        s.CoPresent,
			"CoPresentResting": s.CoPresentResting,
			"CoPresentAsleep":  s.CoPresentAsleep,
		}
		for group, members := range groups {
			for _, m := range members {
				if snap.Actors[m.ID] == nil {
					t.Errorf("%s: %s carries %q, which has no actor in the snapshot", sc.name, group, m.ID)
				}
			}
		}
	}
}
