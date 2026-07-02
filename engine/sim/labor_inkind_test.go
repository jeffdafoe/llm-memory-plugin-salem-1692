package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// labor_inkind_test.go — LLM-225 coverage of the in-kind reward leg: a labor
// offer's reward may carry goods (RewardItems) alongside or instead of coins,
// validated against the EMPLOYER's holdings at solicit (auto-decline), accept
// (gate 8), and settle (authoritative re-check), and transferred atomically
// with the coins at completion. Coins-only behavior is pinned by the LLM-26
// tests in labor_commands_test.go and must be unchanged.

// readInventoryMap snapshots one actor's full inventory on the world goroutine
// (the shared readInventory helper reads a single kind; the settle tests assert
// on line deletion, which needs key presence).
func readInventoryMap(t *testing.T, w *sim.World, id sim.ActorID) map[sim.ItemKind]int {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a, ok := world.Actors[id]
		if !ok || a.Inventory == nil {
			return map[sim.ItemKind]int{}, nil
		}
		out := make(map[sim.ItemKind]int, len(a.Inventory))
		for k, v := range a.Inventory {
			out[k] = v
		}
		return out, nil
	}})
	if err != nil {
		t.Fatalf("readInventory %q: %v", id, err)
	}
	return res.(map[sim.ItemKind]int)
}

func TestSolicitWork_InKindReward_MintsOfferWithItems(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "anne", displayName: "Anne", huddleID: "h1", worker: true},
		{id: "hannah", displayName: "Hannah", huddleID: "h1", coins: 50,
			inventory: map[sim.ItemKind]int{"porridge": 3}},
	})
	defer stop()
	events := captureLaborEvents(t, w)

	now := time.Now().UTC()
	res, err := w.Send(sim.SolicitWork("anne", "Hannah", 2,
		[]sim.PayItemInput{{Item: "porridge", Qty: 1}}, 120, now))
	if err != nil {
		t.Fatalf("SolicitWork with reward_items: %v", err)
	}
	out := res.(sim.LaborSolicitResult)
	if out.State != sim.LaborStatePending {
		t.Fatalf("result State = %q, want pending", out.State)
	}

	o := readLaborLedger(t, w)[out.ID]
	if o.Reward != 2 {
		t.Errorf("offer Reward = %d, want 2", o.Reward)
	}
	if len(o.RewardItems) != 1 || o.RewardItems[0].Kind != "porridge" || o.RewardItems[0].Qty != 1 {
		t.Errorf("offer RewardItems = %+v, want [{porridge 1}]", o.RewardItems)
	}
	// The received event snapshots the in-kind leg for subscribers.
	if len(events.Received) != 1 {
		t.Fatalf("LaborOfferReceived count = %d, want 1", len(events.Received))
	}
	if ri := events.Received[0].RewardItems; len(ri) != 1 || ri[0].Kind != "porridge" || ri[0].Qty != 1 {
		t.Errorf("event RewardItems = %+v, want [{porridge 1}]", ri)
	}
	// Nothing moves on solicit — no coins, no goods.
	if got := readInventoryMap(t, w, "hannah")["porridge"]; got != 3 {
		t.Errorf("employer porridge = %d after solicit, want 3 (no move)", got)
	}
}

func TestSolicitWork_ItemsOnlyReward_ZeroCoinsAllowed(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "anne", displayName: "Anne", huddleID: "h1", worker: true},
		{id: "hannah", displayName: "Hannah", huddleID: "h1",
			inventory: map[sim.ItemKind]int{"porridge": 1}}, // 0 coins — goods ARE the pay
	})
	defer stop()

	now := time.Now().UTC()
	res, err := w.Send(sim.SolicitWork("anne", "Hannah", 0,
		[]sim.PayItemInput{{Item: "porridge", Qty: 1}}, 120, now))
	if err != nil {
		t.Fatalf("SolicitWork with items-only reward: %v", err)
	}
	if out := res.(sim.LaborSolicitResult); out.State != sim.LaborStatePending {
		t.Errorf("result State = %q, want pending (broke employer holds the goods)", out.State)
	}
}

