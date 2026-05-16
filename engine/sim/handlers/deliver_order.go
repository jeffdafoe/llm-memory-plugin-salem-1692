package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// deliver_order.go — production deliver_order tool registration +
// handler. Phase 3 PR S6: seller-only tool that finalizes a Ready
// Order minted at AcceptPay for a take-away pay-with-item offer.
//
// The seller (typically an NPC keeper) emits
// {"order_id": 42} as the tool's arguments. Decode parses + applies
// the schema-bounded uint64 range; HandleDeliverOrder is a pure
// builder that returns the sim.DeliverOrder Command; the world
// goroutine runs the 7-gate validation matrix (existence + auth +
// state + TTL + seller-stock + co-presence + catalog) atomically
// with the transfer + state flip.

// DeliverOrderArgs is the decoded shape of the deliver_order tool's
// arguments. Single field — the OrderID the seller is finalizing.
type DeliverOrderArgs struct {
	OrderID uint64 `json:"order_id"`
}

// deliverOrderSchema is the JSON Schema bytes shipped to the LLM
// provider. Minimal — single required field, no flavor text. The
// handover narrative beat lives in the seller's same-tick speak
// ("Here you are, Jefferey."), NOT in the tool args.
var deliverOrderSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "order_id": {
            "type": "integer",
            "minimum": 1,
            "maximum": 18446744073709551615,
            "description": "Identifier of the Ready order to deliver. Surfaced to you in the 'Orders to deliver' section of your perception."
        }
    },
    "required": ["order_id"],
    "additionalProperties": false
}`)

// deliverOrderDescription is the tool description advertised to the
// model. Terse — schema's field description carries the detail.
const deliverOrderDescription = "Hand over goods you sold but haven't delivered yet. " +
	"Use to fulfill a pending order from your 'Orders to deliver' list. " +
	"You can only deliver to a buyer/consumer who is currently in your conversation. " +
	"Pair with a brief speak (\"Here you are.\") to land the handover narratively."

// DecodeDeliverOrderArgs parses the raw tool-call arguments into a
// DeliverOrderArgs. Same posture as DecodePayArgs: reject non-object
// payloads early, DisallowUnknownFields, trailing-data check,
// minimum-bound check. Numeric upper bound is enforced by JSON
// Schema (`maximum`) plus the natural uint64 range; the Go decoder
// won't overflow on values up to 2^64-1.
func DecodeDeliverOrderArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("deliver_order: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args DeliverOrderArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("deliver_order: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("deliver_order: trailing data after JSON object")
		}
		return nil, fmt.Errorf("deliver_order: malformed trailing data: %w", err)
	}
	if args.OrderID < 1 {
		return nil, fmt.Errorf("deliver_order: order_id must be at least 1 (got %d)", args.OrderID)
	}
	return args, nil
}

// HandleDeliverOrder is the CommitFn for the deliver_order tool. Pure
// builder — no world reads. Constructs a sim.DeliverOrder Command
// with the seller (= caller) and the requested OrderID. The world
// goroutine's Fn runs the full 7-gate validation matrix.
//
// No static validation beyond what Decode did — the OrderID is just
// an opaque uint64 here; whether it resolves to a real Order, whether
// the caller owns it, whether it's at Ready, etc. — all live-world
// state checks done by the Command.
func HandleDeliverOrder(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(DeliverOrderArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("deliver_order: handler received unexpected args type %T", in.Args)
	}
	return sim.DeliverOrder(in.ActorID, sim.OrderID(args.OrderID), time.Now().UTC()), nil
}
