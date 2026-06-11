package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// turn_state_test.go — ZBBS-WORK-370 (2/2) perception half: buildTurnState
// derives the subject's live awaiting/owed edges off the snapshot, renderTurnState
// writes the nudge lines, and renderTriage swaps its act-now coda for a
// wait-framing when the actor is awaiting a reply.

func turnStateSnapshot(now time.Time, actors map[sim.ActorID]*sim.ActorSnapshot) *sim.Snapshot {
	return &sim.Snapshot{
		PublishedAt:         now,
		Actors:              actors,
		PCAwaitReplyWindow:  5 * time.Minute,
		NPCAwaitReplyWindow: 60 * time.Second,
	}
}

// TestBuildTurnState_AwaitingAndOwed — the subject awaits a live reply from one
// peer (AwaitingReplyFrom) and owes a reply to another (OwedReplyTo); names are
// the acquaintance-gated labels.
func TestBuildTurnState_AwaitingAndOwed(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		AwaitingReplyFrom: map[sim.ActorID]time.Time{"ezekiel": now.Add(-5 * time.Second)},
	}
	ezekiel := &sim.ActorSnapshot{Kind: sim.KindNPCShared, DisplayName: "Ezekiel Crane"}
	bob := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Bob",
		AwaitingReplyFrom: map[sim.ActorID]time.Time{"hannah": now.Add(-5 * time.Second)},
	}
	snap := turnStateSnapshot(now, map[sim.ActorID]*sim.ActorSnapshot{
		"hannah": subj, "ezekiel": ezekiel, "bob": bob,
	})
	members := []HuddleMember{
		{ID: "bob", DisplayName: "Bob", Acquainted: true},
		{ID: "ezekiel", DisplayName: "Ezekiel Crane", Acquainted: true},
	}

	ts := buildTurnState(snap, "hannah", subj, members)
	if got := strings.Join(ts.AwaitingReplyFrom, ","); got != "Ezekiel Crane" {
		t.Errorf("AwaitingReplyFrom = %q, want [Ezekiel Crane]", got)
	}
	if got := strings.Join(ts.OwedReplyTo, ","); got != "Bob" {
		t.Errorf("OwedReplyTo = %q, want [Bob]", got)
	}
	if !ts.AwaitingReply() {
		t.Error("AwaitingReply() = false, want true")
	}
}

// TestBuildTurnState_WindowExpiryDropsEdge — an edge older than the
// addressee-kind window is not surfaced (matches the sim.Speak backstop expiry).
func TestBuildTurnState_WindowExpiryDropsEdge(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	// Addressed an NPC 2 minutes ago — past the 60s NPC window.
	subj := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		AwaitingReplyFrom: map[sim.ActorID]time.Time{"ezekiel": now.Add(-2 * time.Minute)},
	}
	ezekiel := &sim.ActorSnapshot{Kind: sim.KindNPCShared, DisplayName: "Ezekiel Crane"}
	snap := turnStateSnapshot(now, map[sim.ActorID]*sim.ActorSnapshot{"hannah": subj, "ezekiel": ezekiel})
	members := []HuddleMember{{ID: "ezekiel", DisplayName: "Ezekiel Crane", Acquainted: true}}

	ts := buildTurnState(snap, "hannah", subj, members)
	if len(ts.AwaitingReplyFrom) != 0 {
		t.Errorf("AwaitingReplyFrom = %v, want empty (edge lapsed)", ts.AwaitingReplyFrom)
	}
	if ts.AwaitingReply() {
		t.Error("AwaitingReply() = true, want false (edge lapsed)")
	}
}

