package perception

import (
	"fmt"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// coin_dealings.go — LLM-572. The "## Coin between you and those here" section:
// what money has actually passed between the subject and each acquaintance in the
// conversation, put in front of them in the scene where a money claim gets made.
//
// The trigger: Moses James told Constable Gideon Marsh he had paid him for help
// that never came, and Marsh — who had no per-peer view at all, being a stateful
// NPC — accepted the premise and handed back five coins. Moses had paid him two
// single coins of town rate, ever. The record was in Postgres and in front of
// neither man.
//
// This section is the twin of "## What you remember of those here" and deliberately
// its opposite. That one is JUDGMENT — one consolidated sentence per peer, an
// impression the LLM wrote. This one is RECORD: engine-authored, counted, with no
// free text of any kind in it. The two are meant to disagree sometimes. An NPC who
// feels cheated should go on feeling cheated; that is character, and LLM-499's
// ledger-authority instruction already tells consolidation which to believe. What
// it should not be able to do is invent the money.
//
// Note what is NOT here: the payer's stated purpose. See CoinPayment.

// CoinDealingsPeerView is one co-present peer's coin record, already reduced to
// counts and totals world-side (sim.CoinDealings). Render only picks phrasing.
type CoinDealingsPeerView struct {
	PeerID   sim.ActorID
	PeerName string
	Dealings sim.CoinDealings
}

// buildCoinDealings projects the per-pair coin record for the co-present peers the
// subject is acquainted with.
//
// Gated on ACQUAINTANCE, not merely co-presence. A stranger has no shared money
// history to dispute, so a "no coin has passed between you" line about one is pure
// prompt weight on every tick of every crowded scene — and unlike the relationship
// section, the zero case here is content rather than an empty view to skip. The
// acquaintance map is maintained for all NPC kinds, which is what lets this cover
// the stateful NPC the relationship section leaves out.
//
// TRAVELERS ARE EXCLUDED, on both sides, and this is a correctness gate rather than
// a cost one. A visitor's own payments reach agent_action_log with a BLANKED
// actor_id — the column FKs to actor(id) and a visitor lives in a separate table, so
// LLM-573 keeps the row for the dream pipeline but strips the id — and the coin
// record's boot seed cannot key an unattributed row to a pair. So after any restart
// a traveler pair reads "no coin has passed between you" when coin certainly did.
// Within a single boot the live tally is complete for them, but a cue that is
// truthful only until the next deploy is worse than one that stays quiet, and Salem
// deploys several times a day. Their ids are minted fresh per visit anyway, so a
// persistent pair record was never going to mean much.
//
// The gate is coupled to that id-blanking, and the coupling is pinned by
// cascade.TestHandlePaidActionLog_VisitorPaymentIsTalliedButNotAttributable. If
// visitor beats ever become attributable to a stable id, that test goes red and this
// gate is the thing to revisit.
//
// Capped at maxRenderedRelationshipPeers for the reason the relationship section is
// (LLM-322): the section is re-sent every tick, so a crowded huddle must not be
// able to balloon it. Peers the subject has actually exchanged coin with are kept
// ahead of ones with nothing on the record — the zero line is worth rendering, but
// not at the expense of a real one.
//
// Ordering is by PeerID, matching SurroundingsView.HuddleMembers, so a reader of
// both blocks sees the same peer order.
func buildCoinDealings(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot, members []HuddleMember, now time.Time) []CoinDealingsPeerView {
	if snap == nil || actorSnap == nil || len(members) == 0 {
		return nil
	}
	if actorSnap.VisitorState != nil {
		return nil // the subject is a traveler — see the doc comment
	}
	out := make([]CoinDealingsPeerView, 0, len(members))
	for _, m := range members {
		if m.ID == actorID || !m.Acquainted || m.Traveler || m.DisplayName == "" {
			continue
		}
		out = append(out, CoinDealingsPeerView{
			PeerID:   m.ID,
			PeerName: m.DisplayName,
			Dealings: snap.CoinDealingsFor(actorID, m.ID, now),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return capCoinDealingsPeers(out, maxRenderedRelationshipPeers)
}

// capCoinDealingsPeers trims to `limit`, preferring peers with coin on the record
// over peers with none, then falling back to PeerID for a stable choice. The kept
// set is returned in PeerID order so the block still matches "## Around you".
func capCoinDealingsPeers(views []CoinDealingsPeerView, limit int) []CoinDealingsPeerView {
	if len(views) <= limit {
		return views
	}
	withCoin := make([]CoinDealingsPeerView, 0, len(views))
	without := make([]CoinDealingsPeerView, 0, len(views))
	for _, v := range views {
		if v.Dealings.Any() {
			withCoin = append(withCoin, v)
			continue
		}
		without = append(without, v)
	}
	// Both halves arrive in PeerID order (members is PeerID-ordered and the loop
	// above preserves it), so taking a prefix of the concatenation is deterministic
	// without re-sorting; only the final PeerID re-order is needed.
	kept := append(withCoin, without...)[:limit]
	for i := 1; i < len(kept); i++ {
		for j := i; j > 0 && kept[j].PeerID < kept[j-1].PeerID; j-- {
			kept[j], kept[j-1] = kept[j-1], kept[j]
		}
	}
	return kept
}

// renderCoinDealings writes the "## Coin between you and those here" section.
// Content-gated: an empty view writes nothing.
//
// The window rides in the HEADER rather than on every line — with up to four peers,
// repeating "this past week" four times reads as a form rather than a recollection.
func renderCoinDealings(b *strings.Builder, peers []CoinDealingsPeerView) {
	if len(peers) == 0 {
		return
	}
	b.WriteString("## Coin between you and those here, this past week\n")
	// Peers with nothing on the record collapse into ONE closing line rather than a
	// line each. The empty record is the answer to a claim and has to be stated, but
	// stated four times in a crowded room it reads as a form being filled in rather
	// than a man recalling his dealings — and in most scenes every peer is on that
	// list, so this is what keeps the section to a single line almost always.
	var quiet []string
	for _, p := range peers {
		name := sanitizeInline(p.PeerName)
		if name == "" {
			name = string(p.PeerID)
		}
		if !p.Dealings.Any() {
			quiet = append(quiet, name)
			continue
		}
		fmt.Fprintf(b, "- %s\n", coinDealingsSentence(name, p.Dealings))
	}
	if len(quiet) > 0 {
		// "or", not joinNames' "and": the sentence already has an "and" in "between
		// you and …", and a second one reads as a single compound party ("between
		// you and Hannah and Silence") rather than as a negative covering each of
		// them separately.
		fmt.Fprintf(b, "- No coin has passed between you and %s.\n", joinNamesOr(quiet))
	}
	b.WriteString("\n")
}

// coinDealingsSentence voices one pair's record. Callers filter the empty case out
// into the collective line, so this only ever renders a pair with coin on it.
//
// No pronoun stands for the peer anywhere in it. The village does not model gender
// on actors, so "you have paid him back" would be a coin-flip on every line; the
// name or a bare direction carries it instead.
//
// A pair with a due on the record takes the two-sentence form below (LLM-607).
// A pair without takes the original wording unchanged, which is most of the village.
func coinDealingsSentence(name string, d sim.CoinDealings) string {
	received := coinFlowPhrase(d.ReceivedCount, d.ReceivedTotal, d.ReceivedAllSingle, d.ReceivedAtLeast)
	paid := coinFlowPhrase(d.PaidCount, d.PaidTotal, d.PaidAllSingle, d.PaidAtLeast)
	receivedDue := coinDueClause(d.ReceivedDueCount, d.ReceivedDueTotal, d.ReceivedCount)
	paidDue := coinDueClause(d.PaidDueCount, d.PaidDueTotal, d.PaidCount)
	if receivedDue != "" || paidDue != "" {
		return coinDealingsDueSentence(name, received, receivedDue, paid, paidDue, d)
	}
	switch {
	case d.ReceivedCount > 0 && d.PaidCount > 0:
		return fmt.Sprintf("%s has paid you %s, and you have paid back %s.", name, received, paid)
	case d.ReceivedCount > 0:
		return fmt.Sprintf("%s has paid you %s, and nothing has gone back the other way.", name, received)
	case d.PaidCount > 0:
		return fmt.Sprintf("You have paid %s %s, and nothing has come back the other way.", name, paid)
	default:
		return fmt.Sprintf("No coin has passed between you and %s.", name)
	}
}

// coinDealingsDueSentence voices a pair where at least one direction carries a due.
//
// One sentence per direction, rather than the single joined sentence the ordinary
// case uses. The joined form has to name the second direction as "you have paid
// BACK", and a levy is not a repayment — that phrasing casts the town rate as a
// debt the keeper owed the constable personally, which is a sharper version of the
// misreading this whole change exists to stop. Splitting also keeps the due clause
// attached to the direction it describes, where a mid-sentence em-dash aside would
// leave a reader to work out which half of the pair it qualified.
//
// The "nothing has come back the other way" tail is dropped whenever a due is
// present, and its absence is the point rather than an omission: for coin that was
// never going to come back, stating that nothing did is the exact inference the
// scene must not invite. The due clause says what the tail was there to say.
func coinDealingsDueSentence(name, received, receivedDue, paid, paidDue string, d sim.CoinDealings) string {
	var parts []string
	if d.ReceivedCount > 0 {
		parts = append(parts, fmt.Sprintf("%s has paid you %s%s.", name, received, receivedDue))
	}
	if d.PaidCount > 0 {
		parts = append(parts, fmt.Sprintf("You have paid %s %s%s.", name, paid, paidDue))
	}
	return strings.Join(parts, " ")
}

// coinDueClause qualifies one direction's flow with how much of it discharged a
// due. Empty when none of it did, which is what dispatches the ordinary wording.
//
// The closing is the load-bearing half and it says the two things the counts cannot:
// that the coin settled when it was handed over, and that no goods were ever owed
// against it. It deliberately echoes townRatePaidFactText's closing — the two are
// read by the same model, one as a recollection and one as a memory, and they should
// not disagree in wording about a thing neither is guessing at.
func coinDueClause(dueCount, dueTotal, count int) string {
	if dueCount <= 0 {
		return ""
	}
	const closing = " — settled as it was handed over, and no goods owed back"
	switch {
	case count == 1:
		// "all of it" about a single coin reads as a quantity being apportioned.
		return ", the town's due" + closing
	case dueCount >= count:
		return ", all of it the town's due" + closing
	default:
		return fmt.Sprintf(", %s of it the town's due%s", coinsOwedPhrase(dueTotal), closing)
	}
}

// coinFlowPhrase voices one direction's flow.
//
// A run of single coins reads as "a coin twice" rather than "2 coins across 2
// payments" — that is how a person recalls a recurring due, and the town rate that
// prompted this ticket is exactly such a run. Anything else states the total and,
// when it took more than one payment, how many.
//
// "at least" prefixes a direction whose count understates because the per-pair cap
// evicted entries. Rare to the point of theoretical at village scale, but the
// alternative is a silent undercount behind a sentence that reads as exact.
func coinFlowPhrase(count, total int, allSingle, atLeast bool) string {
	if count <= 0 {
		return ""
	}
	prefix := ""
	if atLeast {
		prefix = "at least "
	}
	switch {
	case count == 1:
		return prefix + coinsPhrase(total)
	case allSingle:
		return fmt.Sprintf("%sa coin %s", prefix, timesPhrase(count))
	default:
		return fmt.Sprintf("%s%s across %s", prefix, coinsPhrase(total), paymentsPhrase(count))
	}
}

// joinNamesOr is joinNames' disjunctive twin — "A", "A or B", "A, B, or C". The
// names arrive already sanitized by the caller.
func joinNamesOr(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " or " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", or " + names[len(names)-1]
	}
}

// timesPhrase voices how many times something happened. "twice" reads as speech in
// a way "2 times" does not; past two, the numeral is what a person would say.
func timesPhrase(n int) string {
	if n == 2 {
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}

// paymentsPhrase voices a payment count for the mixed-amount case.
func paymentsPhrase(n int) string {
	if n == 1 {
		return "1 payment"
	}
	return fmt.Sprintf("%d payments", n)
}
