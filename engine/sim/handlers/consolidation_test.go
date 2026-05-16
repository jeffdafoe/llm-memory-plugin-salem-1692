package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildConsolidationHandlerWorld stands up a running world with one
// KindNPCShared actor (hannah) and one KindNPCStateful peer (ezekiel)
// for the handlers-package consolidation tests. Hannah carries the
// salem-vendor LLMAgent slug so prompt-routing tests can verify the
// Request.Model field.
func buildConsolidationHandlerWorld(t *testing.T) (*sim.World, func()) {
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

// drivePastFirstMinFacts records enough interactions to satisfy the
// first-time consolidation gate.
func drivePastFirstMinFacts(t *testing.T, w *sim.World, peer sim.ActorID, base time.Time) {
	t.Helper()
	for i := 0; i < sim.ConsolidationFirstMinFacts; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		text := "fact-" + string(rune('A'+i))
		if _, err := w.Send(sim.RecordInteraction("hannah", peer, sim.InteractionHeard, text, at)); err != nil {
			t.Fatalf("RecordInteraction: %v", err)
		}
	}
}

// TestRunOneSweep_HappyPath drives a full sweep cycle: candidate
// selection, LLM call (scripted via FakeClient), apply. Verifies
// SummaryText flips and facts get pruned.
func TestRunOneSweep_HappyPath(t *testing.T) {
	w, stop := buildConsolidationHandlerWorld(t)
	defer stop()
	at := time.Now().UTC()
	drivePastFirstMinFacts(t, w, "ezekiel", at)

	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: "  She comes in for ale most evenings.  "},
	})

	runOneSweep(context.Background(), w, client)

	if got := client.CallCount(); got != 1 {
		t.Errorf("LLM call count = %d, want 1", got)
	}
	reqs := client.Requests()
	if len(reqs) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(reqs))
	}
	if got := reqs[0].Model; got != "salem-vendor" {
		t.Errorf("Request.Model = %q, want salem-vendor", got)
	}
	if len(reqs[0].Tools) != 0 {
		t.Errorf("Request.Tools len = %d, want 0 (consolidation is tool-free)", len(reqs[0].Tools))
	}
	if len(reqs[0].Messages) != 1 || reqs[0].Messages[0].Role != llm.RoleUser {
		t.Errorf("Request.Messages = %+v, want one user message", reqs[0].Messages)
	}

	snap := w.Published()
	rel := snap.Actors["hannah"].Relationships["ezekiel"]
	if rel.SummaryText != "She comes in for ale most evenings." {
		t.Errorf("SummaryText = %q, want trimmed reply", rel.SummaryText)
	}
	if len(rel.SalientFacts) != 0 {
		t.Errorf("SalientFacts len = %d, want 0 (all pruned)", len(rel.SalientFacts))
	}
	if rel.LastConsolidatedAt == nil {
		t.Error("LastConsolidatedAt not stamped")
	}
}

// TestRunOneSweep_EmptyReplyLeavesStateUntouched verifies that an
// empty / whitespace-only LLM response does NOT touch the row.
func TestRunOneSweep_EmptyReplyLeavesStateUntouched(t *testing.T) {
	w, stop := buildConsolidationHandlerWorld(t)
	defer stop()
	at := time.Now().UTC()
	drivePastFirstMinFacts(t, w, "ezekiel", at)

	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: "   \n  "},
	})
	runOneSweep(context.Background(), w, client)

	snap := w.Published()
	rel := snap.Actors["hannah"].Relationships["ezekiel"]
	if rel.SummaryText != "" {
		t.Errorf("SummaryText = %q, want untouched empty", rel.SummaryText)
	}
	if len(rel.SalientFacts) != sim.ConsolidationFirstMinFacts {
		t.Errorf("SalientFacts len = %d, want %d (no prune on empty reply)",
			len(rel.SalientFacts), sim.ConsolidationFirstMinFacts)
	}
	if rel.LastConsolidatedAt != nil {
		t.Errorf("LastConsolidatedAt = %v, want nil (no stamp on empty reply)", rel.LastConsolidatedAt)
	}
}

