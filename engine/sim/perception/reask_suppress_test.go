package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// reask_suppress_test.go — LLM-232 perception half: buildTurnState folds a plain
// spoken proposal to the sole awake peer of a two-body huddle into
// AwaitingReplyFrom at the coarse sim.ReaskSuppressWindow, so the existing
// renderTurnState line + renderTriage coda swap steer the actor to wait rather
// than re-pitch. This is the storm WORK-370's directed 60s edge misses: the ask
// named no addressee (opened no edge) or its edge lapsed between minutes-apart
// re-asks. The anchor is derived purely from the huddle's recent-conversation
// ring + peer awake state — no WORK-370 edge is set in these cases.

// reaskSnapshot builds a snapshot carrying a huddle ring so solePeerReaskAnchor
// has utterance history to read.
func reaskSnapshot(now time.Time, hID sim.HuddleID, ring []sim.Utterance, actors map[sim.ActorID]*sim.ActorSnapshot) *sim.Snapshot {
	return &sim.Snapshot{
		PublishedAt:         now,
		Actors:              actors,
		Huddles:             map[sim.HuddleID]*sim.Huddle{hID: {ID: hID, RecentUtterances: ring}},
		PCAwaitReplyWindow:  5 * time.Minute,
		NPCAwaitReplyWindow: 60 * time.Second,
	}
}

// TestReaskAnchor_SolePeerSilentAnchored — the core case: john asked ~90s ago
// (PAST the 60s WORK-370 window, so a directed edge would already have lapsed —
// this pins the minutes-scale gap) and the sole awake peer, a shelved LABORING
// worker, has said nothing since → the peer is folded into AwaitingReplyFrom and
// the wait line renders.
func TestReaskAnchor_SolePeerSilentAnchored(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	ring := []sim.Utterance{{SpeakerID: "john", At: now.Add(-90 * time.Second)}}
	subj := &sim.ActorSnapshot{Kind: sim.KindNPCShared, CurrentHuddleID: "h1", State: sim.StateIdle}
	patience := &sim.ActorSnapshot{Kind: sim.KindNPCShared, DisplayName: "Patience Walker", State: sim.StateLaboring}
	snap := reaskSnapshot(now, "h1", ring, map[sim.ActorID]*sim.ActorSnapshot{"john": subj, "patience": patience})
	members := []HuddleMember{{ID: "patience", DisplayName: "Patience Walker", Acquainted: true}}

	ts := buildTurnState(snap, "john", subj, members)
	if got := strings.Join(ts.AwaitingReplyFrom, ","); got != "Patience Walker" {
		t.Errorf("AwaitingReplyFrom = %q, want [Patience Walker]", got)
	}
	if !ts.AwaitingReply() {
		t.Error("AwaitingReply() = false, want true (drives the wait coda)")
	}
	var b strings.Builder
	renderTurnState(&b, ts, false)
	if !strings.Contains(b.String(), "waiting for their reply") {
		t.Errorf("expected the wait line, got:\n%s", b.String())
	}
}

// TestReaskAnchor_PeerRepliedNotAnchored — the peer's own later line clears it:
// once patience has spoken more recently than john, it is no longer an
// unanswered re-ask and nothing is anchored.
func TestReaskAnchor_PeerRepliedNotAnchored(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	ring := []sim.Utterance{
		{SpeakerID: "john", At: now.Add(-30 * time.Second)},
		{SpeakerID: "patience", At: now.Add(-10 * time.Second)},
	}
	subj := &sim.ActorSnapshot{Kind: sim.KindNPCShared, CurrentHuddleID: "h1", State: sim.StateIdle}
	patience := &sim.ActorSnapshot{Kind: sim.KindNPCShared, DisplayName: "Patience Walker", State: sim.StateIdle}
	snap := reaskSnapshot(now, "h1", ring, map[sim.ActorID]*sim.ActorSnapshot{"john": subj, "patience": patience})
	members := []HuddleMember{{ID: "patience", DisplayName: "Patience Walker", Acquainted: true}}

	if ts := buildTurnState(snap, "john", subj, members); ts.AwaitingReply() {
		t.Errorf("AwaitingReply() = true, want false (peer replied): %v", ts.AwaitingReplyFrom)
	}
}

