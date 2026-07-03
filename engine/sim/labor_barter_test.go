package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// labor_barter_test.go — LLM-243. A worker soliciting a coin-poor-but-goods-rich
// employer for terms it can't cover in coin must NOT foreclose that employer.
// The mint-time affordability gate (LLM-193) auto-declines an offer the employer
// can't cover; before LLM-243 that fired even when the employer held tradeable
// goods and could hire in kind (LLM-225), which stamped the "this shop declined
// me" memory and dropped the employer from the solicit audience — the worker
// then perceived "No one here can hire you" and routed past a viable in-kind
// employer. The fix: when the employer can't cover the ASK but holds goods,
// SolicitWork returns LaborStateBarterPossible with NO ledger entry (no
// LaborOfferReceived, no LaborResolved, no offer recorded), so nothing is
// foreclosed and the harness steers the worker to re-ask in kind. Only a
// genuinely destitute employer (no coin AND no goods) still hits the LLM-193
// Declined auto-decline. This mirrors LLM-222 on the hiring side.

// TestSolicitWork_GoodsRichCoinPoorEmployer_BarterPossibleNoForeclosure is the
// live PW-Apothecary case: Silence (worker) solicits Prudence (0 coins, holding
// berries) for 5 coins + porridge she holds neither of. The offer must resolve
// to the barter signal without minting anything — no ledger entry (so
// employerDeclinedSubject can't fire), no LaborResolved (so the seek-work
// ObservedDeclinedWork memory is never stamped), and no LaborOfferReceived (the
// employer is not woken for a doomed coin ask).
func TestSolicitWork_GoodsRichCoinPoorEmployer_BarterPossibleNoForeclosure(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "silence", displayName: "Silence Walker", huddleID: "h1", coins: 15, worker: true},
		{id: "prudence", displayName: "Prudence Ward", huddleID: "h1", // 0 coins, goods on hand
			inventory: map[sim.ItemKind]int{"blueberry": 4, "coca_tea": 9}},
	})
	defer stop()
	events := captureLaborEvents(t, w)

	now := time.Now().UTC()
	res, err := w.Send(sim.SolicitWork("silence", "Prudence Ward", 5,
		[]sim.PayItemInput{{Item: "porridge", Qty: 1}}, 240, now))
	if err != nil {
		t.Fatalf("SolicitWork: %v", err)
	}
	out := res.(sim.LaborSolicitResult)
	if out.State != sim.LaborStateBarterPossible {
		t.Fatalf("result State = %q, want barter_possible (employer holds goods, can hire in kind)", out.State)
	}
	// Nothing minted: no offer recorded, so the employer stays solicitable and
	// keeps its shop in the seek-work directory.
	if led := readLaborLedger(t, w); len(led) != 0 {
		t.Errorf("ledger has %d offers after a barter solicit, want 0 (nothing minted): %+v", len(led), led)
	}
	if len(events.Received) != 0 {
		t.Errorf("LaborOfferReceived fired %d times, want 0 (employer not woken)", len(events.Received))
	}
	if len(events.Resolved) != 0 {
		t.Errorf("LaborResolved fired %d times, want 0 (no decline recorded — no ObservedDeclinedWork stamp)", len(events.Resolved))
	}
}

// TestSolicitWork_GoodsRichEmployer_CoinOnlyAsk_BarterPossible proves the barter
// branch fires whenever the ASK is uncoverable and the employer holds goods —
// including a pure-coin ask (no reward_items). A 0-coin employer asked for coins
// alone could still hire in kind, so it must not be declined either.
func TestSolicitWork_GoodsRichEmployer_CoinOnlyAsk_BarterPossible(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "silence", displayName: "Silence Walker", huddleID: "h1", coins: 15, worker: true},
		{id: "prudence", displayName: "Prudence Ward", huddleID: "h1",
			inventory: map[sim.ItemKind]int{"raspberry": 14}},
	})
	defer stop()
	events := captureLaborEvents(t, w)

	now := time.Now().UTC()
	res, err := w.Send(sim.SolicitWork("silence", "Prudence Ward", 5, nil, 240, now))
	if err != nil {
		t.Fatalf("SolicitWork: %v", err)
	}
	if out := res.(sim.LaborSolicitResult); out.State != sim.LaborStateBarterPossible {
		t.Fatalf("result State = %q, want barter_possible (0-coin goods-rich employer, coin-only ask)", out.State)
	}
	if len(events.Received) != 0 {
		t.Errorf("LaborOfferReceived fired %d times, want 0", len(events.Received))
	}
	if len(events.Resolved) != 0 {
		t.Errorf("LaborResolved fired %d times, want 0", len(events.Resolved))
	}
}

