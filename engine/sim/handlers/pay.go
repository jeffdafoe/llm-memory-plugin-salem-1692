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

// pay.go — production pay tool registration + handler. Phase 3 PR B —
// scope: pure coin transfer (no items, no qty, no consumers, no
// in_response_to, no deliberation). Mismatched-pay haggling, ledger, and
// inventory port in follow-up PRs alongside their substrate.
//
// The model emits {"recipient": "...", "amount": N, "for": "..."} as the
// pay tool's arguments. Decode parses + applies schema-bounded length +
// numeric range; HandlePay applies semantic validation that JSON Schema
// doesn't express (trim-empty recipient, control-char scan in ForText);
// the returned sim.Pay Command runs on the world goroutine and performs
// the world-state-dependent validation (huddle gate, recipient resolve,
// balance check) + transfer + emit + relationship writes (see
// sim/pay_commands.go).

// PayArgs is the decoded shape of the pay tool's arguments. The
// model-facing schema enforces:
//
//   - recipient: minLength 1, maxLength 100
//   - amount:    integer, minimum 1, maximum math.MaxInt32
//   - for:       maxLength MaxPayForChars (optional)
type PayArgs struct {
	Recipient string `json:"recipient"`
	Amount    int    `json:"amount"`
	For       string `json:"for"`
}

// MaxPayRecipientChars caps the recipient DisplayName length on the
// model-facing schema. 100 characters comfortably accommodates the full
// canonical "First Last" form plus titles ("Captain Ezekiel Crane the
// Younger") while rejecting pathological inputs that aren't real names.
const MaxPayRecipientChars = 100

// MaxPayForChars caps the optional flavor text length. 200 characters is
// enough for "ale and bread shared with friends" or "the news of his
// brother's letter" without letting the field bloat the per-interaction
// SalientFact text (which itself caps at MaxSalientFactTextLen=220 after
// the engine wraps the for-text into a full sentence).
const MaxPayForChars = 200

// Pay's numeric upper bound is shared with the substrate-level Pay Command
// via sim.MaxPayAmount (= math.MaxInt32) — the JSON Schema's `maximum`
// literal below restates that value as 2147483647 because schema bytes are
// static (Go doesn't interpolate constants into raw JSON). Schema and
// constant must stay in sync; if MaxPayAmount ever moves, update the
// schema literal here too.

