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

// turn_in.go — production turn_in tool registration + handler, LLM-447.
//
// The model emits {} or {"say": "Goodnight to you both"}. DecodeTurnInArgs parses
// + bounds the optional say; HandleTurnIn normalizes it and returns the sim.TurnIn
// Command, which runs on the world goroutine and does the world-state work:
// re-validate the residency / off-shift / evening gate, speak the goodnight to the
// huddle, leave it classified as a retire, and bed the actor via the existing
// sleep machine (see sim/turn_in.go).
//
// turn_in is the evening's exit — the verb that was missing when three women at
// the Walker Residence traded 26 goodnights in two minutes and none of them went
// to bed. Bedding down ends the day, so it is terminal-on-success.

// TurnInArgs is the decoded shape of the turn_in tool's arguments.
//
//   - say: OPTIONAL, maxLength MaxTurnInSayChars. The goodnight, spoken to any
//     companions as the actor rises to go; omitted to retire quietly.
type TurnInArgs struct {
	Say string `json:"say"`
}

// MaxTurnInSayChars caps the optional goodnight on the model-facing schema.
const MaxTurnInSayChars = 200

// turnInSchema is the JSON Schema bytes shipped to the LLM provider. `say` is
// optional (no "required") — an actor alone turns in without a word.
var turnInSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "say": {
            "type": "string",
            "maxLength": 200,
            "description": "Optional. Your goodnight to any companions here, spoken as you rise to go (e.g. \"Goodnight to you both — I'm for my bed\"). Omit to turn in quietly."
        }
    },
    "additionalProperties": false
}`)

// turnInDescription is the tool description advertised to the model.
//
// It states plainly that this ENDS the day — the failure it exists to prevent is
// a model that says goodnight and keeps talking, so the description makes the
// finality explicit and folds the goodnight into this call rather than a separate
// speak (speak is itself terminal; asking for both would be unobeyable).
const turnInDescription = "Go to bed for the night. You bid any companions here goodnight, retire to your bed, and sleep until morning. Put your goodnight in say and it will be spoken as you rise to go — do NOT speak separately, this call carries your parting words. Turning in ENDS your day: you will not act again until you wake."

// DecodeTurnInArgs parses the raw tool-call arguments into a TurnInArgs. Errors are
// typed validation failures the harness surfaces to the model as tool errors.
func DecodeTurnInArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("turn_in: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args TurnInArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("turn_in: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("turn_in: trailing data after JSON object")
		}
		return nil, fmt.Errorf("turn_in: malformed trailing data: %w", err)
	}
	if n := utf8.RuneCountInString(args.Say); n > MaxTurnInSayChars {
		return nil, modelSafef(
			"turn_in: say exceeds %d-character cap (got %d characters)", MaxTurnInSayChars, n)
	}
	return args, nil
}

// HandleTurnIn is the CommitFn for the turn_in tool. Pure builder — does NOT touch
// the world. Static validation (control-char scan of the optional say) runs here;
// world-state validation (residency, off-shift, evening window, not already abed)
// runs inside the returned sim.TurnIn Command.
func HandleTurnIn(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(TurnInArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("turn_in: handler received unexpected args type %T", in.Args)
	}
	say := strings.TrimSpace(args.Say)
	if say != "" {
		if i := indexInvalidControlChar(say); i >= 0 {
			return sim.Command{}, modelSafef(
				"turn_in: say contains a disallowed control character at byte offset %d", i)
		}
	}
	return sim.TurnIn(in.ActorID, say, in.HasNewNews, time.Now().UTC()), nil
}
