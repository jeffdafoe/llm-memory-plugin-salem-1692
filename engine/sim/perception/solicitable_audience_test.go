package perception

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// solicitable_audience_test.go — LLM-145. The CanSolicitWork gate narrows the
// shared HasAudience() predicate to hasSolicitableAudience: a worker can only be
// advertised solicit_work when at least one co-present actor is NEITHER a
// housemate (same home structure) NOR a co-worker (same work structure). Without
// this, the four Walkers — who all live at the Walker Residence — woke broke and
// bid each other for coin none of them had.

func solicitMember(id sim.ActorID) HuddleMember { return HuddleMember{ID: id} }

func TestHasSolicitableAudience(t *testing.T) {
	const (
		walkerHome = sim.StructureID("walker-residence")
		ellisFarm  = sim.StructureID("ellis-farm")
		craneHouse = sim.StructureID("crane-house")
		smithy     = sim.StructureID("smithy")
	)

	// Subject: a Walker who lives at the Walker Residence and works Ellis Farm.
	subject := &sim.ActorSnapshot{HomeStructureID: walkerHome, WorkStructureID: ellisFarm}

	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{
		"housemate": {HomeStructureID: walkerHome},                          // shares home
		"coworker":  {WorkStructureID: ellisFarm},                           // shares work
		"stranger":  {HomeStructureID: craneHouse, WorkStructureID: smithy}, // shares neither
	}}

	cases := []struct {
		name string
		surr SurroundingsView
		want bool
	}{
		{"empty audience", SurroundingsView{}, false},
		{"only housemate (huddle)", SurroundingsView{HuddleMembers: []HuddleMember{solicitMember("housemate")}}, false},
		{"only coworker (huddle)", SurroundingsView{HuddleMembers: []HuddleMember{solicitMember("coworker")}}, false},
		{"only housemate (co-present)", SurroundingsView{CoPresent: []HuddleMember{solicitMember("housemate")}}, false},
		{"only kin (housemate + coworker)", SurroundingsView{HuddleMembers: []HuddleMember{solicitMember("housemate"), solicitMember("coworker")}}, false},
		{"stranger present", SurroundingsView{HuddleMembers: []HuddleMember{solicitMember("stranger")}}, true},
		{"stranger co-present", SurroundingsView{CoPresent: []HuddleMember{solicitMember("stranger")}}, true},
		{"mix kin + stranger", SurroundingsView{HuddleMembers: []HuddleMember{solicitMember("housemate"), solicitMember("stranger")}}, true},
		{"member absent from snapshot is skipped", SurroundingsView{HuddleMembers: []HuddleMember{solicitMember("ghost")}}, false},
	}
	for _, c := range cases {
		if got := hasSolicitableAudience(snap, subject, c.surr); got != c.want {
			t.Errorf("%s: hasSolicitableAudience() = %v, want %v", c.name, got, c.want)
		}
	}
}

// Two actors with NO home/work anchors must not be treated as sharing a
// household or a workplace — empty must never match empty. Otherwise a homeless,
// work-less worker (Ezekiel is homeless by design) could never solicit anyone
// equally anchor-less. LLM-145.
func TestHasSolicitableAudience_EmptyAnchorsDoNotShare(t *testing.T) {
	subject := &sim.ActorSnapshot{}
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{"other": {}}}
	surr := SurroundingsView{CoPresent: []HuddleMember{solicitMember("other")}}
	if !hasSolicitableAudience(snap, subject, surr) {
		t.Error("two anchor-less actors: want solicitable (empty != empty), got not")
	}
}

func TestHasSolicitableAudience_NilGuards(t *testing.T) {
	surr := SurroundingsView{CoPresent: []HuddleMember{solicitMember("x")}}
	if hasSolicitableAudience(nil, &sim.ActorSnapshot{}, surr) {
		t.Error("nil snapshot: want false")
	}
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{"x": {}}}
	if hasSolicitableAudience(snap, nil, surr) {
		t.Error("nil subject: want false")
	}
}

func TestSharesHouseholdAndWorkplace(t *testing.T) {
	empty := &sim.ActorSnapshot{}
	if sharesHousehold(empty, empty) {
		t.Error("sharesHousehold(empty, empty) = true, want false (empty never matches)")
	}
	if sharesWorkplace(empty, empty) {
		t.Error("sharesWorkplace(empty, empty) = true, want false (empty never matches)")
	}

	home := &sim.ActorSnapshot{HomeStructureID: "h"}
	if !sharesHousehold(home, &sim.ActorSnapshot{HomeStructureID: "h"}) {
		t.Error("same non-empty home: want shared")
	}
	if sharesHousehold(home, &sim.ActorSnapshot{HomeStructureID: "other"}) {
		t.Error("different home: want not shared")
	}

	work := &sim.ActorSnapshot{WorkStructureID: "w"}
	if !sharesWorkplace(work, &sim.ActorSnapshot{WorkStructureID: "w"}) {
		t.Error("same non-empty work: want shared")
	}
	if sharesWorkplace(work, &sim.ActorSnapshot{WorkStructureID: "other"}) {
		t.Error("different work: want not shared")
	}
}
