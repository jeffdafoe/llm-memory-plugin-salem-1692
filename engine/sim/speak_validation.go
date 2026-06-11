package sim

import (
	"fmt"
	"regexp"
	"time"
)

// speak_validation.go — ZBBS-WORK-323. The speak-side prose-validation gate
// ported from v1 (engine/speech_state_claim_gate.go), reworked as in-memory
// reads on the world goroutine. It defends against the narration-vs-mechanism
// desync: an NPC *speaking* a transaction/service into apparent existence
// ("yes, you're booked") that no tool call actually performed, so the words
// misrepresent engine state to listeners.
//
// The gate runs from sim.Speak's Fn (so it sees live World state). Numbering is
// kept stable across the gate-1 and gate-2 removals below:
//
//  1. ITEM-PRESENCE — REMOVED in ZBBS-HOME-416. It scanned speech for item-kind
//     names and rejected any the speaker didn't hold. The scan was purely lexical
//     (no speech-act awareness), so it punished legitimate buyer / procurement /
//     disclaimer speech ("I shall buy the ale", "I must restock on meat", "I have
//     no meat to sell, I'm but a blacksmith") far more than it caught seller lies
//     — across the 2026-06-05..08 corpus its true-positive rate was ~0. Economic
//     integrity (you cannot transfer stock you don't hold) is enforced at the
//     transfer commands (pay_ledger / pay_with_item), not here; the gate only
//     governed chat accuracy, which the perception's "You are carrying:" line owns.
//  2. TRANSFER-VERB — REMOVED in ZBBS-WORK-397 (was v1 ZBBS-HOME-265, relocated
//     act → speak per the WORK-323 option-B decision). It rejected past-tense
//     handover narration ("served stew to Ezekiel") that named a catalog item.
//     The HOME-420 full-corpus read (2026-06-10) found ZERO fires across the
//     gate's entire production life (Feb→Jun transcript store + all of
//     virtual_agent_calls), zero true positives in a raw trigger-surface scan
//     of every speak call ever emitted (the one genuinely eligible line was a
//     buyer-side false positive — the HOME-420 class the gate existed to risk),
//     and a structural blind spot: ~66% of speech contains "?" and skipped the
//     gate wholesale via the ask-shape exemption. Decorative coverage plus a
//     real false-positive class. Economic integrity lives at the transfer
//     commands; fabricated-handover pressure died with the prompt improvements
//     (carrying-line, seller cues, act-then-speak) — item-talk rejections fell
//     1,421→40/day across 06-05→06-09 before removal.
//  3. STATE-CLAIM (v1 ZBBS-HOME-270) — reject *completed* second-person booking
//     ("you are booked", "your room is ready", "welcome, lodger") and payment
//     ("you've paid me", "you've settled up") claims that lack a backing
//     RoomAccess / pay_ledger row for a huddle peer. The motivating incident:
//     John Ellis told Jefferey "you're booked" with zero ledger rows.
//
// PC speech is NOT gated (only NPC-keeper LLM hallucination is the target) and
// the caller fails open — these are advisory rejections surfaced to the model so
// it retries with corrected phrasing, never a hard engine fault.

// recentPaymentWindow bounds the payment state-claim: "you've paid me" is only
// backed by an accepted pay_ledger entry resolved within this window — a fresh-
// transaction acknowledgment, not an open-ended historical claim. Matches v1's
// 5-minute window.
const recentPaymentWindow = 5 * time.Minute

// speechClaimKind tags which backing lookup a matched state-claim phrase needs.
type speechClaimKind int

const (
	claimBooking speechClaimKind = iota // requires an active lodging grant for a huddle peer
	claimPayment                        // requires an accepted pay_ledger to a huddle peer < 5 min old
)

// stateClaimPattern pairs a compiled regex with its lookup kind. All patterns
// are \b-anchored so partial substrings ("rebooked", "unsettled") don't trigger,
// and require fully-committed second-person phrasing ("your room IS ready", not
// "your room WILL be ready"; "you ARE booked", not "I CAN book you") so offers
// aren't mistaken for state claims. Ported verbatim from v1
// (engine/speech_state_claim_gate.go).
type stateClaimPattern struct {
	re   *regexp.Regexp
	kind speechClaimKind
}

var stateClaimPatterns = []stateClaimPattern{
	{regexp.MustCompile(`(?i)\byou (?:are|'re|have been) (?:booked|checked in)\b`), claimBooking},
	{regexp.MustCompile(`(?i)\byour room is (?:ready|prepared|set)\b`), claimBooking},
	{regexp.MustCompile(`(?i)\bwelcome,? (?:my )?(?:lodger|boarder|guest of the (?:inn|tavern))\b`), claimBooking},
	{regexp.MustCompile(`(?i)\byou(?:'ve| have) paid (?:me|us|in full)\b`), claimPayment},
	{regexp.MustCompile(`(?i)\byou(?:'ve| have) settled (?:up|your bill|your account|in full)\b`), claimPayment},
}

