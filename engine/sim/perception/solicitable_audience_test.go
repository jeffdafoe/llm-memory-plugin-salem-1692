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
		if got := hasSolicitableAudience(snap, "walker", subject, c.surr); got != c.want {
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
	if !hasSolicitableAudience(snap, "subject", subject, surr) {
		t.Error("two anchor-less actors: want solicitable (empty != empty), got not")
	}
}

func TestHasSolicitableAudience_NilGuards(t *testing.T) {
	surr := SurroundingsView{CoPresent: []HuddleMember{solicitMember("x")}}
	if hasSolicitableAudience(nil, "x", &sim.ActorSnapshot{}, surr) {
		t.Error("nil snapshot: want false")
	}
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{"x": {}}}
	if hasSolicitableAudience(snap, "subject", nil, surr) {
		t.Error("nil subject: want false")
	}
	// Non-nil snapshot with a nil Actors map — reading a nil map is safe and
	// every candidate misses, so no one is solicitable (code_review).
	if hasSolicitableAudience(&sim.Snapshot{}, "subject", &sim.ActorSnapshot{}, surr) {
		t.Error("nil Actors map: want false")
	}
}

// LLM-181: an employer who already declined this worker drops out of the
// solicitable audience, even though it shares neither household nor workplace. The
// decline is the engine's "no one here can hire you" memory; keeping the refuser
// solicitable is what suppressed the seek-work directive and trapped a worker
// (Lewis Walker) re-soliciting the same refusal at the General Store.
func TestHasSolicitableAudience_DeclineAware(t *testing.T) {
	const (
		lewis  = sim.ActorID("lewis")
		josiah = sim.ActorID("josiah")
		john   = sim.ActorID("john")
	)
	// Both josiah and john are strangers (share neither home nor work with Lewis).
	subject := &sim.ActorSnapshot{HomeStructureID: "walker-residence"}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			josiah: {HomeStructureID: "thorne-house", WorkStructureID: "general-store"},
			john:   {HomeStructureID: "ellis-house", WorkStructureID: "tavern"},
		},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {ID: 1, WorkerID: lewis, EmployerID: josiah, State: sim.LaborStateDeclined},
		},
	}

	// Only the employer who declined is present → audience is empty (off-ramp arms).
	onlyJosiah := SurroundingsView{HuddleMembers: []HuddleMember{solicitMember(josiah)}}
	if hasSolicitableAudience(snap, lewis, subject, onlyJosiah) {
		t.Error("declined employer alone: want NOT solicitable (seek-work off-ramp should arm)")
	}

	// A second, un-declined stranger is still solicitable — the exclusion is
	// per-candidate, not whole-audience.
	bothPresent := SurroundingsView{HuddleMembers: []HuddleMember{solicitMember(josiah), solicitMember(john)}}
	if !hasSolicitableAudience(snap, lewis, subject, bothPresent) {
		t.Error("declined employer + fresh stranger: want solicitable (john is still a prospect)")
	}

	// The decline is worker-scoped: a different worker is unaffected by Lewis's decline.
	if !hasSolicitableAudience(snap, "anne", subject, onlyJosiah) {
		t.Error("different worker: Lewis's decline must not suppress another worker's prospect")
	}
}

// LLM-181: only a terminal Declined offer suppresses the prospect. A still-pending
// offer (the worker is waiting on an answer — that is subjectHasPendingLaborOffer's
// job) or any other state must NOT drop the employer from the audience.
func TestHasSolicitableAudience_OnlyDeclinedSuppresses(t *testing.T) {
	const (
		lewis  = sim.ActorID("lewis")
		josiah = sim.ActorID("josiah")
	)
	subject := &sim.ActorSnapshot{HomeStructureID: "walker-residence"}
	surr := SurroundingsView{HuddleMembers: []HuddleMember{solicitMember(josiah)}}
	withOffer := func(state sim.LaborLedgerState) *sim.Snapshot {
		return &sim.Snapshot{
			Actors: map[sim.ActorID]*sim.ActorSnapshot{
				josiah: {HomeStructureID: "thorne-house", WorkStructureID: "general-store"},
			},
			LaborLedger: map[sim.LaborID]*sim.LaborOffer{
				1: {ID: 1, WorkerID: lewis, EmployerID: josiah, State: state},
			},
		}
	}
	for _, state := range []sim.LaborLedgerState{
		sim.LaborStatePending, sim.LaborStateWorking, sim.LaborStateCompleted,
		sim.LaborStateExpired, sim.LaborStateFailedUnavailable,
	} {
		if !hasSolicitableAudience(withOffer(state), lewis, subject, surr) {
			t.Errorf("offer state %q: want still solicitable (only declined suppresses)", state)
		}
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
