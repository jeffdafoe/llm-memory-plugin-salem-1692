package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// disperse.go — production disperse tool registration + handler, LLM-453.
//
// The model emits {"say": "I'll see you all at supper, then"}. DecodeDisperseArgs
// parses + applies the schema-bounded length check; HandleDisperse normalizes the
// say (trim + control-char reject) and returns the sim.Disperse Command, which runs
// on the world goroutine and does the world-state-dependent work: not-in-a-huddle
// reject, the best-effort parting speak, the "for business" huddle leave, and the
// re-huddle cooldown stamp (see sim/disperse.go).
//
// disperse is the daytime counterpart to bedding down: a graceful, terminal exit
// from a conversation that has wound down, so an NPC stops echoing farewells at a
// settled conversation forever (the Walker turboyap loop). The parting line rides
// the tool's `say` per the terminal-verb rule — a successful disperse ends the
// tick, so the farewell cannot be a separate speak.

// DisperseArgs is the decoded shape of the disperse tool's arguments.
//
//   - say: required, minLength 1, maxLength MaxDisperseSayChars. The parting
//     words, spoken to the others as the actor takes its leave.
type DisperseArgs struct {
	Say string `json:"say"`
}

// MaxDisperseSayChars caps the parting line on the model-facing schema. A farewell
// is a sentence or two; 280 leaves generous headroom while bounding a pathological
// input before it reaches the speak path (which rune-caps and control-char-rejects
// its own text anyway).
const MaxDisperseSayChars = 280

// disperseSchema is the JSON Schema bytes shipped to the LLM provider. The say
// length bound is restated as a literal because schema bytes are static — keep it
// in sync with DecodeDisperseArgs's defensive range check.
var disperseSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "say": {
            "type": "string",
            "minLength": 1,
            "maxLength": 280,
            "description": "Your parting words — a brief goodbye. Spoken to the others as you take your leave (e.g. \"I'll see you all at supper, then\", \"Take care — I've things to see to\")."
        }
    },
    "required": ["say"],
    "additionalProperties": false
}`)

// disperseDescription is the tool description advertised to the model. The schema's
// per-field description carries the say guidance; this frames when to reach for the
// tool and the one thing the model must understand: this ends your turn, and your
// farewell rides the say (so you do not also speak).
const disperseDescription = "Take your leave of a conversation that has wound down — you step out and turn back to your own affairs, and the others are free to do the same. Use this when the talk here is finished and there is nothing left to do together but say your goodbyes. Your parting words go in `say`; they are spoken as you leave. Taking your leave ENDS your turn."

// DecodeDisperseArgs parses the raw tool-call arguments into a DisperseArgs.
// Errors are typed validation failures the harness surfaces to the model as tool
// errors (so the model can retry with corrected args).
//
// Checks:
//
//   - JSON parses, no trailing data, no unknown fields
//   - say present and within the character cap
//
// What DecodeDisperseArgs does NOT check (handled in HandleDisperse / sim.Disperse):
//
//   - Trim-emptiness + control-character scan of say: HandleDisperse's job.
//   - not-in-a-conversation reject: world state, done in the Command.
func DecodeDisperseArgs(raw json.RawMessage) (any, error) {
	// Reject non-object payloads early — same rationale as take_break/consume: a
	// bare null / number / string decodes quietly to zero values, producing a
	// misleading "say is required" instead of a crisp "must be a JSON object".
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("disperse: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args DisperseArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("disperse: malformed arguments: %w", err)
	}
	// Trailing-data check — matches the take_break/consume/speak pattern.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("disperse: trailing data after JSON object")
		}
		return nil, fmt.Errorf("disperse: malformed trailing data: %w", err)
	}
	if args.Say == "" {
		return nil, modelSafef("disperse: say is required (a brief parting word)")
	}
	if n := utf8.RuneCountInString(args.Say); n > MaxDisperseSayChars {
		return nil, modelSafef(
			"disperse: say exceeds %d-character cap (got %d characters)",
			MaxDisperseSayChars, n,
		)
	}
	return args, nil
}

// HandleDisperse is the CommitFn for the disperse tool. Pure builder — does NOT
// touch the world. Static validation that JSON Schema cannot express runs here
// (trim-empty say, control-char scan); world-state validation (not-in-a-huddle
// reject, parting speak, huddle leave, cooldown stamp) runs inside the returned
// sim.Disperse Command on the world goroutine.
func HandleDisperse(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(DisperseArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("disperse: handler received unexpected args type %T", in.Args)
	}
	say := strings.TrimSpace(args.Say)
	if say == "" {
		return sim.Command{}, modelSafef("disperse: say is empty after trim")
	}
	// say is freeform prose spoken aloud — allow the same \n/\r/\t set the
	// speak/take_break freeform text fields allow, reject other control characters
	// (typos at best, prompt-forge attempts at worst).
	if i := indexInvalidControlChar(say); i >= 0 {
		return sim.Command{}, modelSafef(
			"disperse: say contains a disallowed control character at byte offset %d", i)
	}
	return sim.Disperse(in.ActorID, say, in.HasNewNews, time.Now().UTC()), nil
}
