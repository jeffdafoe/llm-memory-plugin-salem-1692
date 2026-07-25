package sim

import (
	"testing"
	"time"
)

// sale_standoff_capture_test.go — LLM-525 capture subscriber. White-box (package
// sim) so it drives handleSaleStandoffOnResolved directly with a
// PayWithItemResolved event and the ledger state the threshold count reads.
//
// The subscriber's job: stamp the buyer's "my offers here went nowhere" memory at
// exactly the moment the co-present soften trips — the SaleStandoffDeclineThreshold'th
// dead end in one conversation — so the hold-off outlives the huddle it was decided in.

// ssWorld builds a world with a pay ledger the threshold count can walk.
func ssWorld() *World {
	return &World{Actors: make(map[ActorID]*Actor), PayLedger: make(map[LedgerID]*PayLedgerEntry)}
}

// ssEntry adds a ledger entry. A zero ResolvedAt marks it still pending.
func ssEntry(w *World, id LedgerID, buyer, seller ActorID, item ItemKind, huddle HuddleID, state PayLedgerState, resolvedAt time.Time) {
	w.PayLedger[id] = &PayLedgerEntry{
		ID: id, BuyerID: buyer, SellerID: seller, ItemKind: item,
		HuddleID: huddle, State: state, ResolvedAt: resolvedAt,
	}
}

func ssResolved(id LedgerID, buyer, seller ActorID, item ItemKind, huddle HuddleID, state PayTerminalState, at time.Time) *PayWithItemResolved {
	return &PayWithItemResolved{LedgerID: id, BuyerID: buyer, SellerID: seller, ItemKind: item, HuddleID: huddle, TerminalState: state, At: at}
}

// ssPair sets up the live shape: Elizabeth buying nails from Ezekiel, who keeps the
// Blacksmith (the workplace the buy directory names and she walks to).
func ssPair(w *World) *Actor {
	buyer := &Actor{ID: "elizabeth", Kind: KindNPCStateful}
	w.Actors["elizabeth"] = buyer
	w.Actors["ezekiel"] = &Actor{ID: "ezekiel", Kind: KindNPCStateful, WorkStructureID: "blacksmith"}
	return buyer
}

var ssKey = ObservedStateKey{StructureID: "blacksmith", ItemKind: "nail", Condition: ObservedSaleStandoff}

func TestSaleStandoff_FirstDeclineDoesNotRecord(t *testing.T) {
	w := ssWorld()
	buyer := ssPair(w)
	now := time.Now()
	ssEntry(w, 1, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now)

	handleSaleStandoffOnResolved(w, ssResolved(1, "elizabeth", "ezekiel", "nail", "hud-a", PayTerminalStateDeclined, now))

	// One no is ordinary haggling — she may press once more (LLM-297). Recording here
	// would drop the shop from her directory after a single refusal.
	if buyer.Observed.Len() != 0 {
		t.Fatalf("a single decline must not record a standoff, got %v", buyer.Observed)
	}
}

func TestSaleStandoff_SecondDeclineRecords(t *testing.T) {
	w := ssWorld()
	buyer := ssPair(w)
	now := time.Now()
	ssEntry(w, 1, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now.Add(-9*time.Minute))
	ssEntry(w, 2, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now)

	handleSaleStandoffOnResolved(w, ssResolved(2, "elizabeth", "ezekiel", "nail", "hud-a", PayTerminalStateDeclined, now))

	at, ok := buyer.Observed.At(ssKey)
	if !ok {
		t.Fatalf("the threshold'th decline must record the standoff against the seller's workplace, got %v", buyer.Observed)
	}
	if !at.Equal(now) {
		t.Errorf("memory stamped at %v, want the event time %v", at, now)
	}
}

// The declines are nine minutes apart above and still count: the standoff takes the
// conversation's whole lifetime with no recency filter. That is the LLM-510 finding —
// re-offers are paced by the staleness wake backoff at multi-minute gaps, so a rolling
// window can never accumulate two. This test pins the capture side of it explicitly.
func TestSaleStandoff_AgedDeclinesInSameHuddleStillCount(t *testing.T) {
	w := ssWorld()
	buyer := ssPair(w)
	now := time.Now()
	ssEntry(w, 1, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now.Add(-53*time.Minute))
	ssEntry(w, 2, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now)

	handleSaleStandoffOnResolved(w, ssResolved(2, "elizabeth", "ezekiel", "nail", "hud-a", PayTerminalStateDeclined, now))

	if _, ok := buyer.Observed.At(ssKey); !ok {
		t.Fatalf("declines spaced across the conversation must still reach the threshold, got %v", buyer.Observed)
	}
}