func TestSolicitWork_NegativeReward_Rejects(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "anne", displayName: "Anne", huddleID: "h1", worker: true},
		{id: "hannah", displayName: "Hannah", huddleID: "h1", coins: 50},
	})
	defer stop()

	now := time.Now().UTC()
	if _, err := w.Send(sim.SolicitWork("anne", "Hannah", -1,
		[]sim.PayItemInput{{Item: "porridge", Qty: 1}}, 120, now)); err == nil {
		t.Fatal("negative reward: want error, got nil")
	}
}

// TestSolicitWork_EmployerLacksItems_AutoDeclines — the LLM-193 affordability
// auto-decline extends to the in-kind leg: an employer who holds the coins but
// not the asked goods can only refuse, so the offer resolves Declined at mint
// without waking them.
func TestSolicitWork_EmployerLacksItems_AutoDeclines(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "anne", displayName: "Anne", huddleID: "h1", worker: true},
		{id: "hannah", displayName: "Hannah", huddleID: "h1", coins: 50}, // no porridge
	})
	defer stop()
	events := captureLaborEvents(t, w)

	now := time.Now().UTC()
	res, err := w.Send(sim.SolicitWork("anne", "Hannah", 2,
		[]sim.PayItemInput{{Item: "porridge", Qty: 1}}, 120, now))
	if err != nil {
		t.Fatalf("SolicitWork: %v", err)
	}
	out := res.(sim.LaborSolicitResult)
	if out.State != sim.LaborStateDeclined {
		t.Fatalf("result State = %q, want declined (employer lacks the goods)", out.State)
	}
	if len(events.Received) != 0 {
		t.Errorf("LaborOfferReceived fired %d times, want 0 (employer never woken)", len(events.Received))
	}
	if o := readLaborLedger(t, w)[out.ID]; o.State != sim.LaborStateDeclined {
		t.Errorf("ledger State = %q, want declined", o.State)
	}
}

// TestAcceptWork_EmployerDroppedItems_FailsUnavailable — gate 8's courtesy
// check spans both legs: an employer who parted with the promised goods
// between solicit and accept can't start the job.
func TestAcceptWork_EmployerDroppedItems_FailsUnavailable(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "anne", displayName: "Anne", huddleID: "h1", worker: true},
		{id: "hannah", displayName: "Hannah", huddleID: "h1", coins: 50}, // holds no porridge at accept
	})
	defer stop()
	now := time.Now().UTC()
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "anne", EmployerID: "hannah",
		Reward: 2, RewardItems: []sim.ItemKindQty{{Kind: "porridge", Qty: 1}},
		DurationMin: 120, State: sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1",
		CreatedAt: now, ExpiresAt: now.Add(3 * time.Minute),
	})

	res, err := w.Send(sim.AcceptWork("hannah", 1, now))
	if err != nil {
		t.Fatalf("AcceptWork: %v", err)
	}
	// A gate-driven flip returns the terminal state directly (the gate
	// failure IS the resolution), not a LaborAcceptResult.
	if state, ok := res.(sim.LaborLedgerState); !ok || state != sim.LaborStateFailedUnavailable {
		t.Errorf("accept result = %v (%T), want failed_unavailable state (goods leg short)", res, res)
	}
}

