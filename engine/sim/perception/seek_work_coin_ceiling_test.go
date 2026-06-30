package perception

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// seek_work_coin_ceiling_test.go — LLM-194. The seek-work coin ceiling: a workless
// worker holding coin at/above the ceiling is "comfortable" and gets neither the
// businesses directory (SeekWorkPlaces) nor the solicit_work affordance (CanSolicitWork),
// so it stops hustling and drains its purse via ordinary consumption. subjectIsComfortable
// is the shared perception-side predicate; sim.workerIsComfortable is its warrant-side
// twin. Both reduce to coins >= effective ceiling, so the warrant and the cues agree.

// TestSubjectIsComfortable pins the shared predicate: the >= boundary, the zero-ceiling
// (test snapshot) resolving to the default, an explicit live ceiling, and the nil guards
// that fail open to seeking.
func TestSubjectIsComfortable(t *testing.T) {
	cases := []struct {
		name    string
		ceiling int // snap.SeekWorkCoinCeiling; 0 resolves to sim.SeekWorkCoinCeilingDefault
		coins   int
		want    bool
	}{
		{"zero ceiling resolves to default, one under", 0, sim.SeekWorkCoinCeilingDefault - 1, false},
		{"zero ceiling resolves to default, at boundary", 0, sim.SeekWorkCoinCeilingDefault, true},
		{"zero ceiling resolves to default, over", 0, sim.SeekWorkCoinCeilingDefault + 5, true},
		{"explicit ceiling, one under", 40, 39, false},
		{"explicit ceiling, at boundary", 40, 40, true},
		{"explicit ceiling, over", 40, 80, true},
		{"broke is never comfortable", 0, 0, false},
	}
	for _, c := range cases {
		snap := &sim.Snapshot{SeekWorkCoinCeiling: c.ceiling}
		subj := &sim.ActorSnapshot{Coins: c.coins}
		if got := subjectIsComfortable(snap, subj); got != c.want {
			t.Errorf("%s: subjectIsComfortable(ceiling=%d, coins=%d) = %v, want %v", c.name, c.ceiling, c.coins, got, c.want)
		}
	}

	// Nil guards fail open (not comfortable → keeps seeking), never panic.
	if subjectIsComfortable(nil, &sim.ActorSnapshot{Coins: 999}) {
		t.Error("nil snapshot: want not comfortable")
	}
	if subjectIsComfortable(&sim.Snapshot{}, nil) {
		t.Error("nil subject: want not comfortable")
	}
}

// TestBuild_ComfortableWorkerSuppressesDirectory proves the directory gate (LLM-194):
// a workless idle worker alone (no solicitable audience) gets the businesses directory
// ONLY while under the ceiling; at/above it the directory is empty, and the live ceiling
// override on the snapshot re-arms a worker the default would have silenced.
func TestBuild_ComfortableWorkerSuppressesDirectory(t *testing.T) {
	worker := func(coins int) *sim.ActorSnapshot {
		a := actorSnap(sim.StateIdle, "", 0, 0, "", coins)
		a.AttributeSlugs = []string{sim.AttrWorker}
		return a
	}
	mk := func(subj *sim.ActorSnapshot, ceiling int) *sim.Snapshot {
		return &sim.Snapshot{
			SeekWorkCoinCeiling: ceiling,
			Actors:              map[sim.ActorID]*sim.ActorSnapshot{"lewis": subj},
			Structures:          map[sim.StructureID]*sim.Structure{"tav": {DisplayName: "Tavern"}},
			VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
				"tav": {ID: "tav", Tags: []string{"business"}},
			},
		}
	}

	// At the default ceiling → comfortable → no directory.
	if p := Build(mk(worker(sim.SeekWorkCoinCeilingDefault), 0), "lewis", nil); len(p.SeekWorkPlaces) != 0 {
		t.Errorf("comfortable worker (coins=%d, default ceiling): SeekWorkPlaces = %+v, want empty",
			sim.SeekWorkCoinCeilingDefault, p.SeekWorkPlaces)
	}
	// One coin under → still seeks (boundary is >=).
	if p := Build(mk(worker(sim.SeekWorkCoinCeilingDefault-1), 0), "lewis", nil); len(p.SeekWorkPlaces) != 1 {
		t.Errorf("worker one coin under the ceiling: SeekWorkPlaces = %+v, want [Tavern]", p.SeekWorkPlaces)
	}
	// Live override: a 60 ceiling re-arms a 40-coin worker the default (25) would suppress.
	if p := Build(mk(worker(40), 60), "lewis", nil); len(p.SeekWorkPlaces) != 1 {
		t.Errorf("worker under a tuned 60 ceiling: SeekWorkPlaces = %+v, want [Tavern]", p.SeekWorkPlaces)
	}
	// And at the tuned ceiling → suppressed.
	if p := Build(mk(worker(60), 60), "lewis", nil); len(p.SeekWorkPlaces) != 0 {
		t.Errorf("worker at the tuned 60 ceiling: SeekWorkPlaces = %+v, want empty", p.SeekWorkPlaces)
	}
}

