package sim_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildConsolidationTestWorld stands up a world with three actors:
// one KindNPCShared (hannah), one KindNPCStateful (ezekiel — must be
// skipped by the scan), and one KindPC (the player — also must be
// skipped). All three are present as potential peers.
func buildConsolidationTestWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()

	now := time.Now().UTC()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:               "hannah",
			DisplayName:      "Hannah",
			Kind:             sim.KindNPCShared,
			LLMAgent:         "salem-vendor",
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
		"ezekiel": {
			ID:               "ezekiel",
			DisplayName:      "Ezekiel Crane",
			Kind:             sim.KindNPCStateful,
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
		"player": {
			ID:               "player",
			DisplayName:      "Wanderer",
			Kind:             sim.KindPC,
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() {
		cancel()
		<-done
	}
}

// recordN drives N RecordInteractions for hannah→peer at staggered
// timestamps and returns the resulting fact count on the live row.
func recordN(t *testing.T, w *sim.World, peer sim.ActorID, n int, base time.Time) {
	t.Helper()
	for i := 0; i < n; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		if _, err := w.Send(sim.RecordInteraction("hannah", peer, sim.InteractionHeard, "x", at)); err != nil {
			t.Fatalf("RecordInteraction #%d: %v", i, err)
		}
	}
}

func candidates(t *testing.T, w *sim.World, at time.Time, limit int) []sim.ConsolidationCandidate {
	t.Helper()
	res, err := w.Send(sim.FindConsolidationCandidates(at, limit))
	if err != nil {
		t.Fatalf("FindConsolidationCandidates: %v", err)
	}
	cs, ok := res.([]sim.ConsolidationCandidate)
	if !ok {
		t.Fatalf("FindConsolidationCandidates: result type = %T", res)
	}
	return cs
}

// TestFindCandidates_NoneWhenSubThreshold confirms that a never-consolidated
// pair with fewer facts than ConsolidationFirstMinFacts is skipped.
func TestFindCandidates_NoneWhenSubThreshold(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	recordN(t, w, "ezekiel", sim.ConsolidationFirstMinFacts-1, at)
	cs := candidates(t, w, at, 5)
	if len(cs) != 0 {
		t.Errorf("len(candidates) = %d, want 0 (below first-min)", len(cs))
	}
}

// TestFindCandidates_FirstQualifiesAtMinFacts confirms the first-time
// gate fires exactly at ConsolidationFirstMinFacts entries.
func TestFindCandidates_FirstQualifiesAtMinFacts(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	recordN(t, w, "ezekiel", sim.ConsolidationFirstMinFacts, at)
	cs := candidates(t, w, at, 5)
	if len(cs) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(cs))
	}
	c := cs[0]
	if c.ActorID != "hannah" || c.PeerID != "ezekiel" {
		t.Errorf("candidate = (%s→%s), want (hannah→ezekiel)", c.ActorID, c.PeerID)
	}
	if c.ActorName != "Hannah" || c.PeerName != "Ezekiel Crane" {
		t.Errorf("display names = (%q,%q)", c.ActorName, c.PeerName)
	}
	if c.ActorLLMAgent != "salem-vendor" {
		t.Errorf("ActorLLMAgent = %q, want salem-vendor", c.ActorLLMAgent)
	}
	if len(c.Facts) != sim.ConsolidationFirstMinFacts {
		t.Errorf("len(Facts) = %d, want %d", len(c.Facts), sim.ConsolidationFirstMinFacts)
	}
}

// TestFindCandidates_CeilingForcesPass confirms a relationship that
// has been consolidated recently (well within the daily floor) but
// has accumulated >= ConsolidationCeiling facts is still picked up.
func TestFindCandidates_CeilingForcesPass(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	// Run enough interactions to cross the ceiling.
	recordN(t, w, "ezekiel", sim.ConsolidationCeiling, at)
	// Pretend we just consolidated 1 minute ago — well inside the
	// daily floor.
	recent := at.Add(-1 * time.Minute)
	if _, err := w.Send(sim.ApplyConsolidation("hannah", "ezekiel", "prior summary", nil, recent)); err != nil {
		t.Fatalf("ApplyConsolidation seed: %v", err)
	}
	cs := candidates(t, w, at, 5)
	if len(cs) != 1 {
		t.Fatalf("len(candidates) = %d, want 1 (ceiling)", len(cs))
	}
	if cs[0].PriorSummary != "prior summary" {
		t.Errorf("PriorSummary = %q, want %q", cs[0].PriorSummary, "prior summary")
	}
	if cs[0].LastConsolidated == nil || !cs[0].LastConsolidated.Equal(recent) {
		t.Errorf("LastConsolidated = %v, want %v", cs[0].LastConsolidated, recent)
	}
}

