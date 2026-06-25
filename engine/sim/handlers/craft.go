package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// craft.go — production `craft` tool registration + handler (LLM-116).
//
// A multi-output crafter (the smith makes skillets AND nails) no longer
// auto-produces every good in parallel; it forges ONE thing at a time and
// chooses which via this tool. The model emits {"item": "nail"}; the tool sets
// the actor's ProductionFocus and produce_tick then fills only that good (at the
// recipe's rate) until the actor chooses again. The SOURCE of truth for "what
// can I make" is the actor's own produce entries — HandleCraft is a pure builder
// and the returned sim.SetProductionFocus Command does the world-state
// validation (item resolves, and the actor actually produces it) on the world
// goroutine, returning a model-facing error the NPC can learn from.

// CraftArgs is the decoded shape of the craft tool's arguments. item is required
// (the good to focus production on).
type CraftArgs struct {
	Item string `json:"item"`
}

// craftSchema is the JSON Schema bytes shipped to the provider. item is required.
var craftSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "item": {
            "type": "string",
            "description": "The good to forge next, e.g. \"skillet\" or \"nail\". You make one thing at a time and keep making it until you choose again."
        }
    },
    "required": ["item"],
    "additionalProperties": false
}`)

// craftDescription is advertised to the model. gateTools only offers this tool to
// a crafter that makes more than one good and is at its workplace, so the copy
// can assume that context.
const craftDescription = "Set what you forge next at your workplace. You make one good at a time — name the " +
	"item to work on it (you can only make what your trade produces). You keep making it until you choose " +
	"again, so favour what people are actually buying."

// DecodeCraftArgs parses the raw tool-call arguments into a CraftArgs. Checks:
// parses to an object, no unknown fields, no trailing data, and item is present
// and non-empty after trimming.
func DecodeCraftArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("craft: arguments must be a JSON object with an \"item\"")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args CraftArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("craft: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("craft: trailing data after JSON object")
		}
		return nil, fmt.Errorf("craft: malformed trailing data: %w", err)
	}
	args.Item = strings.TrimSpace(args.Item)
	if args.Item == "" {
		return nil, modelSafef("craft: item is required (name the good to forge)")
	}
	return args, nil
}

// HandleCraft is the CommitFn for the craft tool. Pure builder — does NOT touch
// the world. The returned sim.SetProductionFocus Command validates on the world
// goroutine that the item resolves and the actor actually produces it.
func HandleCraft(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(CraftArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("craft: handler received unexpected args type %T", in.Args)
	}
	return sim.SetProductionFocus(in.ActorID, args.Item), nil
}
