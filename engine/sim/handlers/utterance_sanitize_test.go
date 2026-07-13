package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// utterance_sanitize_test.go — LLM-235. Coverage for the mojibake guard:
// the indexCorruptSpeechRune detector, the checkUtteranceText wrapper, and
// the two live entry points (speak.text via HandleSpeak, a folded `say`
// via DecodeDeclinePayArgs). The rest of the say-path sites call the same
// checkUtteranceText, so the detector table + wrapper test carry them.

func TestIndexCorruptSpeechRune(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		wantOffset int    // -1 when clean
		wantReason string // substring the reason must contain (ignored when clean)
	}{
		// --- clean: must pass untouched --------------------------------
		{"empty", "", -1, ""},
		{"plain_english", "Hello there.", -1, ""},
		// The correct form of the live corrupted line — the guard must NOT
		// bounce the version the model was supposed to send.
		{"clean_contraction", "I'm peckish now. I want to buy more porridge.", -1, ""},
		{"accented_latin", "café crêpes naïve résumé", -1, ""},
		{"accented_caps", "ÀÈÌ Björk", -1, ""},
		{"digits_and_emoji", "I have 3 apples 🍎 and 2 pears", -1, ""},
		// A wholly-foreign word is a legitimate foreign word, not mojibake.
		{"wholly_cyrillic_word", "Привет", -1, ""},
		// Foreign word beside an English word (space-separated, not spliced).
		{"cyrillic_then_latin", "Привет friend", -1, ""},
		{"wholly_cjk", "日本語", -1, ""},

		// --- rule 1: U+FFFD replacement character -----------------------
		{"fffd_mid", "huh�?", 3, "corrupted"},
		{"fffd_start", "�hello", 0, "corrupted"},
		// The verbatim ticket case: "I�ге peckish now." — the FFFD sits
		// one byte in (after ASCII 'I'), so it is caught before the Cyrillic.
		{"ticket_case", "I�ге peckish now.", 1, "corrupted"},

		// --- rule 2: Latin/non-Latin splice within one word ------------
		// "Iге" — Latin I with Cyrillic ге glued on (the ticket's residue
		// once the FFFD is set aside).
		{"latin_cyrillic_splice", "Iге", 0, "mixed alphabets"},
		// "pоrridge" — a Cyrillic о (U+043E) hidden inside a Latin word.
		{"cyrillic_o_in_latin", "pоrridge", 0, "mixed alphabets"},
		// Splice mid-sentence — offset points at the start of the bad word.
		{"splice_midsentence", "buy pоrridge now", 4, "mixed alphabets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOffset, gotReason := indexCorruptSpeechRune(tc.text)
			if gotOffset != tc.wantOffset {
				t.Errorf("offset = %d, want %d (reason %q)", gotOffset, tc.wantOffset, gotReason)
			}
			if tc.wantOffset >= 0 && !strings.Contains(gotReason, tc.wantReason) {
				t.Errorf("reason = %q, want substring %q", gotReason, tc.wantReason)
			}
			if tc.wantOffset < 0 && gotReason != "" {
				t.Errorf("clean text got non-empty reason %q", gotReason)
			}
		})
	}
}

// TestIndexCorruptSpeechRune_InvalidUTF8 documents why this guard is separate
// from the generic control-char helper: a raw invalid UTF-8 byte is decoded by
// range as utf8.RuneError (== U+FFFD), so rule 1 catches it as corruption even
// though it was never literally encoded as EF BF BD. The distinction does not
// matter for model feedback.
func TestIndexCorruptSpeechRune_InvalidUTF8(t *testing.T) {
	gotOffset, gotReason := indexCorruptSpeechRune(string([]byte{'h', 0xff, 'i'}))
	if gotOffset != 1 || !strings.Contains(gotReason, "corrupted") {
		t.Fatalf("offset/reason = %d/%q, want 1/contains %q", gotOffset, gotReason, "corrupted")
	}
}

// TestCheckUtteranceText verifies the wrapper scopes the error to the tool
// and field name and steers the model toward a retry, and that it never
// echoes the offending text back (fail-closed).
func TestCheckUtteranceText(t *testing.T) {
	if err := checkUtteranceText("speak", "text", "all clean here"); err != nil {
		t.Fatalf("clean text errored: %v", err)
	}
	err := checkUtteranceText("pay_with_item", "say", "buy pоrridge")
	if err == nil {
		t.Fatal("want error for mojibake say, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"pay_with_item", "say", "say it again in plain words"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing substring %q", msg, want)
		}
	}
	// Fail-closed: the garbled token must not be echoed into the transcript.
	if strings.Contains(msg, "pоrridge") {
		t.Errorf("error echoed the offending text: %q", msg)
	}
}

// TestHandleSpeak_RejectsMojibake exercises the speak path end to end — the
// ticket's primary target. handleSpeakInput lives in speak_test.go.
func TestHandleSpeak_RejectsMojibake(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"replacement_char", "I�ге peckish now.", "corrupted"},
		{"mixed_script_word", "I want pоrridge please", "mixed alphabets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := handleSpeakInput(t, tc.text)
			if err == nil {
				t.Fatal("HandleSpeak: want error for mojibake, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q lacks %q", err, tc.want)
			}
		})
	}
}

// TestHandleSpeak_AllowsCleanContraction guards against a false positive on
// the correct form of the live line — the apostrophe breaks "I'm" into two
// Latin runs, so nothing is flagged.
func TestHandleSpeak_AllowsCleanContraction(t *testing.T) {
	if _, err := handleSpeakInput(t, "I'm peckish now. I want to buy more porridge, Hannah."); err != nil {
		t.Errorf("HandleSpeak rejected clean line: %v", err)
	}
}

// TestDecodeDeclinePayArgs_RejectsMojibakeSay proves the guard fires through
// a real folded-say decoder (selectSayAlias) for both the canonical `say`
// and the legacy `reason` alias, each named in its own error.
func TestDecodeDeclinePayArgs_RejectsMojibakeSay(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"say", `{"ledger_id":42,"say":"too pоrridge low"}`, "say"},
		{"reason_alias", `{"ledger_id":42,"reason":"I�ге decline"}`, "reason"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeDeclinePayArgs(json.RawMessage(tc.raw))
			if err == nil {
				t.Fatal("want error for mojibake say, got nil")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.want) || !strings.Contains(msg, "plain words") {
				t.Errorf("error %q missing field %q or retry cue", msg, tc.want)
			}
		})
	}
}