// TestFindCandidates_FloorOverdue confirms a relationship past the
// daily floor qualifies even with a tiny fact count.
func TestFindCandidates_FloorOverdue(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	// One fact — far below the first-min gate.
	recordN(t, w, "ezekiel", 1, at)
	// Pretend we consolidated 25h ago — past the daily floor.
	stale := at.Add(-25 * time.Hour)
	if _, err := w.Send(sim.ApplyConsolidation("hannah", "ezekiel", "old summary", nil, stale)); err != nil {
		t.Fatalf("ApplyConsolidation seed: %v", err)
	}
	cs := candidates(t, w, at, 5)
	if len(cs) != 1 {
		t.Fatalf("len(candidates) = %d, want 1 (floor)", len(cs))
	}
	if len(cs[0].Facts) != 1 {
		t.Errorf("len(Facts) = %d, want 1", len(cs[0].Facts))
	}
}

// TestFindCandidates_SkipsStatefulAndPC verifies the substrate gate:
// only KindNPCShared actors participate. A stateful NPC or PC with
// facts on a Relationship must be skipped.
func TestFindCandidates_SkipsStatefulAndPC(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	// hannah→ezekiel qualifies, but we want to confirm that
	// ezekiel→hannah (stateful actor) and player→hannah (PC) do not
	// produce candidates — RecordInteraction's Kind gate prevents
	// them populating Relationships in the first place, so the scan
	// should never see them. Drive enough interactions to qualify on
	// the hannah side.
	recordN(t, w, "ezekiel", sim.ConsolidationFirstMinFacts, at)
	// Try to write from the stateful side — should be no-op.
	if _, err := w.Send(sim.RecordInteraction("ezekiel", "hannah", sim.InteractionHeard, "x", at)); err != nil {
		t.Fatalf("RecordInteraction stateful→shared: %v", err)
	}
	// And from the PC side.
	if _, err := w.Send(sim.RecordInteraction("player", "hannah", sim.InteractionHeard, "x", at)); err != nil {
		t.Fatalf("RecordInteraction pc→shared: %v", err)
	}
	cs := candidates(t, w, at, 10)
	if len(cs) != 1 {
		t.Fatalf("len(candidates) = %d, want 1 (only hannah qualifies)", len(cs))
	}
	if cs[0].ActorID != "hannah" {
		t.Errorf("candidate actor = %q, want hannah", cs[0].ActorID)
	}
}

// TestFindCandidates_OrderingCeilingFirst verifies the sort order:
// ceiling-overdue entries come before non-ceiling, then NULLS first,
// then oldest LastConsolidatedAt.
func TestFindCandidates_OrderingCeilingFirst(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	// Seed two pairs:
	// - hannah→ezekiel: just-consolidated, but crosses ceiling (must be #1)
	// - hannah→<other>: never-consolidated, just-qualifies on first-min gate (must be #2)
	//
	// We need a second peer. Use the player slot; RecordInteraction
	// writes hannah→player regardless of player.Kind because gating
	// is on the rememberer side (hannah is KindNPCShared).
	recordN(t, w, "ezekiel", sim.ConsolidationCeiling, at)
	recent := at.Add(-1 * time.Minute)
	if _, err := w.Send(sim.ApplyConsolidation("hannah", "ezekiel", "recent summary", nil, recent)); err != nil {
		t.Fatalf("ApplyConsolidation seed: %v", err)
	}
	recordN(t, w, "player", sim.ConsolidationFirstMinFacts, at)

	cs := candidates(t, w, at, 5)
	if len(cs) != 2 {
		t.Fatalf("len(candidates) = %d, want 2", len(cs))
	}
	if cs[0].PeerID != "ezekiel" {
		t.Errorf("first candidate peer = %q, want ezekiel (ceiling)", cs[0].PeerID)
	}
	if cs[1].PeerID != "player" {
		t.Errorf("second candidate peer = %q, want player", cs[1].PeerID)
	}
}