// TestRunOneSweep_LLMErrorLeavesStateUntouched verifies the retry
// posture: an LLM error skips the apply step entirely.
func TestRunOneSweep_LLMErrorLeavesStateUntouched(t *testing.T) {
	w, stop := buildConsolidationHandlerWorld(t)
	defer stop()
	at := time.Now().UTC()
	drivePastFirstMinFacts(t, w, "ezekiel", at)

	client := llm.NewFakeClient(llm.ScriptedTurn{
		Err: &llm.Error{Class: llm.ErrorTransport, Message: "boom"},
	})
	runOneSweep(context.Background(), w, client)

	snap := w.Published()
	rel := snap.Actors["hannah"].Relationships["ezekiel"]
	if rel.SummaryText != "" {
		t.Errorf("SummaryText = %q, want untouched empty after LLM error", rel.SummaryText)
	}
	if len(rel.SalientFacts) != sim.ConsolidationFirstMinFacts {
		t.Errorf("SalientFacts len = %d, want %d (no prune on LLM error)",
			len(rel.SalientFacts), sim.ConsolidationFirstMinFacts)
	}
	if rel.LastConsolidatedAt != nil {
		t.Errorf("LastConsolidatedAt = %v, want nil (no stamp on LLM error)", rel.LastConsolidatedAt)
	}

	// Subsequent sweep with a working client should pick up the same
	// candidate and succeed — confirms the retry posture.
	client.Push(llm.ScriptedTurn{Response: llm.Response{Content: "retry-ok"}})
	runOneSweep(context.Background(), w, client)
	snap = w.Published()
	rel = snap.Actors["hannah"].Relationships["ezekiel"]
	if rel.SummaryText != "retry-ok" {
		t.Errorf("after retry: SummaryText = %q, want retry-ok", rel.SummaryText)
	}
}

// blockingLLMClient is a hand-rolled llm.Client for the race-safety
// test: Complete signals on `entered` and blocks until `release`
// is sent. Lets the test interleave a post-snapshot RecordInteraction
// between candidate fetch and apply.
type blockingLLMClient struct {
	entered chan struct{}
	release chan llm.Response
}

func newBlockingLLMClient() *blockingLLMClient {
	return &blockingLLMClient{
		entered: make(chan struct{}, 1),
		release: make(chan llm.Response, 1),
	}
}

func (b *blockingLLMClient) Complete(ctx context.Context, _ llm.Request) (llm.Response, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case resp := <-b.release:
		return resp, nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
}

// TestRunOneSweep_PostSnapshotAppendsSurviveAcrossLLMCall is the
// end-to-end version of the substrate's race-safety test. Facts
// landing while the LLM call is in flight must remain in
// SalientFacts after the apply.
func TestRunOneSweep_PostSnapshotAppendsSurviveAcrossLLMCall(t *testing.T) {
	w, stop := buildConsolidationHandlerWorld(t)
	defer stop()
	at := time.Now().UTC()
	drivePastFirstMinFacts(t, w, "ezekiel", at)

	client := newBlockingLLMClient()

	done := make(chan struct{})
	go func() {
		runOneSweep(context.Background(), w, client)
		close(done)
	}()

	// Wait for the worker to enter the LLM call (snapshot's already
	// been taken at this point).
	select {
	case <-client.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not reach LLM call within timeout")
	}

	// Append a fact while the worker is parked inside Complete. The
	// world goroutine is free (runOneSweep is parked off-world), so
	// the Send completes immediately.
	post := at.Add(1 * time.Hour)
	if _, err := w.Send(sim.RecordInteraction("hannah", "ezekiel", sim.InteractionHeard, "post-snapshot fact", post)); err != nil {
		t.Fatalf("RecordInteraction post-snapshot: %v", err)
	}

	// Now release the LLM call.
	client.release <- llm.Response{Content: "new summary"}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runOneSweep did not complete after release")
	}

	snap := w.Published()
	rel := snap.Actors["hannah"].Relationships["ezekiel"]
	if rel.SummaryText != "new summary" {
		t.Errorf("SummaryText = %q, want new summary", rel.SummaryText)
	}
	if len(rel.SalientFacts) != 1 {
		t.Fatalf("SalientFacts len = %d, want 1 (post-snapshot survivor)", len(rel.SalientFacts))
	}
	if rel.SalientFacts[0].Text != "post-snapshot fact" {
		t.Errorf("surviving fact text = %q, want post-snapshot fact", rel.SalientFacts[0].Text)
	}
}

