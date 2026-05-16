package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// scene_quote_sweep_test.go — Phase 3 PR S3 coverage of the aging
// sweep Command (EvaluateSceneQuoteSweep). The AfterFunc self-rearm
// chain is the same shape as the locomotion ticker; the substrate
// test exercises EvaluateSceneQuoteSweep directly to keep the test
// timing-deterministic.

// TestEvaluateSceneQuoteSweep_ExpiresPastTTL: a quote whose
// ExpiresAt has passed flips to Expired with ResolvedAt stamped,
// drops from the scene index, and emits SceneQuoteExpired{Reason: "ttl"}.
func TestEvaluateSceneQuoteSweep_ExpiresPastTTL(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	expired := captureSceneQuoteExpired(t, w)

	// Create at a fixed past time so the sweep "now" puts us past expiry.
	created := time.Now().UTC().Add(-15 * time.Minute) // past the 10-min default TTL
	res, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 2, false, "", nil, created))
	if err != nil {
		t.Fatalf("SceneQuoteCreate: %v", err)
	}
	qid := res.(sim.SceneQuoteCreateResult).QuoteID

	now := time.Now().UTC()
	if _, err := w.Send(sim.EvaluateSceneQuoteSweep(now)); err != nil {
		t.Fatalf("EvaluateSceneQuoteSweep: %v", err)
	}

	view := readLiveQuotes(t, w)
	q, ok := view.Quotes[qid]
	if !ok {
		t.Fatalf("quote %d missing post-sweep", qid)
	}
	if q.State != sim.SceneQuoteStateExpired {
		t.Errorf("State = %q, want expired", q.State)
	}
	if !q.ResolvedAt.Equal(now) {
		t.Errorf("ResolvedAt = %v, want %v", q.ResolvedAt, now)
	}
	if len(view.SceneIdx["sc1"]) != 0 {
		t.Errorf("scene index = %v, want empty post-sweep", view.SceneIdx["sc1"])
	}
	if len(*expired) != 1 || (*expired)[0].QuoteID != qid || (*expired)[0].Reason != sim.SceneQuoteExpiredReasonTTL {
		t.Fatalf("expired events = %+v, want one ttl flip for %d", *expired, qid)
	}
}

// TestEvaluateSceneQuoteSweep_SkipsActive: a quote still within TTL
// stays Active and emits nothing.
func TestEvaluateSceneQuoteSweep_SkipsActive(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	expired := captureSceneQuoteExpired(t, w)

	res, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 2, false, "", nil, time.Now().UTC()))
	if err != nil {
		t.Fatalf("SceneQuoteCreate: %v", err)
	}
	qid := res.(sim.SceneQuoteCreateResult).QuoteID

	if _, err := w.Send(sim.EvaluateSceneQuoteSweep(time.Now().UTC())); err != nil {
		t.Fatalf("EvaluateSceneQuoteSweep: %v", err)
	}

	view := readLiveQuotes(t, w)
	if view.Quotes[qid].State != sim.SceneQuoteStateActive {
		t.Errorf("state = %q, want active (within TTL)", view.Quotes[qid].State)
	}
	if len(*expired) != 0 {
		t.Errorf("expired events = %d, want 0", len(*expired))
	}
}

// TestEvaluateSceneQuoteSweep_SkipsNonActive: a quote already in a
// terminal state (e.g. Superseded by a prior duplicate-key) does NOT
// emit a second SceneQuoteExpired event when the sweep runs.
func TestEvaluateSceneQuoteSweep_SkipsNonActive(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	expired := captureSceneQuoteExpired(t, w)

	// q1 is stale (past TTL); q2 supersedes it BEFORE the sweep.
	// q2's CreatedAt uses a fresh `now` so q2 is well within TTL
	// when the sweep runs — that way the assertion isolates the
	// "sweep skips non-active" behavior from "sweep also expires
	// stale active quotes."
	staleAt := time.Now().UTC().Add(-15 * time.Minute)
	res1, _ := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 2, true, "", nil, staleAt))
	q1 := res1.(sim.SceneQuoteCreateResult).QuoteID

	// Supersede with different amount → q1 enters Superseded terminal.
	freshAt := time.Now().UTC()
	if _, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 5, true, "", nil, freshAt)); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	if got := len(*expired); got != 1 {
		t.Fatalf("post-supersede expired count = %d, want 1", got)
	}

	// Sweep now — q1 is past TTL but already Superseded, so it
	// should NOT re-fire as expired; q2 is still within TTL, so it
	// stays Active. Net: zero additional events.
	if _, err := w.Send(sim.EvaluateSceneQuoteSweep(freshAt)); err != nil {
		t.Fatalf("EvaluateSceneQuoteSweep: %v", err)
	}

	view := readLiveQuotes(t, w)
	if view.Quotes[q1].State != sim.SceneQuoteStateSuperseded {
		t.Errorf("q1 state = %q, want superseded (sweep must not overwrite)", view.Quotes[q1].State)
	}
	// Still only the supersede event — sweep added nothing.
	if got := len(*expired); got != 1 {
		t.Errorf("expired count after sweep = %d, want 1 (sweep should not re-fire)", got)
	}
}

// TestEvaluateSceneQuoteSweep_MultipleInOneSweep: two quotes expired
// at the same tick both flip in one sweep call.
func TestEvaluateSceneQuoteSweep_MultipleInOneSweep(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 5}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	expired := captureSceneQuoteExpired(t, w)

	staleAt := time.Now().UTC().Add(-15 * time.Minute)
	// Different qty so no supersede fires between them.
	res1, _ := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 2, false, "", nil, staleAt))
	res2, _ := w.Send(sim.SceneQuoteCreate("aldous", "ale", 2, 5, false, "", nil, staleAt))
	q1 := res1.(sim.SceneQuoteCreateResult).QuoteID
	q2 := res2.(sim.SceneQuoteCreateResult).QuoteID

	if _, err := w.Send(sim.EvaluateSceneQuoteSweep(time.Now().UTC())); err != nil {
		t.Fatalf("EvaluateSceneQuoteSweep: %v", err)
	}

	view := readLiveQuotes(t, w)
	if view.Quotes[q1].State != sim.SceneQuoteStateExpired {
		t.Errorf("q1 state = %q, want expired", view.Quotes[q1].State)
	}
	if view.Quotes[q2].State != sim.SceneQuoteStateExpired {
		t.Errorf("q2 state = %q, want expired", view.Quotes[q2].State)
	}
	if len(*expired) != 2 {
		t.Fatalf("expired events = %d, want 2", len(*expired))
	}
	// Emission order is sorted by QuoteID (substrate guarantee).
	if (*expired)[0].QuoteID > (*expired)[1].QuoteID {
		t.Errorf("emission order not sorted: %v then %v", (*expired)[0].QuoteID, (*expired)[1].QuoteID)
	}
}

// TestEvaluateSceneQuoteSweep_EmptyMap: sweep on a world with no
// quotes returns cleanly and emits nothing.
func TestEvaluateSceneQuoteSweep_EmptyMap(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 1}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	expired := captureSceneQuoteExpired(t, w)

	if _, err := w.Send(sim.EvaluateSceneQuoteSweep(time.Now().UTC())); err != nil {
		t.Fatalf("EvaluateSceneQuoteSweep on empty world: %v", err)
	}
	if len(*expired) != 0 {
		t.Errorf("expired events = %d, want 0", len(*expired))
	}
}
