package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// --- shouldSkipNoop unit tests --------------------------------------------

// quietPayload is the all-empty perception baseline that triggers the gate
// when warrants are also low-info: no peer, no needs at red.
func quietPayload() perception.Payload {
	return perception.Payload{
		ActorID: "alice",
		Actor:   perception.ActorView{Needs: map[sim.NeedKey]int{}},
		Surroundings: perception.SurroundingsView{
			HuddleMembers: nil,
		},
	}
}

func idleBackstopWarrant() sim.WarrantMeta {
	return sim.WarrantMeta{
		TriggerActorID: "alice",
		Reason:         sim.IdleBackstopWarrantReason{QuietDuration: 30 * time.Minute},
	}
}

func defaultThresholds() sim.NeedThresholds {
	return sim.DefaultNeedThresholds()
}

func TestShouldSkipNoop_IdleBackstopAlone_NoPeerNoNeeds_Skips(t *testing.T) {
	got := shouldSkipNoop(quietPayload(), defaultThresholds(), []sim.WarrantMeta{idleBackstopWarrant()})
	if !got {
		t.Fatalf("expected skip=true for idle-backstop-only + no peer + no needs")
	}
}

// TestShouldSkipNoop_SeekWorkAlone_NoPeerNoNeeds_DoesNotSkip: the seek-work
// warrant is HIGH-info — a broke worker alone, no peer, no red need must STILL
// tick so it perceives its empty purse and goes to find work. This is the exact
// regression the idle-backstop kind suffers (eaten by the gate); proving
// WarrantKindSeekWork is NOT in isLowInfoWarrantKind is the whole point of the
// new kind (LLM-141). Locks the high-info classification through the gate, not
// just renderWarrantLine.
func TestShouldSkipNoop_SeekWorkAlone_NoPeerNoNeeds_DoesNotSkip(t *testing.T) {
	w := sim.WarrantMeta{
		TriggerActorID: "alice",
		Reason:         sim.SeekWorkWarrantReason{},
	}
	// WarrantMeta has no Kind field — Kind() derives from Reason, exactly as
	// production stamps it (tryStampWarrant carries the Reason). Assert it so the
	// gate test can't pass on a stray default rather than the real seek-work kind.
	if w.Kind() != sim.WarrantKindSeekWork {
		t.Fatalf("WarrantMeta.Kind() = %q, want %q", w.Kind(), sim.WarrantKindSeekWork)
	}
	if shouldSkipNoop(quietPayload(), defaultThresholds(), []sim.WarrantMeta{w}) {
		t.Fatalf("expected skip=false for a lone broke worker — seek-work must tick (high-info)")
	}
}

// TestShouldSkipNoop_AtEaseAlone_NoPeerNoNeeds_DoesNotSkip: the at-ease warrant is
// HIGH-info too — a comfortable idle worker alone, no peer, no red need must STILL
// tick so it perceives "the day is your own" and does something (wander/visit/consume)
// instead of freezing. Proves WarrantKindAtEase is NOT in isLowInfoWarrantKind — the
// default-behavior invariant the whole LLM-352 daytime fix rests on, locked through the
// gate (not just renderWarrantLine).
func TestShouldSkipNoop_AtEaseAlone_NoPeerNoNeeds_DoesNotSkip(t *testing.T) {
	w := sim.WarrantMeta{
		TriggerActorID: "alice",
		Reason:         sim.AtEaseWarrantReason{},
	}
	if w.Kind() != sim.WarrantKindAtEase {
		t.Fatalf("WarrantMeta.Kind() = %q, want %q", w.Kind(), sim.WarrantKindAtEase)
	}
	if shouldSkipNoop(quietPayload(), defaultThresholds(), []sim.WarrantMeta{w}) {
		t.Fatalf("expected skip=false for a lone comfortable idler — at-ease must tick (high-info)")
	}
}

func TestShouldSkipNoop_HuddleConcludedAlone_NoPeerNoNeeds_Skips(t *testing.T) {
	w := sim.WarrantMeta{
		TriggerActorID: "",
		Reason:         sim.BasicWarrantReason{K: sim.WarrantKindHuddleConcluded},
	}
	if !shouldSkipNoop(quietPayload(), defaultThresholds(), []sim.WarrantMeta{w}) {
		t.Fatalf("expected skip=true for huddle-concluded-only + no peer + no needs")
	}
}

