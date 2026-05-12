package main

// State-claim validation for agent speech (ZBBS-HOME-270).
//
// Extends the prose-validation architecture WORK-227 / HOME-265 built
// for item-presence and item-transfer claims, to a third class:
// **transactional-state claims**. Speech that asserts a ledger-backed
// state ("you are booked", "your room is ready", "welcome, lodger",
// "you've paid me") must be backed by a real pay_ledger / room_access
// row, otherwise the speech is a conversational hallucination that
// misrepresents engine state to the listener.
//
// Observed 2026-05-12 01:11-01:12 UTC, action_log 3973-3979. John Ellis
// quoted Jefferey nights_stay@4 coins, the PC client's pay dialog was
// broken (Bug A — separate ticket), Jefferey replied "yes" without
// triggering a pay action. When Jefferey then asked "am I booked for
// a room tonight?", John's LLM responded "Yes, you are booked for a
// room tonight" despite zero pay_ledger rows existing. Pure
// conversational hallucination — same shape as Hannah's bread-and-
// water serve narration that WORK-227 closed, but for STATE rather
// than INVENTORY.
//
// Gate shape: regex match on a narrow vocabulary of second-person
// state-claim phrases, then per-pattern DB lookup against pay_ledger.
// Speaker is the seller in the claim; addressee is whoever else is
// in the speaker's huddle (the conversational referent). Passes if
// ANY huddle peer has a backing ledger row — name-strict matching
// would over-reject (e.g. "you both are booked" addressed to two
// peers).
//
// False-positive guard. Patterns are intentionally narrow — require
// fully-committed state phrasing ("your room IS ready", not "your
// room WILL be ready"; "you ARE booked", not "I CAN book you").
// Rejection feedback is educational so the LLM retries with corrected
// phrasing instead of getting stuck.
//
// PC speech is NOT gated — players can roleplay state assertions
// without engine-side validation. Bug C is specifically about NPC-
// keeper LLM hallucination of transactional state. The gate fires
// only in the agent_tick.go speak and act paths.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// stateClaimKind tags which DB lookup applies to a matched phrase.
type stateClaimKind int

const (
	claimBooking stateClaimKind = iota // requires accepted+delivered nights_stay ledger
	claimPayment                       // requires accepted pay_ledger in last 5 minutes
)

// stateClaimPattern pairs a compiled regex with its lookup kind. All
// patterns are anchored with \b word boundaries so partial substring
// matches (e.g. "rebooked", "unsettled") don't trigger.
type stateClaimPattern struct {
	re   *regexp.Regexp
	kind stateClaimKind
}

// stateClaimPatterns is the v1 vocabulary. Compiled once at package
// init since item_kind catalogs (the WORK-227 prior art) demonstrated
// the cost benefit. Add patterns conservatively — every addition
// expands the false-positive surface for legitimate speech.
//
// Booking patterns require second-person framing ("you are X", "your
// room is Y", "welcome, lodger"). "Room available" / "I can offer a
// room" / "would you like to book" intentionally don't match — those
// are offers, not state claims.
//
// Payment patterns require second-person + completed phrasing
// ("you've paid", "you have settled"). "Pay me four coins" / "that
// will be four coins" don't match.
var stateClaimPatterns = []stateClaimPattern{
	// Booking confirmations.
	{regexp.MustCompile(`(?i)\byou (?:are|'re|have been) (?:booked|checked in)\b`), claimBooking},
	{regexp.MustCompile(`(?i)\byour room is (?:ready|prepared|set)\b`), claimBooking},
	{regexp.MustCompile(`(?i)\bwelcome,? (?:my )?(?:lodger|boarder|guest of the (?:inn|tavern))\b`), claimBooking},
	// Payment confirmations. Restricted to explicit second-person
	// completed-action phrasing; generic "the deal is done" was
	// considered but rejected on first-pass false-positive risk
	// (could be third-person about a separate transaction).
	{regexp.MustCompile(`(?i)\byou(?:'ve| have) paid (?:me|us|in full)\b`), claimPayment},
	{regexp.MustCompile(`(?i)\byou(?:'ve| have) settled (?:up|your bill|your account|in full)\b`), claimPayment},
}

