package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// sharedSnap is a test-fixture actor snapshot helper for a shared-VA
// actor with optional narrative, relationships, and acquaintances.
func sharedSnap(id sim.ActorID, name string, huddle sim.HuddleID) *sim.ActorSnapshot {
	return &sim.ActorSnapshot{
		Kind:            sim.KindNPCShared,
		DisplayName:     name,
		State:           sim.StateIdle,
		CurrentHuddleID: huddle,
		Needs:           map[sim.NeedKey]int{},
	}
}

func peerSnap(id sim.ActorID, name, role string, kind sim.ActorKind, huddle sim.HuddleID) *sim.ActorSnapshot {
	return &sim.ActorSnapshot{
		Kind:            kind,
		DisplayName:     name,
		Role:            role,
		State:           sim.StateIdle,
		CurrentHuddleID: huddle,
		Needs:           map[sim.NeedKey]int{},
	}
}

func TestBuild_NarrativeStatePopulatedForShared(t *testing.T) {
	a := sharedSnap("hannah", "Hannah", "")
	a.Narrative = &sim.NarrativeState{
		SeedText:        "You are Hannah, daughter of the innkeeper.",
		EvolvingSummary: "Has been worried about the harvest.",
	}
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{"hannah": a}}

	p := Build(snap, "hannah", nil)
	if p.NarrativeState == nil {
		t.Fatal("NarrativeState nil for shared actor with populated narrative")
	}
	if p.NarrativeState.SeedText != a.Narrative.SeedText {
		t.Errorf("SeedText = %q", p.NarrativeState.SeedText)
	}
	if p.NarrativeState.EvolvingSummary != a.Narrative.EvolvingSummary {
		t.Errorf("EvolvingSummary = %q", p.NarrativeState.EvolvingSummary)
	}
}

func TestBuild_NarrativeStateNilForStateful(t *testing.T) {
	a := sharedSnap("ezekiel", "Ezekiel Crane", "")
	a.Kind = sim.KindNPCStateful
	a.Narrative = &sim.NarrativeState{
		SeedText:        "Should NOT appear — stateful actors get this from their VA.",
		EvolvingSummary: "...",
	}
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": a}}

	p := Build(snap, "ezekiel", nil)
	if p.NarrativeState != nil {
		t.Errorf("NarrativeState should be nil for stateful actor; got %+v", p.NarrativeState)
	}
}

func TestBuild_NarrativeStateNilForSharedEmpty(t *testing.T) {
	// Shared actor with a NarrativeState pointer but both fields empty —
	// content-gated nil so Render skips the section cleanly.
	a := sharedSnap("hannah", "Hannah", "")
	a.Narrative = &sim.NarrativeState{}
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{"hannah": a}}

	p := Build(snap, "hannah", nil)
	if p.NarrativeState != nil {
		t.Errorf("NarrativeState should be nil for empty narrative; got %+v", p.NarrativeState)
	}
}

func TestBuild_RelationshipsPopulatedForSharedWithHuddlePeers(t *testing.T) {
	at := time.Now().UTC()
	a := sharedSnap("hannah", "Hannah", "h1")
	a.Relationships = map[sim.ActorID]*sim.Relationship{
		"ezekiel": {
			SummaryText:  "Buys ale, talks about iron.",
			SalientFacts: []sim.SalientFact{{At: at, Kind: sim.InteractionHeard, Text: "Said he needs charcoal."}},
		},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah":  a,
			"ezekiel": peerSnap("ezekiel", "Ezekiel Crane", "blacksmith", sim.KindNPCStateful, "h1"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"hannah": {}, "ezekiel": {}}},
		},
	}

	p := Build(snap, "hannah", nil)
	if len(p.Relationships) != 1 {
		t.Fatalf("Relationships len = %d, want 1", len(p.Relationships))
	}
	r := p.Relationships[0]
	if r.PeerID != "ezekiel" || r.PeerName != "Ezekiel Crane" {
		t.Errorf("PeerID=%q PeerName=%q", r.PeerID, r.PeerName)
	}
	if r.SummaryText != "Buys ale, talks about iron." {
		t.Errorf("SummaryText = %q", r.SummaryText)
	}
	if len(r.RecentFacts) != 1 || r.RecentFacts[0].Text != "Said he needs charcoal." {
		t.Errorf("RecentFacts = %+v", r.RecentFacts)
	}
}

