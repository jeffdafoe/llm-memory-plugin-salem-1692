package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// speak.go — production speak tool registration + handler. Phase 3 PR A.
//
// The model emits {"text": "..."} as the speak tool's arguments. Decode
// parses + applies schema-bounded length; HandleSpeak applies semantic
// validation that JSON Schema doesn't express (trim-empty, control char
// scan); the returned sim.Speak Command runs on the world goroutine and
// performs the world-state-dependent validation + emit + relationship
// writes (see sim/speak_commands.go).
//
// Static validation here is kept cheap on purpose: the harness pipeline
// already runs Decode then dispatches CommitFn synchronously, so any
// rejection at this layer avoids ever building the Command. World-state
// rejections (walk-in-flight, vocative-stale) MUST be in the Command —
// they need the live world that decode/handler can't read.

// SpeakArgs is the decoded shape of the speak tool's arguments. The
// model-facing schema (advertised via the registry) enforces minLength
// 1 / maxLength 1000 on Text. Decode's role is to parse, reject unknown
// fields, and re-validate the length bound (defense in depth — a
// provider that ignores the schema can't smuggle oversized text past us).
type SpeakArgs struct {
	Text string `json:"text"`
}

// MaxSpeakTextChars is the rune (character) length cap for raw speak
// text — enforced by both the advertised schema's maxLength (JSON
// Schema's maxLength is character-based per the spec) and the post-
// decode size check in DecodeSpeakArgs (`utf8.RuneCountInString`).
// Defense in depth. Choosing character-based over byte-based means a
// 1000-character Japanese utterance passes consistently across the
// provider's schema check and our engine check — a byte-based cap
// would silently fail multi-byte text the schema lets through.
const MaxSpeakTextChars = 1000