// validateSpeechStateClaims scans the text for state-claim phrases.
// Returns:
//   - ("", nil) when no claim matches or every matched claim has a
//     backing ledger row in the speaker's current huddle.
//   - (rejectMessage, nil) when one or more claims lack backing. The
//     message names the offending phrase and points the LLM toward
//     the corrective action (wait for the pay, etc.).
//   - ("", err) for DB / catalog errors. Caller should fail open
//     (let the speech through rather than blocking on transient DB
//     failures), matching the WORK-227 implicit-mention gate's
//     fail-open semantics.
//
// "speakerID" is the actor.id of the LLM agent emitting the speech.
// The function reads the speaker's current_huddle_id transactionally
// to find the peer set; if the speaker is huddleless, any state
// claim is unbacked by definition (no one to be claiming about) and
// the function returns a rejection without touching pay_ledger.
func (app *App) validateSpeechStateClaims(ctx context.Context, speakerID, text string) (string, error) {
	if text == "" {
		return "", nil
	}
	// Find which patterns match. A single speech can hit multiple
	// patterns ("welcome, lodger! you've paid in full!") — we want
	// to validate each kind once even if matched multiple times.
	var matchedKinds map[stateClaimKind]string
	for _, p := range stateClaimPatterns {
		if m := p.re.FindString(text); m != "" {
			if matchedKinds == nil {
				matchedKinds = make(map[stateClaimKind]string, 2)
			}
			if _, dup := matchedKinds[p.kind]; !dup {
				matchedKinds[p.kind] = m
			}
		}
	}
	if len(matchedKinds) == 0 {
		return "", nil
	}

	// Speaker huddle. The gate scopes to current_huddle_id — only
	// peers in the SAME conversational unit count as referents. A
	// speaker outside any huddle making a state claim is by
	// definition addressing no one; reject without DB lookup.
	var speakerHuddle string
	err := app.DB.QueryRow(ctx,
		`SELECT COALESCE(current_huddle_id::text, '') FROM actor WHERE id = $1::uuid`,
		speakerID,
	).Scan(&speakerHuddle)
	if err != nil {
		return "", fmt.Errorf("lookup speaker huddle: %w", err)
	}
	if speakerHuddle == "" {
		quoted := firstMatchedPhrase(matchedKinds)
		return fmt.Sprintf("You said %q but you're not in a conversation with anyone — state claims need a listener. Re-check who is present before speaking.", quoted), nil
	}

	for kind, phrase := range matchedKinds {
		var backed bool
		switch kind {
		case claimBooking:
			// Require an accepted+delivered nights_stay ledger row
			// speaker→any-huddle-peer, ready_by ≤ today (i.e. the
			// stay's check-in date has arrived; future bookings
			// don't count as "you ARE booked" for tonight).
			err := app.DB.QueryRow(ctx,
				`SELECT EXISTS (
				   SELECT 1 FROM pay_ledger pl
				    WHERE pl.seller_id = $1::uuid
				      AND pl.buyer_id IN (
				          SELECT id FROM actor
				           WHERE current_huddle_id::text = $2
				             AND id != $1::uuid
				      )
				      AND pl.item_kind = 'nights_stay'
				      AND pl.state = 'accepted'
				      AND pl.fulfillment_status = 'delivered'
				      AND pl.ready_by <= CURRENT_DATE
				 )`,
				speakerID, speakerHuddle,
			).Scan(&backed)
			if err != nil {
				return "", fmt.Errorf("query booking ledger: %w", err)
			}
			if !backed {
				return fmt.Sprintf("You said %q but no one in your conversation has a current accepted nights_stay booking with you. If they intend to book, wait for them to pay — don't confirm the booking before the ledger reflects it.", phrase), nil
			}
		case claimPayment:
			// Require an accepted pay_ledger row speaker(seller)→
			// any-huddle-peer(buyer) within the last 5 minutes. The
			// recency window captures "you've paid me" as a fresh-
			// transaction acknowledgment rather than a historical
			// state claim. Any item_kind, including pure coin
			// transfers (item_kind IS NULL).
			err := app.DB.QueryRow(ctx,
				`SELECT EXISTS (
				   SELECT 1 FROM pay_ledger pl
				    WHERE pl.seller_id = $1::uuid
				      AND pl.buyer_id IN (
				          SELECT id FROM actor
				           WHERE current_huddle_id::text = $2
				             AND id != $1::uuid
				      )
				      AND pl.state = 'accepted'
				      AND pl.resolved_at > NOW() - INTERVAL '5 minutes'
				 )`,
				speakerID, speakerHuddle,
			).Scan(&backed)
			if err != nil {
				return "", fmt.Errorf("query payment ledger: %w", err)
			}
			if !backed {
				return fmt.Sprintf("You said %q but no one in your conversation has paid you in the last 5 minutes. Wait for the pay to land before acknowledging it as completed.", phrase), nil
			}
		}
	}
	return "", nil
}

// firstMatchedPhrase returns one representative phrase for the error
// message. Map iteration order is unspecified in Go but for a single-
// key map the iteration deterministically yields that key; for
// multi-key maps the message uses whichever phrase iterates first,
// which is good enough — the rejection is educational regardless of
// which specific phrase is named.
func firstMatchedPhrase(matched map[stateClaimKind]string) string {
	for _, m := range matched {
		return m
	}
	return ""
}

// init validates the pattern catalog at engine startup. Panics on a
// malformed regex so a deploy with a broken pattern fails loudly
// instead of silently disabling speech validation. Mirrors WORK-227's
// item-kind regex compile-time check.
func init() {
	for i, p := range stateClaimPatterns {
		if p.re == nil {
			panic(fmt.Sprintf("speech_state_claim_gate: pattern %d has nil regex", i))
		}
		if strings.TrimSpace(p.re.String()) == "" {
			panic(fmt.Sprintf("speech_state_claim_gate: pattern %d has empty regex", i))
		}
	}
}
