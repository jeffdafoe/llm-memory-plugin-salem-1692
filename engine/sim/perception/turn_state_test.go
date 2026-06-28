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
	}, false)
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
	renderTurnState(&empty, TurnStateView{}, false)
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
	renderTriage(&awaiting, needs, thresholds, true, false, false, false, nil, nil)
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
	renderTriage(&normal, needs, thresholds, false, false, false, false, nil, nil)
	if !strings.Contains(normal.String(), "Choose one action") {
		t.Errorf("non-awaiting coda should keep the act-now imperative:\n%s", normal.String())
	}
}

// TestRenderTriage_MidWalkCoda (ZBBS-HOME-439) — a tick firing while the
// actor has a committed walk swaps the act-now coda for a keep-walking
// framing: continuing is the legible default, stop needs cause. The mid-walk
// variant outranks the awaiting-reply swap (the walk is the stronger current
// commitment), and the pay-offers-first line still leads when present.
func TestRenderTriage_MidWalkCoda(t *testing.T) {
	needs := map[sim.NeedKey]int{}
	thresholds := sim.NeedThresholds{}
	move := &InFlightMoveView{DestinationLabel: "General Store", Kind: sim.MoveDestinationStructureEnter}

	var b strings.Builder
	renderTriage(&b, needs, thresholds, false, false, false, false, move, nil)
	got := b.String()
	if !strings.Contains(got, "You are already walking to enter the General Store.") {
		t.Errorf("mid-walk coda should name the committed walk:\n%s", got)
	}
	if !strings.Contains(got, "call done() and keep walking") {
		t.Errorf("mid-walk coda should make continuing the default:\n%s", got)
	}
	if strings.Contains(got, "Choose one action") {
		t.Errorf("mid-walk coda must not command an action:\n%s", got)
	}

	// Mid-walk wins over awaiting-reply.
	var both strings.Builder
	renderTriage(&both, needs, thresholds, true, false, false, false, move, nil)
	if !strings.Contains(both.String(), "keep walking") {
		t.Errorf("mid-walk should outrank awaiting-reply:\n%s", both.String())
	}

	// Pay-offers line still leads.
	var offers strings.Builder
	renderTriage(&offers, needs, thresholds, false, false, false, true, move, nil)
	if !strings.HasPrefix(offers.String(), "A buyer's offer awaits your answer") {
		t.Errorf("offer-first line must still lead the mid-walk coda:\n%s", offers.String())
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
		renderTriage(&b, needs, thresholds, awaiting, false, false, true, nil, nil)
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
	renderTriage(&b, needs, thresholds, false, false, false, false, nil, nil)
	if strings.Contains(b.String(), "accept_pay") {
		t.Errorf("no-offers coda must not mention accept_pay:\n%s", b.String())
	}
}

// TestRenderTriage_SeekWorkCoda (LLM-160) — the seek-work directive (a broke
// worker with no employer present) commands leaving for a business and pre-empts
// the awaiting-reply/default codas, but yields to an in-flight walk (already going).
func TestRenderTriage_SeekWorkCoda(t *testing.T) {
	needs := map[sim.NeedKey]int{}
	thresholds := sim.NeedThresholds{}

	var b strings.Builder
	renderTriage(&b, needs, thresholds, true /*awaitingReply*/, false /*conversationLooping*/, true /*seekWork*/, false, nil, nil)
	got := b.String()
	if !strings.Contains(got, "call move_to now") {
		t.Errorf("seek-work coda should command move_to:\n%s", got)
	}
	if strings.Contains(got, "Choose one action") || strings.Contains(got, "awaiting someone's reply") {
		t.Errorf("seek-work coda must pre-empt the default/awaiting codas:\n%s", got)
	}

	// An in-flight walk still wins — the actor is already on its way.
	move := &InFlightMoveView{DestinationLabel: "Inn", Kind: sim.MoveDestinationStructureEnter}
	var walking strings.Builder
	renderTriage(&walking, needs, thresholds, false, false, true, false, move, nil)
	if !strings.Contains(walking.String(), "keep walking") {
		t.Errorf("in-flight walk should outrank the seek-work coda:\n%s", walking.String())
	}
}

// TestRenderTurnState_SuppressOwedReply (LLM-160) — the seek-work directive drops
// the "X is waiting for your reply" nag while keeping the "you already spoke" half.
func TestRenderTurnState_SuppressOwedReply(t *testing.T) {
	var b strings.Builder
	renderTurnState(&b, TurnStateView{
		AwaitingReplyFrom: []string{"Ezekiel Crane"},
		OwedReplyTo:       []string{"Bob"},
	}, true)
	out := b.String()
	if strings.Contains(out, "Bob is waiting for your reply.") {
		t.Errorf("owed-reply nag should be suppressed under the seek-work directive:\n%s", out)
	}
	if !strings.Contains(out, "You already spoke to Ezekiel Crane") {
		t.Errorf("awaiting-reply half should still render:\n%s", out)
	}
}

// TestRenderTriage_ConversationLoopingCoda (LLM-169) — a looping huddle (members
// re-echoing a settled agreement) swaps the coda for an "act now or done()" steer
// that names the loop. It pre-empts the awaiting-reply/default codas (the more
// specific read of why a reply is pending) but yields to the seek-work go-line and
// to an in-flight walk (the stronger current commitment).
func TestRenderTriage_ConversationLoopingCoda(t *testing.T) {
	needs := map[sim.NeedKey]int{}
	thresholds := sim.NeedThresholds{}

	var b strings.Builder
	renderTriage(&b, needs, thresholds, true /*awaitingReply*/, true /*conversationLooping*/, false, false, nil, nil)
	got := b.String()
	if !strings.Contains(got, "keep saying the same thing") {
		t.Errorf("looping coda should name the loop:\n%s", got)
	}
	if !strings.Contains(got, "call done()") {
		t.Errorf("looping coda should permit resolving with done():\n%s", got)
	}
	if strings.Contains(got, "Choose one action") || strings.Contains(got, "awaiting someone's reply") {
		t.Errorf("looping coda must pre-empt the default/awaiting codas:\n%s", got)
	}

	// Seek-work outranks looping — a broke worker should still be told to leave.
	var seek strings.Builder
	renderTriage(&seek, needs, thresholds, false, true /*looping*/, true /*seekWork*/, false, nil, nil)
	if !strings.Contains(seek.String(), "call move_to now") {
		t.Errorf("seek-work should outrank the looping coda:\n%s", seek.String())
	}

	// An in-flight walk still wins — the actor is already on its way.
	move := &InFlightMoveView{DestinationLabel: "Inn", Kind: sim.MoveDestinationStructureEnter}
	var walking strings.Builder
	renderTriage(&walking, needs, thresholds, false, true /*looping*/, false, false, move, nil)
	if !strings.Contains(walking.String(), "keep walking") {
		t.Errorf("in-flight walk should outrank the looping coda:\n%s", walking.String())
	}
}