// TestEvaluateLaborLedgerSweep_SettlesInKind — the completion sweep transfers
// coins AND goods atomically: employer debited both legs (goods delete on
// zero), worker credited both, terminal Completed, event carries the leg.
func TestEvaluateLaborLedgerSweep_SettlesInKind(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "anne", displayName: "Anne", huddleID: "h1", worker: true},
		{id: "hannah", displayName: "Hannah", huddleID: "h1", coins: 50,
			inventory: map[sim.ItemKind]int{"porridge": 1, "bread": 2}},
	})
	defer stop()
	events := captureLaborEvents(t, w)
	now := time.Now().UTC()
	accepted := now.Add(-31 * time.Minute)
	until := now.Add(-time.Minute)
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "anne", EmployerID: "hannah",
		Reward: 2, RewardItems: []sim.ItemKindQty{{Kind: "porridge", Qty: 1}},
		DurationMin: 30, State: sim.LaborStateWorking,
		HuddleID: "h1", SceneID: "sc1",
		AcceptedAt: &accepted, WorkingUntil: &until,
	})
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["anne"]
		u := until
		a.LaborID = 1
		a.LaboringUntil = &u
		a.State = sim.StateLaboring
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed worker mirror: %v", err)
	}

	if _, err := w.Send(sim.EvaluateLaborLedgerSweep(now)); err != nil {
		t.Fatalf("EvaluateLaborLedgerSweep: %v", err)
	}
	if o := readLaborLedger(t, w)[1]; o.State != sim.LaborStateCompleted {
		t.Fatalf("offer State = %q, want completed", o.State)
	}
	// Coins leg.
	if got := readActor(t, w, "anne").Coins; got != 2 {
		t.Errorf("worker coins = %d, want 2", got)
	}
	if got := readActor(t, w, "hannah").Coins; got != 48 {
		t.Errorf("employer coins = %d, want 48", got)
	}
	// Goods leg: porridge moved whole; the zeroed employer line is deleted;
	// unrelated bread untouched.
	empInv := readInventoryMap(t, w, "hannah")
	if _, still := empInv["porridge"]; still {
		t.Errorf("employer porridge line still present after settle: %v", empInv)
	}
	if empInv["bread"] != 2 {
		t.Errorf("employer bread = %d, want 2 (untouched)", empInv["bread"])
	}
	if got := readInventoryMap(t, w, "anne")["porridge"]; got != 1 {
		t.Errorf("worker porridge = %d, want 1", got)
	}
	if len(events.Resolved) != 1 {
		t.Fatalf("LaborResolved count = %d, want 1", len(events.Resolved))
	}
	if ri := events.Resolved[0].RewardItems; len(ri) != 1 || ri[0].Kind != "porridge" || ri[0].Qty != 1 {
		t.Errorf("resolved event RewardItems = %+v, want [{porridge 1}]", ri)
	}
}

// TestEvaluateLaborLedgerSweep_ItemsGoneAtCompletionUnpaid — the authoritative
// settle re-check spans the goods leg: an employer who no longer holds the
// promised goods pays NOTHING (all-or-nothing — the coins don't part-pay), the
// job resolves failed_unavailable with WorkPerformed=true, and the stiffed-
// worker relationship facts name the full promised payment.
func TestEvaluateLaborLedgerSweep_ItemsGoneAtCompletionUnpaid(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "anne", displayName: "Anne", huddleID: "h1", worker: true},
		{id: "hannah", displayName: "Hannah", huddleID: "h1", coins: 50}, // porridge gone by settle time
	})
	defer stop()
	events := captureLaborEvents(t, w)
	now := time.Now().UTC()
	accepted := now.Add(-31 * time.Minute)
	until := now.Add(-time.Minute)
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "anne", EmployerID: "hannah",
		Reward: 2, RewardItems: []sim.ItemKindQty{{Kind: "porridge", Qty: 1}},
		DurationMin: 30, State: sim.LaborStateWorking,
		HuddleID: "h1", SceneID: "sc1",
		AcceptedAt: &accepted, WorkingUntil: &until,
	})
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["anne"]
		u := until
		a.LaborID = 1
		a.LaboringUntil = &u
		a.State = sim.StateLaboring
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed worker mirror: %v", err)
	}

	if _, err := w.Send(sim.EvaluateLaborLedgerSweep(now)); err != nil {
		t.Fatalf("EvaluateLaborLedgerSweep: %v", err)
	}
	if o := readLaborLedger(t, w)[1]; o.State != sim.LaborStateFailedUnavailable {
		t.Fatalf("offer State = %q, want failed_unavailable (goods leg gone)", o.State)
	}
	// All-or-nothing: the coin leg was coverable but must not move alone.
	if got := readActor(t, w, "hannah").Coins; got != 50 {
		t.Errorf("employer coins = %d, want 50 (no partial pay)", got)
	}
	if got := readActor(t, w, "anne").Coins; got != 0 {
		t.Errorf("worker coins = %d, want 0 (unpaid)", got)
	}
	if len(events.Resolved) != 1 || !events.Resolved[0].WorkPerformed {
		t.Fatalf("LaborResolved = %+v, want one with WorkPerformed=true", events.Resolved)
	}
}