// TestFindCandidates_LimitRespected confirms the limit caps results.
func TestFindCandidates_LimitRespected(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	// Two qualifying pairs.
	recordN(t, w, "ezekiel", sim.ConsolidationFirstMinFacts, at)
	recordN(t, w, "player", sim.ConsolidationFirstMinFacts, at)
	cs := candidates(t, w, at, 1)
	if len(cs) != 1 {
		t.Errorf("len(candidates) = %d, want 1 (limit)", len(cs))
	}
	// Limit=0 returns empty.
	cs = candidates(t, w, at, 0)
	if len(cs) != 0 {
		t.Errorf("len(candidates) with limit=0 = %d, want 0", len(cs))
	}
}

// TestFindCandidates_DeterministicOrder verifies ties break by
// (actor, peer) ID so the worker's per-sweep order is stable across
// runs even when ranking dimensions tie.
func TestFindCandidates_DeterministicOrder(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	// Two pairs with identical fact counts, both first-time-qualifying.
	recordN(t, w, "ezekiel", sim.ConsolidationFirstMinFacts, at)
	recordN(t, w, "player", sim.ConsolidationFirstMinFacts, at)
	cs := candidates(t, w, at, 5)
	peers := make([]sim.ActorID, len(cs))
	for i, c := range cs {
		peers[i] = c.PeerID
	}
	if !sort.SliceIsSorted(peers, func(i, j int) bool { return peers[i] < peers[j] }) {
		t.Errorf("peers not sorted by ID for deterministic tiebreak: %v", peers)
	}
}

// TestApplyConsolidation_BasicReplaceAndPrune confirms the apply path
// replaces SummaryText, prunes the snapshotted facts from the prefix,
// and stamps LastConsolidatedAt.
func TestApplyConsolidation_BasicReplaceAndPrune(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	recordN(t, w, "ezekiel", sim.ConsolidationFirstMinFacts, at)

	cs := candidates(t, w, at, 1)
	if len(cs) != 1 {
		t.Fatalf("setup: candidates = %d", len(cs))
	}
	c := cs[0]
	apply := at.Add(1 * time.Second)
	if _, err := w.Send(sim.ApplyConsolidation(c.ActorID, c.PeerID, "she's a regular", c.Facts, apply)); err != nil {
		t.Fatalf("ApplyConsolidation: %v", err)
	}

	snap := w.Published()
	rel := snap.Actors["hannah"].Relationships["ezekiel"]
	if rel.SummaryText != "she's a regular" {
		t.Errorf("SummaryText = %q, want %q", rel.SummaryText, "she's a regular")
	}
	if len(rel.SalientFacts) != 0 {
		t.Errorf("SalientFacts len = %d, want 0 (all pruned)", len(rel.SalientFacts))
	}
	if rel.LastConsolidatedAt == nil || !rel.LastConsolidatedAt.Equal(apply) {
		t.Errorf("LastConsolidatedAt = %v, want %v", rel.LastConsolidatedAt, apply)
	}
}

