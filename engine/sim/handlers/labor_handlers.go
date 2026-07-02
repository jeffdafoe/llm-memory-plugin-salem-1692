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

// labor_handlers.go — LLM-26 tool handlers for the worker-initiated
// service-for-pay flow. Three tools:
//
//   - solicit_work — worker-side, proposes {employer, reward,
//                    duration_minutes}.
//   - accept_work  — employer-side, accepts a pending offer by labor_id.
//   - decline_work — employer-side, declines a pending offer by labor_id.
//
// Same three-stage split as the pay family (pay_with_item_handlers.go):
// narrow JSON schema → hardening decoder (null / bare-value / unknown-field
// / trailing-data tolerant, bounds-checked) → pure Handle<X> builder that
// trims + scans control chars and returns the sim.<X> Command. All
// world-state validation runs inside the Command Fn on the world goroutine
// (labor_commands.go).
//
// The labor tools are deliberately minimal — no consumers, no counter, no
// message fields. Everything but {employer, reward, duration} is negotiated
// in conversation. The one term the schema DOES carry beyond the MVP is the
// in-kind reward leg (reward_items, LLM-225): spoken hire terms like "a bowl
// of porridge for some help" must be expressible as enforceable contract
// terms, or the in-kind leg silently evaporates when the contract commits
// (the live Hannah Boggs Inn hires — workers bought the promised porridge
// with their own coins).

// MaxLaborEmployerNameChars caps the employer name field on the
// model-facing schema. Mirrors MaxPayWithItemNameChars.
const MaxLaborEmployerNameChars = 100

// laborRewardItemsSchemaFragment is the schema for solicit_work's in-kind
// reward leg (LLM-225). Structurally identical to payItemsSchemaFragment
// (same maxItems / rune / qty literals — the shared payItemList decode and
// validatePayItemsDecode enforce the same bounds), but with the direction
// REVERSED in the copy: these are goods the EMPLOYER hands over as pay, not
// goods the caller carries and offers. Reusing the pay fragment verbatim
// would tell the worker to name goods "you carry", steering the weak model
// away from the exact porridge-for-help case this field exists for.
const laborRewardItemsSchemaFragment = `{
        "type": "array",
        "maxItems": 8,
        "items": {
            "type": "object",
            "properties": {
                "item": {"type": "string", "minLength": 1, "maxLength": 64, "description": "Item kind the employer holds and will hand over as pay (e.g. 'porridge', 'bread')."},
                "qty": {"type": "integer", "minimum": 1, "maximum": 2147483647, "description": "How many of this item you are asking for."}
            },
            "required": ["item", "qty"],
            "additionalProperties": false
        },
        "description": "Optional goods you want as pay, handed over by the employer when the work is finished — use this when the agreed pay is a meal or goods rather than (or as well as) coins. The reward must include coins, goods, or both."
    }`

// ====================================================================
// solicit_work — worker-side offer creation
// ====================================================================

// SolicitWorkArgs is the decoded shape of the solicit_work tool's
// arguments.
//
// Schema-enforced constraints:
//   - employer:         minLength 1, maxLength MaxLaborEmployerNameChars
//   - reward:           integer, minimum 0, maximum math.MaxInt32 (coins may
//     be 0 when reward_items carries the payment — the combined-empty reject
//     is decoder + Command-side, matching the pay family's all-zero-offer rule)
//   - reward_items:     optional goods leg (LLM-225), payItemsSchemaFragment shape
//   - duration_minutes: integer, minimum MinLaborDurationMinutes, maximum MaxLaborDurationMinutes
type SolicitWorkArgs struct {
	Employer        string      `json:"employer"`
	Reward          int         `json:"reward"`
	RewardItems     payItemList `json:"reward_items"`
	DurationMinutes int         `json:"duration_minutes"`
}

var solicitWorkSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "employer": {
            "type": "string",
            "minLength": 1,
            "maxLength": 100,
            "description": "Display name of the person in your conversation you are offering to work for."
        },
        "reward": {
            "type": "integer",
            "minimum": 0,
            "maximum": 2147483647,
            "description": "Coins you want to be paid for the job, handed over when the work is finished. May be 0 if you are asking to be paid in goods via reward_items instead — but the reward must include coins, goods, or both."
        },
        "reward_items": ` + laborRewardItemsSchemaFragment + `,
        "duration_minutes": {
            "type": "integer",
            "minimum": 120,
            "maximum": 480,
            "description": "How long the job takes, in minutes. Pick a full stretch of work: 120 (2 hours), 240 (4 hours), 360 (6 hours), or 480 (8 hours). You are occupied for this whole time once they accept, and you cannot work past the employer's closing time."
        }
    },
    "required": ["employer", "reward", "duration_minutes"],
    "additionalProperties": false
}`)

const solicitWorkDescription = "Offer to do a job for another villager in your current conversation, for pay. " +
	"You set who you'll work for (employer), the pay you want — coins (reward), goods they hold (reward_items, e.g. a meal), or both — and how long it takes (duration_minutes — a real stretch of work: 2, 4, 6, or 8 hours). " +
	"This creates a pending offer they must accept or decline. " +
	"On accept you're paid when the work finishes — the coins and any goods are handed over together then — and you're occupied with the job the whole time; you get on with it rather than standing about talking. " +
	"If the pay you agreed out loud includes food or goods, name them in reward_items so the bargain is real. " +
	"What the work actually is, and any back-and-forth on terms, is up to your conversation — re-offer with new terms if they want something different."

// DecodeSolicitWorkArgs parses raw tool-call arguments into a
// SolicitWorkArgs. Checks: JSON parses, no trailing data, no unknown
// fields, required fields present, numeric bounds, employer rune cap.
// Trim-emptiness + control-char scan are deferred to HandleSolicitWork;
// world-state lookups (huddle, employer resolve, worker attribute) to
// sim.SolicitWork.
func DecodeSolicitWorkArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("solicit_work: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args SolicitWorkArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("solicit_work: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("solicit_work: trailing data after JSON object")
		}
		return nil, fmt.Errorf("solicit_work: malformed trailing data: %w", err)
	}

	if args.Employer == "" {
		return nil, modelSafef("solicit_work: employer is required")
	}
	if n := utf8.RuneCountInString(args.Employer); n > MaxLaborEmployerNameChars {
		return nil, modelSafef(
			"solicit_work: employer exceeds %d-character cap (got %d characters)",
			MaxLaborEmployerNameChars, n,
		)
	}
	if args.Reward < 0 {
		return nil, modelSafef("solicit_work: reward cannot be negative (got %d)", args.Reward)
	}
	// The pay-nothing hole (LLM-225): the reward must carry coins, goods, or
	// both. The coin floor only applies when no goods leg is offered.
	if args.Reward < sim.MinLaborReward && len(args.RewardItems) == 0 {
		return nil, modelSafef(
			"solicit_work: the reward must be worth something — ask for at least %d coin, or goods via reward_items, or both",
			sim.MinLaborReward,
		)
	}
	if args.Reward > sim.MaxLaborReward {
		return nil, modelSafef("solicit_work: reward exceeds maximum (got %d, max %d)", args.Reward, sim.MaxLaborReward)
	}
	if err := validatePayItemsDecode("solicit_work", "reward_items", args.RewardItems); err != nil {
		return nil, err
	}
	if args.DurationMinutes < sim.MinLaborDurationMinutes {
		return nil, modelSafef("solicit_work: duration_minutes must be at least %d (got %d)", sim.MinLaborDurationMinutes, args.DurationMinutes)
	}
	if args.DurationMinutes > sim.MaxLaborDurationMinutes {
		return nil, modelSafef("solicit_work: duration_minutes exceeds maximum (got %d, max %d)", args.DurationMinutes, sim.MaxLaborDurationMinutes)
	}
	return args, nil
}

// HandleSolicitWork is the CommitFn for the solicit_work tool. Pure builder
// — trims the employer name, rejects control chars, and returns the
// sim.SolicitWork Command wrapped in the co-presence huddle bootstrap (so a
// worker who walked up to the employer can offer on arrival without a
// separate prior speak — same as pay_with_item).
func HandleSolicitWork(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(SolicitWorkArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("solicit_work: handler received unexpected args type %T", in.Args)
	}

	employer := strings.TrimSpace(args.Employer)
	if employer == "" {
		return sim.Command{}, modelSafef("solicit_work: employer is empty after trim")
	}
	if i := indexStrictControlChar(employer); i >= 0 {
		return sim.Command{}, modelSafef(
			"solicit_work: employer contains a disallowed control character at byte offset %d", i)
	}
	rewardItems, err := buildPayItemInputs("solicit_work", "reward_items", args.RewardItems)
	if err != nil {
		return sim.Command{}, err
	}

	now := time.Now().UTC()
	return withHuddleBootstrap(in.ActorID, now, sim.SolicitWork(
		in.ActorID,
		employer,
		args.Reward,
		rewardItems,
		args.DurationMinutes,
		now,
	)), nil
}

// ====================================================================
// accept_work — employer-side accept
// ====================================================================

// AcceptWorkArgs is the decoded shape of the accept_work tool's arguments.
type AcceptWorkArgs struct {
	LaborID LenientID `json:"labor_id"`
}

var acceptWorkSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "labor_id": {
            "type": "integer",
            "minimum": 1,
            "description": "The numeric labor ID of the pending work offer to accept. You'll see this in your perception of the worker's offer."
        }
    },
    "required": ["labor_id"],
    "additionalProperties": false
}`)

