package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// speak_test.go — handler-package coverage of DecodeSpeakArgs +
// HandleSpeak static validation. World-state validation (walk-in-flight,
// vocative-stale, recipient set, RecordInteraction matrix) is tested at
// the sim.Speak Command level in sim/speak_commands_test.go.

// --- DecodeSpeakArgs --------------------------------------------------

func TestDecodeSpeakArgs_Valid(t *testing.T) {
	args, err := DecodeSpeakArgs(json.RawMessage(`{"text":"Hello."}`))
	if err != nil {
		t.Fatalf("DecodeSpeakArgs: %v", err)
	}
	got, ok := args.(SpeakArgs)
	if !ok {
		t.Fatalf("Decoded type = %T, want SpeakArgs", args)
	}
	if got.Text != "Hello." {
		t.Errorf("Text = %q, want %q", got.Text, "Hello.")
	}
}

// TestDecodeSpeakArgs_WithTo — the optional ZBBS-WORK-369 addressee decodes
// alongside text.
func TestDecodeSpeakArgs_WithTo(t *testing.T) {
	args, err := DecodeSpeakArgs(json.RawMessage(`{"text":"Good morrow.","to":"Ezekiel"}`))
	if err != nil {
		t.Fatalf("DecodeSpeakArgs: %v", err)
	}
	got, ok := args.(SpeakArgs)
	if !ok {
		t.Fatalf("Decoded type = %T, want SpeakArgs", args)
	}
	if got.Text != "Good morrow." {
		t.Errorf("Text = %q, want %q", got.Text, "Good morrow.")
	}
	if got.To != "Ezekiel" {
		t.Errorf("To = %q, want %q", got.To, "Ezekiel")
	}
}

// TestDecodeSpeakArgs_ToOmittedIsEmpty — an omitted `to` decodes to empty
// (the whole-huddle / addressee-less path).
func TestDecodeSpeakArgs_ToOmittedIsEmpty(t *testing.T) {
	args, err := DecodeSpeakArgs(json.RawMessage(`{"text":"Good morrow."}`))
	if err != nil {
		t.Fatalf("DecodeSpeakArgs: %v", err)
	}
	if got := args.(SpeakArgs); got.To != "" {
		t.Errorf("To = %q, want empty", got.To)
	}
}

func TestDecodeSpeakArgs_MissingText(t *testing.T) {
	_, err := DecodeSpeakArgs(json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("DecodeSpeakArgs({}): want error, got nil")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error message lacks 'required': %v", err)
	}
}

func TestDecodeSpeakArgs_EmptyTextField(t *testing.T) {
	_, err := DecodeSpeakArgs(json.RawMessage(`{"text":""}`))
	if err == nil {
		t.Fatal(`DecodeSpeakArgs({"text":""}): want error, got nil`)
	}
}

func TestDecodeSpeakArgs_UnknownField(t *testing.T) {
	_, err := DecodeSpeakArgs(json.RawMessage(`{"text":"Hi","price":5}`))
	if err == nil {
		t.Fatal("DecodeSpeakArgs with unknown field: want error, got nil")
	}
}

func TestDecodeSpeakArgs_TrailingData(t *testing.T) {
	_, err := DecodeSpeakArgs(json.RawMessage(`{"text":"Hi"} {"text":"oops"}`))
	if err == nil {
		t.Fatal("DecodeSpeakArgs with trailing data: want error, got nil")
	}
	if !strings.Contains(err.Error(), "trailing") {
		t.Errorf("error message lacks 'trailing': %v", err)
	}
}

func TestDecodeSpeakArgs_OverMaxChars(t *testing.T) {
	// Build a JSON body with 1001 'a' chars in text — defense-in-depth
	// against a provider that ignores the advertised maxLength.
	long := `{"text":"` + strings.Repeat("a", MaxSpeakTextChars+1) + `"}`
	_, err := DecodeSpeakArgs(json.RawMessage(long))
	if err == nil {
		t.Fatal("DecodeSpeakArgs with oversized text: want error, got nil")
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Errorf("error message lacks size guidance: %v", err)
	}
}

func TestDecodeSpeakArgs_BareMaxLengthAllowed(t *testing.T) {
	// Exactly MaxSpeakTextChars — boundary case, should pass.
	body := `{"text":"` + strings.Repeat("a", MaxSpeakTextChars) + `"}`
	if _, err := DecodeSpeakArgs(json.RawMessage(body)); err != nil {
		t.Errorf("DecodeSpeakArgs at exactly cap: %v", err)
	}
}

