package sim

import (
	"testing"
	"time"
)

// settled_test.go — LLM-1018. The passive party of an engine settlement gets its
// own `settled` ring entry and durable row; nothing about the active party's rows
// or the coin record changes.

func ringOfType(w *World, t ActionType) []ActionLogEntry {
	var out []ActionLogEntry
	for _, e := range w.ActionLog {
		if e.ActionType == t {
			out = append(out, e)
		}
	}
	return out
}

func durableOfType(rows []DurableActionLogRow, t ActionType) []DurableActionLogRow {
	var out []DurableActionLogRow
	for _, r := range rows {
		if r.ActionType == t {
			out = append(out, r)
		}
	}
	return out
}

func settledTestWorld() *World {
	w := &World{}
	w.Actors = map[ActorID]*Actor{
		"hannah": {ID: "hannah", DisplayName: "Hannah Boggs", Kind: KindNPCStateful, CurrentHuddleID: "hud-busy"},
		"thomas": {ID: "thomas", DisplayName: "Thomas Hale", Kind: KindNPCShared},
	}
	return w
}

func TestRecordPassiveSettlement_WritesRingAndDurableRowWithNoHuddle(t *testing.T) {
	w := settledTestWorld()
	sink := &recordingActionLogSink{}
	w.SetActionLogSink(sink)
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	RecordPassiveSettlement(w, PassiveSettlement{
		PartyID: "hannah", CounterpartyID: "thomas", CounterpartyName: "Thomas Hale",
		Kind: SettlementRent, Amount: 3, Text: "Thomas Hale paid you 3 coins for another night's lodging.",
		Extra: map[string]any{"structure_id": "inn"}, At: at,
	})

	ring := ringOfType(w, ActionTypeSettled)
	if len(ring) != 1 {
		t.Fatalf("settled ring entries = %+v, want 1", w.ActionLog)
	}
	e := ring[0]
	if e.ActorID != "hannah" || e.CounterpartyName != "Thomas Hale" || e.Amount != 3 || e.Text == "" {
		t.Errorf("ring entry = %+v", e)
	}
	// She sits in a busy huddle; the sweep's settlement must not land in that
	// conversation.
	if e.HuddleID != "" {
		t.Errorf("ring HuddleID = %q, want empty", e.HuddleID)
	}
	rows := durableOfType(sink.rows, ActionTypeSettled)
	if len(rows) != 1 {
		t.Fatalf("durable settled rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.ActorID != "hannah" || r.HuddleID != "" || r.Source != "engine" || r.SpeakerName != "Hannah Boggs" {
		t.Errorf("durable row = %+v", r)
	}
	p := r.Payload
	if p["settlement"] != "rent" || p["counterparty"] != "Thomas Hale" || p["counterparty_actor_id"] != "thomas" ||
		p["amount"] != 3 || p["text"] != e.Text || p["structure_id"] != "inn" {
		t.Errorf("payload = %+v", p)
	}
	// None of the keys the magistrates' dealings query or the coin-record seed
	// match on: the active party's row already carries the deal.
	for _, k := range []string{"recipient", "recipient_actor_id", "employer", "employer_actor_id", "payer", "payer_actor_id", "buyer", "seller", "worker"} {
		if _, ok := p[k]; ok {
			t.Errorf("payload carries %q, a key another reader matches on", k)
		}
	}
	if len(w.CoinRecord) != 0 {
		t.Errorf("coin record = %+v, want untouched", w.CoinRecord)
	}
}

func TestRecordPassiveSettlement_Skips(t *testing.T) {
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	cases := map[string]PassiveSettlement{
		"party gone":    {PartyID: "nobody", Kind: SettlementWage, Text: "x", At: at},
		"party visitor": {PartyID: "vstr-0123abcd", Kind: SettlementWage, Text: "x", At: at},
		"no text":       {PartyID: "hannah", Kind: SettlementWage, At: at},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			w := settledTestWorld()
			w.Actors["vstr-0123abcd"] = &Actor{ID: "vstr-0123abcd", DisplayName: "A Visitor"}
			sink := &recordingActionLogSink{}
			w.SetActionLogSink(sink)
			RecordPassiveSettlement(w, s)
			if len(w.ActionLog) != 0 || len(sink.rows) != 0 {
				t.Errorf("wrote ring %+v / rows %+v, want nothing", w.ActionLog, sink.rows)
			}
		})
	}
}

