package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// huddle_loop_ledger_test.go — LLM-309. The transactional-futility arm: a huddle
// caught in a silent offer→decline loop (zero spoken lines, every tool call
// succeeds) rides the same LoopingSince onset, silent-conclude, and per-tick
// ConversationLooping steer as the chatty utterance arm.

// stagePayTerminal writes a resolved pay-ledger entry directly on the world
// goroutine, so a test can stage a decline loop without replaying the full
// pay_with_item → decline_pay command flow.
func stagePayTerminal(t *testing.T, w *sim.World, id int, huddle sim.HuddleID, buyer, seller sim.ActorID, item sim.ItemKind, state sim.PayLedgerState, resolvedAt time.Time) {
	t.Helper()
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.PayLedger[sim.LedgerID(id)] = &sim.PayLedgerEntry{
			ID:         sim.LedgerID(id),
			BuyerID:    buyer,
			SellerID:   seller,
			ItemKind:   item,
			State:      state,
			HuddleID:   huddle,
			ResolvedAt: resolvedAt,
		}
		return nil, nil
	}})
}

// clearPayLedger empties the ledger between test phases.
func clearPayLedger(t *testing.T, w *sim.World) {
	t.Helper()
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		for id := range world.PayLedger {
			delete(world.PayLedger, id)
		}
		return nil, nil
	}})
}

// ledgerStandoff runs the one-pass scan on the world goroutine and returns the
// (present, armed) huddle sets.
func ledgerStandoff(t *testing.T, w *sim.World, now time.Time) (present, armed map[sim.HuddleID]struct{}) {
	t.Helper()
	v := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		p, a := sim.LedgerStandoffHuddles(world, now)
		return []any{p, a}, nil
	}})
	r := v.([]any)
	present, _ = r[0].(map[sim.HuddleID]struct{})
	armed, _ = r[1].(map[sim.HuddleID]struct{})
	return present, armed
}

func hasHuddle(m map[sim.HuddleID]struct{}, id sim.HuddleID) bool {
	_, ok := m[id]
	return ok
}

// TestHuddleLoopLedgerFutileState pins the counted terminal set: declines and the
// three insufficient-* material failures are futile; the completed sale, a live
// counter, and the abandonment/broken-context terminals are not.
func TestHuddleLoopLedgerFutileState(t *testing.T) {
	futile := []sim.PayLedgerState{
		sim.PayLedgerStateDeclined,
		sim.PayLedgerStateFailedInsufficientFunds,
		sim.PayLedgerStateFailedInsufficientStock,
		sim.PayLedgerStateFailedInsufficientGoods,
	}
	for _, st := range futile {
		if !sim.HuddleLoopLedgerFutileState(st) {
			t.Errorf("%s should count as a futile terminal", st)
		}
	}
	notFutile := []sim.PayLedgerState{
		sim.PayLedgerStatePending,
		sim.PayLedgerStateAccepted,
		sim.PayLedgerStateCountered,
		sim.PayLedgerStateWithdrawnByBuyer,
		sim.PayLedgerStateExpired,
		sim.PayLedgerStateFailedUnavailable,
	}
	for _, st := range notFutile {
		if sim.HuddleLoopLedgerFutileState(st) {
			t.Errorf("%s must NOT count as a futile terminal", st)
		}
	}
}

// TestLedgerStandoff_ArmsSilentDeclineLoop is the core positive: three recent
// same-pair/same-item declines with no intervening progress read as both present
// (durable onset) and armed (live), while two declines stay below the threshold.
func TestLedgerStandoff_ArmsSilentDeclineLoop(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	// The second (genuinely-new) join stamped LastProgressAt; clear it so the ledger
	// signal is isolated (that stamp would correctly suppress the pre-join declines).
	setHuddleLoopState(t, w, h, nil, nil, time.Time{})

	// Two declines is a pattern, not yet a loop.
	stagePayTerminal(t, w, 1, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-40*time.Second))
	stagePayTerminal(t, w, 2, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-20*time.Second))
	if present, _ := ledgerStandoff(t, w, now); hasHuddle(present, h) {
		t.Errorf("two declines are below huddleLoopLedgerMinTerminals (%d) — must not be present", sim.HuddleLoopLedgerMinTerminals)
	}

	// The third decline crosses the threshold → present AND armed (newest fresh).
	stagePayTerminal(t, w, 3, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-5*time.Second))
	present, armed := ledgerStandoff(t, w, now)
	if !hasHuddle(present, h) {
		t.Error("three same-pair/item declines should be present (durable onset)")
	}
	if !hasHuddle(armed, h) {
		t.Error("three declines with a fresh newest terminal should be armed (live)")
	}
}