// The count is scoped to one conversation, matching perception's own scan. A fresh
// huddle is a fresh negotiation and starts a fresh counter (LLM-510 accepted this
// deliberately) — so a decline carried over from a prior conversation must not join
// today's to trip the memory.
func TestSaleStandoff_DeclinesInOtherHuddleDoNotCount(t *testing.T) {
	w := ssWorld()
	buyer := ssPair(w)
	now := time.Now()
	ssEntry(w, 1, "elizabeth", "ezekiel", "nail", "hud-earlier", PayLedgerStateDeclined, now.Add(-time.Hour))
	ssEntry(w, 2, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now)

	handleSaleStandoffOnResolved(w, ssResolved(2, "elizabeth", "ezekiel", "nail", "hud-a", PayTerminalStateDeclined, now))

	if buyer.Observed.Len() != 0 {
		t.Fatalf("declines from a prior conversation must not count toward this one's standoff, got %v", buyer.Observed)
	}
}

// The resolving entry counts whether or not its own terminal-state write has landed by
// the time the subscriber runs, and is never double-counted. Both orderings must give
// the same answer or the memory would stamp a decline early or late depending on
// subscriber order — a genuinely order-dependent bug. (The third ordering, the entry
// not being in the ledger at all, is covered by
// TestSaleStandoff_ResolvingEntryAbsentFromLedgerStillCounts.)
func TestSaleStandoff_ResolvingEntryCountedExactlyOnce(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		state   PayLedgerState
		resolve time.Time
	}{
		{"state_already_written", PayLedgerStateDeclined, now},
		{"state_not_yet_written", PayLedgerStatePending, time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := ssWorld()
			buyer := ssPair(w)
			ssEntry(w, 1, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now.Add(-5*time.Minute))
			ssEntry(w, 2, "elizabeth", "ezekiel", "nail", "hud-a", tc.state, tc.resolve)

			handleSaleStandoffOnResolved(w, ssResolved(2, "elizabeth", "ezekiel", "nail", "hud-a", PayTerminalStateDeclined, now))

			if _, ok := buyer.Observed.At(ssKey); !ok {
				t.Fatalf("the resolving entry must count toward the threshold regardless of write order, got %v", buyer.Observed)
			}
		})
	}
	// And the resolving entry alone is not two dead ends.
	w := ssWorld()
	buyer := ssPair(w)
	ssEntry(w, 1, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now)
	handleSaleStandoffOnResolved(w, ssResolved(1, "elizabeth", "ezekiel", "nail", "hud-a", PayTerminalStateDeclined, now))
	if buyer.Observed.Len() != 0 {
		t.Fatalf("the resolving entry must not be counted twice (once from the ledger, once by hand), got %v", buyer.Observed)
	}
}

// Only the terminals that mean "this deal did not close" count. A counter is the
// negotiation continuing; an insufficient-funds failure is the buyer's own empty purse,
// already governed by merchantConserve and liable to clear within the hour — neither
// should cost the shop four hours in the directory.
func TestSaleStandoff_NonStandoffTerminalsDoNotCount(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name   string
		ledger PayLedgerState
		event  PayTerminalState
	}{
		{"countered", PayLedgerStateCountered, PayTerminalStateCountered},
		{"insufficient_funds", PayLedgerStateFailedInsufficientFunds, PayTerminalStateFailedInsufficientFunds},
		{"withdrawn_by_buyer", PayLedgerStateWithdrawnByBuyer, PayTerminalStateWithdrawnByBuyer},
		{"expired", PayLedgerStateExpired, PayTerminalStateExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := ssWorld()
			buyer := ssPair(w)
			// A prior genuine decline sits in the ledger, so only the terminal under test
			// decides whether the threshold is reached.
			ssEntry(w, 1, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now.Add(-5*time.Minute))
			ssEntry(w, 2, "elizabeth", "ezekiel", "nail", "hud-a", tc.ledger, now)

			handleSaleStandoffOnResolved(w, ssResolved(2, "elizabeth", "ezekiel", "nail", "hud-a", tc.event, now))

			if buyer.Observed.Len() != 0 {
				t.Fatalf("%s must not count as a dead end, got %v", tc.name, buyer.Observed)
			}
		})
	}
}

// A seller who cannot fill and a buyer whose bundle falls short are both dead ends —
// the same three terminals perception's coPresentBuyStandoff counts, so the memory
// stamps on exactly the event that softens the co-present cue.
func TestSaleStandoff_StockAndGoodsFailuresCount(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name   string
		ledger PayLedgerState
		event  PayTerminalState
	}{
		{"insufficient_stock", PayLedgerStateFailedInsufficientStock, PayTerminalStateFailedInsufficientStock},
		{"insufficient_goods", PayLedgerStateFailedInsufficientGoods, PayTerminalStateFailedInsufficientGoods},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := ssWorld()
			buyer := ssPair(w)
			ssEntry(w, 1, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now.Add(-5*time.Minute))
			ssEntry(w, 2, "elizabeth", "ezekiel", "nail", "hud-a", tc.ledger, now)

			handleSaleStandoffOnResolved(w, ssResolved(2, "elizabeth", "ezekiel", "nail", "hud-a", tc.event, now))

			if _, ok := buyer.Observed.At(ssKey); !ok {
				t.Fatalf("%s must count toward the standoff, got %v", tc.name, buyer.Observed)
			}
		})
	}
}

