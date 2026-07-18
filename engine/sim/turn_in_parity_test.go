package sim_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// turn_in_parity_test.go — LLM-447. The cross-package gate-parity guard.
//
// It lives in the EXTERNAL sim_test package because it is the only place that can
// see both sides of the seam: sim.ExportedNpcMayTurnIn (via export_test.go, which
// compiles into this test binary) and perception.Build. Going through the public
// Build (rather than the unexported builder) also exercises the real wiring — the
// payload field and the LLM-36 supersede — instead of the gate in isolation. The
// perception package's own test binary cannot reach sim's unexported predicate,
// and sim cannot import perception in production code.

// TestTurnInCueMatchesSubstrateAcrossTheMatrix is the parity guard for the one
// structural weakness of this design: the tool's gate lives in the sim package
// (sim.npcMayTurnIn, over *World) and its cue's gate lives here (buildTurnInChoice,
// over *Snapshot). Two implementations of one rule can drift, and the code review
// of LLM-447 flagged exactly that risk. The world-side refactor (npcSleepArmFor)
// removed drift BETWEEN the two sim-side gates; it does nothing for this seam.
//
// So: walk every combination of residency, schedule shape and clock, build BOTH a
// World and a matching Snapshot from one fixture, and require the two predicates
// to agree. Disagreement in either direction is a bug — a cue with no tool wastes
// the model's turn on a call the substrate refuses, and a tool with no cue is an
// affordance the model was never told about.
//
// This table is what would have caught the night-shift lodger hole (the lodger arm
// inheriting the auto-bed's deliberate absence of a shift check, which is safe at a
// 22:00 window and not at a dusk one) without the review having to reason it out.
func TestTurnInCueMatchesSubstrateAcrossTheMatrix(t *testing.T) {
	const (
		dawnMin = 7 * 60
		duskMin = 19 * 60
	)
	type fixture struct {
		name       string
		homed      bool   // has a home structure
		inside     string // "home" | "inn" | "tavern" | "" (outdoors)
		lodging    bool   // holds an active grant on the inn
		worker     bool
		schedStart int // -1 for unscheduled
		schedEnd   int
	}
	fixtures := []fixture{
		{"unscheduled worker at home", true, "home", false, true, -1, -1},
		{"unscheduled non-worker at home", true, "home", false, false, -1, -1},
		{"day-shift worker at home", true, "home", false, true, 9 * 60, 19 * 60},
		{"night-shift keeper at home", true, "home", false, true, 16 * 60, 3 * 60},
		{"day-shift worker in the tavern", true, "tavern", false, true, 9 * 60, 19 * 60},
		{"homed worker outdoors", true, "", false, true, -1, -1},
		{"lodger at its inn", false, "inn", true, true, -1, -1},
		{"night-shift lodger at its inn", false, "inn", true, true, 16 * 60, 3 * 60},
		{"day-shift lodger at its inn", false, "inn", true, true, 7 * 60, 16 * 60},
		{"lodger away from its inn", false, "tavern", true, true, -1, -1},
		{"homeless non-lodger in the inn", false, "inn", false, true, -1, -1},
	}
	clock := []int{0, 6*60 + 59, 7 * 60, 12 * 60, 18*60 + 59, 19 * 60, 20*60 + 30, 22 * 60, 23 * 60}

	for _, f := range fixtures {
		for _, nowMin := range clock {
			name := fmt.Sprintf("%s@%02d:%02d", f.name, nowMin/60, nowMin%60)
			t.Run(name, func(t *testing.T) {
				// --- world side ---
				a := &sim.Actor{
					ID:                "subject",
					Kind:              sim.KindNPCShared,
					InsideStructureID: sim.StructureID(f.inside),
				}
				if f.homed {
					a.HomeStructureID = "home"
				}
				if f.worker {
					a.Attributes = map[string][]byte{sim.AttrWorker: nil}
				}
				if f.schedStart >= 0 {
					s, e := f.schedStart, f.schedEnd
					a.ScheduleStartMin, a.ScheduleEndMin = &s, &e
				}
				when := time.Date(2026, 7, 16, nowMin/60, nowMin%60, 0, 0, time.UTC)
				grantExpiry := when.Add(72 * time.Hour)
				if f.lodging {
					a.RoomAccess = map[sim.RoomAccessKey]*sim.RoomAccess{
						{RoomID: 1, Source: sim.AccessSourceLedger}: {
							RoomID: 1, Source: sim.AccessSourceLedger, Active: true, ExpiresAt: &grantExpiry,
						},
					}
				}
				w := &sim.World{
					Actors:         map[sim.ActorID]*sim.Actor{a.ID: a},
					NarrationPools: nil,
					Settings: sim.WorldSettings{
						Location:                 time.UTC,
						DawnTime:                 "07:00",
						DuskTime:                 "19:00",
						LodgingBedtimeHour:       22,
						NPCSleepMaxDurationHours: 12,
					},
					Structures: map[sim.StructureID]*sim.Structure{
						"home": {ID: "home", DisplayName: "Cottage"},
						"inn": {ID: "inn", DisplayName: "the Inn", Rooms: []*sim.Room{
							{ID: 1, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
						}},
						"tavern": {ID: "tavern", DisplayName: "the Tavern"},
					},
				}
				substrate := sim.ExportedNpcMayTurnIn(w, a, when)

				// --- perception side: the same fixture as a snapshot ---
				min := nowMin
				actorSnap := &sim.ActorSnapshot{
					Kind:              sim.KindNPCShared,
					State:             sim.StateIdle,
					InsideStructureID: sim.StructureID(f.inside),
					HomeStructureID:   a.HomeStructureID,
					ScheduleStartMin:  a.ScheduleStartMin,
					ScheduleEndMin:    a.ScheduleEndMin,
					Needs:             map[sim.NeedKey]int{},
				}
				if f.worker {
					actorSnap.AttributeSlugs = []string{sim.AttrWorker}
				}
				if f.lodging {
					actorSnap.RoomAccess = map[sim.RoomAccessKey]*sim.RoomAccess{
						{RoomID: 1, Source: sim.AccessSourceLedger}: {
							RoomID: 1, Source: sim.AccessSourceLedger, Active: true, ExpiresAt: &grantExpiry,
						},
					}
				}
				snap := &sim.Snapshot{
					PublishedAt:          when,
					LocalMinuteOfDay:     &min,
					DawnMinute:           dawnMin,
					DuskMinute:           duskMin,
					DawnDuskMinuteOK:     true,
					LodgingBedtimeMinute: 22 * 60,
					Actors:               map[sim.ActorID]*sim.ActorSnapshot{"subject": actorSnap},
					Structures:           w.Structures,
				}
				cue := perception.Build(snap, "subject", nil).TurnInChoice != nil

				if cue != substrate {
					t.Errorf("gate drift: cue=%v substrate=%v.\n"+
						"The turn_in tool is advertised iff buildTurnInChoice is non-nil, but sim.npcMayTurnIn "+
						"decides whether the call succeeds. cue&&!substrate wastes the model's turn on a refusal; "+
						"substrate&&!cue hides an affordance it was never offered.", cue, substrate)
				}
			})
		}
	}
}