// TestLedgerStandoff_ProgressGuardAndDecay covers the progress guard (a completed
// transaction newer than the declines spares the huddle), recency decay (declines
// older than the recency window fall out), and the live-window split (present but
// not armed once the newest terminal goes stale).
func TestLedgerStandoff_ProgressGuardAndDecay(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))

	stageThree := func() {
		clearPayLedger(t, w)
		stagePayTerminal(t, w, 1, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-70*time.Second))
		stagePayTerminal(t, w, 2, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-40*time.Second))
		stagePayTerminal(t, w, 3, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-10*time.Second))
	}

	// A completed transaction after every decline (LastProgressAt = now) → the
	// declines are pre-progress and the deal is not a loop.
	stageThree()
	setHuddleLoopState(t, w, h, nil, nil, now)
	if present, _ := ledgerStandoff(t, w, now); hasHuddle(present, h) {
		t.Error("declines that pre-date a completed transaction must not arm (progress guard)")
	}

	// Control: with the progress stamp cleared the same three declines are present.
	setHuddleLoopState(t, w, h, nil, nil, time.Time{})
	if present, _ := ledgerStandoff(t, w, now); !hasHuddle(present, h) {
		t.Error("with no intervening progress the three declines should be present")
	}

	// Declines older than the recency window decay out of the signal entirely.
	clearPayLedger(t, w)
	stagePayTerminal(t, w, 4, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-10*time.Minute))
	stagePayTerminal(t, w, 5, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-9*time.Minute))
	stagePayTerminal(t, w, 6, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-8*time.Minute))
	if present, _ := ledgerStandoff(t, w, now); hasHuddle(present, h) {
		t.Error("declines older than the recency window must decay out of the signal")
	}

	// Within the recency window but past the live window → present, NOT armed.
	clearPayLedger(t, w)
	stagePayTerminal(t, w, 7, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-4*time.Minute))
	stagePayTerminal(t, w, 8, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-3*time.Minute))
	stagePayTerminal(t, w, 9, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-2*time.Minute))
	present, armed := ledgerStandoff(t, w, now)
	if !hasHuddle(present, h) {
		t.Error("declines within the recency window should still be present")
	}
	if hasHuddle(armed, h) {
		t.Error("a standoff whose newest terminal is past the live window must NOT be armed")
	}
}

// TestLedgerStandoff_BoundaryTimestamps pins the recency/live boundaries: terminals
// resolved at exactly `now` count (present + armed, age 0 is live), while
// future-dated terminals (out-of-order / replayed clock) count toward NEITHER set —
// the durable `present` side must reject the future just as the live side does.
func TestLedgerStandoff_BoundaryTimestamps(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	setHuddleLoopState(t, w, h, nil, nil, time.Time{}) // clear the join's progress stamp

	// Three terminals resolved at exactly `now` → present AND armed (inclusive boundary).
	stagePayTerminal(t, w, 1, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now)
	stagePayTerminal(t, w, 2, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now)
	stagePayTerminal(t, w, 3, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now)
	present, armed := ledgerStandoff(t, w, now)
	if !hasHuddle(present, h) || !hasHuddle(armed, h) {
		t.Errorf("terminals at exactly now should be present+armed: present=%v armed=%v", hasHuddle(present, h), hasHuddle(armed, h))
	}

	// Three future-dated terminals → neither present nor armed (out-of-order clock).
	clearPayLedger(t, w)
	stagePayTerminal(t, w, 4, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(10*time.Second))
	stagePayTerminal(t, w, 5, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(20*time.Second))
	stagePayTerminal(t, w, 6, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(30*time.Second))
	present, armed = ledgerStandoff(t, w, now)
	if hasHuddle(present, h) || hasHuddle(armed, h) {
		t.Error("future-dated terminals must count toward neither present nor armed")
	}
}

