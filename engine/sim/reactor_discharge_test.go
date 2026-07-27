package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// reactor_discharge_test.go — LLM-542. Discharge-at-render: a stimulus the
// tick's PROMPT contained but whose warrant the tick never consumed, because
// it landed between the emit (which fixes the warrant batch) and the snapshot
// perception was built from.
//
// The live shape: a lifecycle-warranted tick (arrived / huddle_joined, both
// zero-discriminator, so the in-flight set is EMPTY) answers a question asked
// after its emit; the speech reactor has meanwhile opened a fresh cycle; that
// cycle fires and the actor answers the same line again, reading its own first
// reply as the counterparty repeating herself.

// arrangeInFlightWithOpenCycle sets alice mid-tick under attempt tk-542 with
// the given consumed keys, and — separately — an already-open warrant cycle
// holding `pending`. That combination is the bug's state at completion time:
// the batch was consumed at emit, and the stimulus the prompt actually carried
// arrived afterwards and stamped its own cycle.
func arrangeInFlightWithOpenCycle(
	t *testing.T, w *sim.World,
	inFlight []sim.WarrantSourceKey,
	pending []sim.WarrantMeta,
	now time.Time,
) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		a.TickInFlight = true
		a.TickAttemptID = "tk-542"
		set := make(map[sim.WarrantSourceKey]struct{}, len(inFlight))
		for _, k := range inFlight {
			set[k] = struct{}{}
		}
		sim.SetActorInFlightSourceKeys(a, set)
		if len(pending) > 0 {
			since := now.Add(-time.Second)
			due := now.Add(10 * time.Millisecond)
			a.WarrantedSince = &since
			a.WarrantDueAt = &due
			a.Warrants = append([]sim.WarrantMeta(nil), pending...)
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("arrange in-flight attempt with open cycle: %v", err)
	}
}

func completeWithDischarge(
	t *testing.T, w *sim.World,
	status sim.TickTerminalStatus,
	discharged []sim.WarrantSourceKey,
	now time.Time,
) {
	t.Helper()
	if _, err := w.Send(sim.CompleteReactorTick("alice", "tk-542", sim.TickResult{
		TerminalStatus:       status,
		DischargedSourceKeys: discharged,
	}, now)); err != nil {
		t.Fatalf("CompleteReactorTick: %v", err)
	}
}

func speechWarrant(id sim.SpeechID) sim.WarrantMeta {
	return sim.WarrantMeta{
		TriggerActorID: "bob",
		Reason: sim.NPCSpeechWarrantReason{
			SpeechID: id,
			Speaker:  "bob",
			Excerpt:  "What brings you out to my farm this midday?",
		},
	}
}

func speechKey(id sim.SpeechID) sim.WarrantSourceKey {
	return sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: uint64(id)}
}

// TestDischarge_RenderedUtteranceDoesNotFireASecondCycle is AC 1: the tick's
// prompt carried the utterance, so the cycle that utterance opened must not
// survive the completion. This is the assertion that would have caught the
// live "I told you —" second reply.
func TestDischarge_RenderedUtteranceDoesNotFireASecondCycle(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// The lifecycle batch contributed NO source keys — both kinds are
	// zero-discriminator, which is exactly why dedup could not see this.
	arrangeInFlightWithOpenCycle(t, w, nil, []sim.WarrantMeta{speechWarrant(77)}, now)
	completeWithDischarge(t, w, sim.TickStatusSuccess, []sim.WarrantSourceKey{speechKey(77)}, now)

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince != nil || a.WarrantDueAt != nil || len(a.Warrants) != 0 {
			t.Errorf("open cycle survived the discharge: since=%v due=%v warrants=%d",
				a.WarrantedSince, a.WarrantDueAt, len(a.Warrants))
		}
		if _, ok := sim.ActorRecentlyConsumedSourceKeys(a)[speechKey(77)]; !ok {
			t.Error("discharged key not recorded as recently-consumed")
		}
	})
}