// TestSolicitWork_DestituteEmployer_StillDeclines guards that LLM-193 is intact:
// an employer with no coin AND no tradeable goods can hire no one, so the offer
// still auto-declines at mint (a real Declined ledger entry + LaborResolved,
// which is what stamps the seek-work memory and steers the worker elsewhere).
// This is the contrast that proves the barter branch is gated on holding goods,
// not a blanket "never decline".
func TestSolicitWork_DestituteEmployer_StillDeclines(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "silence", displayName: "Silence Walker", huddleID: "h1", coins: 15, worker: true},
		{id: "prudence", displayName: "Prudence Ward", huddleID: "h1"}, // 0 coins, empty inventory
	})
	defer stop()
	events := captureLaborEvents(t, w)

	now := time.Now().UTC()
	res, err := w.Send(sim.SolicitWork("silence", "Prudence Ward", 5, nil, 240, now))
	if err != nil {
		t.Fatalf("SolicitWork: %v", err)
	}
	out := res.(sim.LaborSolicitResult)
	if out.State != sim.LaborStateDeclined {
		t.Fatalf("result State = %q, want declined (destitute employer — no coin, no goods)", out.State)
	}
	if o := readLaborLedger(t, w)[out.ID]; o.State != sim.LaborStateDeclined {
		t.Errorf("ledger offer %d state = %q, want a declined entry", out.ID, o.State)
	}
	if len(events.Received) != 0 {
		t.Errorf("LaborOfferReceived fired %d times, want 0 (employer never woken)", len(events.Received))
	}
	if len(events.Resolved) != 1 {
		t.Errorf("LaborResolved fired %d times, want 1 (the decline that seeds the seek-work memory)", len(events.Resolved))
	}
}

// TestSolicitWork_GoodsRichEmployer_CoverableAsk_MintsPending guards that the
// barter branch never hijacks an ask the employer CAN cover: a goods-rich
// employer asked only for goods it holds is hired the normal way (Pending offer
// minted, employer woken), exactly as before LLM-243.
func TestSolicitWork_GoodsRichEmployer_CoverableAsk_MintsPending(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "silence", displayName: "Silence Walker", huddleID: "h1", coins: 15, worker: true},
		{id: "prudence", displayName: "Prudence Ward", huddleID: "h1",
			inventory: map[sim.ItemKind]int{"blueberry": 4}},
	})
	defer stop()
	events := captureLaborEvents(t, w)

	now := time.Now().UTC()
	res, err := w.Send(sim.SolicitWork("silence", "Prudence Ward", 0,
		[]sim.PayItemInput{{Item: "blueberry", Qty: 2}}, 240, now))
	if err != nil {
		t.Fatalf("SolicitWork: %v", err)
	}
	out := res.(sim.LaborSolicitResult)
	if out.State != sim.LaborStatePending {
		t.Fatalf("result State = %q, want pending (employer holds the asked goods)", out.State)
	}
	if o := readLaborLedger(t, w)[out.ID]; o.State != sim.LaborStatePending {
		t.Errorf("ledger offer %d state = %q, want a pending entry", out.ID, o.State)
	}
	if len(events.Received) != 1 {
		t.Errorf("LaborOfferReceived fired %d times, want 1 (employer woken to answer)", len(events.Received))
	}
}