func TestBuild_RelationshipsEmptyForStateful(t *testing.T) {
	a := sharedSnap("ezekiel", "Ezekiel Crane", "h1")
	a.Kind = sim.KindNPCStateful
	a.Relationships = map[sim.ActorID]*sim.Relationship{
		"hannah": {SummaryText: "Should NOT render."},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"ezekiel": a,
			"hannah":  peerSnap("hannah", "Hannah", "innkeeper", sim.KindNPCShared, "h1"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"ezekiel": {}, "hannah": {}}},
		},
	}

	p := Build(snap, "ezekiel", nil)
	if len(p.Relationships) != 0 {
		t.Errorf("Relationships should be empty for stateful actor; got %+v", p.Relationships)
	}
}

func TestBuild_RelationshipsOnlyForCoHuddlePeers(t *testing.T) {
	// Subject has a Relationship row for bob, but bob isn't in the huddle —
	// it shouldn't appear in Relationships (the perception block is "those
	// here," not all known relationships).
	a := sharedSnap("hannah", "Hannah", "h1")
	a.Relationships = map[sim.ActorID]*sim.Relationship{
		"bob":   {SummaryText: "Not in huddle — should not render."},
		"alice": {SummaryText: "In huddle — should render."},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah": a,
			"alice":  peerSnap("alice", "Alice", "farmer", sim.KindNPCStateful, "h1"),
			"bob":    peerSnap("bob", "Bob", "miller", sim.KindNPCStateful, "h-other"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"hannah": {}, "alice": {}}},
		},
	}

	p := Build(snap, "hannah", nil)
	if len(p.Relationships) != 1 || p.Relationships[0].PeerID != "alice" {
		t.Errorf("Relationships = %+v, want just alice", p.Relationships)
	}
}

func TestBuild_RecentFactsMostRecentFirstAndCapped(t *testing.T) {
	at := time.Now().UTC()
	a := sharedSnap("hannah", "Hannah", "h1")
	// Five facts stored oldest-first; view should carry last 3 reversed.
	a.Relationships = map[sim.ActorID]*sim.Relationship{
		"ezekiel": {
			SalientFacts: []sim.SalientFact{
				{At: at.Add(1 * time.Minute), Text: "fact-1"},
				{At: at.Add(2 * time.Minute), Text: "fact-2"},
				{At: at.Add(3 * time.Minute), Text: "fact-3"},
				{At: at.Add(4 * time.Minute), Text: "fact-4"},
				{At: at.Add(5 * time.Minute), Text: "fact-5"},
			},
		},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah":  a,
			"ezekiel": peerSnap("ezekiel", "Ezekiel", "blacksmith", sim.KindNPCStateful, "h1"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"hannah": {}, "ezekiel": {}}},
		},
	}

	p := Build(snap, "hannah", nil)
	facts := p.Relationships[0].RecentFacts
	if len(facts) != recentSalientFactsPerPeer {
		t.Fatalf("RecentFacts len = %d, want %d", len(facts), recentSalientFactsPerPeer)
	}
	wantOrder := []string{"fact-5", "fact-4", "fact-3"}
	for i, w := range wantOrder {
		if facts[i].Text != w {
			t.Errorf("facts[%d].Text = %q, want %q", i, facts[i].Text, w)
		}
	}
}

func TestBuild_HuddleMembersCarryAcquaintanceFlag(t *testing.T) {
	a := sharedSnap("hannah", "Hannah", "h1")
	a.Acquaintances = map[string]sim.Acquaintance{
		"Ezekiel Crane": {FirstInteractedAt: time.Now().UTC()},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah":   a,
			"ezekiel":  peerSnap("ezekiel", "Ezekiel Crane", "blacksmith", sim.KindNPCStateful, "h1"),
			"stranger": peerSnap("stranger", "John Doe", "farmer", sim.KindNPCStateful, "h1"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"hannah": {}, "ezekiel": {}, "stranger": {}}},
		},
	}

	p := Build(snap, "hannah", nil)
	if len(p.Surroundings.HuddleMembers) != 2 {
		t.Fatalf("HuddleMembers len = %d", len(p.Surroundings.HuddleMembers))
	}
	for _, m := range p.Surroundings.HuddleMembers {
		switch m.ID {
		case "ezekiel":
			if !m.Acquainted {
				t.Errorf("ezekiel should be acquainted")
			}
			if m.DisplayName != "Ezekiel Crane" {
				t.Errorf("ezekiel DisplayName = %q", m.DisplayName)
			}
		case "stranger":
			if m.Acquainted {
				t.Errorf("stranger should NOT be acquainted")
			}
			if m.Role != "farmer" {
				t.Errorf("stranger Role = %q", m.Role)
			}
		}
	}
}