// paySchema is the JSON Schema bytes shipped to the LLM provider. Narrow
// on purpose — PR B advertises only recipient/amount/for while item +
// qty + consume_now + consumers + in_response_to subsystems are deferred.
var paySchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "recipient": {
            "type": "string",
            "minLength": 1,
            "maxLength": 100,
            "description": "Display name of the person in your conversation you want to pay."
        },
        "amount": {
            "type": "integer",
            "minimum": 1,
            "maximum": 2147483647,
            "description": "Whole-number coin amount to transfer. Must be at least 1."
        },
        "for": {
            "type": "string",
            "maxLength": 200,
            "description": "Optional brief note describing what the payment is for (e.g. 'ale', 'news from the harbor')."
        }
    },
    "required": ["recipient", "amount"],
    "additionalProperties": false
}`)

// payDescription is the tool description advertised to the model. Terse —
// the schema's per-field descriptions carry the detailed guidance.
const payDescription = "Pay another villager coins. Use for tips, gifts, " +
	"and news payments. The recipient must be in your current conversation. " +
	"Item purchases come later — for now this is coin-only."

// DecodePayArgs parses the raw tool-call arguments into a PayArgs. Errors
// are typed validation failures the harness surfaces to the model as tool
// errors (so the model can retry with corrected args).
//
// Checks:
//
//   - JSON parses, no trailing data
//   - No unknown fields (DisallowUnknownFields)
//   - recipient present and non-empty post-decode
//   - amount in [1, math.MaxInt32]
//   - recipient byte length <= MaxPayRecipientChars (defense in depth vs schema)
//   - for byte length <= MaxPayForChars (defense in depth vs schema)
//
// What DecodePayArgs does NOT check (handled in HandlePay / Pay Command):
//
//   - Trim-emptiness of recipient: HandlePay trims and rejects after
//     normalization, so args carry the pre-trim text intact for debugging.
//   - Control-character scan on ForText: HandlePay's responsibility.
//   - Walk-in-flight / no-huddle / recipient-resolve / balance / self-pay:
//     world-state checks done by the sim.Pay Command on the world goroutine.
func DecodePayArgs(raw json.RawMessage) (any, error) {
	// Reject non-object payloads early. The Go json decoder happily
	// accepts `null` / a bare number / a string into our PayArgs struct,
	// leaving zero values that the downstream Recipient/Amount checks
	// then mis-report as "recipient is required" / "amount must be at
	// least 1". Tool args are always a JSON object by contract — the
	// crisper error matches the contract.
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("pay: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args PayArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("pay: malformed arguments: %w", err)
	}
	// Trailing-data check — matches the speak pattern. dec.More() reports
	// array/object continuation only, not a second top-level JSON value.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("pay: trailing data after JSON object")
		}
		return nil, fmt.Errorf("pay: malformed trailing data: %w", err)
	}
	if args.Recipient == "" {
		return nil, modelSafef("pay: recipient is required")
	}
	if n := utf8.RuneCountInString(args.Recipient); n > MaxPayRecipientChars {
		return nil, modelSafef(
			"pay: recipient exceeds %d-character cap (got %d characters)",
			MaxPayRecipientChars, n,
		)
	}
	if args.Amount < 1 {
		return nil, modelSafef("pay: amount must be at least 1 (got %d)", args.Amount)
	}
	if args.Amount > sim.MaxPayAmount {
		return nil, modelSafef("pay: amount exceeds maximum (got %d, max %d)", args.Amount, sim.MaxPayAmount)
	}
	if n := utf8.RuneCountInString(args.For); n > MaxPayForChars {
		return nil, modelSafef(
			"pay: 'for' text exceeds %d-character cap (got %d characters)",
			MaxPayForChars, n,
		)
	}
	return args, nil
}

// HandlePay is the CommitFn for the pay tool. Pure builder — it does NOT
// touch the world. Static validation that JSON Schema cannot express runs
// here (trim-empty recipient, control-char scan on ForText); world-state
// validation runs inside the returned sim.Pay Command's Fn on the world
// goroutine.
//
// Returns:
//
//   - sim.Pay Command on success — the harness submits it via
//     sim.RunTickToolCommand, which runs Fn on the world goroutine
//     atomically with the attempt-staleness check.
//   - typed error on static-validation failure — surfaces to the model
//     as a tool error so the model can retry.
func HandlePay(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(PayArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("pay: handler received unexpected args type %T", in.Args)
	}
	recipient := strings.TrimSpace(args.Recipient)
	if recipient == "" {
		return sim.Command{}, modelSafef("pay: recipient is empty after trim")
	}
	// Normalize ForText whitespace + reject ALL control chars. Unlike
	// speak's freeform `text` (where \n / \r / \t are legitimate paragraph
	// shaping the speech-renderer preserves), `for` is short-form metadata
	// embedded inline in relationship facts and the seller's perception
	// prompt — a literal newline there would split the warrant line and
	// forge prompt layout. `strings.Fields` collapses any run of Unicode
	// whitespace (spaces, tabs, newlines, NBSP) to single spaces; the
	// subsequent control-char scan catches everything else (DEL, C1,
	// invalid UTF-8). Empty ForText after normalize is fine — the field
	// is optional.
	forText := strings.Join(strings.Fields(args.For), " ")
	if forText != "" {
		if i := indexInvalidControlChar(forText); i >= 0 {
			return sim.Command{}, modelSafef(
				"pay: 'for' contains a disallowed control character at byte offset %d", i)
		}
	}
	// ZBBS-HOME-400: form/join the co-located huddle on the pay call itself so
	// a buyer can pay someone present without a separate prior speak. No-op when
	// already huddled, alone, or out of scope.
	now := time.Now().UTC()
	return withHuddleBootstrap(in.ActorID, now, sim.Pay(in.ActorID, recipient, args.Amount, forText, now)), nil
}