func TestShouldSkipNoop_HuddleLeftAlone_NoPeerNoNeeds_Skips(t *testing.T) {
	w := sim.WarrantMeta{
		TriggerActorID: "alice",
		Reason:         sim.BasicWarrantReason{K: sim.WarrantKindHuddleLeft},
	}
	if !shouldSkipNoop(quietPayload(), defaultThresholds(), []sim.WarrantMeta{w}) {
		t.Fatalf("expected skip=true for huddle-left-only + no peer + no needs")
	}
}

func TestShouldSkipNoop_HuddlePeerLeftAlone_NoPeerNoNeeds_Skips(t *testing.T) {
	// ZBBS-WORK-367: a peer leaving that leaves the actor alone is a
	// do-nothing tick — nothing left to respond to (same shape as
	// HuddleConcluded). Without this the lone "X stepped away" tick burned
	// an LLM call and the weak 70B coughed up out-of-character boilerplate.
	w := sim.WarrantMeta{
		TriggerActorID: "bob",
		Reason:         sim.BasicWarrantReason{K: sim.WarrantKindHuddlePeerLeft},
	}
	if !shouldSkipNoop(quietPayload(), defaultThresholds(), []sim.WarrantMeta{w}) {
		t.Fatalf("expected skip=true for huddle-peer-left-only + no peer + no needs")
	}
}

func TestShouldSkipNoop_HuddlePeerLeft_PeerRemains_DoesNotSkip(t *testing.T) {
	// Safety property: a peer leaving a 3+-party huddle still leaves someone
	// present, so the actor should still tick and react to the changed group.
	// The no-co-present-peer gate (check 2) keeps the gate open here even
	// though HuddlePeerLeft is now classed low-info (ZBBS-WORK-367).
	pl := quietPayload()
	pl.Surroundings.HuddleMembers = []perception.HuddleMember{
		{ID: "carol", DisplayName: "Carol", Acquainted: true},
	}
	w := sim.WarrantMeta{
		TriggerActorID: "bob",
		Reason:         sim.BasicWarrantReason{K: sim.WarrantKindHuddlePeerLeft},
	}
	if shouldSkipNoop(pl, defaultThresholds(), []sim.WarrantMeta{w}) {
		t.Fatalf("expected skip=false when a peer remains after another peer left")
	}
}

func TestShouldSkipNoop_PeerPresent_DoesNotSkip(t *testing.T) {
	pl := quietPayload()
	pl.Surroundings.HuddleMembers = []perception.HuddleMember{
		{ID: "bob", DisplayName: "Bob", Acquainted: true},
	}
	if shouldSkipNoop(pl, defaultThresholds(), []sim.WarrantMeta{idleBackstopWarrant()}) {
		t.Fatalf("expected skip=false when a co-huddle peer is present")
	}
}

func TestShouldSkipNoop_NeedAtRed_DoesNotSkip(t *testing.T) {
	pl := quietPayload()
	// Hunger value at the default red threshold (18).
	pl.Actor.Needs = map[sim.NeedKey]int{"hunger": sim.DefaultHungerRedThreshold}
	if shouldSkipNoop(pl, defaultThresholds(), []sim.WarrantMeta{idleBackstopWarrant()}) {
		t.Fatalf("expected skip=false when hunger value == red threshold")
	}
}

func TestShouldSkipNoop_NeedAboveRed_DoesNotSkip(t *testing.T) {
	pl := quietPayload()
	pl.Actor.Needs = map[sim.NeedKey]int{"tiredness": sim.DefaultTirednessRedThreshold + 5}
	if shouldSkipNoop(pl, defaultThresholds(), []sim.WarrantMeta{idleBackstopWarrant()}) {
		t.Fatalf("expected skip=false when tiredness value > red threshold")
	}
}

func TestShouldSkipNoop_NeedSubRed_StillSkipsForLowInfoBatch(t *testing.T) {
	pl := quietPayload()
	// Below default red thresholds — gate stays open.
	pl.Actor.Needs = map[sim.NeedKey]int{
		"hunger":    sim.DefaultHungerRedThreshold - 1,
		"thirst":    sim.DefaultThirstRedThreshold - 1,
		"tiredness": sim.DefaultTirednessRedThreshold - 1,
	}
	if !shouldSkipNoop(pl, defaultThresholds(), []sim.WarrantMeta{idleBackstopWarrant()}) {
		t.Fatalf("expected skip=true with all needs below red threshold")
	}
}

