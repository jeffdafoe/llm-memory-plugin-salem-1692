package sim_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildNarrativeTestWorld stands up a world with three actors covering
// the Kind gate: one KindNPCShared (hannah), one KindNPCStateful
// (ezekiel — must be skipped), and one KindPC (the player — also
// skipped). Hannah carries the salem-vendor LLMAgent slug so prompt-
// routing tests can verify Request.Model.
func buildNarrativeTestWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()

	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:            "hannah",
			DisplayName:   "Hannah",
			Kind:          sim.KindNPCShared,
			LLMAgent:      "salem-vendor",
			State:         sim.StateIdle,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
		"ezekiel": {
			ID:            "ezekiel",
			DisplayName:   "Ezekiel Crane",
			Kind:          sim.KindNPCStateful,
			State:         sim.StateIdle,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
		"player": {
			ID:            "player",
			DisplayName:   "Wanderer",
			Kind:          sim.KindPC,
			State:         sim.StateIdle,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
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

// narrativeCandidates is a test helper that invokes
// FindNarrativeConsolidationCandidates and type-asserts the result.
func narrativeCandidates(t *testing.T, w *sim.World, at time.Time, limit int) []sim.NarrativeCandidate {
	t.Helper()
	res, err := w.Send(sim.FindNarrativeConsolidationCandidates(at, limit))
	if err != nil {
		t.Fatalf("FindNarrativeConsolidationCandidates: %v", err)
	}
	cs, ok := res.([]sim.NarrativeCandidate)
	if !ok {
		t.Fatalf("FindNarrativeConsolidationCandidates: result type = %T", res)
	}
	return cs
}

// TestFindNarrativeCandidates_FirstTimeNilNarrative confirms an actor
// with Narrative == nil qualifies (never-consolidated branch).
func TestFindNarrativeCandidates_FirstTimeNilNarrative(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	cs := narrativeCandidates(t, w, at, 5)
	if len(cs) != 1 {
		t.Fatalf("len(candidates) = %d, want 1 (hannah, nil Narrative)", len(cs))
	}
	c := cs[0]
	if c.ActorID != "hannah" {
		t.Errorf("ActorID = %q, want hannah", c.ActorID)
	}
	if c.ActorName != "Hannah" {
		t.Errorf("ActorName = %q, want Hannah", c.ActorName)
	}
	if c.ActorLLMAgent != "salem-vendor" {
		t.Errorf("ActorLLMAgent = %q, want salem-vendor", c.ActorLLMAgent)
	}
	if c.PriorSummary != "" {
		t.Errorf("PriorSummary = %q, want empty", c.PriorSummary)
	}
	if c.LastConsolidated != nil {
		t.Errorf("LastConsolidated = %v, want nil", c.LastConsolidated)
	}
}

// TestFindNarrativeCandidates_FirstTimeWithNarrativeButNilStamp
// confirms qualification when Narrative exists but
// LastConsolidatedAt is nil (e.g. seeded by dream pipeline but never
// consolidated by us).
func TestFindNarrativeCandidates_FirstTimeWithNarrativeButNilStamp(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	// Seed hannah's Narrative with a seed text but no LastConsolidatedAt.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"].Narrative = &sim.NarrativeState{
			SeedText:  "Hannah keeps the tavern.",
			CreatedAt: at.Add(-1 * time.Hour),
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed Narrative: %v", err)
	}

	cs := narrativeCandidates(t, w, at, 5)
	if len(cs) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(cs))
	}
	if cs[0].LastConsolidated != nil {
		t.Errorf("LastConsolidated = %v, want nil", cs[0].LastConsolidated)
	}
}

// TestFindNarrativeCandidates_FloorOverdue confirms an actor whose
// last consolidation is past the daily floor qualifies.
func TestFindNarrativeCandidates_FloorOverdue(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	stale := at.Add(-25 * time.Hour)
	if _, err := w.Send(sim.ApplyNarrativeConsolidation("hannah", "earlier reflection", stale)); err != nil {
		t.Fatalf("seed apply: %v", err)
	}

	cs := narrativeCandidates(t, w, at, 5)
	if len(cs) != 1 {
		t.Fatalf("len(candidates) = %d, want 1 (floor-overdue)", len(cs))
	}
	if cs[0].PriorSummary != "earlier reflection" {
		t.Errorf("PriorSummary = %q, want earlier reflection", cs[0].PriorSummary)
	}
	if cs[0].LastConsolidated == nil || !cs[0].LastConsolidated.Equal(stale) {
		t.Errorf("LastConsolidated = %v, want %v", cs[0].LastConsolidated, stale)
	}
}

