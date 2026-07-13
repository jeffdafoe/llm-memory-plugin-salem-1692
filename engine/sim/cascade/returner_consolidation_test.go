package cascade

// Returner episodic-memory fold tests (LLM-383). Drive the returner-consolidation
// sweep and its sim primitives (FindReturnerConsolidationCandidates /
// ApplyReturnerConsolidation) against a running world with a durable returner set.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

const testRVID = sim.RecurringVisitorID("rvis-0000cafe")

// buildReturnerFoldWorld stands up a running world seeded with one durable
// returner carrying one PC acquaintance whose fact trail = facts. The returner is
// DEPARTED (no in-flight actor links to it) unless present is true, in which case
// a minimal in-flight visitor actor is injected so presentReturnerIDs sees it.
func buildReturnerFoldWorld(t *testing.T, facts []sim.SalientFact, priorSummary string, lastConsolidated *time.Time, present bool) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	rv := &sim.RecurringVisitor{
		ID: testRVID, Name: "Elias Drum", Archetype: "peddler", Origin: "Boston",
		Disposition: "weary", VisitCount: 2,
		FirstSeenAt: time.Now().UTC().Add(-40 * 24 * time.Hour), LastSeenAt: time.Now().UTC().Add(-2 * time.Hour),
		Acquaintances: map[sim.ActorID]*sim.RecurringAcquaintance{
			"pc-jeff": {
				PCActorID: "pc-jeff", PCDisplayName: "Jeff",
				FirstMetAt: time.Now().UTC().Add(-40 * 24 * time.Hour), LastMetAt: time.Now().UTC().Add(-2 * time.Hour),
				SalientFacts: facts, SummaryText: priorSummary, LastConsolidatedAt: lastConsolidated,
			},
		},
	}
	handles.RecurringVisitors.Seed(map[sim.RecurringVisitorID]*sim.RecurringVisitor{rv.ID: rv})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	if present {
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Actors["vstr-0000cafe"] = &sim.Actor{
				ID: "vstr-0000cafe", Kind: sim.KindNPCShared, LLMAgent: sim.VisitorAgentName,
				VisitorState: &sim.VisitorState{Archetype: "peddler", RecurringID: string(testRVID), ExpiresAt: time.Now().Add(time.Hour), Phase: sim.VisitorPhasePresent},
			}
			return nil, nil
		}}); err != nil {
			cancel()
			<-done
			t.Fatalf("inject present visitor: %v", err)
		}
	}
	return w, func() { cancel(); <-done }
}

func heardFacts(base time.Time, n int) []sim.SalientFact {
	out := make([]sim.SalientFact, n)
	for i := 0; i < n; i++ {
		out[i] = sim.NewSalientFact(base.Add(time.Duration(i)*time.Minute), sim.InteractionHeard, "fact-"+string(rune('A'+i)))
	}
	return out
}

func returnerFacts(t *testing.T, w *sim.World) (facts []sim.SalientFact, summary string, stamped bool) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		acq := world.RecurringVisitors[testRVID].Acquaintances["pc-jeff"]
		facts = append([]sim.SalientFact(nil), acq.SalientFacts...)
		summary = acq.SummaryText
		stamped = acq.LastConsolidatedAt != nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("read returner facts: %v", err)
	}
	return facts, summary, stamped
}

