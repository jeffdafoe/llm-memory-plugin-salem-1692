package handlers

import "unicode"

// utterance_sanitize.go — LLM-235. A deterministic guard that keeps
// corrupt (mojibake) model output out of the world. Weak models
// (llama-3.3-70b) occasionally emit a token-level glitch inside spoken
// free text — the live case was
//
//	speak {"text": "I�ге peckish now. I want to buy more porridge..."}
//
// where a U+FFFD replacement character and a Cyrillic "ге" landed where
// "'m" belonged. Passed through verbatim it sits in RecentUtterances /
// chat history for every villager (and player) to read.
//
// The guard runs at the same static-validation layer as the trim /
// control-char / rune-cap checks, on every free-text field that lands on
// the world utterance path: speak.text and the folded `say` on the
// transactional verbs (pay_with_item / accept_pay / decline_pay /
// counter_pay / sell / offer_work / accept_work / decline_work). A hit
// rejects the tool call with a model-legible reason so the model retries
// the line — preferred over silent stripping, which would leave broken
// grammar ("I peckish").
//
// Detection is deliberately conservative — it targets corruption
// signatures, never legitimate foreign words:
//
//  1. U+FFFD REPLACEMENT CHARACTER anywhere. In an NPC utterance there is
//     no legitimate use for it; it is always a decode/token glitch. This
//     is a deliberate departure from indexInvalidControlChar (speak.go),
//     which allows U+FFFD as a printable code point — that helper is a
//     generic control-char scan reused for identifier inputs, and speech
//     content is held to the stricter bar LLM-235 asks for.
//  2. Mixed-script WORD — a maximal run of letters that contains BOTH a
//     Latin letter and a letter from another script (the Cyrillic "ге"
//     spliced into "Iге", a Cyrillic "о" hidden inside "pоrridge"). A run
//     that is wholly non-Latin — a genuine foreign word — is NOT flagged.
//     Anchoring on "Latin mixed with something else" (rather than "two or
//     more scripts") keeps a wholly-Japanese or wholly-Cyrillic line
//     passable while still catching the splice, which is the only shape
//     the glitch takes in an otherwise-English village.
//
// The reason strings never echo the offending text back (fail-closed, like
// the LLM-221 decode classifier) — only the corruption category and the
// byte offset, so no model-supplied content re-enters the transcript.

// indexCorruptSpeechRune scans utterance text for the two corruption
// signatures above and returns the byte offset of the first offending rune
// together with a short model-legible reason, or (-1, "") when the text is
// clean.
func indexCorruptSpeechRune(text string) (int, string) {
	// Current letter-run bookkeeping for the mixed-script rule. wordStart is
	// the byte offset where the run began; the flags record whether the run
	// has yet seen a Latin letter and a non-Latin letter.
	wordStart := -1
	sawLatin := false
	sawOther := false
	for i, r := range text {
		// Rule 1. A genuine U+FFFD (bytes EF BF BD) and the utf8.RuneError
		// that range yields for an invalid UTF-8 byte are the same rune value
		// here — both are corruption, so one test covers both.
		if r == '�' {
			return i, "a corrupted character (U+FFFD)"
		}
		if !unicode.IsLetter(r) {
			// Any non-letter (space, punctuation, digit, apostrophe, emoji)
			// ends the current word run. Accented Latin stays a Latin letter;
			// a combining mark is a non-letter and simply closes the run
			// without flagging — both fine.
			wordStart = -1
			sawLatin = false
			sawOther = false
			continue
		}
		if wordStart < 0 {
			wordStart = i
		}
		if unicode.Is(unicode.Latin, r) {
			sawLatin = true
		} else {
			sawOther = true
		}
		if sawLatin && sawOther {
			// Report the start of the word (not the interior switch rune) so a
			// human eyeballing the offset sees the whole garbled token.
			return wordStart, "a garbled word (letters from mixed alphabets)"
		}
	}
	return -1, ""
}

// checkUtteranceText runs indexCorruptSpeechRune over one free-text field
// and, on a hit, returns a modelSafeError naming the tool and field so a
// weak model can map the correction back to what it sent. Returns nil for
// clean (or empty) text. Callers add this beside the existing rune-cap
// check on every say-path field.
func checkUtteranceText(toolName, fieldName, text string) error {
	if i, reason := indexCorruptSpeechRune(text); i >= 0 {
		return modelSafef(
			"%s: %s contains %s at byte offset %d — say it again in plain words",
			toolName, fieldName, reason, i)
	}
	return nil
}