// TestFindNarrativeCandidates_RecentlyConsolidatedSkipped confirms an
// actor consolidated within the floor is not picked.
func TestFindNarrativeCandidates_RecentlyConsolidatedSkipped(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	recent := at.Add(-1 * time.Hour)
	if _, err := w.Send(sim.ApplyNarrativeConsolidation("hannah", "fresh reflection", recent)); err != nil {
		t.Fatalf("seed apply: %v", err)
	}

	cs := narrativeCandidates(t, w, at, 5)
	if len(cs) != 0 {
		t.Errorf("len(candidates) = %d, want 0 (within floor)", len(cs))
	}
}

// TestFindNarrativeCandidates_SkipsStatefulAndPC verifies the Kind gate.
func TestFindNarrativeCandidates_SkipsStatefulAndPC(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	// Try to set a NarrativeState on the stateful actor and the PC
	// directly via a Command — the scan must skip both regardless.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["ezekiel"].Narrative = &sim.NarrativeState{}
		world.Actors["player"].Narrative = &sim.NarrativeState{}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed non-shared Narratives: %v", err)
	}

	cs := narrativeCandidates(t, w, at, 5)
	if len(cs) != 1 {
		t.Fatalf("len(candidates) = %d, want 1 (only hannah qualifies)", len(cs))
	}
	if cs[0].ActorID != "hannah" {
		t.Errorf("candidate = %q, want hannah", cs[0].ActorID)
	}
}

// TestFindNarrativeCandidates_LimitRespected confirms the limit caps results.
func TestFindNarrativeCandidates_LimitRespected(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	// Seed a second KindNPCShared actor to get 2 candidates.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["mara"] = &sim.Actor{
			ID:            "mara",
			DisplayName:   "Mara",
			Kind:          sim.KindNPCShared,
			LLMAgent:      "salem-vendor",
			State:         sim.StateIdle,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed mara: %v", err)
	}

	cs := narrativeCandidates(t, w, at, 1)
	if len(cs) != 1 {
		t.Errorf("len(candidates) = %d, want 1 (limit)", len(cs))
	}

	cs = narrativeCandidates(t, w, at, 0)
	if len(cs) != 0 {
		t.Errorf("len(candidates) with limit=0 = %d, want 0", len(cs))
	}
}

// TestFindNarrativeCandidates_OrderingNullsFirst confirms a never-
// consolidated actor sorts ahead of a previously-consolidated one.
func TestFindNarrativeCandidates_OrderingNullsFirst(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	// Seed mara (never-consolidated) and a stale consolidation on
	// hannah. Mara should rank first via NULLS-first.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["mara"] = &sim.Actor{
			ID:            "mara",
			DisplayName:   "Mara",
			Kind:          sim.KindNPCShared,
			LLMAgent:      "salem-vendor",
			State:         sim.StateIdle,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed mara: %v", err)
	}
	stale := at.Add(-25 * time.Hour)
	if _, err := w.Send(sim.ApplyNarrativeConsolidation("hannah", "stale reflection", stale)); err != nil {
		t.Fatalf("seed apply: %v", err)
	}

	cs := narrativeCandidates(t, w, at, 5)
	if len(cs) != 2 {
		t.Fatalf("len(candidates) = %d, want 2", len(cs))
	}
	if cs[0].ActorID != "mara" {
		t.Errorf("first = %q, want mara (NULLS first)", cs[0].ActorID)
	}
	if cs[1].ActorID != "hannah" {
		t.Errorf("second = %q, want hannah", cs[1].ActorID)
	}
}

