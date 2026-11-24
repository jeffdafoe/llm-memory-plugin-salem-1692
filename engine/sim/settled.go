package sim

import (
	"fmt"
	"log"
	"time"
)

// settled.go — LLM-1018. A settlement the engine makes outside a turn writes its
// rows for the ACTIVE party (the carter who bought, the worker who was paid, the
// lodger who was charged). Both memory readers take only an actor's own entries:
// the day push (repo/pg loadDayEventsSQL) and the narrative consolidation
// (snapshotEventsForActor). So the PASSIVE party — the holder, the employer, the
// keeper — kept no memory of the deal past a one-turn warrant. RecordPassiveSettlement
// gives that party its own ring entry and durable row, ActionTypeSettled.
//
// Best effort, the same exposure as the active party's row: the durable append is
// asynchronous and a crash before the insert loses it. The row is a memory
// projection, not the record of the coin — the active party's row and its
// RecordCoinPaid call stay the only coin record, so nothing here touches it.

// SettlementKind names which engine settlement a `settled` row records.
type SettlementKind string

const (
	SettlementCarterBuy SettlementKind = "carter_buy" // a carter's buy leg took the holder's goods
	SettlementWage      SettlementKind = "wage"       // a labor contract completed and paid the worker
	SettlementRent      SettlementKind = "rent"       // the rebook sweep charged a lodger the night's rate
)

// PassiveSettlement is one passive party's side of a settlement.
type PassiveSettlement struct {
	PartyID          ActorID
	CounterpartyID   ActorID // a visitor id is dropped from the payload; it does not outlive the visit
	CounterpartyName string
	Kind             SettlementKind
	Amount           int    // coins that moved; 0 for a goods-only settlement
	Text             string // the party's pre-rendered line, second person
	Extra            map[string]any
	At               time.Time
}

// RecordPassiveSettlement appends the passive party's ring entry and durable
// `settled` row. Skipped for a party that is gone or is a visitor: a visitor's
// durable row has its actor id blanked (World.AppendActionLogDurable), so the
// row would belong to no one's day. MUST run on the world goroutine, after the
// coin and goods moved.
//
// No HuddleID on either write. The party may be in an unrelated conversation
// when a sweep settles, and a huddle id would put the line in that
// conversation's transcript and talk panel. loadDayEventsSQL skips `settled`
// (by type, with the other presence-less engine types) when it works out
// co-presence, so the row does not cut off the speech the party overhears.
func RecordPassiveSettlement(w *World, s PassiveSettlement) {
	if s.PartyID == "" || s.Text == "" || isVisitorParty(w, s.PartyID) {
		return
	}
	party := w.Actors[s.PartyID]
	if party == nil {
		return
	}
	if _, err := AppendActionLogEntry(ActionLogEntry{
		ActorID:          party.ID,
		OccurredAt:       s.At,
		ActionType:       ActionTypeSettled,
		Text:             s.Text,
		CounterpartyName: s.CounterpartyName,
		Amount:           s.Amount,
	}).Fn(w); err != nil {
		log.Printf("sim/settled: append %s for %s: %v", s.Kind, party.DisplayName, err)
	}
	// Extras first, so the fields this helper owns always win over a caller's
	// key of the same name.
	payload := map[string]any{}
	for k, v := range s.Extra {
		payload[k] = v
	}
	payload["settlement"] = string(s.Kind)
	payload["counterparty"] = s.CounterpartyName
	payload["amount"] = s.Amount
	payload["text"] = s.Text
	delete(payload, "counterparty_actor_id")
	if s.CounterpartyID != "" && !isVisitorParty(w, s.CounterpartyID) {
		payload["counterparty_actor_id"] = string(s.CounterpartyID)
	}
	w.AppendActionLogDurable(DurableActionLogRow{
		ActorID:     party.ID,
		OccurredAt:  s.At,
		ActionType:  ActionTypeSettled,
		Payload:     payload,
		SpeakerName: party.DisplayName,
		Source:      "engine",
	})
}

// isVisitorParty is World.AppendActionLogDurable's visitor test: a live actor
// with VisitorState, or an id in the visitor mint shape once the actor is gone.
func isVisitorParty(w *World, id ActorID) bool {
	if a := w.Actors[id]; a != nil && a.VisitorState != nil {
		return true
	}
	return IsVisitorActorID(id)
}

// CarterSettledNarration is the holder's line for a carter buy leg.
func CarterSettledNarration(carterName, lot string, price int) string {
	if carterName == "" {
		carterName = "The carter"
	}
	return fmt.Sprintf("%s bought %s from you for %s, to carry off in the cart.", carterName, lot, formatPayment(price, nil))
}

// WageSettledNarration is the employer's line for a completed job. Pronoun-free,
// like LaborSettledEmployerNarration: the engine does not know the worker's.
func WageSettledNarration(workerName string, reward int, rewardItems []ItemKindQty) string {
	if workerName == "" {
		workerName = "your hired worker"
	}
	return fmt.Sprintf("You paid %s %s for the work done for you, as agreed.", workerName, formatPayment(reward, rewardItems))
}

// RentSettledNarration is the keeper's line for a rebooked night.
func RentSettledNarration(lodgerName string, nightly int) string {
	if lodgerName == "" {
		lodgerName = "Your lodger"
	}
	return fmt.Sprintf("%s paid you %s for another night's lodging.", lodgerName, formatPayment(nightly, nil))
}