func TestSaleStandoff_AcceptedBuyClearsMemory(t *testing.T) {
	w := ssWorld()
	now := time.Now()
	buyer := &Actor{ID: "elizabeth", Kind: KindNPCStateful, Observed: NewObservedStates(map[ObservedStateKey]time.Time{
		ssKey: now.Add(-time.Hour),
	})}
	w.Actors["elizabeth"] = buyer
	w.Actors["ezekiel"] = &Actor{ID: "ezekiel", Kind: KindNPCStateful, WorkStructureID: "blacksmith"}

	handleSaleStandoffOnResolved(w, ssResolved(9, "elizabeth", "ezekiel", "nail", "hud-a", PayTerminalStateAccepted, now))

	// They dealt after all — keeping the shop out of her directory would be a memory
	// contradicted by the coins that just changed hands.
	if _, ok := buyer.Observed.At(ssKey); ok {
		t.Fatalf("an accepted buy must clear the standoff memory, got %v", buyer.Observed)
	}
}

// A seller with no workplace is a peer met on the road, not a shop — there is no
// directory entry to suppress and nothing to walk-avoid.
func TestSaleStandoff_SkipsCoPresentPeerSeller(t *testing.T) {
	w := ssWorld()
	now := time.Now()
	buyer := &Actor{ID: "elizabeth", Kind: KindNPCStateful}
	w.Actors["elizabeth"] = buyer
	w.Actors["nathaniel"] = &Actor{ID: "nathaniel", Kind: KindNPCStateful} // no workplace
	ssEntry(w, 1, "elizabeth", "nathaniel", "nail", "hud-a", PayLedgerStateDeclined, now.Add(-5*time.Minute))
	ssEntry(w, 2, "elizabeth", "nathaniel", "nail", "hud-a", PayLedgerStateDeclined, now)

	handleSaleStandoffOnResolved(w, ssResolved(2, "elizabeth", "nathaniel", "nail", "hud-a", PayTerminalStateDeclined, now))

	if buyer.Observed.Len() != 0 {
		t.Fatalf("a workplace-less peer seller has no destination to avoid, got %v", buyer.Observed)
	}
}

// A PC buyer gets no experiential memory (the store exists to steer NPC perception).
func TestSaleStandoff_SkipsNonAgentBuyer(t *testing.T) {
	w := ssWorld()
	now := time.Now()
	buyer := &Actor{ID: "player", Kind: KindPC}
	w.Actors["player"] = buyer
	w.Actors["ezekiel"] = &Actor{ID: "ezekiel", Kind: KindNPCStateful, WorkStructureID: "blacksmith"}
	ssEntry(w, 1, "player", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now.Add(-5*time.Minute))
	ssEntry(w, 2, "player", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now)

	handleSaleStandoffOnResolved(w, ssResolved(2, "player", "ezekiel", "nail", "hud-a", PayTerminalStateDeclined, now))

	if buyer.Observed.Len() != 0 {
		t.Fatalf("a PC buyer carries no observed-state memory, got %v", buyer.Observed)
	}
}

// The count is scoped by the EVENT's huddle, so a resolving entry that is not in the
// ledger at all — reaped, or the subscriber running before the insert — still reaches
// the threshold off the conversation's prior entries. This is the guarantee the
// original ledger-lookup version claimed but did not hold (code_review LLM-525):
// there it discarded the event and silently recorded nothing.
func TestSaleStandoff_ResolvingEntryAbsentFromLedgerStillCounts(t *testing.T) {
	w := ssWorld()
	buyer := ssPair(w)
	now := time.Now()
	ssEntry(w, 1, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now.Add(-5*time.Minute))
	// Entry 2 never lands in the ledger.
	handleSaleStandoffOnResolved(w, ssResolved(2, "elizabeth", "ezekiel", "nail", "hud-a", PayTerminalStateDeclined, now))

	if _, ok := buyer.Observed.At(ssKey); !ok {
		t.Fatalf("the standoff must not depend on the resolving entry being present in the ledger, got %v", buyer.Observed)
	}
}

