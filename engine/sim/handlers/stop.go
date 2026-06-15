package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// stop.go — ZBBS-HOME-338. The `stop` tool: a voluntary halt of an in-flight
// walk. Advertised (gateTools) ONLY while the actor is moving, and the inverse
// of the walking gate (ZBBS-HOME-337) that hides the movement-incompatible
// action tools mid-walk: stop is the escape hatch so a walking NPC can abandon
// a route it no longer wants and act (eat, rest, speak) on the next tick,
// rather than being obliged to finish the walk before any non-move tool
// becomes usable.

const stopDescription = "Halt your current walk where you stand, abandoning your destination. " +
	"Use this when you no longer want to finish walking — e.g. to eat, rest, or speak now instead " +
	"of continuing. Only available while you are walking; it ends your turn."

// stopSchema advertises no arguments — stop takes none.
var stopSchema = json.RawMessage(`{
    "type": "object",
    "properties": {},
    "additionalProperties": false
}`)

// stopArgs is the (empty) decoded argument value for the stop tool.
type stopArgs struct{}

// DecodeStopArgs validates that the stop call carries an empty JSON object (or
// nothing). stop has no arguments; an absent / null / empty payload is the
// empty object, anything else (unknown fields, trailing data, non-object) is a
// typed error surfaced to the model.
func DecodeStopArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return stopArgs{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var a stopArgs
	if err := dec.Decode(&a); err != nil {
		return nil, fmt.Errorf("stop: %w", err)
	}
	// Require EOF after the object. dec.More() only reports another element in
	// the current array/object — it does NOT catch a trailing top-level value
	// (e.g. `{} {}` or `{} 5`), so a second decode that must hit io.EOF is the
	// correct trailing-data check.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, decodeErrf("stop: trailing data after JSON object")
		}
		return nil, fmt.Errorf("stop: trailing data after JSON object: %w", err)
	}
	return a, nil
}

// HandleStop is the CommitFn for the stop tool — a pure builder returning the
// sim.StopMove Command. The MoveIntent clear, the ActorMoveStopped{cancelled}
// emit, and the not-walking rejection all run inside StopMove on the world
// goroutine.
func HandleStop(in HandlerInput) (sim.Command, error) {
	return sim.StopMove(in.ActorID, time.Now().UTC()), nil
}
