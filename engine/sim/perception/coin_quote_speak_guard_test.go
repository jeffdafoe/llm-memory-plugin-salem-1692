package perception

import (
	"strings"
	"testing"
)

// TestGoldensCoinQuoteTakeCarriesAntiSpeakGuard enforces LLM-457: every rendered
// coin-quote take-line — the "call pay_with_item … it settles at once" cue a
// posted scene_quote puts a buyer on (renderQuoteWarrantLine, render.go) — must
// also carry the anti-speak-first guard. Without it a buyer voices acceptance
// through the terminal speak tool, ends the turn without paying, and livelocks
// re-announcing the same deal (Nathaniel Cole ↔ John Ellis, porridge quote open
// ~13m, 2026-07-17). The co-present buy cue (restock.go) and the pay_with_item
// tool description already carry the guard; this invariant keeps the coin-quote
// cue from drifting back out of parity with them.
func TestGoldensCoinQuoteTakeCarriesAntiSpeakGuard(t *testing.T) {
	var checked int
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			for _, line := range strings.Split(renderScenario(sc), "\n") {
				// Immediate-settle take-line signature: instructs pay_with_item AND
				// promises the quote settles this turn. This is specific to the
				// coin-quote take — restock's co-present offer says "accept or counter",
				// never "it settles at once" — so the check can't false-positive on the
				// walk-then-pay or offer cues.
				if !strings.Contains(line, "call pay_with_item") || !strings.Contains(line, "it settles at once") {
					continue
				}
				checked++
				if !strings.Contains(line, "speaking ends your turn") {
					t.Errorf("scenario %q: a coin-quote take-line instructs pay_with_item for an "+
						"immediate settle but omits the anti-speak-first guard — a buyer who voices "+
						"acceptance with the terminal speak tool ends the turn unpaid and livelocks "+
						"(LLM-457). Append the \"speaking ends your turn\" guard.\n    %s",
						sc.name, line)
				}
			}
		})
	}
	// Guard against the invariant going vacuous — a phrasing change to the take-line
	// ("it settles at once") would otherwise make every line stop matching and the
	// test would pass having asserted nothing. buyer_offered_quote_take_names_terms
	// renders exactly this cue, so the floor is met today.
	if checked == 0 {
		t.Fatal("invariant matched no coin-quote take-lines — renderQuoteWarrantLine phrasing " +
			"probably drifted (LLM-457); update the signature or the matrix lost its quote scenario")
	}
}
