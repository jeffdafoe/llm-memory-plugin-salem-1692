package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestRenderQuoteWarrantLineEachBranchCarriesGuard exercises renderQuoteWarrantLine
// directly so EVERY take-line branch — bundle (len(Lines) > 1), single-item
// (len == 1), and the defensive zero-line arm — is proven to carry the
// anti-speak-first guard, independent of which branches the golden scenario
// matrix happens to render (LLM-457). The corpus invariant below only proves the
// guard survives the assembled prompt for the branches the matrix exercises;
// this proves each branch of the renderer itself.
func TestRenderQuoteWarrantLineEachBranchCarriesGuard(t *testing.T) {
	cases := []struct {
		name  string
		lines []sim.QuoteLine
	}{
		{"defensive_zero_lines", nil},
		{"single_item", []sim.QuoteLine{{ItemKind: "stew", Qty: 1}}},
		{"bundle_multi_line", []sim.QuoteLine{{ItemKind: "stew", Qty: 1}, {ItemKind: "bread", Qty: 2}}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			r := sim.SceneQuoteTargetedWarrantReason{
				QuoteID: sim.QuoteID(1),
				Lines:   tc.lines,
				Amount:  4,
			}
			got := renderQuoteWarrantLine(1, "John Ellis", r, false, "")
			// Sanity: a "" redundancy + non-empty amount must land on a take-line, not
			// a decline branch — otherwise the guard assertion below is vacuous.
			if !strings.Contains(got, "call pay_with_item") {
				t.Fatalf("branch %q rendered no take-line (no pay_with_item cue): %q", tc.name, got)
			}
			if !strings.Contains(got, "speaking ends your turn") {
				t.Errorf("branch %q omits the anti-speak-first guard (LLM-457):\n    %s", tc.name, got)
			}
		})
	}
}

// TestGoldensCoinQuoteTakeCarriesAntiSpeakGuard enforces LLM-457 at the assembled-
// prompt level: every rendered coin-quote take-line — the "call pay_with_item …
// it settles at once" cue a posted scene_quote puts a buyer on — must carry the
// critical guard clause ("speaking ends your turn"). Without it a buyer voices
// acceptance through the terminal speak tool, ends the turn without paying, and
// livelocks re-announcing the same deal (Nathaniel Cole ↔ John Ellis, porridge
// quote open ~13m, 2026-07-17).
//
// This checks only the load-bearing guard clause, NOT full wording parity with
// restock.go / the pay_with_item tool description — sharing the entire phrasing
// would make the test brittle. Branch completeness (bundle/single/defensive) is
// covered by the direct-renderer test above; this one guards the real Build →
// Render path for whatever quote scenarios the matrix exercises.
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