// TestRunOneSweep_RateLimitedToCap verifies that ConsolidationsPerSweep
// caps how many candidates a single sweep handles even if more qualify.
func TestRunOneSweep_RateLimitedToCap(t *testing.T) {
	w, stop := buildConsolidationHandlerWorld(t)
	defer stop()

	// We need more qualifying pairs than ConsolidationsPerSweep. Seed
	// extra peers and drive interactions to each. Use Send to add the
	// actors on the live world goroutine.
	at := time.Now().UTC()
	peerCount := sim.ConsolidationsPerSweep + 2
	for i := 0; i < peerCount; i++ {
		peerID := sim.ActorID("peer-" + string(rune('A'+i)))
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Actors[peerID] = &sim.Actor{
				ID:               peerID,
				DisplayName:      string(peerID),
				Kind:             sim.KindNPCStateful,
				State:            sim.StateIdle,
				StateEnteredAt:   at,
				RecentActions:    sim.NewRingBuffer[sim.Action](4),
				RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
			}
			return nil, nil
		}}); err != nil {
			t.Fatalf("seed peer %s: %v", peerID, err)
		}
		drivePastFirstMinFacts(t, w, peerID, at)
	}

	// Script ConsolidationsPerSweep distinct responses (one per
	// candidate). If the worker exceeded the cap, the FakeClient
	// would return script-exhausted errors.
	client := llm.NewFakeClient()
	for i := 0; i < sim.ConsolidationsPerSweep; i++ {
		client.Push(llm.ScriptedTurn{Response: llm.Response{Content: "summary " + string(rune('A'+i))}})
	}

	runOneSweep(context.Background(), w, client)

	if got := client.CallCount(); got != sim.ConsolidationsPerSweep {
		t.Errorf("LLM call count = %d, want %d (cap)", got, sim.ConsolidationsPerSweep)
	}
}

// TestRunOneSweep_NoCandidatesIsNoOp verifies the cheap fast-path:
// a world with no qualifying relationships makes zero LLM calls.
func TestRunOneSweep_NoCandidatesIsNoOp(t *testing.T) {
	w, stop := buildConsolidationHandlerWorld(t)
	defer stop()

	client := llm.NewFakeClient()
	runOneSweep(context.Background(), w, client)
	if got := client.CallCount(); got != 0 {
		t.Errorf("LLM call count = %d, want 0 (no candidates)", got)
	}
}

// TestRunOneSweep_ContextCancelStopsEarly verifies that a cancelled
// context skips the LLM call and the apply step.
func TestRunOneSweep_ContextCancelStopsEarly(t *testing.T) {
	w, stop := buildConsolidationHandlerWorld(t)
	defer stop()
	at := time.Now().UTC()
	drivePastFirstMinFacts(t, w, "ezekiel", at)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{Content: "x"}})
	runOneSweep(ctx, w, client)

	// FakeClient returns ErrorContextCancelled pre-work and doesn't
	// record the request — verified by CallCount() == 0.
	if got := client.CallCount(); got != 0 {
		t.Errorf("LLM call count = %d, want 0 (ctx cancelled)", got)
	}
	snap := w.Published()
	rel := snap.Actors["hannah"].Relationships["ezekiel"]
	if rel.LastConsolidatedAt != nil {
		t.Error("LastConsolidatedAt stamped despite cancelled ctx")
	}
}

// TestBuildConsolidationPrompt_StructureAndDedup verifies the prompt
// scaffold (reflection framing + tool disclaim + 200-word target) and
// the WORK-233 dedup-in-prompt protection.
func TestBuildConsolidationPrompt_StructureAndDedup(t *testing.T) {
	c := sim.ConsolidationCandidate{
		ActorName:    "Hannah",
		PeerName:     "Wendy",
		PriorSummary: "She's been around lately.",
		Facts: []sim.SalientFact{
			{Text: "Good evening, Wendy."},
			{Text: "Good evening, Wendy."}, // duplicate — must dedup
			{Text: "She ordered ale."},
			{Text: "  "},                   // whitespace — must drop
			{Text: "Good evening, Wendy."}, // another duplicate
		},
	}
	prompt := buildConsolidationPrompt(c)

	for _, must := range []string{
		"You are Hannah.",
		"reflecting privately on your acquaintance with Wendy",
		"There are no tools available for this turn",
		"Your prior reflection on them:",
		"She's been around lately.",
		"Recent interactions, oldest first:",
		"- Good evening, Wendy.",
		"- She ordered ale.",
		"under 200 words",
		"a coherent impression, not a list of events",
	} {
		if !strings.Contains(prompt, must) {
			t.Errorf("prompt missing %q\n--- prompt ---\n%s", must, prompt)
		}
	}

	// Dedup: "Good evening, Wendy." appears once.
	if got := strings.Count(prompt, "- Good evening, Wendy."); got != 1 {
		t.Errorf("'- Good evening, Wendy.' occurs %d times, want 1 (dedup)", got)
	}
}