// TestLedgerStandoff_ScopedByPairAndItem confirms the tally is per (buyer, seller,
// item): declines split across different items or different sellers don't
// aggregate, and an accepted sale never counts toward the futility threshold.
func TestLedgerStandoff_ScopedByPairAndItem(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("charlie", "tavern", "", now))
	setHuddleLoopState(t, w, h, nil, nil, time.Time{}) // clear the joins' progress stamps

	// Two sage declines + one ale decline (same pair): no single item reaches 3.
	stagePayTerminal(t, w, 1, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-40*time.Second))
	stagePayTerminal(t, w, 2, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-25*time.Second))
	stagePayTerminal(t, w, 3, h, "alice", "bob", "ale", sim.PayLedgerStateDeclined, now.Add(-10*time.Second))
	if present, _ := ledgerStandoff(t, w, now); hasHuddle(present, h) {
		t.Error("declines split across different items must not aggregate into one standoff")
	}

	// Two declines to bob + one to charlie (same item): no single pair reaches 3.
	clearPayLedger(t, w)
	stagePayTerminal(t, w, 4, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-40*time.Second))
	stagePayTerminal(t, w, 5, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-25*time.Second))
	stagePayTerminal(t, w, 6, h, "alice", "charlie", "sage", sim.PayLedgerStateDeclined, now.Add(-10*time.Second))
	if present, _ := ledgerStandoff(t, w, now); hasHuddle(present, h) {
		t.Error("declines split across different sellers must not aggregate into one standoff")
	}

	// Two declines + an accepted sale: the completed sale doesn't count.
	clearPayLedger(t, w)
	stagePayTerminal(t, w, 7, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-40*time.Second))
	stagePayTerminal(t, w, 8, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-25*time.Second))
	stagePayTerminal(t, w, 9, h, "alice", "bob", "sage", sim.PayLedgerStateAccepted, now.Add(-10*time.Second))
	if present, _ := ledgerStandoff(t, w, now); hasHuddle(present, h) {
		t.Error("an accepted sale must not count toward the futility threshold")
	}
}

// TestLedgerStandoff_MembershipChangeResets drives the REAL join path: a
// genuinely-new member joining stamps LastProgressAt (JoinHuddle), which resets the
// loop clock so the pre-join declines no longer post-date progress.
func TestLedgerStandoff_MembershipChangeResets(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	setHuddleLoopState(t, w, h, nil, nil, time.Time{}) // clear the join's progress stamp

	stagePayTerminal(t, w, 1, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-40*time.Second))
	stagePayTerminal(t, w, 2, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-25*time.Second))
	stagePayTerminal(t, w, 3, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-10*time.Second))
	if present, _ := ledgerStandoff(t, w, now); !hasHuddle(present, h) {
		t.Fatal("precondition: three declines should be present before the membership change")
	}

	// A genuinely-new member joins → JoinHuddle stamps LastProgressAt.
	joinAt := now.Add(1 * time.Second)
	sendT(t, w, sim.JoinHuddle("charlie", "tavern", "", joinAt))

	present, armed := ledgerStandoff(t, w, now.Add(2*time.Second))
	if hasHuddle(present, h) || hasHuddle(armed, h) {
		t.Error("a genuinely-new member joining must reset the standoff (declines now pre-date progress)")
	}
}