// TestDischarge_ReStampOfADischargedSpeechIsRejected covers the other half of
// the discharge: recently-consumed is what stops the mirror ordering, where the
// warrant stamp lands after the completion rather than before it.
func TestDischarge_ReStampOfADischargedSpeechIsRejected(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	arrangeInFlightWithOpenCycle(t, w, nil, nil, now)
	completeWithDischarge(t, w, sim.TickStatusSuccess, []sim.WarrantSourceKey{speechKey(77)}, now)

	res, err := w.Send(sim.StampWarrant("alice", speechWarrant(77), now))
	if err != nil {
		t.Fatalf("StampWarrant: %v", err)
	}
	if res.(sim.StampWarrantResult).Stamped {
		t.Error("StampWarrant reported the discharged re-stamp as recorded")
	}
	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince != nil || len(a.Warrants) != 0 {
			t.Errorf("a re-stamp of the discharged speech opened a cycle: warrants=%d", len(a.Warrants))
		}
	})
}

// TestDischarge_NewUtteranceDuringTickStillWarrants is AC 2: a SECOND line
// spoken after the snapshot read is not in the prompt, so it is still owed.
// Only the rendered one is discharged, and the cycle survives to answer the
// other — the accumulate-during-in-flight behaviour tryStampWarrant documents
// must not be traded away for this fix.
func TestDischarge_NewUtteranceDuringTickStillWarrants(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	arrangeInFlightWithOpenCycle(t, w, nil,
		[]sim.WarrantMeta{speechWarrant(77), speechWarrant(78)}, now)
	completeWithDischarge(t, w, sim.TickStatusSuccess, []sim.WarrantSourceKey{speechKey(77)}, now)

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if a.WarrantedSince == nil || a.WarrantDueAt == nil {
			t.Fatal("cycle cleared even though an unrendered utterance was still pending")
		}
		if len(a.Warrants) != 1 {
			t.Fatalf("Warrants len = %d, want 1 (only the rendered line discharged)", len(a.Warrants))
		}
		r, ok := a.Warrants[0].Reason.(sim.NPCSpeechWarrantReason)
		if !ok || r.SpeechID != 78 {
			t.Errorf("surviving warrant = %+v, want the unrendered SpeechID 78", a.Warrants[0].Reason)
		}
	})
}

// TestDischarge_UnansweredStatusesDischargeNothing: discharge is gated on the
// model having actually produced a turn, which is STRICTER than the
// terminalStatusAddresses table governing the consumed-batch move (code_review).
//
// failed-after-render is the case that forces the distinction: it addresses —
// the prompt was built and the stimulus was in it — but the LLM call then
// failed, so nobody replied. Pruning there would destroy a live, post-emit
// warrant and lose the reply outright. failed-before-render never perceived
// the stimulus at all, and skipped never rendered a prompt.
func TestDischarge_UnansweredStatusesDischargeNothing(t *testing.T) {
	for _, status := range []sim.TickTerminalStatus{
		sim.TickStatusFailedAfterRender,
		sim.TickStatusFailedBeforeRender,
		sim.TickStatusSkipped,
		sim.TickStatusShutdown,
	} {
		w, cancel, _ := buildPR3aWorld(t)
		now := time.Now().UTC()

		arrangeInFlightWithOpenCycle(t, w, nil, []sim.WarrantMeta{speechWarrant(77)}, now)
		completeWithDischarge(t, w, status, []sim.WarrantSourceKey{speechKey(77)}, now)

		inspectActor(t, w, "alice", func(a *sim.Actor) {
			if len(a.Warrants) != 1 {
				t.Errorf("status %v: Warrants len = %d, want 1 — an unanswered turn discharges nothing",
					status, len(a.Warrants))
			}
			if _, ok := sim.ActorRecentlyConsumedSourceKeys(a)[speechKey(77)]; ok {
				t.Errorf("status %v: an unanswered turn recorded a discharged key", status)
			}
		})
		cancel()
	}
}

