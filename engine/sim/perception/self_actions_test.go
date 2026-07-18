package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// self_actions_test.go — LLM-217 "## What you've recently done" edges the
// golden scenario doesn't isolate: the window cutoff, the line cap, the
// prior-huddle spoke inclusion, and the AgoPhrase buckets.

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
		// LLM-374: a mixed coins+goods (barter) settlement shows BOTH legs, not
		// just the coins — otherwise a value-matched barter reads as a shortchange.
		{ActionType: sim.ActionTypePaid, CounterpartyName: "Joseph Scott", Amount: 4, PayItems: []sim.ItemKindQty{{Kind: "cheese", Qty: 3}}, Text: "5x flour", At: renderedAt.Add(-5 * time.Minute)},
		{ActionType: sim.ActionTypePaid, At: renderedAt.Add(-6 * time.Minute)},
	}, renderedAt)
	out := b.String()
	for _, want := range []string{
		"## What you've recently done",
		"- You arrived at the Tavern (45s ago)",
		"- You paid John Ellis 2 coins for carrot (4m ago)",
		"- You paid Joseph Scott 3 cheese and 4 coins for 5x flour (5m ago)",
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
		{23 * time.Hour, "23h ago"},
		// Long scales (LLM-390): prose past a day, for the recall memory-age
		// framing. Duration-based buckets, not calendar days.
		{25 * time.Hour, "a day ago"},
		{49 * time.Hour, "two days ago"},
		{6*24*time.Hour + time.Hour, "six days ago"},
		{8 * 24 * time.Hour, "a week ago"},
		{15 * 24 * time.Hour, "two weeks ago"},
		{29 * 24 * time.Hour, "four weeks ago"},
		{45 * 24 * time.Hour, "a month ago"},
		{90 * 24 * time.Hour, "three months ago"},
		{360 * 24 * time.Hour, "twelve months ago"},
		{400 * 24 * time.Hour, "over a year ago"},
	}
	for _, c := range cases {
		if got := AgoPhrase(now.Add(-c.before), now); got != c.want {
			t.Errorf("AgoPhrase(%v before now) = %q, want %q", c.before, got, c.want)
		}
	}
	if got := AgoPhrase(time.Time{}, now); got != "" {
		t.Errorf("zero At must yield no stamp, got %q", got)
	}
	if got := AgoPhrase(now, time.Time{}); got != "" {
		t.Errorf("zero renderedAt must yield no stamp, got %q", got)
	}
}

// LLM-366: buildSelfActions flags a walked entry whose destination the subject
// still remembers finding shut (active ObservedClosed) so the churn trail can show
// the trip was a dead end. Only ActionTypeWalked entries with a live shut memory
// are flagged — a walk to a place with no such memory, and any non-walk action,
// are not.
func TestBuildSelfActions_FoundShutFromObservedClosed(t *testing.T) {
	published := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	// The store was found shut at -3m — that arrival stamped the memory (a walked
	// entry and its ObservedClosed stamp share the same arrival time). An EARLIER
	// walk to the store at -10m found it open (which would have cleared any memory),
	// so ONLY the -3m arrival must read as a dead end — the timestamp-aware guard
	// must not retroactively rewrite the earlier open trip.
	observedAt := published.Add(-3 * time.Minute)
	openWalk := published.Add(-10 * time.Minute)
	// StructureID (the conversational scope the actor came to REST in) is left
	// blank on every walked entry on purpose: that is what the engine really
	// stamps for a walk to a shut business — loiterScopeConversable blanks the
	// outdoor loiter scope of a keeperless structure — and keying FoundShut off
	// it is the LLM-463 bug. Only DestStructureID (where the trip was AIMED) may
	// drive the flag.
	log := []sim.ActionLogEntry{
		{Seq: 1, ActorID: "ezekiel", OccurredAt: openWalk, ActionType: sim.ActionTypeWalked, Text: "General Store", DestStructureID: "store"},
		{Seq: 2, ActorID: "ezekiel", OccurredAt: published.Add(-6 * time.Minute), ActionType: sim.ActionTypeWalked, Text: "Inn", DestStructureID: "inn"},
		{Seq: 3, ActorID: "ezekiel", OccurredAt: observedAt, ActionType: sim.ActionTypeWalked, Text: "General Store", DestStructureID: "store"},
		// Deliberately carries a DestStructureID the real engine would never stamp on
		// a Departed row (the field is walked-only). That is the point: it proves the
		// ActionType gate is what rejects this entry, not an incidentally empty field.
		{Seq: 4, ActorID: "ezekiel", OccurredAt: published.Add(-1 * time.Minute), ActionType: sim.ActionTypeDeparted, Text: "General Store", DestStructureID: "store", StructureID: "store"},
	}
	snap, subject := selfActionsFixture(published, log, "")
	subject.Observed = sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
		{StructureID: "store", Condition: sim.ObservedClosed}: observedAt,
	})

	got := buildSelfActions(snap, "ezekiel", subject)
	find := func(text string, at time.Time) SelfActionView {
		t.Helper()
		for _, v := range got {
			if v.Text == text && v.At.Equal(at) {
				return v
			}
		}
		t.Fatalf("no self-action %q at %v in trail", text, at)
		return SelfActionView{}
	}
	if !find("General Store", observedAt).FoundShut {
		t.Error("the -3m walk that stamped the shut memory should be FoundShut")
	}
	if find("General Store", openWalk).FoundShut {
		t.Error("the earlier -10m walk (found open, before the shut observation) must NOT be FoundShut")
	}
	if find("Inn", published.Add(-6*time.Minute)).FoundShut {
		t.Error("a walk to a place with no shut memory must not be FoundShut")
	}
	if find("General Store", published.Add(-1*time.Minute)).FoundShut {
		t.Error("a non-walk action (departed) must never be FoundShut, even for a shut structure")
	}
}

// LLM-366: a FoundShut walked entry names the dead end so a churn of these reads
// AS dead ends; an ordinary walked entry keeps its neutral arrival line.
func TestRenderSelfActions_FoundShutNamesDeadEnd(t *testing.T) {
	renderedAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	var b strings.Builder
	renderSelfActions(&b, []SelfActionView{
		{ActionType: sim.ActionTypeWalked, Text: "General Store", FoundShut: true, At: renderedAt.Add(-2 * time.Minute)},
		{ActionType: sim.ActionTypeWalked, Text: "Inn", At: renderedAt.Add(-5 * time.Minute)},
	}, renderedAt)
	out := b.String()
	if want := "- You went to the General Store but found it shut, no one tending it (2m ago)"; !strings.Contains(out, want) {
		t.Errorf("missing FoundShut phrasing %q in:\n%s", want, out)
	}
	if want := "- You arrived at the Inn (5m ago)"; !strings.Contains(out, want) {
		t.Errorf("missing neutral arrival %q in:\n%s", want, out)
	}
}