// TestFindNarrativeCandidates_DeterministicOrder confirms ties break
// by ActorID.
func TestFindNarrativeCandidates_DeterministicOrder(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	// Seed two more never-consolidated shared actors so the tiebreak
	// path runs.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		for _, id := range []sim.ActorID{"mara", "abigail"} {
			world.Actors[id] = &sim.Actor{
				ID:            id,
				DisplayName:   string(id),
				Kind:          sim.KindNPCShared,
				LLMAgent:      "salem-vendor",
				State:         sim.StateIdle,
				RecentActions: sim.NewRingBuffer[sim.Action](4),
			}
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed extra actors: %v", err)
	}

	cs := narrativeCandidates(t, w, at, 10)
	ids := make([]sim.ActorID, len(cs))
	for i, c := range cs {
		ids[i] = c.ActorID
	}
	if !sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] < ids[j] }) {
		t.Errorf("candidate IDs not sorted for deterministic tiebreak: %v", ids)
	}
}

// TestFindNarrativeCandidates_EventsWindowAndLimit verifies the events
// snapshot honors the window cutoff and per-actor limit.
func TestFindNarrativeCandidates_EventsWindowAndLimit(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	// Append entries: 3 inside the 24h window, 2 outside it, plus 1
	// belonging to a different actor (must not appear).
	entries := []sim.ActionLogEntry{
		{ActorID: "hannah", OccurredAt: at.Add(-30 * time.Hour), ActionType: sim.ActionTypeSpoke, Text: "old-A"},
		{ActorID: "hannah", OccurredAt: at.Add(-26 * time.Hour), ActionType: sim.ActionTypeSpoke, Text: "old-B"},
		{ActorID: "hannah", OccurredAt: at.Add(-12 * time.Hour), ActionType: sim.ActionTypeSpoke, Text: "in-window-A"},
		{ActorID: "hannah", OccurredAt: at.Add(-6 * time.Hour), ActionType: sim.ActionTypePaid, Text: "in-window-B"},
		{ActorID: "hannah", OccurredAt: at.Add(-1 * time.Hour), ActionType: sim.ActionTypeConsumed, Text: "in-window-C"},
		{ActorID: "ezekiel", OccurredAt: at.Add(-2 * time.Hour), ActionType: sim.ActionTypeSpoke, Text: "other-actor"},
	}
	for _, e := range entries {
		if _, err := w.Send(sim.AppendActionLogEntry(e)); err != nil {
			t.Fatalf("AppendActionLogEntry: %v", err)
		}
	}

	cs := narrativeCandidates(t, w, at, 5)
	if len(cs) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(cs))
	}
	got := cs[0].Events
	if len(got) != 3 {
		t.Fatalf("len(Events) = %d, want 3 (only in-window hannah rows)", len(got))
	}
	if got[0].Text != "in-window-A" || got[1].Text != "in-window-B" || got[2].Text != "in-window-C" {
		t.Errorf("events out of order or unexpected: %+v", got)
	}
}

// TestFindNarrativeCandidates_EventsLimitDropsOldest verifies that when
// more events qualify than NarrativeEventsLimit, the oldest are dropped.
func TestFindNarrativeCandidates_EventsLimitDropsOldest(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	// Append NarrativeEventsLimit + 5 in-window entries.
	total := sim.NarrativeEventsLimit + 5
	for i := 0; i < total; i++ {
		// Stagger by minutes so they're all within the 24h window but
		// strictly ordered.
		occurredAt := at.Add(-1 * time.Hour).Add(time.Duration(i) * time.Minute)
		if _, err := w.Send(sim.AppendActionLogEntry(sim.ActionLogEntry{
			ActorID:    "hannah",
			OccurredAt: occurredAt,
			ActionType: sim.ActionTypeSpoke,
			Text:       "entry-" + string(rune('A'+i%26)),
		})); err != nil {
			t.Fatalf("AppendActionLogEntry #%d: %v", i, err)
		}
	}

	cs := narrativeCandidates(t, w, at, 1)
	if len(cs) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(cs))
	}
	if got := len(cs[0].Events); got != sim.NarrativeEventsLimit {
		t.Errorf("len(Events) = %d, want %d (limit)", got, sim.NarrativeEventsLimit)
	}
	// First retained event should be one of the LATER ones (oldest 5
	// dropped). Specifically: index 5 of the original list.
	want := at.Add(-1 * time.Hour).Add(5 * time.Minute)
	if got := cs[0].Events[0].OccurredAt; !got.Equal(want) {
		t.Errorf("first retained event OccurredAt = %v, want %v (5 oldest dropped)", got, want)
	}
}