// TestDecodeSpeakArgs_MultibyteCharsAtCap covers the rune-vs-byte
// contract: 1000 multibyte characters pass (since the cap is character-
// based, not byte-based), but 1001 fail. Catches regression toward a
// byte-based check that would disagree with the JSON Schema layer the
// provider validates against.
func TestDecodeSpeakArgs_MultibyteCharsAtCap(t *testing.T) {
	// "日" is 3 bytes in UTF-8 — so 1000 of them is 3000 bytes but 1000
	// characters. A byte-based cap would reject this; a character-based
	// cap (the intended contract) accepts it.
	body := `{"text":"` + strings.Repeat("日", MaxSpeakTextChars) + `"}`
	if _, err := DecodeSpeakArgs(json.RawMessage(body)); err != nil {
		t.Errorf("DecodeSpeakArgs at exactly cap with multibyte chars: %v", err)
	}
	overBody := `{"text":"` + strings.Repeat("日", MaxSpeakTextChars+1) + `"}`
	if _, err := DecodeSpeakArgs(json.RawMessage(overBody)); err == nil {
		t.Error("DecodeSpeakArgs over cap with multibyte chars: want error, got nil")
	}
}

func TestDecodeSpeakArgs_NonObject(t *testing.T) {
	_, err := DecodeSpeakArgs(json.RawMessage(`"just a string"`))
	if err == nil {
		t.Fatal("DecodeSpeakArgs of non-object: want error, got nil")
	}
}

// --- HandleSpeak ------------------------------------------------------

func handleSpeakInput(t *testing.T, text string) (sim.Command, error) {
	t.Helper()
	return HandleSpeak(HandlerInput{
		ActorID:   "hannah",
		AttemptID: "tk-test",
		Args:      SpeakArgs{Text: text},
	})
}

func TestHandleSpeak_BuildsCommandForValidInput(t *testing.T) {
	cmd, err := handleSpeakInput(t, "Hello there.")
	if err != nil {
		t.Fatalf("HandleSpeak: %v", err)
	}
	if cmd.Fn == nil {
		t.Error("Returned Command has nil Fn")
	}
}

func TestHandleSpeak_RejectsWhitespaceOnly(t *testing.T) {
	_, err := handleSpeakInput(t, "   \n\t  ")
	if err == nil {
		t.Fatal("HandleSpeak: want error for whitespace-only text, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error lacks 'empty': %v", err)
	}
}

func TestHandleSpeak_TrimsLeadingTrailingWhitespace(t *testing.T) {
	// We can't observe the trimmed text without invoking Fn (which needs
	// a world). The behavior is structurally tested: trim happens before
	// the empty check, so a string that is non-empty post-trim should
	// not error.
	if _, err := handleSpeakInput(t, "  Hello.  "); err != nil {
		t.Errorf("HandleSpeak trimmed-but-non-empty: %v", err)
	}
}

func TestHandleSpeak_RejectsControlChars(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"null_byte", "hello\x00world"},
		{"bell", "hello\x07world"},
		{"vertical_tab", "hello\x0Bworld"},
		{"escape", "hello\x1B[31mred\x1B[0m"},
		{"del", "hello\x7Fworld"},
		{"c1_control", "helloworld"}, // NEL (next line) — 0x85
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := handleSpeakInput(t, tc.text)
			if err == nil {
				t.Fatal("HandleSpeak: want error for control char, got nil")
			}
			if !strings.Contains(err.Error(), "control") {
				t.Errorf("error lacks 'control': %v", err)
			}
		})
	}
}

func TestHandleSpeak_AllowsTabNewlineCarriageReturn(t *testing.T) {
	cases := []string{
		"line one\nline two",
		"line one\r\nline two",
		"tab\there",
	}
	for _, text := range cases {
		t.Run(text, func(t *testing.T) {
			if _, err := handleSpeakInput(t, text); err != nil {
				t.Errorf("HandleSpeak rejected legal whitespace %q: %v", text, err)
			}
		})
	}
}

func TestHandleSpeak_AllowsUnicodeLetters(t *testing.T) {
	cases := []string{
		"Café and crêpes", // Latin extended
		"naïve résumé",    // diacritics
		"日本語",             // CJK
		"ÀÈÌ",             // accented capitals
		"— a dash —",      // em dashes
	}
	for _, text := range cases {
		t.Run(text, func(t *testing.T) {
			if _, err := handleSpeakInput(t, text); err != nil {
				t.Errorf("HandleSpeak rejected Unicode %q: %v", text, err)
			}
		})
	}
}