// TestApplyConsolidation_PostSnapshotAppendsSurvive is the load-bearing
// race-safety test: facts appended between snapshot and apply (without
// FIFO eviction shifting the prefix) must remain in SalientFacts after
// the prune.
func TestApplyConsolidation_PostSnapshotAppendsSurvive(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	recordN(t, w, "ezekiel", sim.ConsolidationFirstMinFacts, at)

	cs := candidates(t, w, at, 1)
	if len(cs) != 1 {
		t.Fatalf("setup: candidates = %d", len(cs))
	}
	c := cs[0]
	snapshotFacts := c.Facts

	// Simulate facts landing during the LLM call.
	post := at.Add(1 * time.Second)
	if _, err := w.Send(sim.RecordInteraction("hannah", "ezekiel", sim.InteractionHeard, "fresh fact A", post)); err != nil {
		t.Fatalf("RecordInteraction post-snapshot: %v", err)
	}
	if _, err := w.Send(sim.RecordInteraction("hannah", "ezekiel", sim.InteractionSpoke, "fresh fact B", post)); err != nil {
		t.Fatalf("RecordInteraction post-snapshot: %v", err)
	}

	apply := at.Add(2 * time.Second)
	if _, err := w.Send(sim.ApplyConsolidation(c.ActorID, c.PeerID, "new summary", snapshotFacts, apply)); err != nil {
		t.Fatalf("ApplyConsolidation: %v", err)
	}

	snap := w.Published()
	rel := snap.Actors["hannah"].Relationships["ezekiel"]
	if len(rel.SalientFacts) != 2 {
		t.Fatalf("SalientFacts len = %d, want 2 (post-snapshot survivors)", len(rel.SalientFacts))
	}
	if rel.SalientFacts[0].Text != "fresh fact A" || rel.SalientFacts[1].Text != "fresh fact B" {
		t.Errorf("post-prune facts = %v %v, want fresh A then B",
			rel.SalientFacts[0].Text, rel.SalientFacts[1].Text)
	}
}

// TestApplyConsolidation_RejectsEmptySummary confirms the guard.
func TestApplyConsolidation_RejectsEmptySummary(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	recordN(t, w, "ezekiel", sim.ConsolidationFirstMinFacts, at)

	_, err := w.Send(sim.ApplyConsolidation("hannah", "ezekiel", "", nil, at))
	if err == nil {
		t.Fatal("ApplyConsolidation with empty summary: no error")
	}
}

// TestApplyConsolidation_RejectsWhitespaceOnly confirms the substrate
// trims at the boundary and rejects whitespace-only input. Defends the
// "SummaryText is never set to whitespace-only via this path" invariant
// against direct callers (tests / admin paths / future code) — the
// cascade driver already trims, but the substrate Command is public.
func TestApplyConsolidation_RejectsWhitespaceOnly(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	recordN(t, w, "ezekiel", sim.ConsolidationFirstMinFacts, at)

	_, err := w.Send(sim.ApplyConsolidation("hannah", "ezekiel", "   \n\t  ", nil, at))
	if err == nil {
		t.Fatal("ApplyConsolidation with whitespace-only summary: no error")
	}

	// Row left untouched: SummaryText empty, no prune, no stamp.
	snap := w.Published()
	rel := snap.Actors["hannah"].Relationships["ezekiel"]
	if rel.SummaryText != "" {
		t.Errorf("SummaryText = %q, want untouched empty", rel.SummaryText)
	}
	if rel.LastConsolidatedAt != nil {
		t.Errorf("LastConsolidatedAt = %v, want nil (no stamp on whitespace-only)", rel.LastConsolidatedAt)
	}
}

// TestApplyConsolidation_TrimsAcceptedInput confirms that when the trim
// leaves non-empty content, the substrate stores the trimmed form (not
// the original with leading/trailing whitespace).
func TestApplyConsolidation_TrimsAcceptedInput(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	recordN(t, w, "ezekiel", sim.ConsolidationFirstMinFacts, at)

	cs := candidates(t, w, at, 1)
	if len(cs) != 1 {
		t.Fatalf("setup: candidates = %d", len(cs))
	}
	apply := at.Add(time.Second)
	if _, err := w.Send(sim.ApplyConsolidation(cs[0].ActorID, cs[0].PeerID, "   surrounded by whitespace.   ", cs[0].Facts, apply)); err != nil {
		t.Fatalf("ApplyConsolidation: %v", err)
	}

	snap := w.Published()
	rel := snap.Actors["hannah"].Relationships["ezekiel"]
	if rel.SummaryText != "surrounded by whitespace." {
		t.Errorf("SummaryText = %q, want trimmed 'surrounded by whitespace.'", rel.SummaryText)
	}
}

// TestApplyConsolidation_RejectsUnknownActor confirms the guard.
func TestApplyConsolidation_RejectsUnknownActor(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	_, err := w.Send(sim.ApplyConsolidation("ghost", "ezekiel", "x", nil, time.Now()))
	if err == nil {
		t.Fatal("ApplyConsolidation with unknown actor: no error")
	}
}