// TestReaskAnchor_WindowLapsedNotAnchored — a silence older than
// ReaskSuppressWindow lifts the suppression so a dropped conversation can
// re-open.
func TestReaskAnchor_WindowLapsedNotAnchored(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	ring := []sim.Utterance{{SpeakerID: "john", At: now.Add(-4 * time.Minute)}}
	subj := &sim.ActorSnapshot{Kind: sim.KindNPCShared, CurrentHuddleID: "h1", State: sim.StateIdle}
	patience := &sim.ActorSnapshot{Kind: sim.KindNPCShared, DisplayName: "Patience Walker", State: sim.StateIdle}
	snap := reaskSnapshot(now, "h1", ring, map[sim.ActorID]*sim.ActorSnapshot{"john": subj, "patience": patience})
	members := []HuddleMember{{ID: "patience", DisplayName: "Patience Walker", Acquainted: true}}

	if ts := buildTurnState(snap, "john", subj, members); ts.AwaitingReply() {
		t.Errorf("AwaitingReply() = true, want false (past the window): %v", ts.AwaitingReplyFrom)
	}
}

// TestReaskAnchor_TwoAwakePeersNotAnchored — with two awake peers "whose turn"
// is ambiguous, so the single-peer anchor stays out (WORK-370's directed edge
// still covers a named re-pitch there).
func TestReaskAnchor_TwoAwakePeersNotAnchored(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	ring := []sim.Utterance{{SpeakerID: "john", At: now.Add(-30 * time.Second)}}
	subj := &sim.ActorSnapshot{Kind: sim.KindNPCShared, CurrentHuddleID: "h1", State: sim.StateIdle}
	patience := &sim.ActorSnapshot{Kind: sim.KindNPCShared, DisplayName: "Patience Walker", State: sim.StateIdle}
	bob := &sim.ActorSnapshot{Kind: sim.KindNPCShared, DisplayName: "Bob", State: sim.StateIdle}
	snap := reaskSnapshot(now, "h1", ring, map[sim.ActorID]*sim.ActorSnapshot{"john": subj, "patience": patience, "bob": bob})
	members := []HuddleMember{
		{ID: "patience", DisplayName: "Patience Walker", Acquainted: true},
		{ID: "bob", DisplayName: "Bob", Acquainted: true},
	}

	if ts := buildTurnState(snap, "john", subj, members); ts.AwaitingReply() {
		t.Errorf("AwaitingReply() = true, want false (two awake peers): %v", ts.AwaitingReplyFrom)
	}
}

// TestReaskAnchor_SleepingPeerLeavesSoleAwake — a second peer being asleep does
// not create ambiguity: the sole AWAKE peer is still anchored.
func TestReaskAnchor_SleepingPeerLeavesSoleAwake(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	ring := []sim.Utterance{{SpeakerID: "john", At: now.Add(-30 * time.Second)}}
	subj := &sim.ActorSnapshot{Kind: sim.KindNPCShared, CurrentHuddleID: "h1", State: sim.StateIdle}
	patience := &sim.ActorSnapshot{Kind: sim.KindNPCShared, DisplayName: "Patience Walker", State: sim.StateIdle}
	sleeper := &sim.ActorSnapshot{Kind: sim.KindNPCShared, DisplayName: "Goodman Stark", State: sim.StateSleeping}
	snap := reaskSnapshot(now, "h1", ring, map[sim.ActorID]*sim.ActorSnapshot{"john": subj, "patience": patience, "stark": sleeper})
	members := []HuddleMember{
		{ID: "patience", DisplayName: "Patience Walker", Acquainted: true},
		{ID: "stark", DisplayName: "Goodman Stark", Acquainted: true},
	}

	if got := strings.Join(buildTurnState(snap, "john", subj, members).AwaitingReplyFrom, ","); got != "Patience Walker" {
		t.Errorf("AwaitingReplyFrom = %q, want [Patience Walker] (sole awake peer)", got)
	}
}