func TestRecordPassiveSettlement_VisitorCounterpartyKeepsNameOnly(t *testing.T) {
	w := settledTestWorld()
	sink := &recordingActionLogSink{}
	w.SetActionLogSink(sink)
	RecordPassiveSettlement(w, PassiveSettlement{
		PartyID: "hannah", CounterpartyID: "vstr-0123abcd", CounterpartyName: "Asa Larkin the carter",
		Kind: SettlementCarterBuy, Amount: 6, Text: "x", At: time.Now().UTC(),
	})
	rows := durableOfType(sink.rows, ActionTypeSettled)
	if len(rows) != 1 {
		t.Fatalf("durable settled rows = %d, want 1", len(rows))
	}
	if _, ok := rows[0].Payload["counterparty_actor_id"]; ok {
		t.Errorf("payload = %+v, want no counterparty_actor_id for a visitor", rows[0].Payload)
	}
	if rows[0].Payload["counterparty"] != "Asa Larkin the carter" {
		t.Errorf("counterparty = %v, want the carter's name", rows[0].Payload["counterparty"])
	}
}

func TestSettleCarterBuyLeg_HolderGetsSettledRow(t *testing.T) {
	w := carterWorld()
	carter, _ := carterOnRoute(w)
	sink := &recordingActionLogSink{}
	w.SetActionLogSink(sink)
	carter.InsideStructureID = "farm"
	advanceCarter(w, carter, carterNow)

	want := "Asa Larkin the carter bought 25 sheaves of wheat from you for 25 coins, to carry off in the cart."
	ring := ringOfType(w, ActionTypeSettled)
	if len(ring) != 1 || ring[0].ActorID != "liz" || ring[0].Text != want || ring[0].Amount != 25 {
		t.Fatalf("settled ring = %+v, want one for liz: %q", ring, want)
	}
	rows := durableOfType(sink.rows, ActionTypeSettled)
	if len(rows) != 1 {
		t.Fatalf("durable settled rows = %d, want 1", len(rows))
	}
	p := rows[0].Payload
	if rows[0].ActorID != "liz" || p["settlement"] != "carter_buy" || p["item"] != "wheat" || p["qty"] != 25 ||
		p["amount"] != 25 || p["text"] != want {
		t.Errorf("durable row = %+v", rows[0])
	}
	if _, ok := p["counterparty_actor_id"]; ok {
		t.Errorf("payload = %+v, want no id for the visitor carter", p)
	}
	// The carter's own paid row is unchanged: still exactly one.
	if n := len(durableOfType(sink.rows, ActionTypePaid)); n != 1 {
		t.Errorf("durable paid rows = %d, want 1", n)
	}
}

func TestRebook_KeeperGetsSettledRow(t *testing.T) {
	lodger := rebookLodger("jefferey", 10, 2, rebookNow.Add(3*time.Hour))
	lodger.DisplayName = "Jefferey"
	keeper := rebookKeeper("hannah")
	w := rebookTestWorld(28, 11, lodger, keeper) // nightly = 4
	sink := &recordingActionLogSink{}
	w.SetActionLogSink(sink)

	runRebook(t, w)

	want := "Jefferey paid you 4 coins for another night's lodging."
	ring := ringOfType(w, ActionTypeSettled)
	if len(ring) != 1 || ring[0].ActorID != "hannah" || ring[0].Text != want {
		t.Fatalf("settled ring = %+v, want one for hannah: %q", ring, want)
	}
	rows := durableOfType(sink.rows, ActionTypeSettled)
	if len(rows) != 1 {
		t.Fatalf("durable settled rows = %d, want 1", len(rows))
	}
	p := rows[0].Payload
	newExpires := *lodger.RoomAccess[RoomAccessKey{RoomID: 2, Source: AccessSourceLedger}].ExpiresAt
	if rows[0].ActorID != "hannah" || p["settlement"] != "rent" || p["counterparty_actor_id"] != "jefferey" ||
		p["amount"] != 4 || p["text"] != want || p["lodging_until"] != newExpires {
		t.Errorf("durable row = %+v", rows[0])
	}
	if p["structure_id"] == nil || p["structure_id"] == "" {
		t.Errorf("payload = %+v, want the room's structure_id", p)
	}
}

