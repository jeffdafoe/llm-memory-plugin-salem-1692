package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// turn_in_decode_test.go — LLM-506. An over-cap say is TRUNCATED, never
// rejected. The live failure (Silas Withrow, 2026-07-19): a 260-char goodnight
// bounced as malformed_args, sim.TurnIn never ran, and the model's recovery was
// to re-deliver the farewell via speak — terminal, tick over, actor still up —
// reconstructing the Long Goodnight loop turn_in exists to end. The say is
// garnish on a world-state act; only the garnish may be trimmed.

func decodeTurnInSay(t *testing.T, raw string) string {
	t.Helper()
	got, err := DecodeTurnInArgs(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("DecodeTurnInArgs(%q): %v", raw, err)
	}
	return got.(TurnInArgs).Say
}

// TestDecodeTurnInArgs_OverCapSayIsTruncatedNotRejected is the regression pin:
// the decode must SUCCEED and hand back a bounded say.
func TestDecodeTurnInArgs_OverCapSayIsTruncatedNotRejected(t *testing.T) {
	long := strings.Repeat("goodnight to you all ", 20) // 420 runes, word-separated
	raw, _ := json.Marshal(TurnInArgs{Say: long})
	say := decodeTurnInSay(t, string(raw))
	if n := utf8.RuneCountInString(say); n > MaxTurnInSayChars {
		t.Errorf("truncated say rune count = %d, want <= %d", n, MaxTurnInSayChars)
	}
	if say == "" {
		t.Error("truncation emptied the say — the goodnight should survive, shortened")
	}
	if strings.HasSuffix(say, " ") {
		t.Errorf("truncated say has trailing space: %q", say)
	}
}

// TestDecodeTurnInArgs_TruncationEndsOnAWholeWord — the cut lands on the word
// boundary before the cap, not mid-word.
func TestDecodeTurnInArgs_TruncationEndsOnAWholeWord(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("farewell ", 40)) // 359 runes
	raw, _ := json.Marshal(TurnInArgs{Say: long})
	say := decodeTurnInSay(t, string(raw))
	for _, w := range strings.Fields(say) {
		if w != "farewell" {
			t.Fatalf("truncation split a word: got fragment %q in %q", w, say)
		}
	}
}

// TestDecodeTurnInArgs_TruncationMultibyteSafe — rune truncation must not split
// a multibyte rune (the recall.go lesson).
func TestDecodeTurnInArgs_TruncationMultibyteSafe(t *testing.T) {
	long := strings.Repeat("か", MaxTurnInSayChars+50)
	raw, _ := json.Marshal(TurnInArgs{Say: long})
	say := decodeTurnInSay(t, string(raw))
	if !utf8.ValidString(say) {
		t.Error("truncation split a multibyte rune (invalid UTF-8)")
	}
	// One unbroken word: no boundary to trim back to, so the hard rune cut holds.
	if n := utf8.RuneCountInString(say); n != MaxTurnInSayChars {
		t.Errorf("unbroken-word say rune count = %d, want the hard cap %d", n, MaxTurnInSayChars)
	}
}

// TestDecodeTurnInArgs_TruncationDropsDanglingPunctuation — a cut that lands
// after "— " must not leave the dash dangling as the spoken line's last word.
func TestDecodeTurnInArgs_TruncationDropsDanglingPunctuation(t *testing.T) {
	filler := strings.Repeat("g", MaxTurnInSayChars-2)
	raw, _ := json.Marshal(TurnInArgs{Say: filler + " — and one more thing"})
	say := decodeTurnInSay(t, string(raw))
	if strings.ContainsAny(say[len(say)-1:], " ,;:—–-") {
		t.Errorf("truncated say ends in dangling punctuation: %q", say)
	}
}

// TestDecodeTurnInArgs_UnderCapSayUntouched — the cap only engages past 200
// runes; a normal goodnight passes through verbatim.
func TestDecodeTurnInArgs_UnderCapSayUntouched(t *testing.T) {
	const goodnight = "Goodnight, Hannah. God keep you till morning."
	raw, _ := json.Marshal(TurnInArgs{Say: goodnight})
	if say := decodeTurnInSay(t, string(raw)); say != goodnight {
		t.Errorf("under-cap say was altered: got %q, want %q", say, goodnight)
	}
}