func TestShouldSkipNoop_DutySteerPresent_DoesNotSkip(t *testing.T) {
	// ZBBS-HOME-441: an off-post on-shift (or away-from-home off-shift)
	// actor carries a standing duty steer even when alone with sub-red
	// needs. The gate must step aside so the steer can be read — the
	// idle-backstop is the actor's only revival path, and eating it
	// skip-locks them until a need crosses red.
	directions := map[string]*perception.DutySteerView{
		"to-work": {ToWork: true, TargetID: "store", TargetLabel: "the General Store"},
		"go-home": {ToWork: false, TargetID: "cottage", TargetLabel: "Thorne Cottage"},
	}
	for name, steer := range directions {
		t.Run(name, func(t *testing.T) {
			pl := quietPayload()
			pl.DutySteer = steer
			if shouldSkipNoop(pl, defaultThresholds(), []sim.WarrantMeta{idleBackstopWarrant()}) {
				t.Fatalf("expected skip=false when payload carries a %s duty steer", name)
			}
		})
	}
	// The nil-steer baseline (skip=true) is pinned by
	// TestShouldSkipNoop_IdleBackstopAlone_NoPeerNoNeeds_Skips —
	// quietPayload carries no DutySteer.
}

func TestShouldSkipNoop_AtPostSteer_StillSkips(t *testing.T) {
	// ZBBS-WORK-431: the at-post stabilizer is render-only. Unlike the to-work /
	// go-home arms it must NOT hold the gate open — an idle owner standing at its
	// post with no stimulus should still skip its idle-backstops (HOME-441). If it
	// forced a tick, the constant deliberation it caused is exactly the churn that
	// drove the wandering this cue exists to stop.
	pl := quietPayload()
	pl.DutySteer = &perception.DutySteerView{AtPost: true}
	if !shouldSkipNoop(pl, defaultThresholds(), []sim.WarrantMeta{idleBackstopWarrant()}) {
		t.Fatalf("expected skip=true with an at-post (render-only) duty steer")
	}
}

func TestShouldSkipNoop_DutyPending_DoesNotSkip(t *testing.T) {
	// ZBBS-HOME-442: an off-post on-shift keeper whose to-work steer is
	// Option-B-suppressed by a MILD need carries DutyPending instead of a
	// rendered steer. The gate must still step aside — this band is where
	// Josiah stayed skip-locked after the HOME-441 steer condition shipped.
	pl := quietPayload()
	pl.DutyPending = true
	if shouldSkipNoop(pl, defaultThresholds(), []sim.WarrantMeta{idleBackstopWarrant()}) {
		t.Fatalf("expected skip=false when payload carries duty-pending")
	}
	// The false baseline (skip=true) is pinned by
	// TestShouldSkipNoop_IdleBackstopAlone_NoPeerNoNeeds_Skips —
	// quietPayload carries DutyPending=false.
}

func TestShouldSkipNoop_EveningLeisurePresent_DoesNotSkip(t *testing.T) {
	// LLM-149: inside the evening window buildDutySteer suppresses the off-shift
	// go-home steer so the evening "tavern's open" cue is the single voice. But
	// the go-home steer is exactly what kept an idle off-shift homed agent ticking
	// (HOME-441); the evening cue must hold the gate open in its place, or an
	// agent with only idle-backstop warrants skip-locks and never sees the
	// invitation.
	pl := quietPayload()
	pl.EveningLeisure = &perception.EveningLeisureView{
		VenueID: "tavern", VenueLabel: "the Tavern",
		HomeID: "cottage", HomeLabel: "Ellis Cottage",
	}
	if shouldSkipNoop(pl, defaultThresholds(), []sim.WarrantMeta{idleBackstopWarrant()}) {
		t.Fatalf("expected skip=false when payload carries an evening-leisure cue")
	}
	// The nil baseline (skip=true) is pinned by
	// TestShouldSkipNoop_IdleBackstopAlone_NoPeerNoNeeds_Skips — quietPayload
	// carries EveningLeisure=nil.
}

