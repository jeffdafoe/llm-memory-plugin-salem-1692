package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// reply_pacing_test.go — LLM-536 perception half: buildTurnState widens the
// WORK-370 await-reply liveness window by the ADDRESSEE's reply pacing
// (sim.ActorSnapshot.ReplyPacingWindow), so an edge pointed at a mid-job or
// mid-bake actor is not treated as lapsed merely because the plain 60s window
// ran out while that actor was shelved and could not answer.
//
// The live trace this exists for: a constable asked a laboring worker a direct
// question at 12:28:07. Her reply cadence opened at 12:29:54 — 107s later. The
// owed-reply edge died at 12:29:07, 47s before she could speak, so restoring
// her tick alone would have handed her a turn with nothing marking the question
// as owed. She got exactly such a turn 22 minutes later and spent it on window
// mending. Deferring the warrant fixes WHEN she speaks; this fixes whether she
// answers.
//
// 107s is used throughout below as the real interval from that trace: past the
// 60s window, inside the widened 240s one.

// pacedSnapshot builds a two-body snapshot with no huddle utterance ring, so the
// LLM-232 sole-peer re-ask anchor (which needs utterance history) can never fold
// a name in and mask what the directed WORK-370 edge did.
func pacedSnapshot(now time.Time, actors map[sim.ActorID]*sim.ActorSnapshot) *sim.Snapshot {
	return &sim.Snapshot{
		PublishedAt:         now,
		Actors:              actors,
		Huddles:             map[sim.HuddleID]*sim.Huddle{"h1": {ID: "h1"}},
		PCAwaitReplyWindow:  5 * time.Minute,
		NPCAwaitReplyWindow: 60 * time.Second,
	}
}

// TestReplyPacing_OwedReplyOutlivesPlainWindow — the core case. The constable
// holds an edge to the laboring worker, stamped 107s ago. Her pacing widens her
// own window to 240s, so she still reads "he is waiting for your reply" on the
// tick her cadence finally hands her.
func TestReplyPacing_OwedReplyOutlivesPlainWindow(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 29, 54, 0, time.UTC)
	silence := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		CurrentHuddleID:   "h1",
		State:             sim.StateLaboring,
		ReplyPacingWindow: 3 * time.Minute,
	}
	marsh := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Constable Marsh",
		AwaitingReplyFrom: map[sim.ActorID]time.Time{"silence": now.Add(-107 * time.Second)},
	}
	snap := pacedSnapshot(now, map[sim.ActorID]*sim.ActorSnapshot{"silence": silence, "marsh": marsh})
	members := []HuddleMember{{ID: "marsh", DisplayName: "Constable Marsh", Acquainted: true}}

	ts := buildTurnState(snap, "silence", silence, members)
	if got := strings.Join(ts.OwedReplyTo, ","); got != "Constable Marsh" {
		t.Fatalf("OwedReplyTo = %q, want [Constable Marsh] — the question is still owed at the cadence boundary", got)
	}
	var b strings.Builder
	renderTurnState(&b, ts, false)
	if !strings.Contains(b.String(), "Constable Marsh is waiting for your reply.") {
		t.Errorf("expected the owed-reply line, got:\n%s", b.String())
	}
}

// TestReplyPacing_UnpacedSubjectKeepsPlainWindow — the widening is scoped to the
// paced states and nothing else. An idle subject at the same 107s reads no owed
// reply, exactly as before LLM-536: she has had turns available all along, so
// her silence really is a lapsed turn.
func TestReplyPacing_UnpacedSubjectKeepsPlainWindow(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 29, 54, 0, time.UTC)
	silence := &sim.ActorSnapshot{Kind: sim.KindNPCShared, CurrentHuddleID: "h1", State: sim.StateIdle}
	marsh := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Constable Marsh",
		AwaitingReplyFrom: map[sim.ActorID]time.Time{"silence": now.Add(-107 * time.Second)},
	}
	snap := pacedSnapshot(now, map[sim.ActorID]*sim.ActorSnapshot{"silence": silence, "marsh": marsh})
	members := []HuddleMember{{ID: "marsh", DisplayName: "Constable Marsh", Acquainted: true}}

	ts := buildTurnState(snap, "silence", silence, members)
	if len(ts.OwedReplyTo) != 0 {
		t.Errorf("OwedReplyTo = %v, want empty — an unpaced subject keeps the plain 60s window", ts.OwedReplyTo)
	}
}

// TestReplyPacing_EdgePastWidenedWindowStillLapses — the anti-lockup posture is
// preserved. The widening buys the paced actor one cadence to answer in, not
// forever: an edge older than window+cadence lapses and the conversation may
// re-open.
func TestReplyPacing_EdgePastWidenedWindowStillLapses(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 29, 54, 0, time.UTC)
	silence := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		CurrentHuddleID:   "h1",
		State:             sim.StateLaboring,
		ReplyPacingWindow: 3 * time.Minute,
	}
	marsh := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Constable Marsh",
		AwaitingReplyFrom: map[sim.ActorID]time.Time{"silence": now.Add(-5 * time.Minute)},
	}
	snap := pacedSnapshot(now, map[sim.ActorID]*sim.ActorSnapshot{"silence": silence, "marsh": marsh})
	members := []HuddleMember{{ID: "marsh", DisplayName: "Constable Marsh", Acquainted: true}}

	ts := buildTurnState(snap, "silence", silence, members)
	if len(ts.OwedReplyTo) != 0 {
		t.Errorf("OwedReplyTo = %v, want empty — 5m is past the widened 240s window", ts.OwedReplyTo)
	}
}

// TestReplyPacing_SpeakerWaitsOutThePacedAddressee — the other direction of the
// same widening, read from the ASKER's side. The constable's own "you already
// spoke, wait" line has to outlive the pacing too, or he is released to re-pitch
// into a worker who is about to answer him — a re-ask storm manufactured by the
// fix for the deadlock.
func TestReplyPacing_SpeakerWaitsOutThePacedAddressee(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 29, 54, 0, time.UTC)
	marsh := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		CurrentHuddleID:   "h1",
		State:             sim.StateIdle,
		AwaitingReplyFrom: map[sim.ActorID]time.Time{"silence": now.Add(-107 * time.Second)},
	}
	silence := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Silence Walker",
		State:             sim.StateLaboring,
		ReplyPacingWindow: 3 * time.Minute,
	}
	snap := pacedSnapshot(now, map[sim.ActorID]*sim.ActorSnapshot{"silence": silence, "marsh": marsh})
	members := []HuddleMember{{ID: "silence", DisplayName: "Silence Walker", Acquainted: true}}

	ts := buildTurnState(snap, "marsh", marsh, members)
	if got := strings.Join(ts.AwaitingReplyFrom, ","); got != "Silence Walker" {
		t.Fatalf("AwaitingReplyFrom = %q, want [Silence Walker] — the asker keeps waiting while she is paced", got)
	}
	if !ts.AwaitingReply() {
		t.Error("AwaitingReply() = false, want true (swaps the act-now coda for the wait framing)")
	}
}
