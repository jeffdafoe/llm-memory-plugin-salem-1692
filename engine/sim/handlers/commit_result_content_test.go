package handlers

import "testing"

// TestCommitResultContent_SpeakEchoesLine pins the speak tool result: a
// successful speak returns its own line back to the model (quoted, the
// ZBBS-WORK-368 within-tick salience echo) plus the ZBBS-WORK-375 post-speak
// continuation steer (bias to done(), forbid re-greet/re-pitch/rephrase),
// instead of the generic "[ok]" every other commit returns. With HOME-381's
// hard cap gone, this tool result is the recency-dominant message the model
// reads before deciding whether to speak again or end the turn.
func TestCommitResultContent_SpeakEchoesLine(t *testing.T) {
	cases := []struct {
		name string
		vc   ValidatedCall
		want string
	}{
		{
			name: "speak echoes the line + continuation steer",
			vc:   ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: "Welcome, friend"}},
			want: `[ok] You said: "Welcome, friend". You have spoken — call done() now unless a new event has arrived or someone asked you something distinct you have not yet answered. Do not greet again, re-pitch, or rephrase what you just said.`,
		},
		{
			name: "speak text is trimmed to match what was actually spoken",
			vc:   ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: "  good morrow  "}},
			want: `[ok] You said: "good morrow". You have spoken — call done() now unless a new event has arrived or someone asked you something distinct you have not yet answered. Do not greet again, re-pitch, or rephrase what you just said.`,
		},
		{
			// %q quotes + escapes, so an utterance containing a double quote
			// can't break out of the echo's "..." framing.
			name: "embedded quote is escaped, framing holds",
			vc:   ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: `say "hi"`}},
			want: `[ok] You said: "say \"hi\"". You have spoken — call done() now unless a new event has arrived or someone asked you something distinct you have not yet answered. Do not greet again, re-pitch, or rephrase what you just said.`,
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
