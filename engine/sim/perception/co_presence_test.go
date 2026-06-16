package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ZBBS-WORK-407 build layer: an UNHUDDLED actor's co-present audience surfaces
// in Surroundings.CoPresent (not HuddleMembers), from the world-precomputed
// ActorSnapshot.ColocatedAudienceIDs, carrying the same acquaintance gating the
// huddle roster uses.
func TestBuild_SurroundingsCoPresentWhenUnhuddled(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Kind:                 sim.KindNPCShared,
		InsideStructureID:    "inn",
		ColocatedAudienceIDs: []sim.ActorID{"hannah", "stranger"},
		Acquaintances:        map[string]sim.Acquaintance{"Hannah Boggs": {}},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"prudence": subj,
			"hannah":   {DisplayName: "Hannah Boggs", Role: "innkeeper"},
			"stranger": {DisplayName: "Goodman Stark", Role: "farmer"},
		},
	}
	p := Build(snap, "prudence", nil)
	if len(p.Surroundings.HuddleMembers) != 0 {
		t.Fatalf("unhuddled: HuddleMembers should be empty, got %v", p.Surroundings.HuddleMembers)
	}
	if len(p.Surroundings.CoPresent) != 2 {
		t.Fatalf("CoPresent = %d, want 2", len(p.Surroundings.CoPresent))
	}
	byID := make(map[sim.ActorID]HuddleMember, 2)
	for _, m := range p.Surroundings.CoPresent {
		byID[m.ID] = m
	}
	if !byID["hannah"].Acquainted {
		t.Errorf("Hannah is in Acquaintances — want Acquainted=true")
	}
	if byID["stranger"].Acquainted {
		t.Errorf("stranger is not in Acquaintances — want Acquainted=false")
	}
}

// A huddled actor uses HuddleMembers and ignores ColocatedAudienceIDs, so the
// co-presence line and the huddle line never double-render (ZBBS-WORK-407).
func TestBuild_NoCoPresentWhenHuddled(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Kind:                 sim.KindNPCShared,
		InsideStructureID:    "inn",
		CurrentHuddleID:      "h1",
		ColocatedAudienceIDs: []sim.ActorID{"hannah"}, // present but must be ignored
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"prudence": subj,
			"john":     {DisplayName: "John Ellis"},
			"hannah":   {DisplayName: "Hannah Boggs"},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"prudence": {}, "john": {}}},
		},
	}
	p := Build(snap, "prudence", nil)
	if len(p.Surroundings.CoPresent) != 0 {
		t.Errorf("huddled: CoPresent should be empty, got %v", p.Surroundings.CoPresent)
	}
	if len(p.Surroundings.HuddleMembers) != 1 || p.Surroundings.HuddleMembers[0].ID != "john" {
		t.Errorf("HuddleMembers = %v, want [john]", p.Surroundings.HuddleMembers)
	}
}

// ZBBS-WORK-407 render layer: the "## Around you" company line has three shapes —
// huddled (existing), co-present-but-not-huddled, and alone. renderSurroundings
// picks by which slice is populated; co-presence renders every turn.
func TestRenderSurroundings_CoPresentNamesThemPlural(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{
		InsideStructureID: "inn",
		StructureName:     "the Inn",
		CoPresent: []HuddleMember{
			{ID: "hannah", DisplayName: "Hannah Boggs", Acquainted: true},
			{ID: "john", DisplayName: "John Ellis", Acquainted: true},
		},
	})
	out := b.String()
	if !strings.Contains(out, "Hannah Boggs and John Ellis are here with you") {
		t.Errorf("co-present plural line missing in:\n%s", out)
	}
	if !strings.Contains(out, "speak to start talking") {
		t.Errorf("co-present speak nudge missing in:\n%s", out)
	}
}

func TestRenderSurroundings_CoPresentSingularVerb(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{
		InsideStructureID: "inn",
		StructureName:     "the Inn",
		CoPresent:         []HuddleMember{{ID: "hannah", DisplayName: "Hannah Boggs", Acquainted: true}},
	})
	if out := b.String(); !strings.Contains(out, "Hannah Boggs is here with you") {
		t.Errorf("singular co-present line wrong in:\n%s", out)
	}
}

func TestRenderSurroundings_UnacquaintedCoPresentUsesDescriptor(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{
		InsideStructureID: "inn",
		StructureName:     "the Inn",
		CoPresent:         []HuddleMember{{ID: "x", DisplayName: "Goodman Stark", Role: "farmer", Acquainted: false}},
	})
	if out := b.String(); !strings.Contains(out, "the farmer is here with you") {
		t.Errorf("unacquainted co-present should render descriptor 'the farmer', got:\n%s", out)
	}
}

func TestRenderSurroundings_AloneStatesItPlainly(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{InsideStructureID: "inn", StructureName: "the Inn"})
	if out := b.String(); !strings.Contains(out, "no one else here to hear you speak") {
		t.Errorf("alone line missing in:\n%s", out)
	}
}

// ZBBS-WORK-422 build layer: a co-present member whose most recent
// ActionTypeWalked is within coPresentJustArrivedWindow is flagged JustArrived;
// one that arrived long ago (settled in) is not. The arrival is read from the
// snapshot action log, so no per-actor arrival state is needed.
func TestBuild_CoPresentJustArrivedFromActionLog(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
		Kind:                 sim.KindNPCShared,
		InsideStructureID:    "inn",
		ColocatedAudienceIDs: []sim.ActorID{"hannah", "newcomer"},
	}
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"prudence": subj,
			"hannah":   {DisplayName: "Hannah Boggs"},
			"newcomer": {DisplayName: "Goodman Stark"},
		},
		ActionLog: []sim.ActionLogEntry{
			// Hannah arrived 10 min ago — settled in, not "just arrived".
			{ActorID: "hannah", ActionType: sim.ActionTypeWalked, OccurredAt: now.Add(-10 * time.Minute)},
			// Newcomer arrived 15s ago — inside the window.
			{ActorID: "newcomer", ActionType: sim.ActionTypeWalked, OccurredAt: now.Add(-15 * time.Second)},
		},
	}
	p := Build(snap, "prudence", nil)
	byID := make(map[sim.ActorID]HuddleMember, 2)
	for _, m := range p.Surroundings.CoPresent {
		byID[m.ID] = m
	}
	if !byID["newcomer"].JustArrived {
		t.Errorf("newcomer arrived 15s ago — want JustArrived=true")
	}
	if byID["hannah"].JustArrived {
		t.Errorf("Hannah arrived 10 min ago — want JustArrived=false")
	}
}

// ZBBS-WORK-422 render layer: a JustArrived co-present member is tagged
// "(just arrived)"; a settled member is not.
func TestRenderSurroundings_JustArrivedTagged(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{
		InsideStructureID: "inn",
		StructureName:     "the Inn",
		CoPresent: []HuddleMember{
			{ID: "hannah", DisplayName: "Hannah Boggs", Acquainted: true},
			{ID: "ezekiel", DisplayName: "Ezekiel Cheever", Acquainted: true, JustArrived: true},
		},
	})
	out := b.String()
	if !strings.Contains(out, "Ezekiel Cheever (just arrived)") {
		t.Errorf("just-arrived member should be tagged, got:\n%s", out)
	}
	if strings.Contains(out, "Hannah Boggs (just arrived)") {
		t.Errorf("settled member should NOT be tagged, got:\n%s", out)
	}
}
