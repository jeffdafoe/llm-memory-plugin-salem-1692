package sim

// text.go — the engine's one way to shorten model-facing free text (LLM-405).
//
// A bare prefix cut is a behavioral defect, not a cosmetic one: text that stops
// mid-clause reads as an UNFINISHED sentence, and a model handed an unfinished
// sentence does the socially obvious thing — it treats the dangling clause as
// something owed an answer. That is the LLM-396 Inn loop (a clipped utterance
// minted a fresh "you were about to say —?" every turn and the huddle could
// never terminate), and it is the same lie a clipped payment note or a clipped
// gift note tells more quietly.
//
// LLM-396 and LLM-400 removed the bare cuts on the two RENDER paths. The write
// paths kept theirs: NewSalientFact and truncatePayMessage both rune-sliced and
// returned the prefix, so the text an NPC would later read back out of memory was
// cut with nothing to say so. These live here now so the package has exactly one
// truncation idiom — the marking one — and the bare-prefix class cannot be
// reintroduced by copying the helper next door.

import "unicode/utf8"

// ElisionMarker terminates any free-text payload the engine had to shorten. It is
// the sole signal to a reading NPC that it is NOT seeing the whole line.
//
// Exported because the render path caps by BYTES (perception.capBytes, bounded by
// the prompt budget) while the write paths cap by RUNES (bounded by what a JSONB
// row should hold) — two different budgets that must nonetheless mark a cut the
// same way, or "is this text whole?" stops having one answer. perception aliases
// this rather than declaring its own.
const ElisionMarker = "…"

// capRunesMarked shortens s to at most maxRunes runes INCLUDING the marker, so a
// caller sizing a field to maxRunes still gets something that fits. It appends
// ElisionMarker whenever it cuts.
//
// The marker is inside the budget rather than added on top of it deliberately:
// every caller here is capping against a storage bound (a JSONB relationship row,
// a ledger column), and a helper that quietly returns maxRunes+1 runes would push
// the overflow onto whoever trusted the number.
func capRunesMarked(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	const markerRunes = 1 // ElisionMarker is a single rune (U+2026)
	if maxRunes <= markerRunes {
		// No room for any of the text — say "there was more" and nothing else,
		// which is still truer than a one-rune prefix.
		return ElisionMarker
	}
	return string([]rune(s)[:maxRunes-markerRunes]) + ElisionMarker
}
