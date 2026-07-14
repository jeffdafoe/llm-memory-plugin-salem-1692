package sim

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// factAt is a fixed timestamp — nothing here depends on the clock.
func factAt() time.Time { return time.Date(1692, 7, 14, 9, 0, 0, 0, time.UTC) }

// text_internal_test.go — LLM-405. The write-path counterpart to the render-path
// invariant in perception/golden_test.go (TestGoldensWarrantTextCompleteOrMarked):
// text the engine SHORTENS BEFORE STORING is complete, or marked — never a bare
// mid-word prefix. Internal because all three cut sites are unexported.

func TestCapRunesMarked_MarksAndStaysInBudget(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		maxRunes int
		want     string
	}{
		{"under budget is untouched", "hello", 10, "hello"},
		{"exactly at budget is untouched", "hello", 5, "hello"},
		{"over budget is cut and marked", "hello world", 8, "hello w" + ElisionMarker},
		// The cap is a STORAGE bound (a JSONB row, a ledger column). A marker added
		// on top of the budget rather than inside it would hand the overflow to
		// whoever sized the field to maxRunes.
		{"marker is inside the budget, not on top of it", strings.Repeat("a", 50), 10, strings.Repeat("a", 9) + ElisionMarker},
		{"no room for text — marker alone", "hello", 1, ElisionMarker},
		{"zero budget", "hello", 0, ""},
		{"negative budget", "hello", -1, ""},
		// Multi-byte input must be cut on a rune boundary, and counted in runes.
		{"multibyte cut on a rune boundary", "héllo wörld", 8, "héllo w" + ElisionMarker},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := capRunesMarked(tc.in, tc.maxRunes)
			if got != tc.want {
				t.Errorf("capRunesMarked(%q, %d) = %q, want %q", tc.in, tc.maxRunes, got, tc.want)
			}
			if n := utf8.RuneCountInString(got); tc.maxRunes > 0 && n > tc.maxRunes {
				t.Errorf("capRunesMarked(%q, %d) returned %d runes — over the budget it was given", tc.in, tc.maxRunes, n)
			}
			if !utf8.ValidString(got) {
				t.Errorf("capRunesMarked(%q, %d) = %q — cut a rune in half", tc.in, tc.maxRunes, got)
			}
		})
	}
}

// TestNewSalientFact_CompleteOrMarked is the write-path invariant. A stored fact
// is read straight back into a prompt, so a fact that ends mid-word is a MEMORY
// that stops mid-sentence — and the reader answers the dangling clause as though
// it were owed something. That is the LLM-396 loop, arriving a day later out of
// the relationship row instead of live off a speech warrant.
func TestNewSalientFact_CompleteOrMarked(t *testing.T) {
	whole := strings.Repeat("a", MaxSalientFactTextLen)
	overlong := strings.Repeat("a", MaxSalientFactTextLen+50)

	for _, tc := range []struct{ name, in string }{
		{"short", "Bob gave me 3 blueberries as a gift."},
		{"exactly at the cap", whole},
		{"over the cap", overlong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NewSalientFact(factAt(), InteractionGave, tc.in).Text

			if n := utf8.RuneCountInString(got); n > MaxSalientFactTextLen {
				t.Fatalf("stored fact is %d runes, over the %d-rune cap", n, MaxSalientFactTextLen)
			}
			if got == tc.in {
				return // complete — the good case
			}
			if !strings.HasSuffix(got, ElisionMarker) {
				t.Errorf(
					"fact was shortened but stored as a bare prefix, with no %q marker — the NPC "+
						"reading this back cannot tell a cut memory from a whole one. Stored tail: %q",
					ElisionMarker, tailRunes(got, 32),
				)
			}
		})
	}
}

func TestTruncatePayMessage_CompleteOrMarked(t *testing.T) {
	overlong := strings.Repeat("a", MaxPayMessageRunes+50)
	got := truncatePayMessage(overlong)

	if n := utf8.RuneCountInString(got); n != MaxPayMessageRunes {
		t.Errorf("message is %d runes, want exactly the %d-rune cap", n, MaxPayMessageRunes)
	}
	if !strings.HasSuffix(got, ElisionMarker) {
		t.Errorf("over-cap message was cut but not marked: %q", tailRunes(got, 32))
	}
	if whole := strings.Repeat("a", MaxPayMessageRunes); truncatePayMessage(whole) != whole {
		t.Error("a message exactly at the cap must pass through untouched, unmarked")
	}
	if truncatePayMessage("   ") != "" {
		t.Error("a blank message must normalize to empty, not to a marker")
	}
}