// speakSchema is the JSON Schema bytes shipped to the LLM provider via
// llm.ToolSpec.Schema. Narrow on purpose — advertises only `text`. The
// WORK-323 item/transfer/state-claim validation gates (sim/speak_validation.go)
// operate on an IMPLICIT scan of `text`, so they need no schema field and don't
// cache-bust the system prompt. A structured `mentions[]` field stays deferred
// (its only remaining consumers are the PC sellable-items dropdown + price-
// quoting, ZBBS-124); advertising fields the engine ignores would pollute the
// model's understanding of the tool, so it's omitted until those land.
var speakSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "text": {
            "type": "string",
            "minLength": 1,
            "maxLength": 1000,
            "description": "Say one message to the actors currently in your conversation."
        }
    },
    "required": ["text"],
    "additionalProperties": false
}`)

// speakDescription is the tool description advertised to the model. Kept
// terse — the schema's text.description carries the per-field guidance.
const speakDescription = "Say one message to the actors currently in your conversation. " +
	"The message is heard by every actor in the same huddle as you. " +
	"You cannot speak while walking — finish the move first, or speak before starting one."

// DecodeSpeakArgs parses the raw tool-call arguments into a SpeakArgs.
// Errors are typed validation failures the harness surfaces to the model
// as tool errors (so the model can retry with corrected args).
//
// Checks:
//
//   - JSON parses, no trailing data
//   - No unknown fields (DisallowUnknownFields)
//   - text field is present (not the zero value)
//   - text byte length ≤ MaxSpeakTextBytes (defense in depth vs schema)
//
// What DecodeSpeakArgs does NOT check (handled in HandleSpeak / Speak
// command Fn):
//
//   - Trim-emptiness: rejected by HandleSpeak after normalization, so
//     the args carry the pre-trim text intact for debugging.
//   - Control-character scan: rejected by HandleSpeak (cheap, post-decode
//     but pre-Command).
//   - Walk-in-flight / vocative-stale / huddle membership: world-state
//     checks done by the sim.Speak Command on the world goroutine.
func DecodeSpeakArgs(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args SpeakArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("speak: malformed arguments: %w", err)
	}
	// Trailing-data check: Decoder.More() only reports whether more
	// elements remain in the current array/object — it does NOT detect
	// a second top-level JSON value (e.g. `{...} {...}`). Do a second
	// Decode and require io.EOF to catch concatenated payloads.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("speak: trailing data after JSON object")
		}
		return nil, fmt.Errorf("speak: malformed trailing data: %w", err)
	}
	if args.Text == "" {
		return nil, errors.New("speak: text is required")
	}
	if n := utf8.RuneCountInString(args.Text); n > MaxSpeakTextChars {
		return nil, fmt.Errorf(
			"speak: text exceeds %d-character cap (got %d characters)",
			MaxSpeakTextChars, n,
		)
	}
	return args, nil
}

// HandleSpeak is the CommitFn for the speak tool. Pure builder — it does
// NOT touch the world. Static validation that JSON Schema cannot express
// runs here (trim-emptiness, control-character scan); world-state
// validation runs inside the returned sim.Speak Command's Fn on the world
// goroutine.
//
// Returns:
//
//   - sim.Speak Command on success — the harness submits it via
//     sim.RunTickToolCommand, which runs Fn on the world goroutine
//     atomically with the attempt-staleness check at handlers/harness.go.
//   - typed error on static-validation failure — surfaces to the model
//     as a tool error so the model can retry with corrected text.
func HandleSpeak(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(SpeakArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("speak: handler received unexpected args type %T", in.Args)
	}
	text := strings.TrimSpace(args.Text)
	if text == "" {
		return sim.Command{}, errors.New("speak: text is empty after trim — nothing to say")
	}
	// Control-character scan: reject anything outside \n \r \t and the
	// printable Unicode range. Catches paste mistakes (null bytes,
	// terminal ANSI escapes) and prompt-injection attempts that smuggle
	// control codes through the model. JSON Schema can express
	// minLength/maxLength but not "no control chars" without ridiculous
	// regex, so the check lives here. The result.text on the Spoke event
	// + the warrant excerpt will both be re-rendered into other actors'
	// perception prompts — control codes there would derail rendering.
	if i := indexInvalidControlChar(text); i >= 0 {
		return sim.Command{}, fmt.Errorf(
			"speak: text contains a disallowed control character at byte offset %d "+
				"(only \\n, \\r, \\t allowed)", i)
	}
	actorID := in.ActorID
	return sim.Command{Fn: func(w *sim.World) (any, error) {
		// ZBBS-HOME-363: form the conversation on the explicit talk action,
		// mirroring the PC path (httpapi.speakPCCommand). An NPC that walked
		// into an open structure to transact (e.g. a starving villager buying
		// from the Tavern keeper) has no huddle — the indoor encounter path
		// never formed one — so without this every pay/speak dies with "you're
		// not in a conversation." EnsureColocatedHuddle joins/forms the indoor
		// huddle with co-located actors; it is a no-op when already huddled,
		// alone, or outdoors, and swallows per-actor join errors internally.
		now := time.Now().UTC()
		if _, err := sim.EnsureColocatedHuddle(actorID, now).Fn(w); err != nil {
			return nil, err
		}
		return sim.Speak(actorID, text, now).Fn(w)
	}}, nil
}

// indexInvalidControlChar returns the byte offset of the first
// disallowed control character in text, or -1 if none. \n (0x0A),
// \r (0x0D), \t (0x09) are allowed; everything else in 0x00..0x1F plus
// 0x7F (DEL) and the C1 control range (0x80..0x9F) is rejected.
//
// Invalid UTF-8 is rejected up front via utf8.ValidString (returning
// offset 0); the per-rune loop does NOT special-case utf8.RuneError,
// because ranging a string yields RuneError for BOTH a decode error AND
// the legitimate replacement character U+FFFD ("�") — guarding on it
// would wrongly reject valid text containing that printable code point.
func indexInvalidControlChar(text string) int {
	if !utf8.ValidString(text) {
		return 0
	}
	for i, r := range text {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			continue
		case r >= 0x20 && r < 0x7F:
			continue
		case r == 0x7F:
			return i
		case r < 0x20:
			return i
		case r >= 0x80 && r <= 0x9F:
			return i
		}
	}
	return -1
}
