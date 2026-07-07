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
		AboutMe: "You are Hannah, daughter of the innkeeper, worried about the harvest.",
	}
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{"hannah": a}}

	p := Build(snap, "hannah", nil)
	if p.NarrativeState == nil {
		t.Fatal("NarrativeState nil for shared actor with populated narrative")
	}
	if p.NarrativeState.AboutMe != a.Narrative.AboutMe {
		t.Errorf("AboutMe = %q", p.NarrativeState.AboutMe)
	}
}

func TestBuild_NarrativeStateNilForStateful(t *testing.T) {
	a := sharedSnap("ezekiel", "Ezekiel Crane", "")
	a.Kind = sim.KindNPCStateful
	a.Narrative = &sim.NarrativeState{
		AboutMe: "Should NOT appear — stateful actors get this from their VA.",
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
		AboutMe:         "You are Hannah, and lately the storm has you anxious.",
		SeedText:        "stale seed should not render",
		EvolvingSummary: "stale summary should not render",
	}
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{"hannah": a}}

	rendered := Render(Build(snap, "hannah", nil), DefaultRenderConfig())
	if !strings.Contains(combinedPrompt(rendered), "## Who you are") {
		t.Error("missing '## Who you are' section")
	}
	if !strings.Contains(combinedPrompt(rendered), "You are Hannah, and lately the storm has you anxious.") {
		t.Error("missing AboutMe in rendered prompt")
	}
	// AboutMe (the synthesized soul) is the only narrative field rendered; the
	// legacy SeedText/EvolvingSummary must not leak in (LLM-199; ZBBS-WORK-374:
	// EvolvingSummary was the frozen diary prose that primed the repeat loop).
	if strings.Contains(combinedPrompt(rendered), "stale seed should not render") ||
		strings.Contains(combinedPrompt(rendered), "stale summary should not render") {
		t.Error("legacy SeedText/EvolvingSummary leaked into the decision prompt")
	}
}

func TestRender_WhoYouAreOmittedForStateful(t *testing.T) {
	a := sharedSnap("ezekiel", "Ezekiel", "")
	a.Kind = sim.KindNPCStateful
	a.Narrative = &sim.NarrativeState{AboutMe: "should not appear"}
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
	combined := combinedPrompt(rendered)
	if !strings.Contains(combined, "## What you remember of those here") {
		t.Error("missing 'What you remember' section")
	}
	if !strings.Contains(combined, "Ezekiel Crane: The blacksmith. Often buys ale.") {
		t.Error("missing peer summary line in 'What you remember'")
	}
	// ZBBS-HOME-412: the section is summary-ONLY now. Per-peer salient facts —
	// the [heard] re-pitch driver and other [kind] facts alike — are no longer
	// rendered here; the turn-by-turn moved to '## Recent conversation here'
	// (the huddle ring, populated for all NPCs).
	if strings.Contains(combined, "[paid_by]") {
		t.Error("salient facts must no longer render in 'What you remember' (summary-only since HOME-412)")
	}
}

// A peer with a relationship row but NO consolidated summary contributes
// nothing now that the per-peer facts are gone — the whole section is skipped
// rather than emitting a bare "- Name:" line (ZBBS-HOME-412).
func TestRender_WhatYouRememberSection_SkippedWhenNoSummary(t *testing.T) {
	a := sharedSnap("hannah", "Hannah", "h1")
	a.Relationships = map[sim.ActorID]*sim.Relationship{
		"ezekiel": {
			SalientFacts: []sim.SalientFact{
				{At: time.Now(), Kind: sim.InteractionHeard, Text: "Ezekiel Crane said: a room, please."},
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

	combined := combinedPrompt(Render(Build(snap, "hannah", nil), DefaultRenderConfig()))
	if strings.Contains(combined, "## What you remember of those here") {
		t.Errorf("section must be skipped when no peer has a summary, got:\n%s", combined)
	}
}

// ZBBS-HOME-412: the huddle's recent-conversation ring renders as
// '## Recent conversation here', oldest-first, marking the subject's own lines
// "You said". Crucially it is populated for a STATEFUL subject (whose
// Relationships are nil), proving the cross-tick continuity reaches the NPCs the
// per-pair relationship trail deliberately skips.
func TestRender_RecentConversationSection_StatefulSubject(t *testing.T) {
	subject := peerSnap("ezekiel", "Ezekiel Crane", "blacksmith", sim.KindNPCStateful, "h1")
	now := time.Now()
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"ezekiel": subject,
			"hannah":  peerSnap("hannah", "Hannah Boggs", "innkeeper", sim.KindNPCShared, "h1"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {
				ID:      "h1",
				Members: map[sim.ActorID]struct{}{"ezekiel": {}, "hannah": {}},
				RecentUtterances: []sim.Utterance{
					{SpeakerID: "ezekiel", SpeakerName: "Ezekiel Crane", Text: "Might I have a room?", At: now},
					{SpeakerID: "hannah", SpeakerName: "Hannah Boggs", Text: "Four coins for the night.", At: now.Add(time.Second)},
				},
			},
		},
	}

	combined := combinedPrompt(Render(Build(snap, "ezekiel", nil), DefaultRenderConfig()))
	if !strings.Contains(combined, "## Recent conversation here") {
		t.Fatalf("missing '## Recent conversation here' for a stateful subject, got:\n%s", combined)
	}
	if !strings.Contains(combined, "- You said: Might I have a room?") {
		t.Errorf("subject's own line should render as 'You said', got:\n%s", combined)
	}
	if !strings.Contains(combined, "- Hannah Boggs said: Four coins for the night.") {
		t.Errorf("peer line should render as '<Name> said', got:\n%s", combined)
	}
	// Oldest-first: the subject's question precedes Hannah's reply.
	if strings.Index(combined, "Might I have a room?") > strings.Index(combined, "Four coins for the night.") {
		t.Error("recent conversation must render oldest-first")
	}
}

// A ring line that is ALSO the current speech warrant this tick is shown only
// once — under "## Since your last turn", de-duped out of "## Recent conversation
// here" (ZBBS-HOME-412, mirroring the heard-fact de-dup WORK-374 added). Pins the
// truncation-key match between the ring text and the warrant excerpt.
func TestRender_RecentConversation_DedupsCurrentWarrant(t *testing.T) {
	const line = "Might I have a room?"
	subject := peerSnap("ezekiel", "Ezekiel Crane", "blacksmith", sim.KindNPCStateful, "h1")
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"ezekiel": subject,
			"hannah":  peerSnap("hannah", "Hannah Boggs", "innkeeper", sim.KindNPCShared, "h1"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {
				ID:      "h1",
				Members: map[sim.ActorID]struct{}{"ezekiel": {}, "hannah": {}},
				RecentUtterances: []sim.Utterance{
					{SpeakerID: "hannah", SpeakerName: "Hannah Boggs", Text: line, At: time.Now()},
				},
			},
		},
	}
	// The same line is the speech warrant Ezekiel is reacting to this tick.
	warrants := []sim.WarrantMeta{speechWarrant(1, "s1", "hannah", line)}

	combined := combinedPrompt(Render(Build(snap, "ezekiel", warrants), DefaultRenderConfig()))
	if !strings.Contains(combined, "## Since your last turn") {
		t.Fatalf("the warrant should render in '## Since your last turn', got:\n%s", combined)
	}
	if strings.Contains(combined, "## Recent conversation here") {
		t.Errorf("the only ring line matched the current warrant and must be de-duped, so the section should be absent, got:\n%s", combined)
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