func TestRender_WhoYouAreSectionForShared(t *testing.T) {
	a := sharedSnap("hannah", "Hannah", "")
	a.Narrative = &sim.NarrativeState{
		SeedText:        "You are Hannah.",
		EvolvingSummary: "Currently anxious about the storm.",
	}
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{"hannah": a}}

	rendered := Render(Build(snap, "hannah", nil), DefaultRenderConfig())
	if !strings.Contains(combinedPrompt(rendered), "## Who you are") {
		t.Error("missing '## Who you are' section")
	}
	if !strings.Contains(combinedPrompt(rendered), "You are Hannah.") {
		t.Error("missing SeedText in rendered prompt")
	}
	if !strings.Contains(combinedPrompt(rendered), "Currently anxious about the storm.") {
		t.Error("missing EvolvingSummary in rendered prompt")
	}
}

func TestRender_WhoYouAreOmittedForStateful(t *testing.T) {
	a := sharedSnap("ezekiel", "Ezekiel", "")
	a.Kind = sim.KindNPCStateful
	a.Narrative = &sim.NarrativeState{SeedText: "should not appear"}
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": a}}

	rendered := Render(Build(snap, "ezekiel", nil), DefaultRenderConfig())
	if strings.Contains(combinedPrompt(rendered), "## Who you are") {
		t.Error("'## Who you are' should not appear for stateful actor")
	}
	if strings.Contains(combinedPrompt(rendered), "should not appear") {
		t.Error("stateful actor's NarrativeState leaked into prompt")
	}
}

func TestRender_WhatYouRememberSection(t *testing.T) {
	a := sharedSnap("hannah", "Hannah", "h1")
	a.Relationships = map[sim.ActorID]*sim.Relationship{
		"ezekiel": {
			SummaryText: "The blacksmith. Often buys ale.",
			SalientFacts: []sim.SalientFact{
				{At: time.Now(), Kind: sim.InteractionPaidBy, Text: "Paid 4 coins for ale."},
			},
		},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah":  a,
			"ezekiel": peerSnap("ezekiel", "Ezekiel Crane", "blacksmith", sim.KindNPCStateful, "h1"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"hannah": {}, "ezekiel": {}}},
		},
	}

	rendered := Render(Build(snap, "hannah", nil), DefaultRenderConfig())
	if !strings.Contains(combinedPrompt(rendered), "## What you remember of those here") {
		t.Error("missing 'What you remember' section")
	}
	if !strings.Contains(combinedPrompt(rendered), "Ezekiel Crane:") {
		t.Error("missing peer name in 'What you remember'")
	}
	if !strings.Contains(combinedPrompt(rendered), "Often buys ale.") {
		t.Error("missing summary in 'What you remember'")
	}
	if !strings.Contains(combinedPrompt(rendered), "[paid_by] Paid 4 coins for ale.") {
		t.Error("missing salient fact in 'What you remember'")
	}
}

func TestRender_AcquaintanceGatesNameInSurroundings(t *testing.T) {
	a := sharedSnap("hannah", "Hannah", "h1")
	a.Acquaintances = map[string]sim.Acquaintance{
		"Ezekiel Crane": {FirstInteractedAt: time.Now()},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah":   a,
			"ezekiel":  peerSnap("ezekiel", "Ezekiel Crane", "blacksmith", sim.KindNPCStateful, "h1"),
			"stranger": peerSnap("stranger", "John Doe", "farmer", sim.KindNPCStateful, "h1"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"hannah": {}, "ezekiel": {}, "stranger": {}}},
		},
	}

	rendered := Render(Build(snap, "hannah", nil), DefaultRenderConfig())
	if !strings.Contains(combinedPrompt(rendered), "Ezekiel Crane") {
		t.Error("acquainted peer should be named")
	}
	if strings.Contains(combinedPrompt(rendered), "John Doe") {
		t.Error("unacquainted peer should NOT be named; got John Doe in prompt")
	}
	if !strings.Contains(combinedPrompt(rendered), "the farmer") {
		t.Error("unacquainted peer should be rendered as 'the <role>'")
	}
}

func TestRender_AcquaintanceUnknownRoleFallsBackToStranger(t *testing.T) {
	a := sharedSnap("hannah", "Hannah", "h1")
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah":  a,
			"mystery": peerSnap("mystery", "Anon", "", sim.KindNPCStateful, "h1"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"hannah": {}, "mystery": {}}},
		},
	}

	rendered := Render(Build(snap, "hannah", nil), DefaultRenderConfig())
	if !strings.Contains(combinedPrompt(rendered), "a stranger") {
		t.Error("peer with no role + unacquainted should render as 'a stranger'")
	}
	if strings.Contains(combinedPrompt(rendered), "Anon") {
		t.Error("unacquainted peer's DisplayName should not leak")
	}
}
