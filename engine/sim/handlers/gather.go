package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// gather.go — production gather tool registration + handler (ZBBS-WORK-328).
//
// The v2 revival of v1's `gather` verb as a general environmental-harvest
// substrate. The model emits {"qty": 2} (or just {}); the tool harvests the
// gatherable source the actor is loitering at — the SOURCE determines the
// item (well→water, bush→berries), so there is no item arg. Decode does the
// schema-bounded numeric range; HandleGather is a pure builder; the returned
// sim.Gather Command runs on the world goroutine and does all the
// world-state-dependent work (resolve the loitering source, pick its
// GatherItem row, decrement bounded supply, credit inventory, emit
// ItemGathered) — see sim/gather_commands.go.

// GatherArgs is the decoded shape of the gather tool's arguments. qty is a
// pointer so an OMITTED qty (nil) is distinguishable from an explicit 0: omitted
// means "gather 1" (the v1 default), while an explicit qty must be >= 1 (the
// schema's minimum) — explicit 0 is rejected at decode rather than silently
// coerced to 1.
type GatherArgs struct {
	Qty *int `json:"qty"`
}

// gatherSchema is the JSON Schema bytes shipped to the LLM provider. qty is
// optional (not in required) so a bare `gather` call harvests 1; when present
// it must be in [1, math.MaxInt32]. The maximum literal restates
// sim.MaxGatherQty (Go doesn't interpolate constants into raw JSON) — keep
// them in sync.
var gatherSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "qty": {
            "type": "integer",
            "minimum": 1,
            "maximum": 2147483647,
            "description": "Whole-number quantity to gather. Omit to gather 1. A finite source yields at most what it has left."
        }
    },
    "required": [],
    "additionalProperties": false
}`)

// gatherDescription is the tool description advertised to the model. The gate
// (gateTools) only advertises this tool when the actor is actually loitering
// at a gatherable source, so the description can assume that context.
const gatherDescription = "Gather a portable item from the natural source you're standing at — " +
	"draw water from a well, pick berries from a bush. Fills your inventory with what the source " +
	"yields (the source decides the item). You must have ARRIVED at the source first. Some sources " +
	"are finite and refill over time; a well never runs dry."

// DecodeGatherArgs parses the raw tool-call arguments into a GatherArgs.
//
// Checks: JSON parses to an object, no unknown fields, no trailing data, and
// qty (when present) is in [1, sim.MaxGatherQty]. A missing qty decodes to 0,
// which the sim.Gather Command treats as "gather 1" — so 0 is allowed here and
// only an explicit negative is rejected.
func DecodeGatherArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	// Empty raw arguments = a bare `gather` call. qty is optional, so this is
	// valid (→ gather 1): some providers send omitted args as empty rather
	// than "{}". A present-but-non-object payload is still a hard error.
	if len(trimmed) == 0 {
		return GatherArgs{}, nil
	}
	if trimmed[0] != '{' {
		return nil, decodeErrf("gather: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args GatherArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("gather: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, decodeErrf("gather: trailing data after JSON object")
		}
		return nil, fmt.Errorf("gather: malformed trailing data: %w", err)
	}
	// qty omitted (nil) → gather 1. Present → must satisfy the schema minimum
	// (>= 1); explicit 0 / negative is rejected here rather than coerced.
	if args.Qty != nil {
		if *args.Qty < 1 {
			return nil, decodeErrf("gather: qty must be at least 1 when provided (got %d)", *args.Qty)
		}
		if *args.Qty > sim.MaxGatherQty {
			return nil, decodeErrf("gather: qty exceeds maximum (got %d, max %d)", *args.Qty, sim.MaxGatherQty)
		}
	}
	return args, nil
}

// HandleGather is the CommitFn for the gather tool. Pure builder — does NOT
// touch the world. All world-state validation (resolve the loitering source,
// gatherable check, finite-supply / depletion gate, inventory credit, emit)
// runs inside the returned sim.Gather Command on the world goroutine.
func HandleGather(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(GatherArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("gather: handler received unexpected args type %T", in.Args)
	}
	qty := 0 // omitted → 0; sim.Gather treats < 1 as the default 1.
	if args.Qty != nil {
		qty = *args.Qty
	}
	return sim.Gather(in.ActorID, qty, time.Now().UTC()), nil
}