// TestShouldSkipNoop_EveningLeisureRenderOnlyVariants_Skip: only the INVITATION is a
// standing actionable signal. The render-only variants are scenes the agent has nothing
// to act on, so an idle-backstop tick under either must still skip — forcing the tick
// would restore exactly the constant deliberation each variant was added to remove
// (LLM-335 the mid-batch pester, LLM-345 the lingerer re-deliberating out of the room).
func TestShouldSkipNoop_EveningLeisureRenderOnlyVariants_Skip(t *testing.T) {
	variants := map[string]*perception.EveningLeisureView{
		"batch hold (LLM-335)": {BatchHold: true, BatchItemLabel: "Cheese"},
		"settled in (LLM-345)": {SettledIn: true, VenueLabel: "the Tavern"},
	}
	for name, view := range variants {
		t.Run(name, func(t *testing.T) {
			pl := quietPayload()
			pl.EveningLeisure = view
			if !shouldSkipNoop(pl, defaultThresholds(), []sim.WarrantMeta{idleBackstopWarrant()}) {
				t.Fatalf("expected skip=true: %s is render-only and must not force an idle tick", name)
			}
		})
	}
}

func TestShouldSkipNoop_HighInfoWarrantInBatch_DoesNotSkip(t *testing.T) {
	cases := []sim.WarrantKind{
		sim.WarrantKindNPCSpoke,
		sim.WarrantKindPCSpoke,
		sim.WarrantKindHuddlePeerJoined,
		sim.WarrantKindArrived,
		sim.WarrantKindNeedThreshold,
		sim.WarrantKindPaid,
		sim.WarrantKindSceneQuoteTargeted,
		sim.WarrantKindAdmin,
		sim.WarrantKindHuddleJoined, // your-own-join: high-info (you're entering a new context)
	}
	for _, k := range cases {
		k := k
		t.Run(string(k), func(t *testing.T) {
			batch := []sim.WarrantMeta{
				idleBackstopWarrant(),
				{TriggerActorID: "bob", Reason: sim.BasicWarrantReason{K: k}},
			}
			if shouldSkipNoop(quietPayload(), defaultThresholds(), batch) {
				t.Fatalf("expected skip=false when batch contains high-info kind %q", k)
			}
		})
	}
}

func TestShouldSkipNoop_ForceBypassesGate(t *testing.T) {
	batch := []sim.WarrantMeta{{
		TriggerActorID: "alice",
		Force:          true,
		Reason:         sim.IdleBackstopWarrantReason{QuietDuration: time.Hour},
	}}
	if shouldSkipNoop(quietPayload(), defaultThresholds(), batch) {
		t.Fatalf("expected skip=false when batch carries a Force warrant")
	}
}

func TestShouldSkipNoop_EmptyBatch_DoesNotSkip(t *testing.T) {
	// Empty batch should NOT skip — it means the evaluator emitted a tick
	// without warrants, which is suspicious. Let the LLM tick run; the
	// alternative (silent skip) would mask an upstream bug.
	if shouldSkipNoop(quietPayload(), defaultThresholds(), nil) {
		t.Fatalf("expected skip=false on empty batch (let suspicious empty ticks proceed)")
	}
}

func TestShouldSkipNoop_AdminTunedThresholds_RespectsSnapshot(t *testing.T) {
	pl := quietPayload()
	pl.Actor.Needs = map[sim.NeedKey]int{"hunger": 10}
	// Admin-tuned: hunger red threshold lowered to 8 (default is 18). At
	// value 10 the actor is at red and should tick.
	tuned := sim.NeedThresholds{"hunger": 8}
	if shouldSkipNoop(pl, tuned, []sim.WarrantMeta{idleBackstopWarrant()}) {
		t.Fatalf("expected skip=false with admin-tuned threshold below the need value")
	}
}

// --- isLowInfoWarrantKind unit tests --------------------------------------

func TestIsLowInfoWarrantKind(t *testing.T) {
	low := []sim.WarrantKind{
		sim.WarrantKindIdleBackstop,
		sim.WarrantKindHuddleConcluded,
		sim.WarrantKindHuddleLeft,
		sim.WarrantKindHuddlePeerLeft,
	}
	for _, k := range low {
		if !isLowInfoWarrantKind(k) {
			t.Errorf("kind %q should be low-info", k)
		}
	}
	high := []sim.WarrantKind{
		sim.WarrantKindUnknown, // default → high-info; unknown gets a tick
		sim.WarrantKindNPCSpoke,
		sim.WarrantKindPCSpoke,
		sim.WarrantKindHuddleJoined,
		sim.WarrantKindHuddlePeerJoined,
		sim.WarrantKindArrived,
		sim.WarrantKindNeedThreshold,
		sim.WarrantKindPaid,
		sim.WarrantKindSceneQuoteTargeted,
		sim.WarrantKindAdmin,
	}
	for _, k := range high {
		if isLowInfoWarrantKind(k) {
			t.Errorf("kind %q should NOT be low-info", k)
		}
	}
}