// TestApplyConsolidation_RejectsNonShared confirms the substrate guard
// (apply must not flip a stateful actor's relationship — they don't
// have one populated, but defensive check).
func TestApplyConsolidation_RejectsNonShared(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	_, err := w.Send(sim.ApplyConsolidation("ezekiel", "hannah", "x", nil, time.Now()))
	if err == nil {
		t.Fatal("ApplyConsolidation on stateful actor: no error")
	}
}

// TestApplyConsolidation_StaleSnapshotErrors verifies that a snapshot
// whose facts no longer match the live SalientFacts prefix returns
// ErrStaleConsolidationSnapshot and writes nothing — no summary
// install, no prune, no LastConsolidatedAt stamp. This is the
// FIFO-eviction-during-LLM-call race case: the prefix the worker saw
// has been evicted off the front, so we cannot safely prune by length
// without losing post-snapshot appends.
func TestApplyConsolidation_StaleSnapshotErrors(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	recordN(t, w, "ezekiel", 3, at)

	// Construct a snapshot that does NOT match the live prefix. The
	// live slice has 3 facts with text "x"; we pass a "snapshot" with
	// different text so prefix-equal returns false.
	bogusSnapshot := []sim.SalientFact{
		{At: at, Kind: sim.InteractionHeard, Text: "wrong text 1"},
		{At: at.Add(time.Second), Kind: sim.InteractionHeard, Text: "wrong text 2"},
		{At: at.Add(2 * time.Second), Kind: sim.InteractionHeard, Text: "wrong text 3"},
	}
	apply := at.Add(time.Minute)
	_, err := w.Send(sim.ApplyConsolidation("hannah", "ezekiel", "would-be summary", bogusSnapshot, apply))
	if err == nil {
		t.Fatal("ApplyConsolidation with stale snapshot: no error (want ErrStaleConsolidationSnapshot)")
	}
	if !errors.Is(err, sim.ErrStaleConsolidationSnapshot) {
		t.Errorf("err = %v, want ErrStaleConsolidationSnapshot via errors.Is", err)
	}

	// Row left untouched: SummaryText empty, all 3 facts present,
	// LastConsolidatedAt nil.
	snap := w.Published()
	rel := snap.Actors["hannah"].Relationships["ezekiel"]
	if rel.SummaryText != "" {
		t.Errorf("SummaryText = %q, want untouched empty", rel.SummaryText)
	}
	if len(rel.SalientFacts) != 3 {
		t.Errorf("SalientFacts len = %d, want 3 (no prune on stale)", len(rel.SalientFacts))
	}
	if rel.LastConsolidatedAt != nil {
		t.Errorf("LastConsolidatedAt = %v, want nil (no stamp on stale)", rel.LastConsolidatedAt)
	}
}

// TestApplyConsolidation_EmptySnapshotInstallsSummary verifies the
// edge case where the snapshot has zero facts (e.g. all facts evicted
// before the worker even got the snapshot from FindCandidates — not
// realistic today since FindCandidates filters on len > 0, but the
// apply path defends against it). Should install summary + stamp.
func TestApplyConsolidation_EmptySnapshotInstallsSummary(t *testing.T) {
	w, stop := buildConsolidationTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	recordN(t, w, "ezekiel", 3, at)
	// Send a first "seed" apply with nil snapshot to install the
	// relationship's LastConsolidatedAt — this is the same path the
	// other test seeds use, and it must succeed.
	if _, err := w.Send(sim.ApplyConsolidation("hannah", "ezekiel", "first summary", nil, at.Add(time.Second))); err != nil {
		t.Fatalf("ApplyConsolidation empty-snapshot: %v", err)
	}
	snap := w.Published()
	rel := snap.Actors["hannah"].Relationships["ezekiel"]
	if rel.SummaryText != "first summary" {
		t.Errorf("SummaryText = %q, want 'first summary'", rel.SummaryText)
	}
	if len(rel.SalientFacts) != 3 {
		t.Errorf("SalientFacts len = %d, want 3 (empty snapshot does not prune)", len(rel.SalientFacts))
	}
	if rel.LastConsolidatedAt == nil {
		t.Error("LastConsolidatedAt not stamped on empty-snapshot apply")
	}
}
