package handlers

import "testing"

// TestCommitResultContent_SpeakEchoesLine pins the ZBBS-WORK-368 within-tick
// salience echo: a successful speak returns its own line back to the model
// (quoted, plus a soft done() nudge) instead of the generic "[ok]" every other
// commit returns. Without this the spoken text lives only in the speak call's
// arguments JSON, which Llama-3.3 can't saliently re-read within a tick — the
// cause of the verbatim speak,speak,done repeat.
func TestCommitResultContent_SpeakEchoesLine(t *testing.T) {
	cases := []struct {
		name string
		vc   ValidatedCall
		want string
	}{
		{
			name: "speak echoes the line + done nudge",
			vc:   ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: "Welcome, friend"}},
			want: `[ok] You said: "Welcome, friend". If you have nothing new to add, call done().`,
		},
		{
			name: "speak text is trimmed to match what was actually spoken",
			vc:   ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: "  good morrow  "}},
			want: `[ok] You said: "good morrow". If you have nothing new to add, call done().`,
		},
		{
			// %q quotes + escapes, so an utterance containing a double quote
			// can't break out of the echo's "..." framing.
			name: "embedded quote is escaped, framing holds",
			vc:   ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: `say "hi"`}},
			want: `[ok] You said: "say \"hi\"". If you have nothing new to add, call done().`,
		},
		{
			// Defensive: can't happen on the success path (sim.Speak rejects
			// empty text), but the guard must not echo `You said: ""`.
			name: "whitespace-only text falls back to generic ok",
			vc:   ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: "   "}},
			want: "[ok]",
		},
		{
			// Defensive: a future refactor that hands the wrong decoded type
			// must degrade to the generic ok, not panic.
			name: "wrong args type falls back to generic ok",
			vc:   ValidatedCall{Name: "speak", DecodedArgs: struct{ X int }{X: 1}},
			want: "[ok]",
		},
		{
			name: "non-speak commit returns the generic ok unchanged",
			vc:   ValidatedCall{Name: "move_to", DecodedArgs: nil},
			want: "[ok]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commitResultContent(&tc.vc)
			if got != tc.want {
				t.Errorf("commitResultContent\n got:  %q\n want: %q", got, tc.want)
			}
		})
	}
}