// --- batchHasNewNews unit tests -------------------------------------------

// TestBatchHasNewNews — the turn-state gate's new-news signal (ZBBS-WORK-370):
// true when any warrant is Force or a high-info kind, false for a low-info-only
// batch or an empty batch.
func TestBatchHasNewNews(t *testing.T) {
	lowInfoOnly := []sim.WarrantMeta{
		idleBackstopWarrant(),
		{Reason: sim.BasicWarrantReason{K: sim.WarrantKindHuddlePeerLeft}},
	}
	if batchHasNewNews(lowInfoOnly) {
		t.Error("low-info-only batch should NOT be new news")
	}
	if batchHasNewNews(nil) {
		t.Error("empty batch should NOT be new news")
	}

	// A high-info kind anywhere in the batch is new news.
	withSpeech := []sim.WarrantMeta{
		idleBackstopWarrant(),
		{TriggerActorID: "bob", Reason: sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke}},
	}
	if !batchHasNewNews(withSpeech) {
		t.Error("batch with a speech warrant should be new news")
	}

	// A Force warrant is new news even when its kind is low-info.
	forced := []sim.WarrantMeta{{
		Force:  true,
		Reason: sim.IdleBackstopWarrantReason{QuietDuration: time.Hour},
	}}
	if !batchHasNewNews(forced) {
		t.Error("a Force warrant should be new news regardless of kind")
	}
}

// --- Harness.RunTick integration ------------------------------------------

// TestHarness_NoopSkip_NoLLMCallEmitted exercises the full RunTick gate
// path: alice has no peer, no needs at red, and a batch of one idle-backstop
// warrant. RunTick must return TickStatusSkipped WITHOUT calling
// FakeClient.Complete (CallCount stays 0). Carry-forward must be empty.
func TestHarness_NoopSkip_NoLLMCallEmitted(t *testing.T) {
	w, _, cancel := newTestWorld(t, 0)
	defer cancel()
	attemptID := sim.TickAttemptID("attempt-noopskip-1")
	setInFlight(t, w, attemptID)

	// FakeClient with no scripted turns. If RunTick calls Complete the
	// fake returns an ErrorMalformed; we also assert CallCount == 0
	// post-hoc as the load-bearing check.
	fake := llm.NewFakeClient()
	// Fixed-step fake clock so the Duration stamp is non-zero on Windows
	// (time.Now has coarse resolution there and can return 0 elapsed for
	// the gate path's ~no-work case).
	clockN := int64(0)
	fakeClock := func() time.Time {
		clockN++
		return time.Unix(0, clockN*int64(time.Millisecond))
	}
	cfg := HarnessConfig{
		Client:   fake,
		Registry: newTestRegistry(t).r,
		Clock:    fakeClock,
	}
	h, err := NewHarness(cfg)
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}

	job := newTestJob(attemptID, []sim.WarrantMeta{idleBackstopWarrant()})

	result := h.RunTick(context.Background(), w, job)

	if result.TerminalStatus != sim.TickStatusSkipped {
		t.Fatalf("TerminalStatus = %v, want TickStatusSkipped", result.TerminalStatus)
	}
	if got := fake.CallCount(); got != 0 {
		t.Fatalf("FakeClient.Complete was called %d times; want 0 (gate must short-circuit before LLM)", got)
	}
	if got := result.IterationCount; got != 0 {
		t.Fatalf("IterationCount = %d, want 0 (iteration loop must not run)", got)
	}
	if got := result.UnaddressedWarrants; len(got) != 0 {
		t.Fatalf("UnaddressedWarrants = %v, want nil (consumed batch is addressed, not carried forward)", got)
	}
	if result.AttemptID != attemptID {
		t.Fatalf("AttemptID = %q, want %q", result.AttemptID, attemptID)
	}
	if result.Duration <= 0 {
		t.Fatalf("Duration not stamped (= %v); the defer must run on the skip path", result.Duration)
	}
}