// TestLaborFacts_PaymentPhraseNamesItems — the relationship facts carry the
// full payment phrase for an in-kind reward, on both the completed beat and
// the stiffed-worker beat (the promised porridge is remembered either way).
func TestLaborFacts_PaymentPhraseNamesItems(t *testing.T) {
	t.Run("completed names both legs", func(t *testing.T) {
		w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
			{id: "anne", displayName: "Anne Walker", huddleID: "h1", worker: true},
			{id: "hannah", displayName: "Hannah Boggs", huddleID: "h1", coins: 50,
				inventory: map[sim.ItemKind]int{"porridge": 1}},
		})
		defer stop()
		now := time.Now().UTC()
		accepted := now.Add(-31 * time.Minute)
		until := now.Add(-time.Minute)
		seedLaborOffer(t, w, sim.LaborOffer{
			ID: 1, WorkerID: "anne", EmployerID: "hannah",
			Reward: 2, RewardItems: []sim.ItemKindQty{{Kind: "porridge", Qty: 1}},
			DurationMin: 30, State: sim.LaborStateWorking,
			HuddleID: "h1", SceneID: "sc1",
			AcceptedAt: &accepted, WorkingUntil: &until,
		})
		if _, err := w.Send(sim.EvaluateLaborLedgerSweep(now)); err != nil {
			t.Fatalf("EvaluateLaborLedgerSweep: %v", err)
		}
		requireFact(t, w, "anne", "hannah", sim.InteractionWorked, "1 porridge and 2 coins")
		requireFact(t, w, "hannah", "anne", sim.InteractionHired, "1 porridge and 2 coins")
	})

	t.Run("stiffed worker remembers the promised goods", func(t *testing.T) {
		w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
			{id: "anne", displayName: "Anne Walker", huddleID: "h1", worker: true},
			{id: "hannah", displayName: "Hannah Boggs", huddleID: "h1", coins: 50}, // porridge gone
		})
		defer stop()
		now := time.Now().UTC()
		accepted := now.Add(-31 * time.Minute)
		until := now.Add(-time.Minute)
		seedLaborOffer(t, w, sim.LaborOffer{
			ID: 1, WorkerID: "anne", EmployerID: "hannah",
			Reward: 2, RewardItems: []sim.ItemKindQty{{Kind: "porridge", Qty: 1}},
			DurationMin: 30, State: sim.LaborStateWorking,
			HuddleID: "h1", SceneID: "sc1",
			AcceptedAt: &accepted, WorkingUntil: &until,
		})
		if _, err := w.Send(sim.EvaluateLaborLedgerSweep(now)); err != nil {
			t.Fatalf("EvaluateLaborLedgerSweep: %v", err)
		}
		requireFact(t, w, "anne", "hannah", sim.InteractionWorkedUnpaid, "1 porridge and 2 coins")
		requireFact(t, w, "hannah", "anne", sim.InteractionLeftWorkerUnpaid, "1 porridge and 2 coins")
	})
}
