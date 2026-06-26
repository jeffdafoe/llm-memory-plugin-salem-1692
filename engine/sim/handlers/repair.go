package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// repair.go — production repair tool registration + handler (LLM-118).
//
// The owner of a worn market stall mends it: a timed, visible activity that
// consumes nails and resets the stall's wear. The tool takes NO arguments — the
// actor's situation (the stall they own and stand at) fully determines the
// target. HandleRepair is a pure builder; the returned sim.StartRepair Command
// runs on the world goroutine and does all world-state validation (ownership,
// co-location, wear threshold, nail count), consumes the nails, and opens the
// SourceActivity window. The wear reset lands at completion via the activity
// sweep — see sim/source_activity.go.

// repairSchema is the JSON Schema shipped to the LLM provider — an empty object;
// repair takes no arguments.
var repairSchema = json.RawMessage(`{
    "type": "object",
    "properties": {},
    "required": [],
    "additionalProperties": false
}`)

// repairDescription is advertised to the model. gateTools only offers this tool
// when the actor owns a co-located stall worn to the repair threshold, so the
// description can assume that context.
const repairDescription = "Mend your market stall, which has worn from use. This is a short, visible job: " +
	"you hammer at the stall for a little while (stay put until it's done) and it uses nails from your pack. " +
	"You must have ARRIVED at your own stall, and you need enough nails — buy them from the smith if you're short."

// DecodeRepairArgs parses the raw tool-call arguments. repair takes no args, so
// this accepts an empty payload or a bare "{}" and rejects anything else (any
// field, trailing data, a non-object).
func DecodeRepairArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return struct{}{}, nil
	}
	if trimmed[0] != '{' {
		return nil, modelSafef("repair: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args struct{}
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("repair: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("repair: trailing data after JSON object")
		}
		return nil, fmt.Errorf("repair: malformed trailing data: %w", err)
	}
	return args, nil
}

// HandleRepair is the CommitFn for the repair tool. Pure builder — all
// world-state validation runs inside the returned sim.StartRepair Command on the
// world goroutine.
func HandleRepair(in HandlerInput) (sim.Command, error) {
	return sim.StartRepair(in.ActorID), nil
}
