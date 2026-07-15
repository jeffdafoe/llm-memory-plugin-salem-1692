package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// stoke.go — hearth stoke tool registration + handler (LLM-412). The repair
// tool's shape with firewood for nails: the actor responsible for a
// structure's hearth (its owner, or a worker hired by the owner — tending the
// fire is work, not leaving) feeds the fire, a timed activity that consumes
// firewood and extends the hearth's burn. The tool takes NO arguments — the
// actor's situation fully determines the target. HandleStoke is a pure
// builder; sim.StartStoke runs on the world goroutine and does all
// world-state validation (responsibility, inside the structure, fire actually
// low, firewood count), consumes the wood, and opens the SourceActivity
// window. The fire-extension lands at completion via the activity sweep.

// stokeSchema is the JSON Schema shipped to the LLM provider — an empty
// object; stoke takes no arguments.
var stokeSchema = json.RawMessage(`{
    "type": "object",
    "properties": {},
    "required": [],
    "additionalProperties": false
}`)

// stokeDescription is advertised to the model. gateTools only offers this tool
// when the hearth cue shows (responsible, inside, fire out/low), so the
// description can assume that context.
const stokeDescription = "Feed the hearth fire here with firewood from your pack. This is a short job: " +
	"you tend the fire for a moment (stay put until it's done) and it uses firewood you carry. " +
	"A burning fire warms the room and everyone in it."

// DecodeStokeArgs parses the raw tool-call arguments. stoke takes no args, so
// this accepts an empty payload or a bare "{}" and rejects anything else.
func DecodeStokeArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return struct{}{}, nil
	}
	if trimmed[0] != '{' {
		return nil, modelSafef("stoke: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args struct{}
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("stoke: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("stoke: trailing data after JSON object")
		}
		return nil, fmt.Errorf("stoke: malformed trailing data: %w", err)
	}
	return args, nil
}

// HandleStoke is the CommitFn for the stoke tool. Pure builder — all
// world-state validation runs inside the returned sim.StartStoke Command on
// the world goroutine.
func HandleStoke(in HandlerInput) (sim.Command, error) {
	return sim.StartStoke(in.ActorID), nil
}

// RegisterStoke adds the stoke tool to r as a ClassCommit entry,
// AvailabilityAvailable. terminalOnSuccess is FALSE, matching repair: feeding
// the fire is a within-tick step — the keeper can speak a word over their
// shoulder in the same tick.
//
// Advertising is gated at the prompt layer by gateTools (offered only with
// the hearth cue: responsible for the hearth, inside its structure, fire
// out/low) — but it stays AvailabilityAvailable so a call that arrives is
// still dispatchable; sim.StartStoke is the authoritative gate. Deliberately
// NOT in laborAbandonTools: a hired hand stoking the employer's fire is doing
// the job, not leaving it (the work-vs-leaving principle, LLM-412).
func RegisterStoke(r *Registry) error {
	return r.RegisterCommit(
		"stoke",
		stokeSchema,
		DecodeStokeArgs,
		HandleStoke,
		false, // non-terminal
		WithDescription(stokeDescription),
	)
}
