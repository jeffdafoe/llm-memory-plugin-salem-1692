package perception

import (
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// huddle_part_golden_test.go — golden scenarios for the LLM-438 peer-naming
// huddle join/leave warrant lines. Registered into perceptionScenarios via
// init() so the whole-prompt golden + determinism harness
// (TestPerceptionGoldens) covers them alongside the rest.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "huddle_left_names_peers",
			summary: "LLM-438: Hannah has just walked away from a tavern conversation with Mercy Lewis (acquainted) " +
				"and Tabitha Porter (never interacted — unacquainted, no known trade). The golden pins the enriched " +
				"departure line 'You left the conversation with Mercy Lewis and a stranger.' — episodic continuity " +
				"against the instant re-greet / huddle-loop failure modes (cf. LLM-176, LLM-196) — replacing the " +
				"contentless 'You left the conversation.' Jeff flagged live. The acquaintance gate holds: Tabitha's " +
				"name appears NOWHERE in the prompt (she renders as 'a stranger' in both the warrant line and the " +
				"co-presence roster).",
			build: huddleLeftNamesPeers,
		},
		perceptionScenario{
			name: "huddle_joined_names_peers",
			summary: "LLM-438: the join twin — the same cast, but Hannah has just joined the huddle the two of them " +
				"were already in. The golden pins 'You joined a conversation with Mercy Lewis and a stranger.' " +
				"(members present when she arrived, acquaintance-gated) replacing the bare 'You joined a " +
				"conversation.'",
			build: huddleJoinedNamesPeers,
		},
	)
}

// huddlePartPeersFixture builds the shared cast: Hannah Putnam, keeper of the
// tavern, on shift inside it with Mercy Lewis (acquainted) and Tabitha Porter
// (unacquainted, roleless → "a stranger") co-present. Mercy and Tabitha are in
// huddle h1 together; whether Hannah shares it is the scenario's choice (she
// does after a join, doesn't after a leave). Clock-free → byte-stable.
func huddlePartPeersFixture(subjectInHuddle bool) (*sim.Snapshot, sim.ActorID) {
	const (
		hannahID  = sim.ActorID("hannah")
		mercyID   = sim.ActorID("mercy")
		tabithaID = sim.ActorID("tabitha")
		tavern    = sim.StructureID("tavern")
	)
	start, end := 360, 1260 // working hours 06:00–21:00
	now := 540              // 09:00 — morning, on shift
	subjectHuddle := sim.HuddleID("")
	if subjectInHuddle {
		subjectHuddle = "h1"
	}
	hannah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Hannah Putnam",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 10},
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		CurrentHuddleID:   subjectHuddle,
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
	// No Role and not in Hannah's Acquaintances — the descriptor gate must
	// show her as "a stranger", never "Tabitha Porter".
	tabitha := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Tabitha Porter",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 10, Y: 11},
		InsideStructureID: tavern,
		CurrentHuddleID:   "h1",
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			hannahID: hannah, mercyID: mercy, tabithaID: tabitha,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			tavern: plainStructure(tavern, "Tavern"),
		},
	}
	if subjectInHuddle {
		// The huddle roster reads snap.Huddles[CurrentHuddleID].Members.
		snap.Huddles = map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{
				hannahID: {}, mercyID: {}, tabithaID: {},
			}},
		}
	} else {
		// Not huddled: the "within earshot" line reads the world-side
		// precomputed audience — she stands in the room the two of them are
		// still talking in.
		hannah.ColocatedAudienceIDs = []sim.ActorID{mercyID, tabithaID}
	}
	return snap, hannahID
}

func huddleLeftNamesPeers() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, hannahID := huddlePartPeersFixture(false)
	warrants := []sim.WarrantMeta{{
		TriggerActorID: hannahID,
		Reason:         sim.HuddlePartReason{K: sim.WarrantKindHuddleLeft, PeerIDs: []sim.ActorID{"mercy", "tabitha"}},
		SourceEventID:  1,
	}}
	return snap, hannahID, warrants
}

func huddleJoinedNamesPeers() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, hannahID := huddlePartPeersFixture(true)
	warrants := []sim.WarrantMeta{{
		TriggerActorID: hannahID,
		Reason:         sim.HuddlePartReason{K: sim.WarrantKindHuddleJoined, PeerIDs: []sim.ActorID{"mercy", "tabitha"}},
		SourceEventID:  1,
	}}
	return snap, hannahID, warrants
}