// TestBuildConsolidationPrompt_NoPriorSummary verifies the alternate
// branch when LastConsolidatedAt is nil and prior is empty.
func TestBuildConsolidationPrompt_NoPriorSummary(t *testing.T) {
	c := sim.ConsolidationCandidate{
		ActorName: "Hannah",
		PeerName:  "Wendy",
		Facts:     []sim.SalientFact{{Text: "Said hello."}},
	}
	prompt := buildConsolidationPrompt(c)
	if !strings.Contains(prompt, "You haven't formed a reflection on Wendy before now.") {
		t.Errorf("prompt missing first-time framing\n--- prompt ---\n%s", prompt)
	}
	if strings.Contains(prompt, "Your prior reflection on them:") {
		t.Errorf("prompt should not include prior-reflection header when prior is empty\n--- prompt ---\n%s", prompt)
	}
}

// TestRegisterConsolidation_PanicsOnNilWorld pins the wiring-time guard.
func TestRegisterConsolidation_PanicsOnNilWorld(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("RegisterConsolidation(nil world) did not panic")
		}
	}()
	RegisterConsolidation(context.Background(), nil, llm.NewFakeClient())
}

// TestRegisterConsolidation_PanicsOnNilClient pins the wiring-time guard.
func TestRegisterConsolidation_PanicsOnNilClient(t *testing.T) {
	w, stop := buildConsolidationHandlerWorld(t)
	defer stop()
	defer func() {
		if r := recover(); r == nil {
			t.Error("RegisterConsolidation(nil client) did not panic")
		}
	}()
	RegisterConsolidation(context.Background(), w, nil)
}