// TestHarness_NoopSkip_PeerPresent_FiresLLMCall confirms the gate steps
// aside when a co-huddle peer exists. We seed alice into a huddle with bob,
// script a content-only response (no tool calls → TickStatusSuccess), and
// assert Complete was called once.
func TestHarness_NoopSkip_PeerPresent_FiresLLMCall(t *testing.T) {
	w, _, cancel := newTestWorldWithActors(t, []sim.ActorID{"alice", "bob"}, 0)
	defer cancel()
	attemptID := sim.TickAttemptID("attempt-noopskip-2")
	setInFlight(t, w, attemptID)

	// Put alice + bob into the same huddle so SurroundingsView.HuddleMembers
	// includes bob from alice's perspective.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		hid := sim.HuddleID("h-1")
		world.Huddles[hid] = &sim.Huddle{
			ID:      hid,
			Members: map[sim.ActorID]struct{}{"alice": {}, "bob": {}},
		}
		world.Actors["alice"].CurrentHuddleID = hid
		world.Actors["bob"].CurrentHuddleID = hid
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed huddle: %v", err)
	}

	// Scripted: an EMPTY content-only response (no tools, no spoken substance)
	// → harness returns Success in one round. A non-empty reply would be
	// reprompted per LLM-378, adding a round; this test only cares that the
	// gate stepped aside and the LLM call FIRED, so the empty response keeps
	// it to the single call the CallCount assertion below pins.
	fake := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{Content: ""}})
	h, _ := newTestHarness(t, fake, 0, 0)

	job := newTestJob(attemptID, []sim.WarrantMeta{idleBackstopWarrant()})
	result := h.RunTick(context.Background(), w, job)

	if result.TerminalStatus != sim.TickStatusSuccess {
		t.Fatalf("TerminalStatus = %v, want TickStatusSuccess (peer present should NOT skip)", result.TerminalStatus)
	}
	if got := fake.CallCount(); got != 1 {
		t.Fatalf("FakeClient.Complete called %d times; want 1", got)
	}
}

// TestHarness_NoopSkip_HighInfoWarrant_FiresLLMCall confirms the gate
// steps aside when the batch contains a high-info warrant kind (here:
// speech). Alice is still alone with no needs — the warrant alone is the
// reason to tick.
func TestHarness_NoopSkip_HighInfoWarrant_FiresLLMCall(t *testing.T) {
	w, _, cancel := newTestWorld(t, 0)
	defer cancel()
	attemptID := sim.TickAttemptID("attempt-noopskip-3")
	setInFlight(t, w, attemptID)

	// Empty content-only response (no tools, no spoken substance) → Success in
	// one round; keeps CallCount at 1 (a non-empty reply would be reprompted,
	// LLM-378). The test's point is that the high-info warrant fired the LLM.
	fake := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{Content: ""}})
	h, _ := newTestHarness(t, fake, 0, 0)

	job := newTestJob(attemptID, []sim.WarrantMeta{{
		TriggerActorID: "bob",
		Reason: sim.NPCSpeechWarrantReason{
			SpeechID: sim.SpeechID(1234),
			Speaker:  "bob",
			Excerpt:  "hello",
		},
	}})
	result := h.RunTick(context.Background(), w, job)

	if result.TerminalStatus != sim.TickStatusSuccess {
		t.Fatalf("TerminalStatus = %v, want TickStatusSuccess (NPCSpoke warrant should NOT skip)", result.TerminalStatus)
	}
	if got := fake.CallCount(); got != 1 {
		t.Fatalf("FakeClient.Complete called %d times; want 1", got)
	}
}

// The "Skipped addresses consumed keys" guarantee (so the gate doesn't
// busy-loop by re-emitting the same warrants on the next scan) is covered
// at the sim layer in TestTerminalStatusAddresses, which now pins
// terminalStatusAddresses(TickStatusSkipped) == true. The end-to-end
// flow (RunTick → Skipped → CompleteReactorTick → recently-consumed) is
// implicit in those two pieces composing.

// TestShouldSkipNoop_StrandedRuns: the anomalous-position backstop warrant
// (ZBBS-HOME-450) is high-info by classification — the whole point is that
// the tick RUNS so the stranded actor perceives standing in the open and
// re-decides. A quiet payload must not eat it.
func TestShouldSkipNoop_StrandedRuns(t *testing.T) {
	w := sim.WarrantMeta{
		TriggerActorID: "alice",
		Reason:         sim.StrandedWarrantReason{},
	}
	if shouldSkipNoop(quietPayload(), defaultThresholds(), []sim.WarrantMeta{w}) {
		t.Fatalf("expected skip=false for the stranded backstop warrant")
	}
}