// TestBuildTurnState_PCAddresseeUsesLongWindow — an edge addressed at a PC uses
// the longer PC window, so a 2-minute-old edge is still live (it would have
// lapsed under the 60s NPC window).
func TestBuildTurnState_PCAddresseeUsesLongWindow(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		AwaitingReplyFrom: map[sim.ActorID]time.Time{"jeff": now.Add(-2 * time.Minute)},
	}
	jeff := &sim.ActorSnapshot{Kind: sim.KindPC, DisplayName: "Jeff"}
	snap := turnStateSnapshot(now, map[sim.ActorID]*sim.ActorSnapshot{"hannah": subj, "jeff": jeff})
	members := []HuddleMember{{ID: "jeff", DisplayName: "Jeff", Acquainted: true}}

	ts := buildTurnState(snap, "hannah", subj, members)
	if got := strings.Join(ts.AwaitingReplyFrom, ","); got != "Jeff" {
		t.Errorf("AwaitingReplyFrom = %q, want [Jeff] (live under the PC window)", got)
	}
}

// TestRenderTurnState_Lines — the owed line and the awaiting line render with
// the resolved names.
func TestRenderTurnState_Lines(t *testing.T) {
	var b strings.Builder
	renderTurnState(&b, TurnStateView{
		AwaitingReplyFrom: []string{"Ezekiel Crane"},
		OwedReplyTo:       []string{"Bob"},
	})
	out := b.String()
	if !strings.Contains(out, "Bob is waiting for your reply.") {
		t.Errorf("missing owed-reply line:\n%s", out)
	}
	if !strings.Contains(out, "You already spoke to Ezekiel Crane and are waiting for their reply.") {
		t.Errorf("missing awaiting-reply line:\n%s", out)
	}
	if !strings.Contains(out, "Do not repeat") {
		t.Errorf("awaiting line should warn against repeating:\n%s", out)
	}

	// No turn → nothing rendered.
	var empty strings.Builder
	renderTurnState(&empty, TurnStateView{})
	if empty.Len() != 0 {
		t.Errorf("empty turn-state should render nothing, got: %q", empty.String())
	}
}

// TestRenderTriage_CodaSwap — the act-now coda becomes a wait-framing when the
// actor is awaiting a reply, and reverts to the standard imperative otherwise.
func TestRenderTriage_CodaSwap(t *testing.T) {
	needs := map[sim.NeedKey]int{}
	thresholds := sim.NeedThresholds{}

	var awaiting strings.Builder
	renderTriage(&awaiting, needs, thresholds, true, false)
	got := awaiting.String()
	if strings.Contains(got, "Choose one action") {
		t.Errorf("awaiting coda must not command an action:\n%s", got)
	}
	if !strings.Contains(got, "call done()") {
		t.Errorf("awaiting coda should permit waiting via done():\n%s", got)
	}

	// Non-awaiting: the universal decision section (ZBBS-WORK-374) keeps an act-now
	// command ("Choose one action") — but now pairs it with the yield-after-speak
	// turn discipline.
	var normal strings.Builder
	renderTriage(&normal, needs, thresholds, false, false)
	if !strings.Contains(normal.String(), "Choose one action") {
		t.Errorf("non-awaiting coda should keep the act-now imperative:\n%s", normal.String())
	}
}

// TestRenderTriage_PayOffersFirst — when offers await the actor's decision,
// the coda leads with settle-the-offer-first, ranking a buyer's money above
// the actor's own felt needs in BOTH coda variants (ZBBS-HOME-424).
func TestRenderTriage_PayOffersFirst(t *testing.T) {
	needs := map[sim.NeedKey]int{}
	thresholds := sim.NeedThresholds{}

	for _, awaiting := range []bool{false, true} {
		var b strings.Builder
		renderTriage(&b, needs, thresholds, awaiting, true)
		got := b.String()
		if !strings.Contains(got, "settle it first with accept_pay") {
			t.Errorf("awaiting=%v: coda should lead with the offer decision:\n%s", awaiting, got)
		}
		if !strings.HasPrefix(got, "A buyer's offer awaits your answer") {
			t.Errorf("awaiting=%v: offer-first line must come before the generic coda:\n%s", awaiting, got)
		}
	}

	// And absent offers, the line must not render.
	var b strings.Builder
	renderTriage(&b, needs, thresholds, false, false)
	if strings.Contains(b.String(), "accept_pay") {
		t.Errorf("no-offers coda must not mention accept_pay:\n%s", b.String())
	}
}