// TestGiftFactText_TakesOneCut is the regression test for the defect LLM-405
// actually fires on. The give tool ADMITS a 200-rune note (it rejects longer
// rather than truncating), and the sentence wrapped around it runs another ~45 —
// so the fact overran the 220-rune cap and NewSalientFact sliced the tail of the
// note AND the closing paren off, mid-word. Two cuts, the second one silent.
//
// The note is now elided against the budget the sentence leaves it, so the fact
// arrives whole: closing punctuation intact, one marker, no second cut.
func TestGiftFactText_TakesOneCut(t *testing.T) {
	w := &World{}
	const maxNoteTheGiveToolAdmits = 200 // handlers.MaxPayWithItemForChars
	note := strings.Repeat("z", maxNoteTheGiveToolAdmits)

	for _, fromGiverPOV := range []bool{true, false} {
		fact := giftFactText(w, "Ezekiel Crane", "Hannah Boggs", []ItemKindQty{{Kind: "blueberry", Qty: 3}}, note, fromGiverPOV)

		if n := utf8.RuneCountInString(fact); n > MaxSalientFactTextLen {
			t.Fatalf("fact is %d runes — over the %d-rune cap, so NewSalientFact will cut it a SECOND time (fromGiverPOV=%v)",
				n, MaxSalientFactTextLen, fromGiverPOV)
		}
		// The whole point: the fact reaches the store already fitting, so the
		// mandatory write-time cap is a no-op on it and cannot chop the sentence.
		if stored := NewSalientFact(factAt(), InteractionGave, fact).Text; stored != fact {
			t.Errorf("NewSalientFact cut the gift fact a second time (fromGiverPOV=%v):\n  built:  %q\n  stored: %q", fromGiverPOV, fact, stored)
		}
		if !strings.HasSuffix(fact, ").") {
			t.Errorf("gift fact lost its closing punctuation to the cut (fromGiverPOV=%v): %q", fromGiverPOV, tailRunes(fact, 40))
		}
		if !strings.Contains(fact, ElisionMarker) {
			t.Errorf("the note WAS shortened to fit, so it must say so (fromGiverPOV=%v): %q", fromGiverPOV, fact)
		}
	}
}

// TestGiftFactText_ShortNoteSurvivesWhole guards the common case against an
// over-eager cut: a note that fits is stored verbatim, with no marker.
func TestGiftFactText_ShortNoteSurvivesWhole(t *testing.T) {
	w := &World{}
	fact := giftFactText(w, "Ezekiel Crane", "Hannah Boggs", []ItemKindQty{{Kind: "blueberry", Qty: 3}}, "for your hunger", false)

	if !strings.Contains(fact, "(for your hunger).") {
		t.Errorf("a note well within budget must be carried whole: %q", fact)
	}
	if strings.Contains(fact, ElisionMarker) {
		t.Errorf("nothing was cut, so nothing may be marked: %q", fact)
	}
}

// TestGiftFactText_GoodsPhraseEatsTheBudget: a gift of enough distinct items can
// spend the entire fact budget on the goods alone. The note is dropped rather
// than rendered as an empty "(…)" — the goods ARE the fact — and the oversized
// sentence still leaves via the marked cut in NewSalientFact, not a bare one.
func TestGiftFactText_GoodsPhraseEatsTheBudget(t *testing.T) {
	w := &World{}
	items := make([]ItemKindQty, 0, 40)
	for i := 0; i < 40; i++ {
		items = append(items, ItemKindQty{Kind: ItemKind("blueberry"), Qty: i + 1})
	}
	fact := giftFactText(w, "Ezekiel Crane", "Hannah Boggs", items, "for your hunger", false)

	if strings.Contains(fact, "(") {
		t.Errorf("no budget was left for the note, so it must be dropped, not rendered empty: %q", fact)
	}
	stored := NewSalientFact(factAt(), InteractionReceivedGift, fact).Text
	if utf8.RuneCountInString(stored) > MaxSalientFactTextLen {
		t.Fatalf("stored fact is over the cap: %d runes", utf8.RuneCountInString(stored))
	}
	if stored != fact && !strings.HasSuffix(stored, ElisionMarker) {
		t.Errorf("the oversized goods phrase was cut but not marked: %q", tailRunes(stored, 32))
	}
}

// tailRunes returns the last n runes of s, for failure messages that need to show
// where a cut landed without dumping 220 runes into the log.
func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
