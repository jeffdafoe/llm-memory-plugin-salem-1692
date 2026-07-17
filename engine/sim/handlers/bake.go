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

// bake.go — production bake tool registration + handler, LLM-454.
//
// The model emits {} or {"say": "I'll get the bread on"}. DecodeBakeArgs parses +
// bounds the optional say; HandleBake normalizes it and returns the
// sim.StartOrJoinBake Command, which runs on the world goroutine and does the
// world-state work: at-home / evening / not-busy checks, start-or-join the shared
// per-home bake session, break off any conversation, and open the bake window until
// bedtime (see sim/bake.go).
//
// bake is the evening occupation that fills the empty home evening — the thing the
// homebodies keep narrating ("let's make bread"), made real. Baking shelves the
// actor's tick for the whole evening, so it is terminal-on-success.

// BakeArgs is the decoded shape of the bake tool's arguments.
//
//   - say: OPTIONAL, maxLength MaxBakeSayChars. A word to the others as the actor
//     heads to the hearth; omitted to begin quietly.
type BakeArgs struct {
	Say string `json:"say"`
}

// MaxBakeSayChars caps the optional announcement on the model-facing schema.
const MaxBakeSayChars = 200

// bakeSchema is the JSON Schema bytes shipped to the LLM provider. `say` is optional
// (no "required") — baking can begin silently.
var bakeSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "say": {
            "type": "string",
            "maxLength": 200,
            "description": "Optional. A word to the others as you head to the hearth (e.g. \"I'll get the bread on for us\"). Spoken as you break off to start; omit to begin quietly."
        }
    },
    "additionalProperties": false
}`)

// bakeDescription is the tool description advertised to the model.
const bakeDescription = "Bake the household's bread for the evening. You go to the hearth and work at it until bedtime — an evening's task, not a moment's — and the loaves are ready by the time you turn in. If someone at home has already started, you lend a hand at the same batch. Starting a batch uses your flour; lending a hand does not. Baking ENDS your turn — you're at it for the evening."

// DecodeBakeArgs parses the raw tool-call arguments into a BakeArgs. Errors are typed
// validation failures the harness surfaces to the model as tool errors.
func DecodeBakeArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("bake: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args BakeArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("bake: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("bake: trailing data after JSON object")
		}
		return nil, fmt.Errorf("bake: malformed trailing data: %w", err)
	}
	if n := utf8.RuneCountInString(args.Say); n > MaxBakeSayChars {
		return nil, modelSafef(
			"bake: say exceeds %d-character cap (got %d characters)", MaxBakeSayChars, n)
	}
	return args, nil
}

// HandleBake is the CommitFn for the bake tool. Pure builder — does NOT touch the
// world. Static validation (control-char scan of the optional say) runs here;
// world-state validation (at home, evening, not busy, start-or-join, huddle break-off,
// window open) runs inside the returned sim.StartOrJoinBake Command.
func HandleBake(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(BakeArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("bake: handler received unexpected args type %T", in.Args)
	}
	say := strings.TrimSpace(args.Say)
	if say != "" {
		if i := indexInvalidControlChar(say); i >= 0 {
			return sim.Command{}, modelSafef(
				"bake: say contains a disallowed control character at byte offset %d", i)
		}
	}
	return sim.StartOrJoinBake(in.ActorID, say, in.HasNewNews, time.Now().UTC()), nil
}