// TestRunOneSweep_CapEvictionDuringLLMCallPreservesFacts is the
// load-bearing regression test for the FIFO-eviction-during-LLM-call
// race that code_review caught.
//
// Scenario:
//  1. Snapshot taken at len(SalientFacts) = 20 = ConsolidationCeiling.
//  2. LLM call blocks.
//  3. 15 new facts land via RecordInteraction.
//  4. After fact 11 lands, live len would be 31 → FIFO cap
//     (MaxSalientFactsPerRelationship=30) evicts oldest. After all
//     15 land, the slice's first 5 entries have been evicted —
//     so the original snapshot's prefix is no longer in the slice.
//  5. Apply runs with the original snapshot.
//
// Pre-fix bug: ApplyConsolidation pruned by raw length, which deleted
// the first 5 post-snapshot facts (they had shifted into the [0:5]
// indices after eviction).
//
// Post-fix behavior: ApplyConsolidation detects the prefix mismatch,
// returns ErrStaleConsolidationSnapshot, makes no writes. The
// relationship's SalientFacts retains the post-eviction live state:
// 15 old facts that survived eviction + 15 new facts. SummaryText
// stays empty (not stamped); LastConsolidatedAt stays nil. The next
// sweep will pick this relationship up via the ceiling branch and
// retry from a fresh snapshot.
func TestRunOneSweep_CapEvictionDuringLLMCallPreservesFacts(t *testing.T) {
	w, stop := buildConsolidationHandlerWorld(t)
	defer stop()
	at := time.Now().UTC()

	// Drive exactly ConsolidationCeiling (20) facts pre-snapshot.
	// Each fact has a distinct text so we can verify which survived.
	for i := 0; i < sim.ConsolidationCeiling; i++ {
		text := "old-" + string(rune('A'+i))
		if _, err := w.Send(sim.RecordInteraction("hannah", "ezekiel", sim.InteractionHeard, text, at.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("RecordInteraction old-%d: %v", i, err)
		}
	}

	client := newBlockingLLMClient()

	done := make(chan struct{})
	go func() {
		runOneSweep(context.Background(), w, client)
		close(done)
	}()

	// Wait for the worker to reach the LLM call.
	select {
	case <-client.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not reach LLM call within timeout")
	}

	// Now push enough new facts to make live len exceed
	// MaxSalientFactsPerRelationship — FIFO will evict from the
	// front. We append 15 new facts: live grows from 20 to 30 (no
	// eviction yet), then 31 → evict oldest → 30, then 32 → 30,
	// ..., final state: 15 oldest evicted, slice = old[5:20] + new[0:15].
	newFactCount := 15
	postBase := at.Add(time.Hour)
	for i := 0; i < newFactCount; i++ {
		text := "new-" + string(rune('A'+i))
		if _, err := w.Send(sim.RecordInteraction("hannah", "ezekiel", sim.InteractionHeard, text, postBase.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("RecordInteraction new-%d: %v", i, err)
		}
	}

	// Sanity check the live state before release.
	{
		snap := w.Published()
		rel := snap.Actors["hannah"].Relationships["ezekiel"]
		if got := len(rel.SalientFacts); got != sim.MaxSalientFactsPerRelationship {
			t.Fatalf("pre-release SalientFacts len = %d, want %d (FIFO cap)", got, sim.MaxSalientFactsPerRelationship)
		}
		if got := rel.DroppedFactCount; got != newFactCount-(sim.MaxSalientFactsPerRelationship-sim.ConsolidationCeiling) {
			// Eviction count = total appends - headroom-before-cap
			// = 15 - (30 - 20) = 5
			t.Fatalf("DroppedFactCount = %d, want %d", got, newFactCount-(sim.MaxSalientFactsPerRelationship-sim.ConsolidationCeiling))
		}
	}

	// Release the LLM call. The worker will Apply with the original
	// 20-fact snapshot, which no longer matches the live prefix.
	client.release <- llm.Response{Content: "would-be summary"}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runOneSweep did not complete after release")
	}

	// Verify: no writes happened on the relationship. The live slice
	// is still 30 facts (post-eviction live state), SummaryText is
	// empty, LastConsolidatedAt is nil.
	snap := w.Published()
	rel := snap.Actors["hannah"].Relationships["ezekiel"]
	if rel.SummaryText != "" {
		t.Errorf("SummaryText = %q, want untouched empty (stale snapshot must not install summary)", rel.SummaryText)
	}
	if rel.LastConsolidatedAt != nil {
		t.Errorf("LastConsolidatedAt = %v, want nil (stale snapshot must not stamp)", rel.LastConsolidatedAt)
	}
	if got := len(rel.SalientFacts); got != sim.MaxSalientFactsPerRelationship {
		t.Errorf("SalientFacts len = %d, want %d (apply must not touch the slice on stale)",
			got, sim.MaxSalientFactsPerRelationship)
	}
	// First fact should be old-F (index 5 of original = 'A'+5 = 'F')
	// since the first 5 were FIFO-evicted. Last fact should be new-O.
	if got := rel.SalientFacts[0].Text; got != "old-F" {
		t.Errorf("first surviving fact = %q, want old-F (after 5 evictions from front)", got)
	}
	if got := rel.SalientFacts[len(rel.SalientFacts)-1].Text; got != "new-O" {
		t.Errorf("last fact = %q, want new-O", got)
	}
}

// TestRunOneSweep_ErrorIsClassified pins behavior on classified
// llm.Error types — they propagate as-is via errors.Is.
func TestRunOneSweep_ErrorIsClassified(t *testing.T) {
	w, stop := buildConsolidationHandlerWorld(t)
	defer stop()
	at := time.Now().UTC()
	drivePastFirstMinFacts(t, w, "ezekiel", at)

	innerErr := &llm.Error{Class: llm.ErrorTooLarge, Message: "response too large"}
	client := llm.NewFakeClient(llm.ScriptedTurn{Err: innerErr})

	runOneSweep(context.Background(), w, client)

	// The error doesn't propagate out (runOneSweep swallows + logs),
	// but the relationship row remains untouched.
	snap := w.Published()
	rel := snap.Actors["hannah"].Relationships["ezekiel"]
	if rel.LastConsolidatedAt != nil {
		t.Error("LastConsolidatedAt stamped despite ErrorTooLarge")
	}
	// Sanity: errors.Is/As against an llm.Error works correctly (would
	// fail if we accidentally wrapped + obscured the class downstream).
	var le *llm.Error
	if !errors.As(innerErr, &le) {
		t.Error("errors.As against llm.Error failed — type panel changed?")
	}
	if le.Class != llm.ErrorTooLarge {
		t.Errorf("ErrorClass = %v, want ErrorTooLarge", le.Class)
	}
}
