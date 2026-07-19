package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// craft.go — the `produce` tool registration + handler (LLM-116, redesigned in
// LLM-319).
//
// One call = one batch. The model emits {"item": "porridge"}; the tool
// validates, consumes the recipe inputs, and opens the actor's single
// production cycle — the batch lands in its stores when the cycle's work is
// done (produce_tick). Nothing is made without this call, and making more
// takes another call: the per-batch decision is the point (the broke-keeper
// agency of LLM-319). The SOURCE of truth for "what can I make" is the actor's
// own produce entries — HandleCraft is a pure builder and the returned
// sim.StartProductionCycle Command does the world-state validation on the
// world goroutine, returning a model-facing error the NPC can learn from.

// CraftArgs is the decoded shape of the produce tool's arguments.
//
//   - item: REQUIRED, the good to make a batch of.
//   - say:  OPTIONAL, maxLength MaxCraftSayChars. A word to whoever is present
//     as the actor sets the batch going (LLM-468). Its purpose is cost, not
//     flavour: without it a producer who wanted to voice its beat had to spend a
//     whole extra LLM round on `speak` (120 measured produce→speak
//     continuations a day, each re-shipping the full ephemeral body). Folding
//     the utterance into the acting call is the same "terminal verbs speak for
//     themselves" move `bake` and the offer/answer verbs already make.
type CraftArgs struct {
	Item string `json:"item"`
	Say  string `json:"say"`
}

// MaxCraftSayChars caps the optional announcement on the model-facing schema.
// Matches MaxBakeSayChars — same kind of utterance, same bound.
const MaxCraftSayChars = 200

// craftSchema is the JSON Schema bytes shipped to the provider. item is required.
var craftSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "item": {
            "type": "string",
            "description": "The good to make one batch of, e.g. \"porridge\" or \"nails\". One call starts one batch; it takes time and lands in your stores when done."
        },
        "say": {
            "type": "string",
            "maxLength": 200,
            "description": "Optional. A word to whoever is here as you set the batch going (e.g. \"I'll get another pot of porridge on\"). Spoken as you start; omit to work quietly. Saying something ENDS your turn."
        }
    },
    "required": ["item"],
    "additionalProperties": false
}`)

// craftDescription is advertised to the model. gateTools only offers this tool
// at the actor's workplace with nothing already in the works, so the copy can
// assume that context.
const craftDescription = "Start one batch of a good your trade makes, at your workplace. A batch uses up its " +
	"ingredients now, takes time to finish, and lands in your stores when done. One call makes one batch — " +
	"whether to make another is a fresh decision when it's finished."

// DecodeCraftArgs parses the raw tool-call arguments into a CraftArgs. Checks:
// parses to an object, no unknown fields, no trailing data, and item is present
// and non-empty after trimming.
func DecodeCraftArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("produce: arguments must be a JSON object with an \"item\"")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args CraftArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("produce: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("produce: trailing data after JSON object")
		}
		return nil, fmt.Errorf("produce: malformed trailing data: %w", err)
	}
	args.Item = strings.TrimSpace(args.Item)
	if args.Item == "" {
		return nil, modelSafef("produce: item is required (name the good to make)")
	}
	if n := utf8.RuneCountInString(args.Say); n > MaxCraftSayChars {
		return nil, modelSafef(
			"produce: say exceeds %d-character cap (got %d characters)", MaxCraftSayChars, n)
	}
	return args, nil
}

// HandleCraft is the CommitFn for the produce tool. Pure builder — does NOT
// touch the world. The returned sim.StartProductionCycle Command validates on
// the world goroutine and consumes the inputs at start.
func HandleCraft(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(CraftArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("produce: handler received unexpected args type %T", in.Args)
	}
	say := strings.TrimSpace(args.Say)
	if say != "" {
		if i := indexInvalidControlChar(say); i >= 0 {
			return sim.Command{}, modelSafef(
				"produce: say contains a disallowed control character at byte offset %d", i)
		}
	}
	return sim.StartProductionCycle(in.ActorID, args.Item, say, in.HasNewNews), nil
}