func TestRebook_OfflineHoldWritesNoSettledRow(t *testing.T) {
	lodger := rebookLodger("jefferey", 10, 2, rebookNow.Add(3*time.Hour))
	seen := rebookNow.Add(-48 * time.Hour)
	lodger.LastPCSeenAt = &seen // offline: the room is held free, not billed
	keeper := rebookKeeper("hannah")
	w := rebookTestWorld(28, 11, lodger, keeper)
	sink := &recordingActionLogSink{}
	w.SetActionLogSink(sink)

	runRebook(t, w)

	if n := len(ringOfType(w, ActionTypeSettled)); n != 0 {
		t.Errorf("settled ring entries = %d, want 0 — no coin moved", n)
	}
	if n := len(durableOfType(sink.rows, ActionTypeSettled)); n != 0 {
		t.Errorf("settled rows = %d, want 0", n)
	}
}

func TestRecordPassiveSettlement_VisitorStatePartyWithAnyIDShapeIsSkipped(t *testing.T) {
	w := settledTestWorld()
	// Not the mint shape: only VisitorState says this is a visitor.
	w.Actors["guest-x"] = &Actor{ID: "guest-x", DisplayName: "A Peddler", VisitorState: &VisitorState{}}
	sink := &recordingActionLogSink{}
	w.SetActionLogSink(sink)
	RecordPassiveSettlement(w, PassiveSettlement{PartyID: "guest-x", Kind: SettlementWage, Text: "x", At: time.Now().UTC()})
	if len(w.ActionLog) != 0 || len(sink.rows) != 0 {
		t.Errorf("wrote ring %+v / rows %+v, want nothing for a visitor party", w.ActionLog, sink.rows)
	}
}

func TestRecordPassiveSettlement_ExtraCannotOverrideOwnedFields(t *testing.T) {
	w := settledTestWorld()
	w.Actors["guest-x"] = &Actor{ID: "guest-x", DisplayName: "A Peddler", VisitorState: &VisitorState{}}
	sink := &recordingActionLogSink{}
	w.SetActionLogSink(sink)
	RecordPassiveSettlement(w, PassiveSettlement{
		PartyID: "hannah", CounterpartyID: "guest-x", CounterpartyName: "A Peddler",
		Kind: SettlementCarterBuy, Amount: 6, Text: "real",
		Extra: map[string]any{"text": "fake", "amount": 99, "settlement": "wage", "counterparty_actor_id": "guest-x", "item": "salt"},
		At:    time.Now().UTC(),
	})
	rows := durableOfType(sink.rows, ActionTypeSettled)
	if len(rows) != 1 {
		t.Fatalf("durable settled rows = %d, want 1", len(rows))
	}
	p := rows[0].Payload
	if p["text"] != "real" || p["amount"] != 6 || p["settlement"] != "carter_buy" || p["item"] != "salt" {
		t.Errorf("payload = %+v, want the helper's own fields to win and the extra item kept", p)
	}
	if _, ok := p["counterparty_actor_id"]; ok {
		t.Errorf("payload = %+v, want no counterparty_actor_id for a visitor, even if Extra sets one", p)
	}
}