// TestHuddleLoopSweep_ConcludesSilentLedgerLoop is the end-to-end lever: a huddle
// with ZERO spoken lines but a sustained offer→decline standoff is concluded by the
// sweep, silently (no HuddleConcluded warrant), with a per-member `stuck` telemetry
// record tagged reason="huddle_loop_ledger".
func TestHuddleLoopSweep_ConcludesSilentLedgerLoop(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	sink := wireLoopTelemetry(t, w)

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	enableLoopSweep(t, w, 2*time.Minute, 60)

	// Empty utterance ring, onset already past the timeout, three recent declines.
	onset := now.Add(-3 * time.Minute)
	setHuddleLoopState(t, w, h, nil, &onset, time.Time{})
	stagePayTerminal(t, w, 1, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-70*time.Second))
	stagePayTerminal(t, w, 2, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-40*time.Second))
	stagePayTerminal(t, w, 3, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-10*time.Second))

	sendT(t, w, sim.EvaluateHuddleLoopSweep(now))

	if huddleConcludedAt(t, w, h) == nil {
		t.Error("a silent offer→decline standoff should be concluded by the ledger arm")
	}
	if actorHasWarrantKind(t, w, "alice", sim.WarrantKindHuddleConcluded) {
		t.Error("ledger-swept member must NOT carry a HuddleConcluded warrant (silent conclude)")
	}
	var stuck int
	for _, rec := range sink.snapshot() {
		if rec.Kind == "stuck" && rec.Detail["reason"] == "huddle_loop_ledger" {
			stuck++
			if rec.Detail["huddle"] != string(h) {
				t.Errorf("telemetry huddle = %q, want %q", rec.Detail["huddle"], h)
			}
		}
	}
	if stuck != 2 {
		t.Errorf("huddle_loop_ledger telemetry records = %d, want 2 (one per member)", stuck)
	}
}

// TestHuddleLoopSweep_LedgerLoopPersistenceGate confirms the ledger arm rides the
// shared persistence gate: a freshly-detected standoff is armed (LoopingSince
// stamped) on the first scan but not concluded until a full timeout has elapsed.
func TestHuddleLoopSweep_LedgerLoopPersistenceGate(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))
	enableLoopSweep(t, w, 60*time.Second, 60)
	setHuddleLoopState(t, w, h, nil, nil, time.Time{}) // clear the join's progress stamp

	stagePayTerminal(t, w, 1, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, t0.Add(-40*time.Second))
	stagePayTerminal(t, w, 2, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, t0.Add(-20*time.Second))
	stagePayTerminal(t, w, 3, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, t0.Add(-5*time.Second))

	// Scan 1: arms the loop, does not conclude.
	sendT(t, w, sim.EvaluateHuddleLoopSweep(t0))
	if huddleConcludedAt(t, w, h) != nil {
		t.Fatal("ledger loop must not conclude on the first scan (persistence gate)")
	}
	if huddleLoopingSince(t, w, h) == nil {
		t.Error("first scan should stamp LoopingSince for a ledger standoff")
	}

	// Scan 2 a full timeout later, with a fresh decline so the live window holds.
	t1 := t0.Add(60 * time.Second)
	stagePayTerminal(t, w, 4, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, t1.Add(-5*time.Second))
	sendT(t, w, sim.EvaluateHuddleLoopSweep(t1))
	if huddleConcludedAt(t, w, h) == nil {
		t.Error("ledger loop should conclude once it has persisted the full timeout")
	}
}

// TestRepublish_ConversationLooping_LedgerArm confirms the per-tick steer arms on a
// silent transactional standoff too: the members of a huddle in a ledger loop carry
// ConversationLooping, and it clears once a completed transaction resets the clock.
func TestRepublish_ConversationLooping_LedgerArm(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Seeded actors default to a conversational NPC kind (the sibling utterance-arm
	// test TestRepublish_ConversationLoopingFlag relies on the same default), so the
	// per-tick steer applies without overriding Kind.
	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	enableLoopSweep(t, w, 2*time.Minute, 60)
	setHuddleLoopState(t, w, h, nil, nil, time.Time{}) // clear the join's progress stamp

	stagePayTerminal(t, w, 1, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-40*time.Second))
	stagePayTerminal(t, w, 2, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-25*time.Second))
	stagePayTerminal(t, w, 3, h, "alice", "bob", "sage", sim.PayLedgerStateDeclined, now.Add(-10*time.Second))

	snap := w.Published()
	if !snap.Actors["alice"].ConversationLooping || !snap.Actors["bob"].ConversationLooping {
		t.Errorf("a silent ledger standoff should flag both members: alice=%v bob=%v",
			snap.Actors["alice"].ConversationLooping, snap.Actors["bob"].ConversationLooping)
	}

	// A completed transaction (LastProgressAt bumped) disarms the steer.
	setHuddleLoopState(t, w, h, nil, nil, now)
	if w.Published().Actors["alice"].ConversationLooping {
		t.Error("a completed transaction newer than the declines must clear the steer")
	}
}
