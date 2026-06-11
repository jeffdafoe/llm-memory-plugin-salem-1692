package sim_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// action_log_test.go — substrate tests for sim.AppendActionLogEntry and
// sim.CompactActionLog. Drives the Commands directly via Send so timing
// is deterministic; subscriber wiring + the goroutine sweep have their
// own tests in engine/sim/cascade/action_log_test.go.

// buildActionLogWorld stands up an empty world (no seeded actors needed
// — AppendActionLogEntry doesn't dereference World.Actors today), runs
// it, and returns ready-to-test handles.
func buildActionLogWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// readActionLog pulls the slice off the world goroutine.
func readActionLog(t *testing.T, w *sim.World) []sim.ActionLogEntry {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		out := make([]sim.ActionLogEntry, len(world.ActionLog))
		copy(out, world.ActionLog)
		return out, nil
	}})
	if err != nil {
		t.Fatalf("readActionLog: %v", err)
	}
	return v.([]sim.ActionLogEntry)
}

// --- TestAppendActionLogEntry_HappyPath -----------------------------
// Two appends produce two entries in order, with field contents
// preserved.
func TestAppendActionLogEntry_HappyPath(t *testing.T) {
	w, cancel := buildActionLogWorld(t)
	defer cancel()

	t0 := time.Now().UTC()
	t1 := t0.Add(1 * time.Second)

	e0 := sim.ActionLogEntry{
		ActorID:    "hannah",
		OccurredAt: t0,
		ActionType: sim.ActionTypeSpoke,
		Text:       "Good morrow.",
		HuddleID:   "h1",
	}
	e1 := sim.ActionLogEntry{
		ActorID:    "bob",
		OccurredAt: t1,
		ActionType: sim.ActionTypeWalked,
		Text:       "the tavern",
	}

	if _, err := w.Send(sim.AppendActionLogEntry(e0)); err != nil {
		t.Fatalf("Append e0: %v", err)
	}
	if _, err := w.Send(sim.AppendActionLogEntry(e1)); err != nil {
		t.Fatalf("Append e1: %v", err)
	}

	got := readActionLog(t, w)
	if len(got) != 2 {
		t.Fatalf("len(ActionLog) = %d, want 2", len(got))
	}
	// The funnel assigns Seq (1, 2, ... per append) — fold it into the
	// expected values so the exact-equality checks still cover every
	// other field.
	e0.Seq = 1
	e1.Seq = 2
	if got[0] != e0 {
		t.Errorf("got[0] = %+v, want %+v", got[0], e0)
	}
	if got[1] != e1 {
		t.Errorf("got[1] = %+v, want %+v", got[1], e1)
	}
}

// --- TestAppendActionLogEntry_RejectsEmptyActorID -------------------
func TestAppendActionLogEntry_RejectsEmptyActorID(t *testing.T) {
	w, cancel := buildActionLogWorld(t)
	defer cancel()

	_, err := w.Send(sim.AppendActionLogEntry(sim.ActionLogEntry{
		OccurredAt: time.Now().UTC(),
		ActionType: sim.ActionTypeSpoke,
	}))
	if err == nil {
		t.Fatal("expected error for empty ActorID")
	}
	if !strings.Contains(err.Error(), "ActorID") {
		t.Errorf("error = %q, want mention of ActorID", err)
	}
	if got := readActionLog(t, w); len(got) != 0 {
		t.Errorf("ActionLog len = %d, want 0 (no append on error)", len(got))
	}
}

// --- TestAppendActionLogEntry_RejectsZeroOccurredAt -----------------
func TestAppendActionLogEntry_RejectsZeroOccurredAt(t *testing.T) {
	w, cancel := buildActionLogWorld(t)
	defer cancel()

	_, err := w.Send(sim.AppendActionLogEntry(sim.ActionLogEntry{
		ActorID:    "hannah",
		ActionType: sim.ActionTypeSpoke,
	}))
	if err == nil {
		t.Fatal("expected error for zero OccurredAt")
	}
	if !strings.Contains(err.Error(), "OccurredAt") {
		t.Errorf("error = %q, want mention of OccurredAt", err)
	}
	if got := readActionLog(t, w); len(got) != 0 {
		t.Errorf("ActionLog len = %d, want 0 (no append on error)", len(got))
	}
}