// The consolidation input builder reads the ring by actor: the passive party gets
// the line, a stranger sharing her huddle does not.
func TestRecordPassiveSettlement_ReachesOnlyThePartysConsolidation(t *testing.T) {
	w := settledTestWorld()
	w.Actors["thomas"].CurrentHuddleID = "hud-busy"
	at := time.Now().UTC()
	RecordPassiveSettlement(w, PassiveSettlement{
		PartyID: "hannah", CounterpartyID: "thomas", CounterpartyName: "Thomas Hale",
		Kind: SettlementRent, Amount: 3, Text: "Thomas Hale paid you 3 coins for another night's lodging.", At: at,
	})
	got := snapshotEventsForActor(w, "hannah", at.Add(-time.Hour))
	if len(got) != 1 || got[0].ActionType != ActionTypeSettled || got[0].Text != "Thomas Hale paid you 3 coins for another night's lodging." {
		t.Errorf("hannah's consolidation events = %+v, want the settled line", got)
	}
	if !actorHasEventSince(w, "hannah", at.Add(-time.Hour)) {
		t.Errorf("actorHasEventSince(hannah) = false, want the settlement to count as activity")
	}
	if other := snapshotEventsForActor(w, "thomas", at.Add(-time.Hour)); len(other) != 0 {
		t.Errorf("thomas's consolidation events = %+v, want none — it is not his row", other)
	}
	// A third villager in her huddle, not a party to the deal.
	w.Actors["silas"] = &Actor{ID: "silas", DisplayName: "Silas", Kind: KindNPCShared, CurrentHuddleID: "hud-busy"}
	if other := snapshotEventsForActor(w, "silas", at.Add(-time.Hour)); len(other) != 0 {
		t.Errorf("silas's consolidation events = %+v, want none", other)
	}
	if actorHasEventSince(w, "silas", at.Add(-time.Hour)) {
		t.Errorf("actorHasEventSince(silas) = true, want false — the settlement is not his activity")
	}
}

func TestSettleCarterBuyLeg_NothingSettledWritesNoSettledRow(t *testing.T) {
	for name, setup := range map[string]func(w *World, carter *Actor){
		"empty purse": func(_ *World, carter *Actor) { carter.Coins, carter.VisitorState.SpendBudget = 0, 0 },
		"no stock":    func(w *World, _ *Actor) { w.Actors["liz"].Inventory["wheat"] = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			w := carterWorld()
			carter, _ := carterOnRoute(w)
			setup(w, carter)
			sink := &recordingActionLogSink{}
			w.SetActionLogSink(sink)
			carter.InsideStructureID = "farm"
			advanceCarter(w, carter, carterNow)
			if n := len(ringOfType(w, ActionTypeSettled)); n != 0 {
				t.Errorf("settled ring entries = %d, want 0", n)
			}
			if n := len(durableOfType(sink.rows, ActionTypeSettled)); n != 0 {
				t.Errorf("settled rows = %d, want 0", n)
			}
		})
	}
}

func TestRebook_UnbilledRenewalWritesNoSettledRow(t *testing.T) {
	for name, build := range map[string]func() *World{
		"broke lodger": func() *World {
			return rebookTestWorld(28, 11, rebookLodger("ezekiel", 2, 2, rebookNow.Add(3*time.Hour)), rebookKeeper("hannah"))
		},
		"no keeper": func() *World {
			return rebookTestWorld(28, 11, rebookLodger("ezekiel", 10, 2, rebookNow.Add(3*time.Hour)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := build()
			sink := &recordingActionLogSink{}
			w.SetActionLogSink(sink)
			runRebook(t, w)
			if n := len(ringOfType(w, ActionTypeSettled)); n != 0 {
				t.Errorf("settled ring entries = %d, want 0", n)
			}
			if n := len(durableOfType(sink.rows, ActionTypeSettled)); n != 0 {
				t.Errorf("settled rows = %d, want 0", n)
			}
		})
	}
}