// TestRunOneReturnerSweep_FoldsDepartedReturner — a departed returner with a fact
// trail folds end to end: the LLM reply becomes SummaryText, facts are pruned, the
// fold is stamped, and the request routes to the shared salem-visitor VA.
func TestRunOneReturnerSweep_FoldsDepartedReturner(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	w, stop := buildReturnerFoldWorld(t, heardFacts(base, 4), "", nil, false /*departed*/)
	defer stop()

	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: "  Jeff frets over his fence line and buys nails each visit.  "},
	})
	runOneReturnerSweep(context.Background(), w, client)

	if got := client.CallCount(); got != 1 {
		t.Fatalf("LLM call count = %d, want 1", got)
	}
	reqs := client.Requests()
	if got := reqs[0].Model; got != sim.VisitorAgentName {
		t.Errorf("Request.Model = %q, want %q", got, sim.VisitorAgentName)
	}
	if len(reqs[0].Tools) != 0 {
		t.Errorf("Request.Tools = %d, want 0 (fold is tool-free)", len(reqs[0].Tools))
	}

	facts, summary, stamped := returnerFacts(t, w)
	if summary != "Jeff frets over his fence line and buys nails each visit." {
		t.Errorf("SummaryText = %q, want the trimmed reply", summary)
	}
	if len(facts) != 0 {
		t.Errorf("SalientFacts len = %d, want 0 (all pruned after fold)", len(facts))
	}
	if !stamped {
		t.Error("LastConsolidatedAt not stamped after fold")
	}
}

// TestFindReturnerConsolidation_PresentBelowCeilingSkipped — a returner still
// in-village (present) with a sub-ceiling trail is NOT a fold candidate; the trail
// keeps accruing until departure (or the ceiling backstop).
func TestFindReturnerConsolidation_PresentBelowCeilingSkipped(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	w, stop := buildReturnerFoldWorld(t, heardFacts(base, 5), "", nil, true /*present*/)
	defer stop()

	res, err := w.Send(sim.FindReturnerConsolidationCandidates(time.Now(), 5))
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if cands := res.([]sim.ConsolidationCandidate); len(cands) != 0 {
		t.Errorf("candidates = %d, want 0 (present returner below ceiling is not folded): %+v", len(cands), cands)
	}
}

// TestFindReturnerConsolidation_PresentAtCeilingFolds — a present returner whose
// trail reached the ceiling IS a candidate (the mid-visit marathon backstop).
func TestFindReturnerConsolidation_PresentAtCeilingFolds(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	w, stop := buildReturnerFoldWorld(t, heardFacts(base, sim.ReturnerConsolidationCeiling), "", nil, true /*present*/)
	defer stop()

	res, err := w.Send(sim.FindReturnerConsolidationCandidates(time.Now(), 5))
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	cands := res.([]sim.ConsolidationCandidate)
	if len(cands) != 1 || cands[0].ActorID != sim.ActorID(testRVID) || cands[0].PeerID != "pc-jeff" {
		t.Fatalf("candidates = %+v, want one for (%s, pc-jeff) at ceiling", cands, testRVID)
	}
}

// TestFindReturnerConsolidation_FirstFoldMinGate — a departed returner that has
// NEVER been folded needs at least ReturnerConsolidationFirstMinFacts to qualify
// (no fake-deep impression from one greeting); once it has a prior summary, any
// non-empty trail folds.
func TestFindReturnerConsolidation_FirstFoldMinGate(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)

	// Below the first-fold gate, never folded → not a candidate.
	wLow, stopLow := buildReturnerFoldWorld(t, heardFacts(base, sim.ReturnerConsolidationFirstMinFacts-1), "", nil, false)
	defer stopLow()
	res, err := wLow.Send(sim.FindReturnerConsolidationCandidates(time.Now(), 5))
	if err != nil {
		t.Fatalf("Find (low): %v", err)
	}
	if cands := res.([]sim.ConsolidationCandidate); len(cands) != 0 {
		t.Errorf("candidates = %d, want 0 (first fold below min facts): %+v", len(cands), cands)
	}

	// Same tiny trail but a prior fold exists → folds any non-empty trail.
	stamp := base.Add(-24 * time.Hour)
	wPrior, stopPrior := buildReturnerFoldWorld(t, heardFacts(base, 1), "an earlier impression", &stamp, false)
	defer stopPrior()
	res2, err := wPrior.Send(sim.FindReturnerConsolidationCandidates(time.Now(), 5))
	if err != nil {
		t.Fatalf("Find (prior): %v", err)
	}
	if cands := res2.([]sim.ConsolidationCandidate); len(cands) != 1 {
		t.Errorf("candidates = %d, want 1 (subsequent fold folds any non-empty trail): %+v", len(cands), cands)
	}
}