// --- TestAppendActionLogEntry_TruncatesText -------------------------
// Text longer than MaxActionLogTextLen is rune-truncated at the
// substrate boundary so subscribers can't accumulate oversized rows
// even if they forget to truncate.
func TestAppendActionLogEntry_TruncatesText(t *testing.T) {
	w, cancel := buildActionLogWorld(t)
	defer cancel()

	longText := strings.Repeat("a", sim.MaxActionLogTextLen+50)
	if _, err := w.Send(sim.AppendActionLogEntry(sim.ActionLogEntry{
		ActorID:    "hannah",
		OccurredAt: time.Now().UTC(),
		ActionType: sim.ActionTypeSpoke,
		Text:       longText,
	})); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1", len(got))
	}
	if runes := []rune(got[0].Text); len(runes) != sim.MaxActionLogTextLen {
		t.Errorf("rune count = %d, want %d", len(runes), sim.MaxActionLogTextLen)
	}
}

// --- TestCompactActionLog_DropsOldEntries ---------------------------
// Entries with OccurredAt < cutoff are dropped; entries at or after
// cutoff are kept.
func TestCompactActionLog_DropsOldEntries(t *testing.T) {
	w, cancel := buildActionLogWorld(t)
	defer cancel()

	base := time.Now().UTC()
	seed := []sim.ActionLogEntry{
		{ActorID: "a", OccurredAt: base.Add(-3 * time.Hour), ActionType: sim.ActionTypeSpoke, Text: "oldest"},
		{ActorID: "b", OccurredAt: base.Add(-2 * time.Hour), ActionType: sim.ActionTypeSpoke, Text: "old"},
		{ActorID: "c", OccurredAt: base.Add(-1 * time.Hour), ActionType: sim.ActionTypeSpoke, Text: "boundary"},
		{ActorID: "d", OccurredAt: base, ActionType: sim.ActionTypeSpoke, Text: "fresh"},
	}
	for _, e := range seed {
		if _, err := w.Send(sim.AppendActionLogEntry(e)); err != nil {
			t.Fatalf("Append %s: %v", e.ActorID, err)
		}
	}

	cutoff := base.Add(-1 * time.Hour)
	v, err := w.Send(sim.CompactActionLog(cutoff))
	if err != nil {
		t.Fatalf("CompactActionLog: %v", err)
	}
	dropped := v.(int)
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}

	got := readActionLog(t, w)
	if len(got) != 2 {
		t.Fatalf("len(ActionLog) = %d, want 2 (boundary + fresh)", len(got))
	}
	if got[0].Text != "boundary" {
		t.Errorf("got[0].Text = %q, want %q (entries at cutoff are kept)", got[0].Text, "boundary")
	}
	if got[1].Text != "fresh" {
		t.Errorf("got[1].Text = %q, want %q", got[1].Text, "fresh")
	}
}

// --- TestCompactActionLog_EmptyLogNoOp ------------------------------
func TestCompactActionLog_EmptyLogNoOp(t *testing.T) {
	w, cancel := buildActionLogWorld(t)
	defer cancel()

	v, err := w.Send(sim.CompactActionLog(time.Now().UTC()))
	if err != nil {
		t.Fatalf("CompactActionLog: %v", err)
	}
	if got := v.(int); got != 0 {
		t.Errorf("dropped = %d, want 0 on empty log", got)
	}
}

// --- TestCompactActionLog_AllKeptNoOp -------------------------------
// All entries are at or after the cutoff — none dropped.
func TestCompactActionLog_AllKeptNoOp(t *testing.T) {
	w, cancel := buildActionLogWorld(t)
	defer cancel()

	now := time.Now().UTC()
	if _, err := w.Send(sim.AppendActionLogEntry(sim.ActionLogEntry{
		ActorID:    "a",
		OccurredAt: now,
		ActionType: sim.ActionTypeSpoke,
	})); err != nil {
		t.Fatalf("Append: %v", err)
	}

	cutoff := now.Add(-1 * time.Hour)
	v, err := w.Send(sim.CompactActionLog(cutoff))
	if err != nil {
		t.Fatalf("CompactActionLog: %v", err)
	}
	if got := v.(int); got != 0 {
		t.Errorf("dropped = %d, want 0 (none below cutoff)", got)
	}
	if got := readActionLog(t, w); len(got) != 1 {
		t.Errorf("len(ActionLog) = %d, want 1 (kept)", len(got))
	}
}