// TestBuild_ComfortableWorkerNoSolicitAffordance proves the affordance gate (LLM-194):
// with a solicitable stranger employer co-present (so CanSolicitWork would otherwise
// fire), a comfortable worker is NOT offered solicit_work — it doesn't need the work.
// The poor worker is the positive control.
func TestBuild_ComfortableWorkerNoSolicitAffordance(t *testing.T) {
	const (
		lewis   = sim.ActorID("lewis")
		josiah  = sim.ActorID("josiah")
		huddle  = sim.HuddleID("h1")
		commons = sim.StructureID("commons")
		store   = sim.StructureID("general_store")
		tavern  = sim.StructureID("tavern")
	)
	mk := func(coins int, workplace sim.StructureID) *sim.Snapshot {
		worker := &sim.ActorSnapshot{
			Kind:              sim.KindNPCShared,
			DisplayName:       "Lewis Walker",
			State:             sim.StateIdle,
			InsideStructureID: commons,
			HomeStructureID:   "walker_residence",
			WorkStructureID:   workplace, // "" = workless; a resolvable id = employed
			CurrentHuddleID:   huddle,
			Coins:             coins,
			AttributeSlugs:    []string{sim.AttrWorker},
			Needs:             map[sim.NeedKey]int{},
		}
		// Josiah is a structural stranger to Lewis (different home; the workless Lewis
		// shares no workplace, and the employed Lewis works the Tavern not the Store) → a
		// solicitable employer. But-for comfort, his presence makes CanSolicitWork true.
		employer := &sim.ActorSnapshot{
			Kind:              sim.KindNPCStateful,
			DisplayName:       "Josiah Thorne",
			State:             sim.StateIdle,
			InsideStructureID: commons,
			HomeStructureID:   "thorne_house",
			WorkStructureID:   store,
			CurrentHuddleID:   huddle,
			Needs:             map[sim.NeedKey]int{},
		}
		return &sim.Snapshot{
			NeedThresholds: sim.NeedThresholds{},
			Actors:         map[sim.ActorID]*sim.ActorSnapshot{lewis: worker, josiah: employer},
			Structures: map[sim.StructureID]*sim.Structure{
				commons: plainStructure(commons, "Village Commons"),
				store:   plainStructure(store, "General Store"),
				tavern:  plainStructure(tavern, "Tavern"),
			},
			Huddles: map[sim.HuddleID]*sim.Huddle{
				huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{lewis: {}, josiah: {}}},
			},
		}
	}

	// Positive control: a broke WORKLESS worker with a solicitable employer present IS
	// offered solicit_work.
	if p := Build(mk(0, ""), lewis, nil); !p.CanSolicitWork {
		t.Fatal("poor workless worker with a solicitable employer present: CanSolicitWork = false, want true (positive control)")
	}
	// The gate: a comfortable WORKLESS worker (coins at the ceiling), same audience → no affordance.
	if p := Build(mk(sim.SeekWorkCoinCeilingDefault, ""), lewis, nil); p.CanSolicitWork {
		t.Error("comfortable workless worker: CanSolicitWork = true, want false (suppressed by the seek-work coin ceiling)")
	}
	// Scope guard (code_review): an EMPLOYED worker (resolvable workplace) at/above the
	// ceiling KEEPS the solicit affordance — comfort suppression is workless-only, so the
	// ceiling must not silence a worker who already has a post. Lewis works the Tavern
	// (not Josiah's Store), so they are not coworkers and the audience stays solicitable.
	if p := Build(mk(sim.SeekWorkCoinCeilingDefault+50, tavern), lewis, nil); !p.CanSolicitWork {
		t.Error("comfortable EMPLOYED worker: CanSolicitWork = false, want true (comfort is workless-scoped; an employed worker is unaffected by the ceiling)")
	}
}