// TestApplyReturnerConsolidation_StaleSnapshot — if the live trail's prefix no
// longer matches the snapshot (capture raced a mid-visit fold), apply writes
// nothing and returns ErrStaleConsolidationSnapshot.
func TestApplyReturnerConsolidation_StaleSnapshot(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	w, stop := buildReturnerFoldWorld(t, heardFacts(base, 3), "", nil, true)
	defer stop()

	// A snapshot whose first fact differs from the live trail (simulating eviction).
	stale := []sim.SalientFact{sim.NewSalientFact(base.Add(-time.Hour), sim.InteractionHeard, "evicted-fact")}
	_, err := w.Send(sim.ApplyReturnerConsolidation(sim.ActorID(testRVID), "pc-jeff", "new summary", stale, time.Now()))
	if !errors.Is(err, sim.ErrStaleConsolidationSnapshot) {
		t.Fatalf("apply err = %v, want ErrStaleConsolidationSnapshot", err)
	}
	facts, summary, stamped := returnerFacts(t, w)
	if len(facts) != 3 || summary != "" || stamped {
		t.Errorf("stale apply mutated state: facts=%d summary=%q stamped=%v, want 3/\"\"/false", len(facts), summary, stamped)
	}
}

// TestApplyReturnerConsolidation_BoundsSummaryLength — a runaway LLM fold is
// rune-truncated to MaxReturnerSummaryRunes before storage, so Go provably
// satisfies the summary_sane DB CHECK (char_length <= 4000) and can't wedge a
// checkpoint. (The blocker code_review caught: the migration claimed the CHECK was
// a bound Go satisfies, but nothing truncated the summary before store.)
func TestApplyReturnerConsolidation_BoundsSummaryLength(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	w, stop := buildReturnerFoldWorld(t, heardFacts(base, 3), "", nil, false)
	defer stop()

	huge := strings.Repeat("x", sim.MaxReturnerSummaryRunes+500)
	// nil snapshot → empty-snapshot install path (no prefix verify), so this pins
	// the summary bound in isolation.
	if _, err := w.Send(sim.ApplyReturnerConsolidation(sim.ActorID(testRVID), "pc-jeff", huge, nil, time.Now())); err != nil {
		t.Fatalf("apply: %v", err)
	}
	_, summary, _ := returnerFacts(t, w)
	if got := utf8.RuneCountInString(summary); got != sim.MaxReturnerSummaryRunes {
		t.Errorf("stored summary rune len = %d, want %d (truncated below the 4000-char DB CHECK)", got, sim.MaxReturnerSummaryRunes)
	}
}

// TestBuildReturnerFoldPrompt_AttributesHeard — the reused buildConsolidationPrompt
// attributes a heard fact to the PC (not the returner), so the fold can't mistake
// the PC's words for the returner's own (the cross-attribution guard the fold half
// of the feature relies on).
func TestBuildReturnerFoldPrompt_AttributesHeard(t *testing.T) {
	c := sim.ConsolidationCandidate{
		ActorID: sim.ActorID(testRVID), PeerID: "pc-jeff",
		ActorName: "Elias Drum", PeerName: "Jeff", ActorLLMAgent: sim.VisitorAgentName,
		Facts: []sim.SalientFact{sim.NewSalientFact(time.Now(), sim.InteractionHeard, "the fence won't hold")},
	}
	prompt := buildConsolidationPrompt(c)
	if !strings.Contains(prompt, `Jeff said: "the fence won't hold"`) {
		t.Errorf("prompt does not attribute the heard fact to Jeff:\n%s", prompt)
	}
}
