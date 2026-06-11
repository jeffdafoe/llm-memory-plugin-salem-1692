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
	// To is the optional addressee (ZBBS-WORK-369): the name of the one
	// actor in the speaker's conversation this line is directed at. Drives
	// the addressee-resolution chain in sim.SpeakTo (to → vocative →
	// whole-huddle); omitted / unmatched falls back to vocative / whole-
	// huddle. Optional — the model may address the whole huddle by omitting
	// it. omitempty so a to-less call serializes without the field.
	To string `json:"to,omitempty"`
	// Mentions are the optional structured sale hints (ZBBS-WORK-400):
	// when the utterance tells people what the speaker has for sale or
	// names prices, the model lists those items here so a human listener's
	// Pay UI can act on them. World-side, sim.filterSpeakMentions silently
	// drops entries the speaker can't actually sell — a bad mention never
	// rejects the speak itself, so decode only bounds the shape.
	Mentions []SpeakMentionArg `json:"mentions,omitempty"`
}

// SpeakMentionArg is one entry in SpeakArgs.Mentions as the model emits
// it: a raw item-kind string (canonicalized world-side by resolveItemKind)
// and the optional per-unit price in coins.
type SpeakMentionArg struct {
	Item  string `json:"item"`
	Price int    `json:"price,omitempty"`
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

// MaxSpeakMentions caps the mentions array (ZBBS-WORK-400) — matches the
// schema's maxItems. Five distinct sale items in one utterance is already
// generous; anything past that is the model dumping inventory.
const MaxSpeakMentions = 5

// MaxSpeakMentionItemChars caps each mention's item-kind length — same
// value as MaxSceneQuoteItemChars (same catalog, same headroom).
const MaxSpeakMentionItemChars = 64

// speakSchema is the JSON Schema bytes shipped to the LLM provider via
// llm.ToolSpec.Schema. The WORK-323 state-claim validation gate
// (sim/speak_validation.go) operates on an IMPLICIT scan of `text`, so it
// needs no schema field. `mentions` (ZBBS-WORK-400) is the structured
// sale-hint side channel the PC Pay UI consumes — v1 parity for
// speak.mentions/price, deferred at the Phase 3 port and revived now that
// the client renders per-seller offer rows from it. The schema bounds are
// defense-in-depth restated in DecodeSpeakArgs (MaxSpeakMentions /
// MaxSpeakMentionItemChars).
var speakSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "text": {
            "type": "string",
            "minLength": 1,
            "maxLength": 1000,
            "description": "Say one message to the actors currently in your conversation."
        },
        "to": {
            "type": "string",
            "minLength": 1,
            "maxLength": 100,
            "description": "Optional. The name of the one person in your conversation you are speaking to. Omit it to address everyone present."
        },
        "mentions": {
            "type": "array",
            "maxItems": 5,
            "items": {
                "type": "object",
                "properties": {
                    "item": {
                        "type": "string",
                        "minLength": 1,
                        "maxLength": 64,
                        "description": "Canonical item kind from your own inventory (e.g. 'ale', 'stew', 'bread')."
                    },
                    "price": {
                        "type": "integer",
                        "minimum": 1,
                        "maximum": 2147483647,
                        "description": "Optional. The per-unit price in coins, if your message names one."
                    }
                },
                "required": ["item"],
                "additionalProperties": false
            },
            "description": "Optional. When your message tells people what you have for sale or names prices for your goods, list those items here so listeners can act on your words. Only your own goods — omit when not discussing things you sell."
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
	// Mentions shape bounds (ZBBS-WORK-400) — defense in depth vs the
	// schema, same posture as the text length re-check above. Content
	// validity (is this a real item kind the speaker can sell) is NOT
	// checked here: that needs world state, and a bad mention silently
	// drops world-side rather than failing the speak.
	if len(args.Mentions) > MaxSpeakMentions {
		return nil, fmt.Errorf(
			"speak: at most %d mentions allowed (got %d)",
			MaxSpeakMentions, len(args.Mentions),
		)
	}
	for i, m := range args.Mentions {
		if strings.TrimSpace(m.Item) == "" {
			return nil, fmt.Errorf("speak: mentions[%d].item is required", i)
		}
		if n := utf8.RuneCountInString(m.Item); n > MaxSpeakMentionItemChars {
			return nil, fmt.Errorf(
				"speak: mentions[%d].item exceeds %d-character cap (got %d characters)",
				i, MaxSpeakMentionItemChars, n,
			)
		}
		if m.Price < 0 {
			return nil, fmt.Errorf("speak: mentions[%d].price must not be negative", i)
		}
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
	to := args.To
	// Mentions pass through as raw model strings; canonicalization +
	// sellability filtering happens world-side in sim.filterSpeakMentions
	// (it needs the catalog + the speaker's live inventory).
	var mentions []sim.SpeakMention
	for _, m := range args.Mentions {
		mentions = append(mentions, sim.SpeakMention{Item: sim.ItemKind(m.Item), Price: m.Price})
	}
	// Captured outside the closure (the harness may reuse `in` across iterations):
	// the turn-state new-news exemption for this tick (ZBBS-WORK-370).
	hasNewNews := in.HasNewNews
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
		return sim.SpeakTo(actorID, text, to, mentions, hasNewNews, now).Fn(w)
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
