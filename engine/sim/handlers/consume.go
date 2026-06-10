package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// indexStrictControlChar mirrors indexInvalidControlChar but does NOT exempt
// \n / \r / \t. Used for identifier-shaped inputs (item names, future kind
// references) where whitespace controls are typos / prompt-forge attempts,
// not legitimate paragraph shaping.
//
// Returns the byte offset of the first disallowed rune, or -1 if the string
// is clean. Same byte-offset reporting contract as indexInvalidControlChar
// so error messages share format.
func indexStrictControlChar(text string) int {
	for i, r := range text {
		switch {
		case r >= 0x20 && r < 0x7F:
			continue
		case r >= 0xA0 && r != utf8.RuneError:
			// Printable Unicode above C1. Allow.
			continue
		default:
			return i
		}
	}
	return -1
}

// consume.go — production consume tool registration + handler. Phase 3 PR S2
// scope: self-consume only (no consumers[] group-feed; no buy/serve verbs —
// those port alongside their substrate in S3-S6).
//
// The model emits {"item": "ale", "qty": 1} as the consume tool's arguments.
// Decode parses + applies schema-bounded length + numeric range; HandleConsume
// normalizes the item name (trim + control-char reject); the returned
// sim.Consume Command runs on the world goroutine and performs the world-
// state-dependent validation (case-insensitive ItemKind resolution against
// w.ItemKinds, Consumable check, walk-in-flight gate, inventory check) +
// inventory decrement + immediate Needs apply + item-source dwell-credit
// upsert + ItemConsumed emit (see sim/item_commands.go).

// ConsumeArgs is the decoded shape of the consume tool's arguments. The
// model-facing schema enforces:
//
//   - item: minLength 1, maxLength MaxConsumeItemChars
//   - qty:  integer, minimum 1, maximum math.MaxInt32
type ConsumeArgs struct {
	Item string `json:"item"`
	Qty  int    `json:"qty"`
}

// MaxConsumeItemChars caps the item name length on the model-facing schema.
// v1's item_kind.name is VARCHAR(32); 64 here gives prompt-typo headroom
// without letting pathological inputs bloat the validator path. Resolution
// is case-insensitive (sim.resolveItemKind) so "Ale" still finds "ale".
const MaxConsumeItemChars = 64

// Consume's numeric upper bound is shared with the substrate-level Consume
// Command via sim.MaxConsumeQty (= math.MaxInt32) — the JSON Schema's
// `maximum` literal below restates that value as 2147483647 because schema
// bytes are static (Go doesn't interpolate constants into raw JSON). Schema
// and constant must stay in sync; if MaxConsumeQty ever moves, update the
// schema literal here too.