// --- TestCompactActionLog_OrderingTolerant --------------------------
// Out-of-order entries (a later subscriber appends an earlier
// OccurredAt) are compacted correctly — the single-pass filter
// doesn't assume sorted order.
func TestCompactActionLog_OrderingTolerant(t *testing.T) {
	w, cancel := buildActionLogWorld(t)
	defer cancel()

	base := time.Now().UTC()
	// Append out of OccurredAt order on purpose.
	seed := []sim.ActionLogEntry{
		{ActorID: "a", OccurredAt: base, ActionType: sim.ActionTypeSpoke, Text: "fresh"},
		{ActorID: "b", OccurredAt: base.Add(-2 * time.Hour), ActionType: sim.ActionTypeSpoke, Text: "old"},
		{ActorID: "c", OccurredAt: base.Add(-30 * time.Minute), ActionType: sim.ActionTypeSpoke, Text: "kept"},
	}
	for _, e := range seed {
		if _, err := w.Send(sim.AppendActionLogEntry(e)); err != nil {
			t.Fatalf("Append %s: %v", e.ActorID, err)
		}
	}

	cutoff := base.Add(-1 * time.Hour)
	v, err := w.Send(sim.CompactActionLog(cutoff))
	if err != nil {
		t.Fatalf("CompactActionLog: %v", err)
	}
	if got := v.(int); got != 1 {
		t.Errorf("dropped = %d, want 1 (only the -2h entry is below cutoff)", got)
	}
	got := readActionLog(t, w)
	if len(got) != 2 {
		t.Fatalf("len(ActionLog) = %d, want 2", len(got))
	}
	// Surviving entries keep their original append order.
	if got[0].Text != "fresh" || got[1].Text != "kept" {
		t.Errorf("texts = [%q, %q], want [fresh, kept]", got[0].Text, got[1].Text)
	}
}

// --- TestSnapshot_ActionLogClonedAndIsolated ------------------------
// republish clones the action log into Snapshot.ActionLog;
// post-republish mutations to World.ActionLog don't bleed into the
// previously-published snapshot.
func TestSnapshot_ActionLogClonedAndIsolated(t *testing.T) {
	w, cancel := buildActionLogWorld(t)
	defer cancel()

	t0 := time.Now().UTC()
	if _, err := w.Send(sim.AppendActionLogEntry(sim.ActionLogEntry{
		ActorID:    "hannah",
		OccurredAt: t0,
		ActionType: sim.ActionTypeSpoke,
		Text:       "first",
	})); err != nil {
		t.Fatalf("Append first: %v", err)
	}
	snap1 := w.Published()
	if snap1 == nil {
		t.Fatal("Published() returned nil")
	}
	if len(snap1.ActionLog) != 1 {
		t.Fatalf("snap1.ActionLog len = %d, want 1", len(snap1.ActionLog))
	}
	if snap1.ActionLog[0].Text != "first" {
		t.Errorf("snap1.ActionLog[0].Text = %q, want %q", snap1.ActionLog[0].Text, "first")
	}

	// Append a second entry; the OLD snapshot must NOT see it.
	if _, err := w.Send(sim.AppendActionLogEntry(sim.ActionLogEntry{
		ActorID:    "bob",
		OccurredAt: t0.Add(1 * time.Second),
		ActionType: sim.ActionTypeSpoke,
		Text:       "second",
	})); err != nil {
		t.Fatalf("Append second: %v", err)
	}
	if len(snap1.ActionLog) != 1 {
		t.Errorf("snap1.ActionLog len after second append = %d, want still 1 (snapshot isolated)", len(snap1.ActionLog))
	}
	snap2 := w.Published()
	if len(snap2.ActionLog) != 2 {
		t.Errorf("snap2.ActionLog len = %d, want 2", len(snap2.ActionLog))
	}
}

// --- TestSnapshot_ActionLogEmptyIsNil -------------------------------
// CloneActionLog returns nil for empty input — snapshot field
// matches.
func TestSnapshot_ActionLogEmptyIsNil(t *testing.T) {
	w, cancel := buildActionLogWorld(t)
	defer cancel()

	snap := w.Published()
	if snap == nil {
		t.Fatal("Published() returned nil")
	}
	if snap.ActionLog != nil {
		t.Errorf("snap.ActionLog = %v, want nil on empty world", snap.ActionLog)
	}
}
