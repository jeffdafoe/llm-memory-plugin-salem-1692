package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// self_actions_test.go — LLM-217 "## What you've recently done" edges the
// golden scenario doesn't isolate: the window cutoff, the line cap, the
// prior-huddle spoke inclusion, and the agoPhrase buckets.

func selfActionsFixture(published time.Time, log []sim.ActionLogEntry, currentHuddle sim.HuddleID) (*sim.Snapshot, *sim.ActorSnapshot) {
	subject := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Ezekiel Crane",
		Role:            "blacksmith",
		State:           sim.StateIdle,
		CurrentHuddleID: currentHuddle,
		Needs:           map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt: published,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subject},
		ActionLog:   log,
	}
	return snap, subject
}

// The trail keeps only entries inside selfActionTrailWindow and at most
// maxSelfActionTrail of them, most-recent-first.
func TestBuildSelfActions_WindowAndCap(t *testing.T) {
	published := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return published.Add(-d) }
	log := []sim.ActionLogEntry{
		// Outside the window — this morning's errand must not clutter the trail.
		{Seq: 1, ActorID: "ezekiel", OccurredAt: ago(selfActionTrailWindow + time.Minute), ActionType: sim.ActionTypeWalked, Text: "Blacksmith"},
	}
	// Eight in-window entries — two more than the cap.
	for i := 0; i < 8; i++ {
		log = append(log, sim.ActionLogEntry{
			Seq: uint64(2 + i), ActorID: "ezekiel",
			OccurredAt: ago(time.Duration(8-i) * time.Minute),
			ActionType: sim.ActionTypeDeparted, Text: "Tavern",
		})
	}
	// OccurredAt is only approximately monotonic vs Seq: an out-of-window
	// entry sitting LATE in the slice must be skipped, not end the scan —
	// the in-window entries before it still qualify.
	log = append(log, sim.ActionLogEntry{
		Seq: 10, ActorID: "ezekiel",
		OccurredAt: ago(selfActionTrailWindow + 2*time.Minute),
		ActionType: sim.ActionTypeWalked, Text: "Blacksmith",
	})
	snap, subject := selfActionsFixture(published, log, "")

	got := buildSelfActions(snap, "ezekiel", subject)
	if len(got) != maxSelfActionTrail {
		t.Fatalf("want the cap of %d lines, got %d", maxSelfActionTrail, len(got))
	}
	// Most-recent-first: the newest in-window entry (1m ago) leads.
	if want := ago(time.Minute); !got[0].At.Equal(want) {
		t.Errorf("first line should be the newest entry (at %v), got %v", want, got[0].At)
	}
	for i := 1; i < len(got); i++ {
		if got[i].At.After(got[i-1].At) {
			t.Errorf("trail must be most-recent-first; line %d (%v) is newer than line %d (%v)", i, got[i].At, i-1, got[i-1].At)
		}
	}
}

// A clockless snapshot (hand-built payloads) yields no trail — the window has
// nothing to measure against.
func TestBuildSelfActions_NoClockNoTrail(t *testing.T) {
	log := []sim.ActionLogEntry{
		{Seq: 1, ActorID: "ezekiel", OccurredAt: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC), ActionType: sim.ActionTypeWalked, Text: "Tavern"},
	}
	snap, subject := selfActionsFixture(time.Time{}, log, "")
	if got := buildSelfActions(snap, "ezekiel", subject); got != nil {
		t.Errorf("zero PublishedAt must yield a nil trail, got %d lines", len(got))
	}
}

// A spoke entry from a PRIOR huddle is the trail's job (the ring can't show
// it); one from the CURRENT huddle is the ring's (the golden pins that half).
func TestBuildSelfActions_PriorHuddleSpokeIncluded(t *testing.T) {
	published := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	log := []sim.ActionLogEntry{
		{Seq: 1, ActorID: "ezekiel", OccurredAt: published.Add(-5 * time.Minute), ActionType: sim.ActionTypeSpoke, Text: "I'll head home now.", HuddleID: "old_huddle"},
		{Seq: 2, ActorID: "ezekiel", OccurredAt: published.Add(-1 * time.Minute), ActionType: sim.ActionTypeSpoke, Text: "Good evening.", HuddleID: "current_huddle"},
	}
	snap, subject := selfActionsFixture(published, log, "current_huddle")

	got := buildSelfActions(snap, "ezekiel", subject)
	if len(got) != 1 {
		t.Fatalf("want exactly the prior-huddle line, got %d lines", len(got))
	}
	if got[0].Text != "I'll head home now." {
		t.Errorf("want the prior-huddle utterance in the trail, got %q", got[0].Text)
	}
}

// renderSelfActions phrases second-person with the interval stamp; a degraded
// entry (paid, no counterparty) still renders its counterparty-less form.
func TestRenderSelfActions_PhrasingAndStamps(t *testing.T) {
	renderedAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	var b strings.Builder
	renderSelfActions(&b, []SelfActionView{
		{ActionType: sim.ActionTypeWalked, Text: "Tavern", At: renderedAt.Add(-45 * time.Second)},
		{ActionType: sim.ActionTypePaid, CounterpartyName: "John Ellis", Amount: 2, Text: "carrot", At: renderedAt.Add(-4 * time.Minute)},
		{ActionType: sim.ActionTypePaid, At: renderedAt.Add(-6 * time.Minute)},
	}, renderedAt)
	out := b.String()
	for _, want := range []string{
		"## What you've recently done",
		"- You arrived at the Tavern (45s ago)",
		"- You paid John Ellis 2 coins for carrot (4m ago)",
		"- You made a payment (6m ago)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in rendered trail:\n%s", want, out)
		}
	}
}

func TestAgoPhraseBuckets(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		before time.Duration
		want   string
	}{
		{5 * time.Second, "just now"},
		{14 * time.Second, "just now"},
		{15 * time.Second, "15s ago"},
		{89 * time.Second, "89s ago"},
		{90 * time.Second, "1m ago"},
		{59 * time.Minute, "59m ago"},
		{time.Hour, "1h ago"},
		{25 * time.Hour, "25h ago"},
	}
	for _, c := range cases {
		if got := agoPhrase(now.Add(-c.before), now); got != c.want {
			t.Errorf("agoPhrase(%v before now) = %q, want %q", c.before, got, c.want)
		}
	}
	if got := agoPhrase(time.Time{}, now); got != "" {
		t.Errorf("zero At must yield no stamp, got %q", got)
	}
	if got := agoPhrase(now, time.Time{}); got != "" {
		t.Errorf("zero renderedAt must yield no stamp, got %q", got)
	}
}