// TestFindNarrativeCandidates_PeerSummariesAssembled verifies that
// non-empty Relationship.SummaryText values surface as
// NarrativePeerSummary entries. Empty summaries and peers missing from
// world.Actors are skipped.
func TestFindNarrativeCandidates_PeerSummariesAssembled(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	// Build hannah's relationships directly:
	//   - ezekiel: non-empty summary (must appear)
	//   - player: empty summary (must be skipped)
	//   - ghost: peer not in world.Actors (must be skipped)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		h := world.Actors["hannah"]
		h.Relationships = map[sim.ActorID]*sim.Relationship{
			"ezekiel": {SummaryText: "He keeps to himself."},
			"player":  {SummaryText: ""},
			"ghost":   {SummaryText: "Unknown to all."},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed relationships: %v", err)
	}

	cs := narrativeCandidates(t, w, at, 5)
	if len(cs) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(cs))
	}
	peers := cs[0].PeerSummaries
	if len(peers) != 1 {
		t.Fatalf("len(PeerSummaries) = %d, want 1 (only ezekiel)", len(peers))
	}
	p := peers[0]
	if p.PeerID != "ezekiel" || p.Name != "Ezekiel Crane" || p.Summary != "He keeps to himself." {
		t.Errorf("peers[0] = %+v, want {ezekiel, Ezekiel Crane, He keeps to himself.}", p)
	}
}

// TestFindNarrativeCandidates_PeerSummariesDistinctIDsSameName verifies
// the substrate doesn't collide two peers with identical DisplayName.
// PeerID is the deduplicating identity; the rendering carries both
// peers as separate entries with their own summaries.
func TestFindNarrativeCandidates_PeerSummariesDistinctIDsSameName(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	// Seed two distinct peers with identical DisplayName "Mary" and
	// different summaries. Both must appear in the snapshot.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		for _, id := range []sim.ActorID{"mary-1", "mary-2"} {
			world.Actors[id] = &sim.Actor{
				ID:            id,
				DisplayName:   "Mary",
				Kind:          sim.KindNPCStateful,
				State:         sim.StateIdle,
				RecentActions: sim.NewRingBuffer[sim.Action](4),
			}
		}
		world.Actors["hannah"].Relationships = map[sim.ActorID]*sim.Relationship{
			"mary-1": {SummaryText: "Mary one's impression."},
			"mary-2": {SummaryText: "Mary two's impression."},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed duplicate-name peers: %v", err)
	}

	cs := narrativeCandidates(t, w, at, 5)
	if len(cs) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(cs))
	}
	peers := cs[0].PeerSummaries
	if len(peers) != 2 {
		t.Fatalf("len(PeerSummaries) = %d, want 2 (both Marys distinct by PeerID)", len(peers))
	}
	// Ordering: same Name → tiebreak by PeerID (mary-1 before mary-2).
	if peers[0].PeerID != "mary-1" || peers[1].PeerID != "mary-2" {
		t.Errorf("peers IDs = %q,%q, want mary-1,mary-2 (PeerID tiebreak)", peers[0].PeerID, peers[1].PeerID)
	}
	if peers[0].Summary != "Mary one's impression." || peers[1].Summary != "Mary two's impression." {
		t.Errorf("peers summaries collided: %q vs %q", peers[0].Summary, peers[1].Summary)
	}
}

