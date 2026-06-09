package sim

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// speak_validation.go — ZBBS-WORK-323. The three speak-side prose-validation
// gates ported from v1 (engine/inventory.go + engine/speech_state_claim_gate.go),
// reworked as in-memory reads on the world goroutine. They defend against the
// narration-vs-mechanism desync: an NPC *speaking* a transaction/service into
// apparent existence ("here's your stew", "yes, you're booked") that no tool call
// actually performed, so the words misrepresent engine state to listeners.
//
// Gates run from sim.Speak's Fn (so they see live World state). Numbering is kept
// stable across the gate-1 removal below:
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
//  2. TRANSFER-VERB (v1 ZBBS-HOME-265, RELOCATED from act → speak per the WORK-323
//     option-B decision). Narrating a completed handover ("served stew to Ezekiel")
//     doesn't move items — speech can't perform a transfer. v1 gated this only on
//     the `act` verb, which is being dropped (ZBBS, 2026-05-25 — ~2% of speak,
//     headline use was fabrication), so its coverage relocates here rather than
//     vanishing.
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

// askShapeRegex matches speech that reads as a buyer-side request or a vendor
// decline rather than a completed-transfer claim — these skip the transfer-verb
// gate so request/decline dialog that names catalog items ("do you have bread?",
// "I'm out of ale") isn't treated as handover narration. Ported verbatim from v1
// (engine/inventory.go); WORK-230 originally added it to stop the now-removed
// (HOME-416) item-presence gate from rejecting vendors *asking* about goods.
var askShapeRegex = regexp.MustCompile(`(?i)(\?|\bdo you\b|\bhave you\b|\b(may|can|could)\s+i\b|\bi\s+(want|need)\b|\b(i'?d|i would)\s+(want|need|like|love|take|have|buy|get|prefer)\b|\bi'?ll\s+(take|have|buy|get|need|like)\b|\bi'?m\s+(looking|after|seeking)\b|\bi'?m\s+out\s+of\b|\bout\s+of\s+\w+\b|\bdon'?t\s+(have|carry|stock|sell)\b|\bran\s+out\b|\bno\s+more\s+\w+\b|\bi\s+haven'?t\s+(any|got)\b)`)

// transferVerbRegex matches past-tense transfer-implying verbs that, combined
// with an item mention, assert a completed handover speech can't perform. Item
// transfers must route through pay_with_item (or a buyer's pay). Conservative on
// purpose — ambiguous candidates ("offered" can be a verbal price; "poured" /
// "brought" can be intransitive movement) are excluded to avoid false rejections
// on flavor narration. Ported verbatim from v1 (engine/inventory.go
// actTransferVerbsRegex).
var transferVerbRegex = regexp.MustCompile(`(?i)\b(handed|gave|served|delivered|dished|ladled|doled)\b`)

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

// isAskShapeSpeech reports whether the speech reads as a buyer request or vendor
// decline rather than a possession/offer claim. Empty text returns false.
func isAskShapeSpeech(text string) bool {
	if text == "" {
		return false
	}
	return askShapeRegex.MatchString(text)
}

// extractItemMentions returns the distinct item-kind names that appear as whole
// words in text, matched case-insensitively against the loaded catalog
// (w.ItemKinds). Tokenization splits on any rune that isn't a letter, digit, or
// underscore, so "ale" matches "ale," and "ale-house" but NOT "sale" or "stale"
// — the v2 equivalent of v1's \b-anchored item-kind alternation regex, without a
// per-call regex compile. Returned sorted for deterministic reject messages.
func extractItemMentions(w *World, text string) []ItemKind {
	if w == nil || len(w.ItemKinds) == 0 || text == "" {
		return nil
	}
	byLower := make(map[string]ItemKind, len(w.ItemKinds))
	for kind := range w.ItemKinds {
		byLower[strings.ToLower(string(kind))] = kind
	}
	tokens := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
	})
	seen := make(map[ItemKind]struct{})
	var out []ItemKind
	for _, tok := range tokens {
		if kind, ok := byLower[tok]; ok {
			if _, dup := seen[kind]; !dup {
				seen[kind] = struct{}{}
				out = append(out, kind)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// validateSpeechClaims runs the three gates against text spoken by `speaker` and
// returns a reject message (surfaced to the model as a tool error so it retries)
// or "" to allow. MUST be called from inside sim.Speak's Fn — it reads live
// World state (Inventory, PayLedger, RoomAccess, huddle membership). The caller
// skips PCs and treats a non-empty return as the speech rejection.
//
// Fail-open by construction: every lookup that can't resolve a backing fact
// either returns a reject (state claims — an unbacked claim is the failure mode)
// or allows (item gates — a missing catalog / empty inventory means nothing to
// validate against). There is no transient-error path because all reads are
// in-memory on the serialized world goroutine.
func validateSpeechClaims(w *World, speaker *Actor, text string, now time.Time) string {
	// Gate 2 (transfer-verb) needs the item-mention scan. Ask-shape speech
	// (questions, declines) skips it — naming an item you're asking about isn't a
	// claim. The former gate 1 (item-presence) was removed in ZBBS-HOME-416 (see
	// the file header); integrity lives at the transfer commands, not here.
	if !isAskShapeSpeech(text) {
		mentions := extractItemMentions(w, text)
		if len(mentions) > 0 {
			// Gate 2: transfer-verb (relocated from act). Narrating a completed
			// handover via speech doesn't move items, even when the speaker holds
			// them.
			if verb := transferVerbRegex.FindString(text); verb != "" {
				names := make([]string, len(mentions))
				for i, m := range mentions {
					names[i] = string(m)
				}
				return fmt.Sprintf(
					"Your words (%q + %s) describe handing over an item, but speech alone doesn't move anything. To actually give or trade %s, use offer_trade (propose your goods for theirs); to buy, use pay_with_item; or let the buyer pay — then say it once the transfer is real.",
					strings.ToLower(verb), strings.Join(names, ", "), strings.Join(names, ", "),
				)
			}
		}
	}

	// Gate 3: transactional state-claim.
	return validateStateClaims(w, speaker, text, now)
}

// validateStateClaims is gate 3 — booking/payment completed-state assertions
// must be backed by a real grant for someone in the speaker's huddle. Returns a
// reject message or "".
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