// An event carrying no huddle leaves nothing to scope the count to — the same
// disabled-scan posture perception takes on an empty huddle, rather than treating an
// unscopeable decline as a standoff.
func TestSaleStandoff_HuddlelessEventDoesNotRecord(t *testing.T) {
	w := ssWorld()
	buyer := ssPair(w)
	now := time.Now()
	ssEntry(w, 1, "elizabeth", "ezekiel", "nail", "", PayLedgerStateDeclined, now.Add(-5*time.Minute))
	ssEntry(w, 2, "elizabeth", "ezekiel", "nail", "", PayLedgerStateDeclined, now)

	handleSaleStandoffOnResolved(w, ssResolved(2, "elizabeth", "ezekiel", "nail", "", PayTerminalStateDeclined, now))

	if buyer.Observed.Len() != 0 {
		t.Fatalf("a huddle-less decline cannot be scoped to a conversation, got %v", buyer.Observed)
	}
}

// The count follows the EVENT's huddle, not the resolving ledger entry's — they are
// the same in production (every emitter stamps the event from the entry), and pinning
// it here documents which one is authoritative if they ever disagree.
func TestSaleStandoff_CountFollowsEventHuddle(t *testing.T) {
	w := ssWorld()
	buyer := ssPair(w)
	now := time.Now()
	ssEntry(w, 1, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now.Add(-5*time.Minute))
	ssEntry(w, 2, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now)

	// The event names a different conversation, so entry 1 is out of scope.
	handleSaleStandoffOnResolved(w, ssResolved(2, "elizabeth", "ezekiel", "nail", "hud-other", PayTerminalStateDeclined, now))

	if buyer.Observed.Len() != 0 {
		t.Fatalf("prior entries from another conversation must not count, got %v", buyer.Observed)
	}
}

// The count is scoped to (buyer, seller, item): another buyer's quarrel with the same
// smith, or this buyer's quarrel over a different good, must not push her nail
// negotiation over the threshold.
func TestSaleStandoff_CountIsScopedToBuyerSellerItem(t *testing.T) {
	now := time.Now()
	t.Run("other_buyer", func(t *testing.T) {
		w := ssWorld()
		buyer := ssPair(w)
		w.Actors["josiah"] = &Actor{ID: "josiah", Kind: KindNPCStateful}
		ssEntry(w, 1, "josiah", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now.Add(-5*time.Minute))
		ssEntry(w, 2, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now)
		handleSaleStandoffOnResolved(w, ssResolved(2, "elizabeth", "ezekiel", "nail", "hud-a", PayTerminalStateDeclined, now))
		if buyer.Observed.Len() != 0 {
			t.Fatalf("another buyer's decline must not count toward this buyer's standoff, got %v", buyer.Observed)
		}
	})
	t.Run("other_item", func(t *testing.T) {
		w := ssWorld()
		buyer := ssPair(w)
		ssEntry(w, 1, "elizabeth", "ezekiel", "shovel", "hud-a", PayLedgerStateDeclined, now.Add(-5*time.Minute))
		ssEntry(w, 2, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now)
		handleSaleStandoffOnResolved(w, ssResolved(2, "elizabeth", "ezekiel", "nail", "hud-a", PayTerminalStateDeclined, now))
		if buyer.Observed.Len() != 0 {
			t.Fatalf("a decline over a different good must not count toward this one, got %v", buyer.Observed)
		}
	})
}

// A pending offer is not a dead end — it has no answer yet.
func TestSaleStandoff_PendingEntriesDoNotCount(t *testing.T) {
	w := ssWorld()
	buyer := ssPair(w)
	now := time.Now()
	ssEntry(w, 1, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStatePending, time.Time{})
	ssEntry(w, 2, "elizabeth", "ezekiel", "nail", "hud-a", PayLedgerStateDeclined, now)

	handleSaleStandoffOnResolved(w, ssResolved(2, "elizabeth", "ezekiel", "nail", "hud-a", PayTerminalStateDeclined, now))

	if buyer.Observed.Len() != 0 {
		t.Fatalf("an unanswered offer is not a dead end, got %v", buyer.Observed)
	}
}

// The memory's TTL is what makes "come back later" mean later rather than never.
func TestSaleStandoff_MemoryDecaysAtTTL(t *testing.T) {
	now := time.Now()
	buyer := &Actor{Observed: NewObservedStates(map[ObservedStateKey]time.Time{
		ssKey: now.Add(-SaleStandoffMemoryTTL + time.Minute),
	})}
	if !buyer.Observed.Active(ssKey, now) {
		t.Error("a memory inside its TTL must read active")
	}
	buyer.Observed = NewObservedStates(map[ObservedStateKey]time.Time{
		ssKey: now.Add(-SaleStandoffMemoryTTL - time.Minute),
	})
	if buyer.Observed.Active(ssKey, now) {
		t.Error("a memory past SaleStandoffMemoryTTL must have decayed — the supplier is worth asking again")
	}
}