// validateStateClaims is gate 3 (the sole surviving gate — see the file header
// numbering) — booking/payment completed-state assertions must be backed by a
// real grant for someone in the speaker's huddle. Returns a reject message
// (surfaced to the model as a tool error so it retries with corrected phrasing)
// or "" to allow. MUST be called from inside sim.Speak's Fn — it reads live
// World state (PayLedger, RoomAccess, huddle membership). The caller skips PCs
// and treats a non-empty return as the speech rejection. An unbacked claim is
// the failure mode, so unresolvable lookups reject; there is no transient-error
// path because all reads are in-memory on the serialized world goroutine.
func validateStateClaims(w *World, speaker *Actor, text string, now time.Time) string {
	var matched map[speechClaimKind]string
	for _, p := range stateClaimPatterns {
		if m := p.re.FindString(text); m != "" {
			if matched == nil {
				matched = make(map[speechClaimKind]string, 2)
			}
			if _, dup := matched[p.kind]; !dup {
				matched[p.kind] = m
			}
		}
	}
	if len(matched) == 0 {
		return ""
	}

	// A state claim needs a listener. A huddleless speaker is addressing no one,
	// so any such claim is unbacked by definition.
	huddleID := speaker.CurrentHuddleID
	if huddleID == "" {
		return fmt.Sprintf(
			"You said %q but you're not in a conversation with anyone — state claims need a listener. Re-check who is present before speaking.",
			firstClaimPhrase(matched),
		)
	}
	members := w.actorsByHuddle[huddleID]

	for kind, phrase := range matched {
		switch kind {
		case claimBooking:
			// Backed when any huddle peer holds an active lodging grant at the
			// speaker's workplace (the inn they keep). Uses the canonical
			// actorIsLodgerAt predicate — the same "does this actor lodge here"
			// the sleep machine + perception lodging views key off, so the gate
			// can't diverge from real occupancy. (v1 queried an accepted+
			// delivered nights_stay ledger with ready_by<=today; v2's active
			// ledger RoomAccess grant — stamped on deliver_order — is the
			// equivalent post-payment truth.)
			backed := false
			for pid := range members {
				peer := w.Actors[pid]
				// Re-verify the peer is still actually in this huddle —
				// actorsByHuddle is the index, but a stale entry shouldn't let a
				// non-listener back the claim (the gate scopes "you" to current
				// peers). (code_review)
				if peer == nil || peer.ID == speaker.ID || peer.CurrentHuddleID != huddleID {
					continue
				}
				if actorIsLodgerAt(w, peer, speaker.WorkStructureID, now) {
					backed = true
					break
				}
			}
			if !backed {
				return fmt.Sprintf(
					"You said %q but no one in your conversation holds a current booking with you. If they mean to book, wait for them to pay — don't confirm the booking before the room is actually granted.",
					phrase,
				)
			}
		case claimPayment:
			// Backed when an accepted pay_ledger entry seller=speaker → a huddle
			// peer resolved within recentPaymentWindow. Any item_kind (covers
			// coin-only and item sales).
			backed := false
			for _, e := range w.PayLedger {
				if e == nil || e.State != PayLedgerStateAccepted || e.ResolvedAt.IsZero() {
					continue
				}
				if e.SellerID != speaker.ID {
					continue
				}
				// Reject both too-old AND future-dated resolutions: a negative
				// age (ResolvedAt after now) must not back "you've paid me".
				// (code_review)
				age := now.Sub(e.ResolvedAt)
				if age < 0 || age > recentPaymentWindow {
					continue
				}
				if _, isPeer := members[e.BuyerID]; !isPeer || e.BuyerID == speaker.ID {
					continue
				}
				// Re-verify the buyer is still in this huddle (index may be
				// stale — same scoping concern as the booking arm). (code_review)
				if buyer := w.Actors[e.BuyerID]; buyer != nil && buyer.CurrentHuddleID == huddleID {
					backed = true
					break
				}
			}
			if !backed {
				return fmt.Sprintf(
					"You said %q but no one in your conversation has paid you in the last 5 minutes. Wait for the pay to land before acknowledging it as completed.",
					phrase,
				)
			}
		}
	}
	return ""
}

// firstClaimPhrase returns one representative matched phrase for the huddleless
// reject message. Map order is unspecified but for the single-key case it's
// deterministic, and for multi-key the message is educational regardless of
// which phrase it names.
func firstClaimPhrase(matched map[speechClaimKind]string) string {
	for _, m := range matched {
		return m
	}
	return ""
}