// TestDischarge_CarriedForwardWarrantIsNotDischarged: a warrant the render
// DROPPED was not shown to the model, so carry-forward wins over discharge —
// the same exclusion the addressed-key move already applies. Without it a
// dropped line would be re-opened by carry-forward and pruned again in the
// same completion.
func TestDischarge_CarriedForwardWarrantIsNotDischarged(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	arrangeInFlightWithOpenCycle(t, w, []sim.WarrantSourceKey{speechKey(77)}, nil, now)
	if _, err := w.Send(sim.CompleteReactorTick("alice", "tk-542", sim.TickResult{
		TerminalStatus:       sim.TickStatusSuccess,
		UnaddressedWarrants:  []sim.WarrantMeta{speechWarrant(77)},
		DischargedSourceKeys: []sim.WarrantSourceKey{speechKey(77)},
	}, now)); err != nil {
		t.Fatalf("CompleteReactorTick: %v", err)
	}

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if len(a.Warrants) != 1 {
			t.Errorf("Warrants len = %d, want 1 — carry-forward outranks discharge", len(a.Warrants))
		}
		if _, ok := sim.ActorRecentlyConsumedSourceKeys(a)[speechKey(77)]; ok {
			t.Error("carried-forward key was recorded as discharged")
		}
	})
}

// TestDischarge_UnrelatedWarrantKindsSurvive: the prune matches on the full
// (Kind, Discriminator) key, so a non-speech warrant that happens to share a
// discriminator is untouched, and zero-discriminator warrants — which have no
// key at all — are never pruned.
func TestDischarge_UnrelatedWarrantKindsSurvive(t *testing.T) {
	w, cancel, _ := buildPR3aWorld(t)
	defer cancel()
	now := time.Now().UTC()

	pending := []sim.WarrantMeta{
		speechWarrant(77),
		{TriggerActorID: "bob", Reason: sim.PaidWarrantReason{PaidID: 77, Buyer: "bob", Amount: 2}},
		{TriggerActorID: "alice", Reason: sim.IdleBackstopWarrantReason{}},
	}
	arrangeInFlightWithOpenCycle(t, w, nil, pending, now)
	completeWithDischarge(t, w, sim.TickStatusSuccess, []sim.WarrantSourceKey{speechKey(77)}, now)

	inspectActor(t, w, "alice", func(a *sim.Actor) {
		if len(a.Warrants) != 2 {
			t.Fatalf("Warrants len = %d, want 2 (paid + idle backstop survive)", len(a.Warrants))
		}
		for _, wm := range a.Warrants {
			if _, isSpeech := wm.Reason.(sim.NPCSpeechWarrantReason); isSpeech {
				t.Error("the discharged speech warrant survived the prune")
			}
		}
	})
}

// TestTerminalStatusAnsweredContract pins the discharge gate's membership
// against the addressed-key table it deliberately diverges from, so a new
// terminal status cannot be added to one without a decision about the other
// (code_review).
//
// The two divergences are the whole point:
//
//	failed-after-render ADDRESSES but did not ANSWER — the prompt was built,
//	  then the LLM call failed. Discharging would destroy a live, post-emit
//	  warrant and lose the reply.
//	skipped ADDRESSES but never rendered a prompt — the noop-skip gate returns
//	  before Render, so it carries no discharge keys at all.
//
// budget-forced is on the answered side: both harness assignment sites are
// reached only after at least one LLM round completed against this prompt.
func TestTerminalStatusAnsweredContract(t *testing.T) {
	cases := []struct {
		status    sim.TickTerminalStatus
		addresses bool
		answered  bool
	}{
		{sim.TickStatusSuccess, true, true},
		{sim.TickStatusDone, true, true},
		{sim.TickStatusBudgetForced, true, true},
		{sim.TickStatusFailedAfterRender, true, false},
		{sim.TickStatusSkipped, true, false},
		{sim.TickStatusFailedBeforeRender, false, false},
		{sim.TickStatusShutdown, false, false},
		{sim.TickStatusStale, false, false},
		{sim.TickStatusUnknown, false, false},
	}
	for _, c := range cases {
		if got := sim.TerminalStatusAddresses(c.status); got != c.addresses {
			t.Errorf("TerminalStatusAddresses(%v) = %v, want %v", c.status, got, c.addresses)
		}
		if got := sim.TerminalStatusAnswered(c.status); got != c.answered {
			t.Errorf("TerminalStatusAnswered(%v) = %v, want %v", c.status, got, c.answered)
		}
		if c.answered && !c.addresses {
			t.Errorf("status %v answers but does not address — answered must stay a subset", c.status)
		}
	}
}