// TestHasSourceMaterial verifies the predicate, including the
// trim-aware prior check (whitespace-only prior is NOT source material).
func TestHasSourceMaterial(t *testing.T) {
	cases := []struct {
		name string
		c    sim.NarrativeCandidate
		want bool
	}{
		{"all empty", sim.NarrativeCandidate{}, false},
		{"prior only", sim.NarrativeCandidate{PriorSummary: "x"}, true},
		{"prior whitespace-only", sim.NarrativeCandidate{PriorSummary: "   \n  "}, false},
		{"events only", sim.NarrativeCandidate{Events: []sim.ActionLogEntry{{}}}, true},
		{"peers only", sim.NarrativeCandidate{PeerSummaries: []sim.NarrativePeerSummary{{Name: "x", Summary: "y"}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.HasSourceMaterial(); got != tc.want {
				t.Errorf("HasSourceMaterial = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestApplyNarrativeConsolidation_BasicInstallAndStamp confirms the
// apply path installs EvolvingSummary and stamps timestamps, auto-
// creating Actor.Narrative when nil.
func TestApplyNarrativeConsolidation_BasicInstallAndStamp(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	if _, err := w.Send(sim.ApplyNarrativeConsolidation("hannah", "she keeps to her own thoughts.", at)); err != nil {
		t.Fatalf("ApplyNarrativeConsolidation: %v", err)
	}

	snap := w.Published()
	ns := snap.Actors["hannah"].Narrative
	if ns == nil {
		t.Fatal("Narrative not auto-created")
	}
	if ns.EvolvingSummary != "she keeps to her own thoughts." {
		t.Errorf("EvolvingSummary = %q, want 'she keeps to her own thoughts.'", ns.EvolvingSummary)
	}
	if ns.LastConsolidatedAt == nil || !ns.LastConsolidatedAt.Equal(at) {
		t.Errorf("LastConsolidatedAt = %v, want %v", ns.LastConsolidatedAt, at)
	}
	if !ns.UpdatedAt.Equal(at) {
		t.Errorf("UpdatedAt = %v, want %v", ns.UpdatedAt, at)
	}
	if !ns.CreatedAt.Equal(at) {
		t.Errorf("CreatedAt = %v, want %v (auto-create stamp)", ns.CreatedAt, at)
	}
}

// TestApplyNarrativeConsolidation_PreservesSeedAndCreatedAt verifies
// that a pre-existing Narrative's SeedText and CreatedAt are not
// overwritten — the apply only touches EvolvingSummary +
// LastConsolidatedAt + UpdatedAt.
func TestApplyNarrativeConsolidation_PreservesSeedAndCreatedAt(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()
	created := at.Add(-48 * time.Hour)

	// Seed an existing Narrative.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"].Narrative = &sim.NarrativeState{
			SeedText:        "Hannah keeps the tavern; she is widowed and stoic.",
			EvolvingSummary: "first impression",
			CreatedAt:       created,
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed Narrative: %v", err)
	}

	if _, err := w.Send(sim.ApplyNarrativeConsolidation("hannah", "second impression", at)); err != nil {
		t.Fatalf("ApplyNarrativeConsolidation: %v", err)
	}

	snap := w.Published()
	ns := snap.Actors["hannah"].Narrative
	if ns.SeedText != "Hannah keeps the tavern; she is widowed and stoic." {
		t.Errorf("SeedText overwritten: %q", ns.SeedText)
	}
	if !ns.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt overwritten: %v, want %v", ns.CreatedAt, created)
	}
	if ns.EvolvingSummary != "second impression" {
		t.Errorf("EvolvingSummary = %q, want 'second impression'", ns.EvolvingSummary)
	}
}

// TestApplyNarrativeConsolidation_RejectsEmpty confirms the guard.
func TestApplyNarrativeConsolidation_RejectsEmpty(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	if _, err := w.Send(sim.ApplyNarrativeConsolidation("hannah", "", at)); err == nil {
		t.Fatal("ApplyNarrativeConsolidation with empty summary: no error")
	}

	snap := w.Published()
	if snap.Actors["hannah"].Narrative != nil {
		t.Errorf("Narrative auto-created on error path; should remain nil")
	}
}

// TestApplyNarrativeConsolidation_RejectsWhitespaceOnly confirms the
// substrate trims at the boundary and rejects whitespace-only input
// (defends the "EvolvingSummary is never set to whitespace-only via
// this path" invariant). The cascade driver already trims; this guard
// covers direct callers (tests, admin paths, future code).
func TestApplyNarrativeConsolidation_RejectsWhitespaceOnly(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	if _, err := w.Send(sim.ApplyNarrativeConsolidation("hannah", "   \n\t  ", at)); err == nil {
		t.Fatal("ApplyNarrativeConsolidation with whitespace-only summary: no error")
	}

	snap := w.Published()
	if snap.Actors["hannah"].Narrative != nil {
		t.Errorf("Narrative auto-created on error path; should remain nil")
	}
}

// TestApplyNarrativeConsolidation_TrimsAcceptedInput confirms that
// when the trim leaves non-empty content, the substrate stores the
// trimmed form (not the original with leading/trailing whitespace).
func TestApplyNarrativeConsolidation_TrimsAcceptedInput(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	if _, err := w.Send(sim.ApplyNarrativeConsolidation("hannah", "   surrounded by whitespace.   ", at)); err != nil {
		t.Fatalf("ApplyNarrativeConsolidation: %v", err)
	}

	snap := w.Published()
	if got := snap.Actors["hannah"].Narrative.EvolvingSummary; got != "surrounded by whitespace." {
		t.Errorf("EvolvingSummary = %q, want trimmed 'surrounded by whitespace.'", got)
	}
}

// TestApplyNarrativeConsolidation_RejectsUnknownActor confirms the guard.
func TestApplyNarrativeConsolidation_RejectsUnknownActor(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	if _, err := w.Send(sim.ApplyNarrativeConsolidation("ghost", "x", time.Now().UTC())); err == nil {
		t.Fatal("ApplyNarrativeConsolidation with unknown actor: no error")
	}
}

// TestApplyNarrativeConsolidation_RejectsNonShared confirms the
// substrate guard: stateful and PC actors cannot have their Narrative
// flipped via this path.
func TestApplyNarrativeConsolidation_RejectsNonShared(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	if _, err := w.Send(sim.ApplyNarrativeConsolidation("ezekiel", "x", time.Now().UTC())); err == nil {
		t.Fatal("ApplyNarrativeConsolidation on stateful actor: no error")
	}
	if _, err := w.Send(sim.ApplyNarrativeConsolidation("player", "x", time.Now().UTC())); err == nil {
		t.Fatal("ApplyNarrativeConsolidation on PC actor: no error")
	}
}

// TestStampNarrativeConsolidated_AutoCreates verifies the stamp-only
// path auto-creates Narrative when nil, without setting EvolvingSummary.
func TestStampNarrativeConsolidated_AutoCreates(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	if _, err := w.Send(sim.StampNarrativeConsolidated("hannah", at)); err != nil {
		t.Fatalf("StampNarrativeConsolidated: %v", err)
	}

	snap := w.Published()
	ns := snap.Actors["hannah"].Narrative
	if ns == nil {
		t.Fatal("Narrative not auto-created")
	}
	if ns.EvolvingSummary != "" {
		t.Errorf("EvolvingSummary = %q, want empty (stamp-only)", ns.EvolvingSummary)
	}
	if ns.LastConsolidatedAt == nil || !ns.LastConsolidatedAt.Equal(at) {
		t.Errorf("LastConsolidatedAt = %v, want %v", ns.LastConsolidatedAt, at)
	}
}

// TestStampNarrativeConsolidated_LeavesEvolvingSummaryAlone verifies
// that an existing EvolvingSummary is not overwritten.
func TestStampNarrativeConsolidated_LeavesEvolvingSummaryAlone(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	if _, err := w.Send(sim.ApplyNarrativeConsolidation("hannah", "first impression", at.Add(-time.Hour))); err != nil {
		t.Fatalf("seed apply: %v", err)
	}
	if _, err := w.Send(sim.StampNarrativeConsolidated("hannah", at)); err != nil {
		t.Fatalf("StampNarrativeConsolidated: %v", err)
	}

	snap := w.Published()
	ns := snap.Actors["hannah"].Narrative
	if ns.EvolvingSummary != "first impression" {
		t.Errorf("EvolvingSummary = %q, want 'first impression' (stamp must not overwrite)", ns.EvolvingSummary)
	}
	if !ns.LastConsolidatedAt.Equal(at) {
		t.Errorf("LastConsolidatedAt = %v, want %v (stamp updated)", ns.LastConsolidatedAt, at)
	}
}

// TestStampNarrativeConsolidated_RejectsUnknownAndNonShared confirms
// the guards.
func TestStampNarrativeConsolidated_RejectsUnknownAndNonShared(t *testing.T) {
	w, stop := buildNarrativeTestWorld(t)
	defer stop()
	at := time.Now().UTC()

	if _, err := w.Send(sim.StampNarrativeConsolidated("ghost", at)); err == nil {
		t.Fatal("StampNarrativeConsolidated with unknown actor: no error")
	}
	if _, err := w.Send(sim.StampNarrativeConsolidated("ezekiel", at)); err == nil {
		t.Fatal("StampNarrativeConsolidated on stateful actor: no error")
	}
}