const acceptWorkDescription = "Accept a pending work offer from a worker in your current conversation. " +
	"At acceptance the engine verifies you're both still in the same conversation and that you hold the offered reward — the coins and any goods asked for — and if a check fails the offer flips to a terminal failed state and nobody is hired. " +
	"On success the worker starts the job; nothing is taken from you now, but the reward is handed over when the work finishes, so you must still hold it then."

// DecodeAcceptWorkArgs parses raw args into an AcceptWorkArgs.
func DecodeAcceptWorkArgs(raw json.RawMessage) (any, error) {
	id, err := decodeLaborIDOnly(raw, "accept_work")
	if err != nil {
		return nil, err
	}
	return AcceptWorkArgs{LaborID: id}, nil
}

// HandleAcceptWork is the CommitFn for the accept_work tool. Pure builder.
func HandleAcceptWork(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(AcceptWorkArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("accept_work: handler received unexpected args type %T", in.Args)
	}
	return sim.AcceptWork(in.ActorID, sim.LaborID(args.LaborID), time.Now().UTC()), nil
}

// ====================================================================
// decline_work — employer-side decline
// ====================================================================

// DeclineWorkArgs is the decoded shape of the decline_work tool's
// arguments.
type DeclineWorkArgs struct {
	LaborID LenientID `json:"labor_id"`
}

var declineWorkSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "labor_id": {
            "type": "integer",
            "minimum": 1,
            "description": "The numeric labor ID of the pending work offer to decline."
        }
    },
    "required": ["labor_id"],
    "additionalProperties": false
}`)

const declineWorkDescription = "Decline a pending work offer from a worker in your current conversation. " +
	"No coins move and nobody is hired. If you want to explain or propose different terms, just say so in conversation."

// DecodeDeclineWorkArgs parses raw args into a DeclineWorkArgs.
func DecodeDeclineWorkArgs(raw json.RawMessage) (any, error) {
	id, err := decodeLaborIDOnly(raw, "decline_work")
	if err != nil {
		return nil, err
	}
	return DeclineWorkArgs{LaborID: id}, nil
}

// HandleDeclineWork is the CommitFn for the decline_work tool. Pure builder.
func HandleDeclineWork(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(DeclineWorkArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("decline_work: handler received unexpected args type %T", in.Args)
	}
	return sim.DeclineWork(in.ActorID, sim.LaborID(args.LaborID), time.Now().UTC()), nil
}

// ---- shared helpers --------------------------------------------------

// decodeLaborIDOnly handles the strict-object / no-trailing / unknown-
// fields / minimum-1 boilerplate for the two tools (accept_work,
// decline_work) that take only a labor_id. The labor analog of
// decodeLedgerOnly; LaborID is decoded via LenientID so the same weak-model
// "null" / numeric-string tolerance applies (LLM-42 readback).
func decodeLaborIDOnly(raw json.RawMessage, toolName string) (LenientID, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return 0, modelSafef("%s: arguments must be a JSON object", toolName)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args struct {
		LaborID LenientID `json:"labor_id"`
	}
	if err := dec.Decode(&args); err != nil {
		return 0, fmt.Errorf("%s: malformed arguments: %w", toolName, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return 0, modelSafef("%s: trailing data after JSON object", toolName)
		}
		return 0, fmt.Errorf("%s: malformed trailing data: %w", toolName, err)
	}
	if args.LaborID < 1 {
		return 0, modelSafef("%s: labor_id must be at least 1 (got %d)", toolName, args.LaborID)
	}
	return args.LaborID, nil
}
