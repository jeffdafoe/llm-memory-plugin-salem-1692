package cascade

import (
	"context"
	"errors"
	"fmt"
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
// first-time consolidation gate. Uses a dealing-relevant (transactional) fact
// kind so the pair also clears the LLM-434 dealing gate in candidate selection.
func drivePastFirstMinFacts(t *testing.T, w *sim.World, peer sim.ActorID, base time.Time) {
	t.Helper()
	for i := 0; i < sim.ConsolidationFirstMinFacts; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		text := "fact-" + string(rune('A'+i))
		if _, err := w.Send(sim.RecordInteraction("hannah", peer, sim.InteractionServed, text, at)); err != nil {
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
				ID:            peerID,
				DisplayName:   string(peerID),
				Kind:          sim.KindNPCStateful,
				State:         sim.StateIdle,
				RecentActions: sim.NewRingBuffer[sim.Action](4),
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
		"What the ledger records of your dealings, oldest first:",
		"- Good evening, Wendy.",
		"- She ordered ale.",
		"one or two sentences",
		"the next time you deal with them",
		"reply with exactly: nothing notable",
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

// TestBuildConsolidationPrompt_AttributesSpeaker pins the fix for the
// cross-attribution corruption: spoke facts (the actor's own words) and heard
// facts (the peer's words) render with distinct speaker attribution, so the
// consolidating model cannot fold the actor's own utterances into its
// impression of the peer. Transactional facts (self-describing text) pass
// through unchanged, and identical utterances from different speakers do NOT
// dedup against each other.
func TestBuildConsolidationPrompt_AttributesSpeaker(t *testing.T) {
	c := sim.ConsolidationCandidate{
		ActorName: "Hannah",
		PeerName:  "Jefferey",
		Facts: []sim.SalientFact{
			{Kind: sim.InteractionHeard, Text: "Hello Hannah"},
			{Kind: sim.InteractionSpoke, Text: "I have bread available."},
			{Kind: sim.InteractionSpoke, Text: "I have bread available."}, // dup of own line — must dedup
			{Kind: sim.InteractionHeard, Text: "I have bread available."}, // same text, peer said it — must survive
			{Kind: sim.InteractionPaidBy, Text: "Jefferey paid me 5 coins for bread."},
		},
	}
	prompt := buildConsolidationPrompt(c)

	for _, must := range []string{
		`- Jefferey said: "Hello Hannah"`,
		`- I said: "I have bread available."`,
		`- Jefferey said: "I have bread available."`,
		"- Jefferey paid me 5 coins for bread.", // transactional fact passes through
	} {
		if !strings.Contains(prompt, must) {
			t.Errorf("prompt missing %q\n--- prompt ---\n%s", must, prompt)
		}
	}

	// The actor's own pitch must never render as a bare bullet that could read
	// back as the peer's words — that was the corruption.
	if strings.Contains(prompt, "- I have bread available.") {
		t.Errorf("own utterance rendered without speaker attribution\n--- prompt ---\n%s", prompt)
	}
	// Dedup keys on the rendered line: the repeated "I said:" pitch collapses to
	// one, but the peer's identical-text line is a distinct fact and survives.
	if got := strings.Count(prompt, `- I said: "I have bread available."`); got != 1 {
		t.Errorf("'I said: \"I have bread available.\"' occurs %d times, want 1 (dedup)", got)
	}
	if got := strings.Count(prompt, `- Jefferey said: "I have bread available."`); got != 1 {
		t.Errorf("'Jefferey said: \"I have bread available.\"' occurs %d times, want 1", got)
	}

	// Grouping (LLM-499): speech renders under the said header, the
	// transactional fact under the ledger header, said section first.
	saidIdx := strings.Index(prompt, "What was said between you, oldest first:")
	ledgerIdx := strings.Index(prompt, "What the ledger records of your dealings, oldest first:")
	if saidIdx < 0 || ledgerIdx < 0 {
		t.Fatalf("prompt missing a group header (said=%d, ledger=%d)\n--- prompt ---\n%s", saidIdx, ledgerIdx, prompt)
	}
	if saidIdx > ledgerIdx {
		t.Errorf("said section renders after ledger section\n--- prompt ---\n%s", prompt)
	}
	paidIdx := strings.Index(prompt, "- Jefferey paid me 5 coins for bread.")
	if paidIdx < ledgerIdx {
		t.Errorf("transactional fact renders outside the ledger section\n--- prompt ---\n%s", prompt)
	}
}

// TestBuildConsolidationPrompt_LedgerAuthority pins the LLM-499 fix for the
// live wage-story inversion: a full labor settle ("earned 1 milk and 12
// coins") and a smaller follow-up pay ("paid me 5 coins") sat in one
// undifferentiated list with the surrounding speech, and the model anchored
// on the last pay line — writing a durable distrust fact about an employer
// who overpaid. The ledger lines must render in their own group, oldest
// first, and the closing instruction must grant them authority over talk
// and state that each happened in addition to the others.
func TestBuildConsolidationPrompt_LedgerAuthority(t *testing.T) {
	c := sim.ConsolidationCandidate{
		ActorName: "Abraham Warren",
		PeerName:  "Elizabeth Ellis",
		Facts: []sim.SalientFact{
			{Kind: sim.InteractionHeard, Text: "Twelve coins and a mug of milk for four hours' work turning the compost heap."},
			{Kind: sim.InteractionWorked, Text: "I worked for Elizabeth Ellis and earned 1 milk and 12 coins for about 4 hours of work."},
			{Kind: sim.InteractionPaidBy, Text: "Elizabeth Ellis paid me 5 coins for day's wages."},
		},
	}
	prompt := buildConsolidationPrompt(c)

	for _, must := range []string{
		"What was said between you, oldest first:",
		`- Elizabeth Ellis said: "Twelve coins and a mug of milk for four hours' work turning the compost heap."`,
		"What the ledger records of your dealings, oldest first:",
		"- I worked for Elizabeth Ellis and earned 1 milk and 12 coins for about 4 hours of work.",
		"- Elizabeth Ellis paid me 5 coins for day's wages.",
		"The ledger is the true record of your dealings",
		"each one in addition to the others",
		"trust the ledger",
	} {
		if !strings.Contains(prompt, must) {
			t.Errorf("prompt missing %q\n--- prompt ---\n%s", must, prompt)
		}
	}

	// The ledger group keeps chronology: the settle line renders before the
	// follow-up pay line.
	settleIdx := strings.Index(prompt, "- I worked for Elizabeth Ellis")
	payIdx := strings.Index(prompt, "- Elizabeth Ellis paid me 5 coins")
	if settleIdx < 0 || payIdx < 0 || settleIdx > payIdx {
		t.Errorf("ledger lines out of order (settle=%d, pay=%d)\n--- prompt ---\n%s", settleIdx, payIdx, prompt)
	}
}

// TestBuildConsolidationPrompt_KindlessFactIsLedger pins the classification
// boundary for a zero-value Kind (legacy/hand-seeded rows — every v2 write
// path stamps a typed kind): it lands in the ledger group, matching
// relHasDealingFact's non-speech-is-dealing default.
func TestBuildConsolidationPrompt_KindlessFactIsLedger(t *testing.T) {
	c := sim.ConsolidationCandidate{
		ActorName: "Hannah",
		PeerName:  "Wendy",
		Facts: []sim.SalientFact{
			{Kind: sim.InteractionSpoke, Text: "Good evening, Wendy."},
			{Text: "Wendy settled her tab."},
		},
	}
	prompt := buildConsolidationPrompt(c)

	ledgerIdx := strings.Index(prompt, "What the ledger records of your dealings, oldest first:")
	factIdx := strings.Index(prompt, "- Wendy settled her tab.")
	if ledgerIdx < 0 || factIdx < ledgerIdx {
		t.Errorf("kindless fact not in ledger group (header=%d, fact=%d)\n--- prompt ---\n%s", ledgerIdx, factIdx, prompt)
	}
}

// TestBuildConsolidationPrompt_SpeechOnlyOmitsLedger — a pure-social fact
// batch (the common case) renders no ledger header and no ledger-authority
// instruction, so speech-only reflections aren't told to trust a ledger
// that lists nothing.
func TestBuildConsolidationPrompt_SpeechOnlyOmitsLedger(t *testing.T) {
	c := sim.ConsolidationCandidate{
		ActorName: "Hannah",
		PeerName:  "Wendy",
		Facts: []sim.SalientFact{
			{Kind: sim.InteractionSpoke, Text: "Good evening, Wendy."},
			{Kind: sim.InteractionHeard, Text: "Good evening, Hannah."},
		},
	}
	prompt := buildConsolidationPrompt(c)

	if !strings.Contains(prompt, "What was said between you, oldest first:") {
		t.Errorf("prompt missing said header\n--- prompt ---\n%s", prompt)
	}
	for _, mustNot := range []string{
		"What the ledger records of your dealings",
		"trust the ledger",
	} {
		if strings.Contains(prompt, mustNot) {
			t.Errorf("speech-only prompt contains %q\n--- prompt ---\n%s", mustNot, prompt)
		}
	}
}

// TestRenderConsolidationFactLine_Attribution locks the per-kind attribution
// invariant: spoke/heard get quoted speaker attribution (with a fallback when
// the peer name is blank), and the transactional kinds — whose text already
// bakes in attribution — pass through unquoted and unchanged. A new bare-speech
// kind added without an explicit case here would surface as a failing/ missing
// row, the guard code_review asked for against silent re-conflation.
func TestRenderConsolidationFactLine_Attribution(t *testing.T) {
	cases := []struct {
		name string
		f    sim.SalientFact
		peer string
		want string
	}{
		{"spoke", sim.SalientFact{Kind: sim.InteractionSpoke, Text: "Hello"}, "Ezekiel", `I said: "Hello"`},
		{"heard", sim.SalientFact{Kind: sim.InteractionHeard, Text: "Hello"}, "Ezekiel", `Ezekiel said: "Hello"`},
		{"heard blank peer falls back", sim.SalientFact{Kind: sim.InteractionHeard, Text: "Hello"}, "   ", `They said: "Hello"`},
		{"paid passthrough", sim.SalientFact{Kind: sim.InteractionPaid, Text: "I paid Ezekiel 5 coins."}, "Ezekiel", "I paid Ezekiel 5 coins."},
		{"paid_by passthrough", sim.SalientFact{Kind: sim.InteractionPaidBy, Text: "Ezekiel paid me 5 coins."}, "Ezekiel", "Ezekiel paid me 5 coins."},
		{"delivered passthrough", sim.SalientFact{Kind: sim.InteractionDelivered, Text: "I delivered bread to Ezekiel."}, "Ezekiel", "I delivered bread to Ezekiel."},
		{"received passthrough", sim.SalientFact{Kind: sim.InteractionReceived, Text: "Ezekiel delivered bread to me."}, "Ezekiel", "Ezekiel delivered bread to me."},
		{"empty text drops", sim.SalientFact{Kind: sim.InteractionSpoke, Text: "   "}, "Ezekiel", ""},
	}
	for _, c := range cases {
		if got := renderConsolidationFactLine(c.f, c.peer); got != c.want {
			t.Errorf("%s: renderConsolidationFactLine = %q, want %q", c.name, got, c.want)
		}
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
//  1. Snapshot taken at len(SalientFacts) = ConsolidationCeiling.
//  2. LLM call blocks.
//  3. gap+evictN new facts land via RecordInteraction, where
//     gap = MaxSalientFactsPerRelationship - ConsolidationCeiling. Live
//     len climbs from the ceiling past the cap; FIFO evicts the oldest
//     evictN off the front — so the original snapshot's prefix is no
//     longer in the slice.
//  4. Apply runs with the original snapshot.
//
// Pre-fix bug: ApplyConsolidation pruned by raw length, which deleted
// the post-snapshot facts that had shifted into the evicted front
// indices.
//
// Post-fix behavior: ApplyConsolidation detects the prefix mismatch,
// returns ErrStaleConsolidationSnapshot, makes no writes. The
// relationship's SalientFacts retains the post-eviction live state.
// SummaryText stays empty (not stamped); LastConsolidatedAt stays nil.
// The next sweep picks this relationship up again and retries from a
// fresh snapshot.
func TestRunOneSweep_CapEvictionDuringLLMCallPreservesFacts(t *testing.T) {
	w, stop := buildConsolidationHandlerWorld(t)
	defer stop()
	at := time.Now().UTC()

	// This test needs headroom between the snapshot point (the ceiling)
	// and the cap for FIFO eviction to be exercised at all.
	if sim.ConsolidationCeiling >= sim.MaxSalientFactsPerRelationship {
		t.Fatalf("test requires ConsolidationCeiling < MaxSalientFactsPerRelationship")
	}

	// Drive exactly ConsolidationCeiling facts pre-snapshot. Each fact
	// has a distinct numeric text so we can verify which survived. Uses a
	// dealing kind so the pair clears the LLM-434 selection gate — the eviction
	// race this test exercises is independent of fact kind.
	for i := 0; i < sim.ConsolidationCeiling; i++ {
		text := fmt.Sprintf("old-%d", i)
		if _, err := w.Send(sim.RecordInteraction("hannah", "ezekiel", sim.InteractionServed, text, at.Add(time.Duration(i)*time.Second))); err != nil {
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
	// MaxSalientFactsPerRelationship — FIFO will evict from the front.
	// gap is the headroom between the snapshot point (the ceiling) and
	// the cap; appending gap+evictN facts evicts the oldest evictN off
	// the front, so the snapshot's prefix no longer matches the live
	// slice. Final state: old[evictN:ceiling] + new[0:newFactCount].
	gap := sim.MaxSalientFactsPerRelationship - sim.ConsolidationCeiling
	const evictN = 10
	newFactCount := gap + evictN
	postBase := at.Add(time.Hour)
	for i := 0; i < newFactCount; i++ {
		text := fmt.Sprintf("new-%d", i)
		if _, err := w.Send(sim.RecordInteraction("hannah", "ezekiel", sim.InteractionServed, text, postBase.Add(time.Duration(i)*time.Second))); err != nil {
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
		if got := rel.DroppedFactCount; got != evictN {
			// Eviction count = appends beyond the cap headroom
			// = newFactCount - gap = evictN.
			t.Fatalf("DroppedFactCount = %d, want %d", got, evictN)
		}
	}

	// Release the LLM call. The worker will Apply with the original
	// ceiling-sized snapshot, which no longer matches the live prefix.
	client.release <- llm.Response{Content: "would-be summary"}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runOneSweep did not complete after release")
	}

	// Verify: no writes happened on the relationship. The live slice
	// is still at the cap (post-eviction live state), SummaryText is
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
	// First surviving fact is old[evictN] (the first evictN were FIFO-
	// evicted off the front); last is the newest appended fact.
	if got, want := rel.SalientFacts[0].Text, fmt.Sprintf("old-%d", evictN); got != want {
		t.Errorf("first surviving fact = %q, want %q (after %d evictions from front)", got, want, evictN)
	}
	if got, want := rel.SalientFacts[len(rel.SalientFacts)-1].Text, fmt.Sprintf("new-%d", newFactCount-1); got != want {
		t.Errorf("last fact = %q, want %q", got, want)
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

// TestRunOneSweep_NothingNotablePrunes verifies the LLM-426 outcome: a reply of
// the "nothing notable" sentinel drops the relationship row entirely rather
// than storing filler prose, so the graph keeps only edges that carry a
// judgment.
func TestRunOneSweep_NothingNotablePrunes(t *testing.T) {
	w, stop := buildConsolidationHandlerWorld(t)
	defer stop()
	at := time.Now().UTC()
	drivePastFirstMinFacts(t, w, "ezekiel", at)

	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: "  Nothing notable.  "},
	})
	runOneSweep(context.Background(), w, client)

	if got := client.CallCount(); got != 1 {
		t.Fatalf("LLM call count = %d, want 1", got)
	}
	snap := w.Published()
	if rel, ok := snap.Actors["hannah"].Relationships["ezekiel"]; ok {
		t.Errorf("relationship row survived a 'nothing notable' reply, want it pruned: %+v", rel)
	}
}

// TestIsNothingNotable pins the sentinel matcher: the phrase (with punctuation /
// casing / a short elaboration) prunes, but a real judgment hiding behind the
// phrase does not.
func TestIsNothingNotable(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"nothing notable", true},
		{"Nothing notable.", true},
		{"  nothing notable  ", true},
		{`"Nothing notable"`, true},
		{"Nothing notable to report.", true},
		{"Nothing notable about them.", true},
		{"Nothing notable except that he pays late.", false},
		{"Nothing notable, but he drives a hard bargain.", false},
		{"He pays what he promises and trades fair.", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isNothingNotable(c.in); got != c.want {
			t.Errorf("isNothingNotable(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
