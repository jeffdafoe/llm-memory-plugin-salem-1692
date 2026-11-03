package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// place_ward.go — LLM-897. The place_ward tool: a villager at home puts a ward
// good from their pack up at the house (sim.PlaceHomeWard). Advertised only with
// the ward place choice (payload.WardPlace, gateTools), the same view the
// "## A ward for the house" cue renders from.

// PlaceWardArgs is the decoded place_ward call.
type PlaceWardArgs struct {
	Item string `json:"item"`
}

var placeWardSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "item": {
            "type": "string",
            "enum": ["horseshoe"],
            "description": "The good to put up, exactly as named."
        }
    },
    "required": ["item"],
    "additionalProperties": false
}`)

const placeWardDescription = "Put up a horseshoe you carry at your own home: you nail it over the door. Only from inside your home. It takes a moment and does not end your turn."

// RegisterPlaceWard adds place_ward as a ClassCommit entry. Non-terminal: it is
// a moment's work, so the villager may pair it with a word in the same turn.
func RegisterPlaceWard(r *Registry) error {
	return r.RegisterCommit(
		placeWardToolName,
		placeWardSchema,
		DecodePlaceWardArgs,
		HandlePlaceWard,
		false,
		WithDescription(placeWardDescription),
	)
}

// DecodePlaceWardArgs parses the raw tool-call arguments. The item must be a
// ward good a villager deals in (sim.IsVillagerWardKind).
func DecodePlaceWardArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("place_ward: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args PlaceWardArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("place_ward: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("place_ward: trailing data after JSON object")
		}
		return nil, fmt.Errorf("place_ward: malformed trailing data: %w", err)
	}
	args.Item = strings.TrimSpace(args.Item)
	if args.Item == "" {
		return nil, modelSafef("place_ward: item is required")
	}
	if !sim.IsVillagerWardKind(sim.ItemKind(args.Item)) {
		// A fixed message: echoing the value could put an unsupported good's name
		// (witch_bottle) into the model-facing surface (code_review).
		return nil, modelSafef("place_ward: only a horseshoe can be put up at home this way")
	}
	return args, nil
}

// HandlePlaceWard is the CommitFn for place_ward. Pure builder; the world-state
// checks run inside sim.PlaceHomeWard.
func HandlePlaceWard(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(PlaceWardArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("place_ward: handler received unexpected args type %T", in.Args)
	}
	return sim.PlaceHomeWard(in.ActorID, sim.ItemKind(args.Item), time.Now().UTC()), nil
}
