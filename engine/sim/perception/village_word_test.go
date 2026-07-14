package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// village_word_test.go — LLM-387: the "## Word about the village" perception
// section, the read surface of the word-of-mouth layer. buildVillageWord projects
// the subject's own carried rumors (ActorSnapshot.Rumors) into bounded, present-
// filtered views; renderVillageWord frames them as fallible talk (first-hand vs
// hearsay). Together they are what makes a spread rumor actually show up in a turn.

func villageWordTestNow() time.Time {
	return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
}

func vwRumor(subjID sim.ActorID, name string, rung int, heardAt time.Time, firstHand bool) sim.KnownRumor {
	return sim.KnownRumor{
		Topic:       sim.RumorTopicShortOnCoin,
		SubjectID:   subjID,
		SubjectName: name,
		Rung:        rung,
		HeardAt:     heardAt,
		FirstHand:   firstHand,
	}
}

// TestBuildVillageWordExcludesPresentAndExpired covers the two content filters: a
// rumor whose subject stands in the scene is dropped (no gossip to their face), and
// a rumor aged past the TTL is dropped — leaving only the live, absent-subject one.
func TestBuildVillageWordExcludesPresentAndExpired(t *testing.T) {
	now := villageWordTestNow()
	a := &sim.ActorSnapshot{
		Kind: sim.KindNPCShared,
		Rumors: []sim.KnownRumor{
			vwRumor("absent", "Ezekiel Crane", 0, now.Add(-time.Minute), true),
			vwRumor("present", "John Ellis", 1, now, false),
			vwRumor("stale", "Old Gossip", 0, now.Add(-sim.RumorTTL-time.Hour), false),
		},
	}
	s := SurroundingsView{HuddleMembers: []HuddleMember{{ID: "present"}}}
	got := buildVillageWord(a, s, now)
	if len(got) != 1 {
		t.Fatalf("got %d views, want 1 (absent only): %+v", len(got), got)
	}
	if !strings.Contains(got[0].Clause, "Ezekiel Crane") || !got[0].FirstHand {
		t.Fatalf("unexpected view %+v", got[0])
	}
}

// TestBuildVillageWordCoPresentSubjectExcluded pins that the present filter also
// covers merely CO-present peers (an unhuddled scene), not just huddle members.
func TestBuildVillageWordCoPresentSubjectExcluded(t *testing.T) {
	now := villageWordTestNow()
	a := &sim.ActorSnapshot{
		Kind:   sim.KindNPCShared,
		Rumors: []sim.KnownRumor{vwRumor("nearby", "Goodwife Alice", 0, now, false)},
	}
	s := SurroundingsView{CoPresent: []HuddleMember{{ID: "nearby"}}}
	if got := buildVillageWord(a, s, now); got != nil {
		t.Fatalf("a co-present subject should be filtered, got %+v", got)
	}
}

// TestBuildVillageWordFreshestFirstAndCap pins recency ordering and the render cap:
// with more shareable rumors than the cap, the freshest survive and lead.
func TestBuildVillageWordFreshestFirstAndCap(t *testing.T) {
	now := villageWordTestNow()
	var rumors []sim.KnownRumor
	for i := 0; i < 5; i++ {
		id := sim.ActorID("subj" + string(rune('0'+i)))
		name := "Resident " + string(rune('A'+i))
		rumors = append(rumors, vwRumor(id, name, 0, now.Add(time.Duration(i)*time.Minute), false))
	}
	a := &sim.ActorSnapshot{Kind: sim.KindNPCStateful, Rumors: rumors}
	got := buildVillageWord(a, SurroundingsView{}, now)
	if len(got) != maxRenderedVillageWord {
		t.Fatalf("got %d views, want cap %d", len(got), maxRenderedVillageWord)
	}
	if !strings.Contains(got[0].Clause, "Resident E") {
		t.Fatalf("freshest rumor should lead, got %+v", got[0])
	}
}

// TestBuildVillageWordSkipsNonGossipKinds pins that PCs and decorative props carry
// no village word even if their snapshot somehow holds rumors.
func TestBuildVillageWordSkipsNonGossipKinds(t *testing.T) {
	now := villageWordTestNow()
	rumors := []sim.KnownRumor{vwRumor("absent", "Ezekiel Crane", 0, now, true)}
	for _, k := range []sim.ActorKind{sim.KindPC, sim.KindDecorative} {
		a := &sim.ActorSnapshot{Kind: k, Rumors: rumors}
		if got := buildVillageWord(a, SurroundingsView{}, now); got != nil {
			t.Fatalf("kind %v should carry no village word, got %+v", k, got)
		}
	}
}

// TestRenderVillageWordFraming pins the two per-line framings and the section
// scaffolding (header + fallible-talk preamble).
func TestRenderVillageWordFraming(t *testing.T) {
	var b strings.Builder
	renderVillageWord(&b, []VillageRumorView{
		{Clause: "Ezekiel Crane came up short of coin for a purchase", FirstHand: true},
		{Clause: "Goodwife Alice has fallen behind on their debts", FirstHand: false},
	})
	out := b.String()
	if !strings.Contains(out, "## Word about the village") {
		t.Fatalf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "it may be true, it may be idle gossip") {
		t.Fatalf("missing fallible-talk preamble:\n%s", out)
	}
	if !strings.Contains(out, "- You saw it yourself: Ezekiel Crane came up short of coin for a purchase.") {
		t.Fatalf("first-hand framing wrong:\n%s", out)
	}
	if !strings.Contains(out, "- Word has it that Goodwife Alice has fallen behind on their debts.") {
		t.Fatalf("hearsay framing wrong:\n%s", out)
	}
}

// TestRenderVillageWordEmptySkips pins that an empty list and an all-blank-clause
// list both render nothing — no orphan header.
func TestRenderVillageWordEmptySkips(t *testing.T) {
	var b strings.Builder
	renderVillageWord(&b, nil)
	renderVillageWord(&b, []VillageRumorView{{Clause: "", FirstHand: true}})
	if b.Len() != 0 {
		t.Fatalf("expected no output, got %q", b.String())
	}
}

// TestVillageWordAppearsInRenderedTurn is the end-to-end guardrail: a carried rumor
// on a subject's snapshot flows through Build → Render into the actual turn prompt.
// This is the observable milestone — proof a spread rumor reaches a turn, not just
// the actor's in-memory known-set.
func TestVillageWordAppearsInRenderedTurn(t *testing.T) {
	now := villageWordTestNow()
	subj := &sim.ActorSnapshot{
		DisplayName: "Hannah Boggs",
		Kind:        sim.KindNPCShared,
		Needs:       map[sim.NeedKey]int{},
		Inventory:   map[sim.ItemKind]int{},
		Rumors:      []sim.KnownRumor{vwRumor("absent", "Ezekiel Crane", 0, now, true)},
	}
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"hannah": subj},
	}
	p := Build(snap, "hannah", nil)
	if len(p.VillageWord) != 1 {
		t.Fatalf("Build should carry 1 village-word view, got %d", len(p.VillageWord))
	}
	out := combinedPrompt(Render(p, RenderConfig{}))
	if !strings.Contains(out, "## Word about the village") {
		t.Fatalf("rendered turn missing the village-word section:\n%s", out)
	}
	if !strings.Contains(out, "Ezekiel Crane came up short of coin for a purchase") {
		t.Fatalf("rendered turn missing the rumor clause:\n%s", out)
	}
}