// consumeSchema is the JSON Schema bytes shipped to the LLM provider. Narrow
// on purpose — PR S2 advertises only item + qty. Group-feed (consumers[])
// and the buy/serve coordination flags from v1's pay tool don't apply to
// self-consume and aren't part of this PR.
var consumeSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "item": {
            "type": "string",
            "minLength": 1,
            "maxLength": 64,
            "description": "Name of an item from your own inventory to consume. Must be a consumable kind (food, drink) — raw materials (wheat, iron) cannot be consumed."
        },
        "qty": {
            "type": "integer",
            "minimum": 1,
            "maximum": 2147483647,
            "description": "Whole-number quantity to consume. Must be at least 1."
        }
    },
    "required": ["item", "qty"],
    "additionalProperties": false
}`)

// consumeDescription is the tool description advertised to the model. Terse —
// the schema's per-field descriptions carry the detailed guidance. Mentions
// the dwell-payoff side because it changes the model's incentive to stay
// versus walk away after consuming.
const consumeDescription = "Consume an item from your own inventory — eat food, drink a drink. " +
	"Decrements the item from your inventory and reduces the matching need (hunger, thirst). " +
	"If you ask for more than your needs can absorb, you consume only what satisfies you and " +
	"the rest stays in your inventory. " +
	"Some items (stew, e.g.) have a slow-burn effect that keeps reducing the need over the " +
	"next several minutes IF you stay near where you ate; walking away ends the slow-burn early."

// DecodeConsumeArgs parses the raw tool-call arguments into a ConsumeArgs.
// Errors are typed validation failures the harness surfaces to the model as
// tool errors (so the model can retry with corrected args).
//
// Checks:
//
//   - JSON parses, no trailing data
//   - No unknown fields (DisallowUnknownFields)
//   - item present and non-empty post-decode
//   - qty in [1, math.MaxInt32]
//   - item byte length <= MaxConsumeItemChars (defense in depth vs schema)
//
// What DecodeConsumeArgs does NOT check (handled in HandleConsume / Consume
// Command):
//
//   - Trim-emptiness of item: HandleConsume trims and rejects after
//     normalization, so args carry the pre-trim text intact for debugging.
//   - Control-character scan on item: HandleConsume's responsibility.
//   - Case-insensitive ItemKind resolution / Consumable check / walk-in-flight
//     gate / inventory sufficiency: world-state checks done by sim.Consume on
//     the world goroutine.
func DecodeConsumeArgs(raw json.RawMessage) (any, error) {
	// Reject non-object payloads early. Same rationale as pay — bare null /
	// number / string decode quietly to zero values, producing misleading
	// downstream "item is required" errors instead of a crisp "must be a
	// JSON object" signal.
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("consume: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args ConsumeArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("consume: malformed arguments: %w", err)
	}
	// Trailing-data check — matches the pay/speak pattern.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("consume: trailing data after JSON object")
		}
		return nil, fmt.Errorf("consume: malformed trailing data: %w", err)
	}
	if args.Item == "" {
		return nil, errors.New("consume: item is required")
	}
	if n := utf8.RuneCountInString(args.Item); n > MaxConsumeItemChars {
		return nil, fmt.Errorf(
			"consume: item exceeds %d-character cap (got %d characters)",
			MaxConsumeItemChars, n,
		)
	}
	if args.Qty < 1 {
		return nil, fmt.Errorf("consume: qty must be at least 1 (got %d)", args.Qty)
	}
	if args.Qty > sim.MaxConsumeQty {
		return nil, fmt.Errorf("consume: qty exceeds maximum (got %d, max %d)", args.Qty, sim.MaxConsumeQty)
	}
	return args, nil
}

// HandleConsume is the CommitFn for the consume tool. Pure builder — does
// NOT touch the world. Static validation that JSON Schema cannot express
// runs here (trim-empty item, control-char scan); world-state validation
// (case-insensitive resolution, Consumable check, walk-in-flight gate,
// inventory check, mutation + emit) runs inside the returned sim.Consume
// Command on the world goroutine.
//
// Returns:
//
//   - sim.Consume Command on success — the harness submits it via
//     sim.RunTickToolCommand which runs Fn on the world goroutine.
//   - typed error on static-validation failure — surfaces to the model as
//     a tool error so the model can retry.
func HandleConsume(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(ConsumeArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("consume: handler received unexpected args type %T", in.Args)
	}
	item := strings.TrimSpace(args.Item)
	if item == "" {
		return sim.Command{}, errors.New("consume: item is empty after trim")
	}
	// Reject control characters in the item name with a stricter scan than
	// indexInvalidControlChar (which exempts \n/\r/\t for speak/pay freeform
	// text). `item` is a short-form identifier passed through to the catalog
	// lookup — newlines, tabs, and carriage returns in there are typos at
	// best and prompt-shaping forge attempts at worst.
	if i := indexStrictControlChar(item); i >= 0 {
		return sim.Command{}, fmt.Errorf(
			"consume: item contains a disallowed control character at byte offset %d", i)
	}
	// Item case + resolution to canonical ItemKind happens inside
	// sim.Consume on the world goroutine (it needs w.ItemKinds to look up).
	// Pass the trimmed item name through; the Command does the
	// case-insensitive match and surfaces ErrUnknownItemKind on miss.
	return sim.Consume(in.ActorID, item, args.Qty, time.Now().UTC()), nil
}