func TestHandleSpeak_WrongArgsType(t *testing.T) {
	_, err := HandleSpeak(HandlerInput{
		ActorID: "hannah",
		Args:    "not a SpeakArgs",
	})
	if err == nil {
		t.Fatal("HandleSpeak: want error for wrong args type, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected args type") {
		t.Errorf("error lacks 'unexpected args type': %v", err)
	}
}

// --- indexInvalidControlChar internal helper --------------------------

func TestIndexInvalidControlChar(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int // -1 if no offending char
	}{
		{"clean", "hello world", -1},
		{"clean_unicode", "café", -1},
		{"clean_newline", "a\nb", -1},
		{"clean_tab", "a\tb", -1},
		{"clean_cr", "a\rb", -1},
		{"null_at_start", "\x00abc", 0},
		{"null_mid", "abc\x00def", 3},
		{"del", "abc\x7F", 3},
		{"escape", "\x1B[", 0},
		{"replacement_char", "huh�?", -1},
		{"invalid_utf8", "abc\xff", 0},
		{"c1_nel", "abc", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := indexInvalidControlChar(tc.text)
			if got != tc.want {
				t.Errorf("indexInvalidControlChar(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

// TestDecodeSpeakArgs_WithMentions — the optional ZBBS-WORK-400 sale hints
// decode alongside text; price is optional per entry.
func TestDecodeSpeakArgs_WithMentions(t *testing.T) {
	args, err := DecodeSpeakArgs(json.RawMessage(
		`{"text":"Stew tonight, three coins.","mentions":[{"item":"stew","price":3},{"item":"bread"}]}`))
	if err != nil {
		t.Fatalf("DecodeSpeakArgs: %v", err)
	}
	got, ok := args.(SpeakArgs)
	if !ok {
		t.Fatalf("Decoded type = %T, want SpeakArgs", args)
	}
	want := []SpeakMentionArg{{Item: "stew", Price: 3}, {Item: "bread"}}
	if len(got.Mentions) != 2 || got.Mentions[0] != want[0] || got.Mentions[1] != want[1] {
		t.Errorf("Mentions = %+v, want %+v", got.Mentions, want)
	}
}

// TestDecodeSpeakArgs_MentionShapeRejects — defense-in-depth bounds on the
// mentions array: count cap, empty item, oversized item, unknown nested
// fields. Content validity (real item? sellable?) is deliberately NOT
// rejected here — that filters silently world-side. A negative price also
// passes decode (clamped to 0 by filterSpeakMentions): a bogus mention must
// degrade, never reject the utterance (code_review round 1).
func TestDecodeSpeakArgs_MentionShapeRejects(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"over count cap", `{"text":"x","mentions":[{"item":"a"},{"item":"b"},{"item":"c"},{"item":"d"},{"item":"e"},{"item":"f"}]}`},
		{"empty item", `{"text":"x","mentions":[{"item":"  "}]}`},
		{"oversized item", `{"text":"x","mentions":[{"item":"` + strings.Repeat("y", 65) + `"}]}`},
		{"unknown nested field", `{"text":"x","mentions":[{"item":"stew","qty":2}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeSpeakArgs(json.RawMessage(tc.raw)); err == nil {
				t.Errorf("DecodeSpeakArgs(%s) succeeded, want error", tc.raw)
			}
		})
	}
}

// TestDecodeSpeakArgs_NegativeMentionPricePasses — pins the degrade-don't-
// reject policy: a negative price survives decode untouched and clamps to 0
// world-side (filterSpeakMentions), so a bogus side-channel value can never
// reject the utterance itself.
func TestDecodeSpeakArgs_NegativeMentionPricePasses(t *testing.T) {
	args, err := DecodeSpeakArgs(json.RawMessage(`{"text":"x","mentions":[{"item":"stew","price":-1}]}`))
	if err != nil {
		t.Fatalf("DecodeSpeakArgs: %v", err)
	}
	got, ok := args.(SpeakArgs)
	if !ok {
		t.Fatalf("Decoded type = %T, want SpeakArgs", args)
	}
	if len(got.Mentions) != 1 || got.Mentions[0].Price != -1 {
		t.Errorf("Mentions = %+v, want price -1 preserved for the world-side clamp", got.Mentions)
	}
}
