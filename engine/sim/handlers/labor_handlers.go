package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// labor_handlers.go — LLM-26 tool handlers for the service-for-pay flow. Four
// tools:
//
//   - solicit_work — worker-side mint, proposes {employer, reward,
//                    duration_minutes}.
//   - offer_work   — employer-side mint, proposes {worker, reward,
//                    duration_minutes, say} (LLM-346).
//   - accept_work  — responder-side, accepts a pending offer by labor_id,
//                    speaking its `say` as the hire is struck (LLM-350).
//   - decline_work — responder-side, declines a pending offer by labor_id,
//                    speaking its `say` as it refuses (LLM-350).
//
// Same three-stage split as the pay family (pay_with_item_handlers.go):
// narrow JSON schema → hardening decoder (null / bare-value / unknown-field
// / trailing-data tolerant, bounds-checked) → pure Handle<X> builder that
// trims + scans control chars and returns the sim.<X> Command. All
// world-state validation runs inside the Command Fn on the world goroutine
// (labor_commands.go).
//
// The labor tools are deliberately minimal — no consumers, no counter. Everything
// but {counterparty, reward, duration} is negotiated in conversation. Two terms
// the schema DOES carry beyond the MVP:
//
//   - the in-kind reward leg (reward_items, LLM-225): spoken hire terms like "a
//     bowl of porridge for some help" must be expressible as enforceable contract
//     terms, or the in-kind leg silently evaporates when the contract commits
//     (the live Hannah Boggs Inn hires — workers bought the promised porridge
//     with their own coins).
//   - offer_work's spoken line (say, LLM-346): offer_work and speak are both
//     tick-terminal, so a keeper who voices "would you lend a hand?" with speak
//     ends her turn and the offer is never posted. Folding the utterance into the
//     tool makes asking and offering one act, which is what they are in the
//     fiction. Exactly the shape LLM-343 gave sell.
//   - the same `say`, on accept_work and decline_work (LLM-350): the answer to an
//     offer is terminal too, so an NPC could take a job or refuse one but never
//     say so. The cue told it to do both and one of the two was always skipped.
//
// solicit_work is NOT given a `say` here. It carries the same latent collision —
// it has been terminal since LLM-180, speak became terminal in LLM-321 — but
// nothing instructs the worker to announce first (renderLaborAffordance names
// only the tool), so there is no live failure to fix and adding the field is an
// unforced change beyond this ticket's scope. The hiring cue, by contrast, has to
// hand the keeper a way to voice the request: an offer of work that arrives in
// silence is not the scene.

// MaxLaborEmployerNameChars caps the employer name field on the
// model-facing schema. Mirrors MaxPayWithItemNameChars.
const MaxLaborEmployerNameChars = 100

// MaxLaborWorkerNameChars caps offer_work's worker name field. Same cap and
// rationale as MaxLaborEmployerNameChars — canonical "First Last" headroom.
const MaxLaborWorkerNameChars = 100

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
            "minimum": 240,
            "maximum": 480,
            "description": "How long the job takes, in minutes. Pick a full stretch of work: 240 (4 hours), 360 (6 hours), or 480 (8 hours). You are occupied for this whole time once they accept, and you cannot work past the employer's closing time."
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
// offer_work — employer-side offer creation (LLM-346)
// ====================================================================

// laborWageItemsSchemaFragment is the schema for offer_work's in-kind wage leg.
// The same shape as laborRewardItemsSchemaFragment, with the copy written from
// the EMPLOYER's side: these are goods the caller holds and will hand over,
// not goods the caller is asking someone else for. The direction has to read
// correctly or the weak model names wares it does not own.
const laborWageItemsSchemaFragment = `{
        "type": "array",
        "maxItems": 8,
        "items": {
            "type": "object",
            "properties": {
                "item": {"type": "string", "minLength": 1, "maxLength": 64, "description": "Item kind you hold and will hand over as pay (e.g. 'porridge', 'bread')."},
                "qty": {"type": "integer", "minimum": 1, "maximum": 2147483647, "description": "How many of this item you are offering."}
            },
            "required": ["item", "qty"],
            "additionalProperties": false
        },
        "description": "Optional goods you will hand over as pay when the work is finished — use this when the pay you agreed is a meal or goods rather than (or as well as) coins. The pay must include coins, goods, or both."
    }`

// OfferWorkArgs is the decoded shape of the offer_work tool's arguments.
//
// Schema-enforced constraints:
//   - worker:           minLength 1, maxLength MaxLaborWorkerNameChars
//   - reward:           integer, minimum 0, maximum math.MaxInt32 (coins may be
//     0 when reward_items carries the wage)
//   - reward_items:     optional goods leg, laborWageItemsSchemaFragment shape
//   - duration_minutes: integer, minimum MinLaborDurationMinutes, maximum MaxLaborDurationMinutes
//   - say:              optional, maxLength MaxSpeakTextChars
type OfferWorkArgs struct {
	Worker          string      `json:"worker"`
	Reward          int         `json:"reward"`
	RewardItems     payItemList `json:"reward_items"`
	DurationMinutes int         `json:"duration_minutes"`
	// Say is the employer's spoken request, delivered as the offer is posted
	// (LLM-346). See the file header: both offer_work and speak end the tick, so
	// asking aloud and offering must be one call. Optional — a silent offer is
	// legal, if a little brusque.
	Say string `json:"say"`
}

var offerWorkSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "worker": {
            "type": "string",
            "minLength": 1,
            "maxLength": 100,
            "description": "Display name of the person in your conversation you are offering the job to. They must be someone who takes work for pay — your perception names them."
        },
        "reward": {
            "type": "integer",
            "minimum": 0,
            "maximum": 2147483647,
            "description": "Coins you will pay for the job, handed over when the work is finished. May be 0 if you are paying in goods via reward_items instead — but the pay must include coins, goods, or both."
        },
        "reward_items": ` + laborWageItemsSchemaFragment + `,
        "duration_minutes": {
            "type": "integer",
            "minimum": 240,
            "maximum": 480,
            "description": "How long the job takes, in minutes. Pick a full stretch of work: 240 (4 hours), 360 (6 hours), or 480 (8 hours). They are occupied for this whole time once they accept, and no one works past your closing time."
        },
        "say": {
            "type": "string",
            "maxLength": 1000,
            "description": "What you say aloud as you ask, in your own voice (e.g. 'There's sorting to be done with the shelves and the herbs — four coins for the afternoon, if you're willing.'). Spoken to the worker you named. Optional: omit to make the offer without a word."
        }
    },
    "required": ["worker", "reward", "duration_minutes"],
    "additionalProperties": false
}`)

const offerWorkDescription = "Ask another villager in your current conversation to do a job for you, for pay. " +
	"This is the transactional surface — speech that mentions a job is just talk, this is what they can actually accept. " +
	"You set who works (worker — they must take work for pay; your perception names who does), the pay you will hand over when the work is finished — coins (reward), goods you hold (reward_items, e.g. a meal), or both — and how long it takes (duration_minutes — a real stretch of work: 2, 4, 6, or 8 hours). " +
	"Ask them aloud in the same breath by passing `say` — do NOT ask with the speak tool and then call this, because speaking ends your turn and the offer would never be made. " +
	"This creates a pending offer they must accept or decline. On accept they come to your workplace and get to work, and you pay when the job finishes — so you must still hold the pay then. " +
	"What the work actually is, and any back-and-forth on terms, is up to your conversation — re-offer with new terms if they want something different."

// DecodeOfferWorkArgs parses raw tool-call arguments into an OfferWorkArgs.
// Checks: JSON parses, no trailing data, no unknown fields, required fields
// present, numeric bounds, worker rune cap, say rune cap. Trim-emptiness +
// control-char scans are deferred to HandleOfferWork; world-state lookups
// (huddle, worker resolve, worker attribute, means-to-pay) to sim.OfferWork.
func DecodeOfferWorkArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("offer_work: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args OfferWorkArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("offer_work: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("offer_work: trailing data after JSON object")
		}
		return nil, fmt.Errorf("offer_work: malformed trailing data: %w", err)
	}

	if args.Worker == "" {
		return nil, modelSafef("offer_work: worker is required")
	}
	if n := utf8.RuneCountInString(args.Worker); n > MaxLaborWorkerNameChars {
		return nil, modelSafef(
			"offer_work: worker exceeds %d-character cap (got %d characters)",
			MaxLaborWorkerNameChars, n,
		)
	}
	if args.Reward < 0 {
		return nil, modelSafef("offer_work: reward cannot be negative (got %d)", args.Reward)
	}
	// The pay-nothing hole (LLM-225): the wage must carry coins, goods, or both.
	// The coin floor only applies when no goods leg is offered.
	if args.Reward < sim.MinLaborReward && len(args.RewardItems) == 0 {
		return nil, modelSafef(
			"offer_work: the pay must be worth something — offer at least %d coin, or goods via reward_items, or both",
			sim.MinLaborReward,
		)
	}
	if args.Reward > sim.MaxLaborReward {
		return nil, modelSafef("offer_work: reward exceeds maximum (got %d, max %d)", args.Reward, sim.MaxLaborReward)
	}
	if err := validatePayItemsDecode("offer_work", "reward_items", args.RewardItems); err != nil {
		return nil, err
	}
	if args.DurationMinutes < sim.MinLaborDurationMinutes {
		return nil, modelSafef("offer_work: duration_minutes must be at least %d (got %d)", sim.MinLaborDurationMinutes, args.DurationMinutes)
	}
	if args.DurationMinutes > sim.MaxLaborDurationMinutes {
		return nil, modelSafef("offer_work: duration_minutes exceeds maximum (got %d, max %d)", sim.MaxLaborDurationMinutes, args.DurationMinutes)
	}
	// say shares speak's rune cap — it lands on the same utterance path, so a line
	// speak would refuse must not sneak in through offer_work (mirrors sell).
	if n := utf8.RuneCountInString(args.Say); n > MaxSpeakTextChars {
		return nil, modelSafef(
			"offer_work: say exceeds %d-character cap (got %d characters)",
			MaxSpeakTextChars, n,
		)
	}
	// Same utterance path ⇒ same mojibake guard as speak (LLM-235).
	if err := checkUtteranceText("offer_work", "say", args.Say); err != nil {
		return nil, err
	}
	return args, nil
}

// HandleOfferWork is the CommitFn for the offer_work tool. Pure builder — trims
// the worker name, rejects control chars, and returns the sim.OfferWork Command
// wrapped in the co-presence huddle bootstrap (so a keeper can hire a customer
// who has walked up to her counter without a separate prior speak — same as
// solicit_work and pay_with_item).
//
// When a `say` line rides along, the offer is minted FIRST and the words follow
// (LLM-343's ordering, for its reason): if the offer is refused — the named person
// takes no work, the keeper cannot cover the wage — nothing has been said, and she
// never asks aloud for help she cannot engage. The reverse order would have her
// voice a bargain that does not exist.
//
// The speak is best-effort in the other direction. SpeakTo keeps gates OfferWork
// does not (the vocative gate; the turn-state "you are owed a reply" gate), and a
// hire must not be lost to a conversational-discipline rule — so the offer stands,
// Announced stays false, and SpeakTo's own reason rides back on the result rather
// than the tool guessing which gate refused it.
func HandleOfferWork(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(OfferWorkArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("offer_work: handler received unexpected args type %T", in.Args)
	}

	worker := strings.TrimSpace(args.Worker)
	if worker == "" {
		return sim.Command{}, modelSafef("offer_work: worker is empty after trim")
	}
	if i := indexStrictControlChar(worker); i >= 0 {
		return sim.Command{}, modelSafef(
			"offer_work: worker contains a disallowed control character at byte offset %d", i)
	}
	rewardItems, err := buildPayItemInputs("offer_work", "reward_items", args.RewardItems)
	if err != nil {
		return sim.Command{}, err
	}
	// The spoken line is prose, so it takes speak's permissive control-char scan
	// (\n \r \t allowed) rather than the strict identifier scan the name field uses.
	say := strings.TrimSpace(args.Say)
	if say != "" {
		if i := indexInvalidControlChar(say); i >= 0 {
			return sim.Command{}, modelSafef(
				"offer_work: say contains a disallowed control character at byte offset %d "+
					"(only \\n, \\r, \\t allowed)", i)
		}
	}

	// Captured outside the closure — the harness may reuse `in` across iterations
	// (same rationale as HandleSpeak / HandleSceneQuote).
	actorID := in.ActorID
	hasNewNews := in.HasNewNews

	now := time.Now().UTC()
	offer := sim.OfferWork(actorID, worker, args.Reward, rewardItems, args.DurationMinutes, now)
	if say == "" {
		return withHuddleBootstrap(actorID, now, offer), nil
	}

	return withHuddleBootstrap(actorID, now, sim.Command{Fn: func(w *sim.World) (any, error) {
		res, err := offer.Fn(w)
		if err != nil {
			return nil, err
		}
		placed, ok := res.(sim.LaborOfferResult)
		if !ok {
			return res, nil
		}
		// The offer names one worker, so the request is spoken to them.
		if _, serr := sim.SpeakTo(actorID, say, worker, nil, hasNewNews, now).Fn(w); serr != nil {
			log.Printf("sim/handlers: offer_work placed offer %d but its say was refused: %v", placed.ID, serr)
			placed.SayRefused = serr.Error()
			return placed, nil
		}
		placed.Announced = true
		return placed, nil
	}}), nil
}

// ====================================================================
// accept_work — responder-side accept
// ====================================================================

// AcceptWorkArgs is the decoded shape of the accept_work tool's arguments.
type AcceptWorkArgs struct {
	LaborID LenientID `json:"labor_id"`
	// Say is the acceptor's spoken line, delivered as the hire is struck
	// (LLM-350). Optional — a wordless accept is legal, if a little cold.
	Say string `json:"say"`
}

var acceptWorkSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "labor_id": {
            "type": "integer",
            "minimum": 1,
            "description": "The numeric labor ID of the pending work offer to accept. You'll see this in your perception of the worker's offer."
        },
        "say": {
            "type": "string",
            "maxLength": 1000,
            "description": "What you say aloud as you agree, in your own voice (e.g. 'Aye, I'll lend a hand — lead on.'). Spoken to the other party. Optional: omit to accept without a word."
        }
    },
    "required": ["labor_id"],
    "additionalProperties": false
}`)

const acceptWorkDescription = "Accept a pending work offer from someone in your current conversation. " +
	"At acceptance the engine verifies you're both still in the same conversation and that the employer holds the offered reward — the coins and any goods asked for — and if a check fails the offer flips to a terminal failed state and nobody is hired. " +
	"On success the work begins; nothing changes hands now, but the reward is handed over when the work finishes. " +
	"Agree aloud in the same breath by passing `say` — do NOT reply with the speak tool, because speaking ends your turn and the offer would go unanswered."

// DecodeAcceptWorkArgs parses raw args into an AcceptWorkArgs.
func DecodeAcceptWorkArgs(raw json.RawMessage) (any, error) {
	id, say, err := decodeLaborIDAndSay(raw, "accept_work")
	if err != nil {
		return nil, err
	}
	return AcceptWorkArgs{LaborID: id, Say: say}, nil
}

// HandleAcceptWork is the CommitFn for the accept_work tool. Pure builder.
//
// Unlike the pay responses, accept_work's spoken line is threaded INTO the
// substrate Command rather than wrapped around it. A relocating accept sends the
// worker walking to the employer's post and drops them out of the huddle
// (sendWorkerToWorkplace), and SpeakTo refuses a walker and refuses a speaker with
// no audience — so a line spoken after this Command returns would be lost exactly
// when a worker takes a job offered away from the shop. sim.AcceptWorkSaying
// speaks it while they still stand together. LLM-350.
func HandleAcceptWork(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(AcceptWorkArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("accept_work: handler received unexpected args type %T", in.Args)
	}
	say, err := normalizeSayLine("accept_work", args.Say)
	if err != nil {
		return sim.Command{}, err
	}
	return sim.AcceptWorkSaying(in.ActorID, sim.LaborID(args.LaborID), say, in.HasNewNews, time.Now().UTC()), nil
}

// ====================================================================
// decline_work — responder-side decline
// ====================================================================

// DeclineWorkArgs is the decoded shape of the decline_work tool's
// arguments.
type DeclineWorkArgs struct {
	LaborID LenientID `json:"labor_id"`
	// Say is the refusal, spoken as the offer is declined (LLM-350). The old
	// tool description told the caller to "just say so in conversation" — which
	// speak, being terminal, could not do after the decline and would have
	// skipped the decline if done before it.
	Say string `json:"say"`
}

var declineWorkSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "labor_id": {
            "type": "integer",
            "minimum": 1,
            "description": "The numeric labor ID of the pending work offer to decline."
        },
        "say": {
            "type": "string",
            "maxLength": 1000,
            "description": "What you say aloud as you refuse, in your own voice (e.g. 'Not today — I've not the coin to spare.'). Spoken to the other party. Optional: omit to decline without a word."
        }
    },
    "required": ["labor_id"],
    "additionalProperties": false
}`)

const declineWorkDescription = "Decline a pending work offer from someone in your current conversation. No coins move and nobody is hired. " +
	"Refuse them aloud in the same breath by passing `say` — do NOT reply with the speak tool, because speaking ends your turn and the offer would go unanswered. " +
	"To propose different terms instead, decline and name them in your `say`."

// DecodeDeclineWorkArgs parses raw args into a DeclineWorkArgs.
func DecodeDeclineWorkArgs(raw json.RawMessage) (any, error) {
	id, say, err := decodeLaborIDAndSay(raw, "decline_work")
	if err != nil {
		return nil, err
	}
	return DeclineWorkArgs{LaborID: id, Say: say}, nil
}

// HandleDeclineWork is the CommitFn for the decline_work tool.
//
// A decline moves no one and dissolves no huddle, so the refusal can be spoken
// after the Command commits — the handler-level composite the pay responses use,
// not accept_work's threaded one. Silence on a refused decline: an offer that was
// no longer the caller's to answer errors out before the words go anywhere.
func HandleDeclineWork(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(DeclineWorkArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("decline_work: handler received unexpected args type %T", in.Args)
	}
	say, err := normalizeSayLine("decline_work", args.Say)
	if err != nil {
		return sim.Command{}, err
	}
	actorID := in.ActorID
	hasNewNews := in.HasNewNews
	now := time.Now().UTC()
	laborID := sim.LaborID(args.LaborID)
	decline := sim.DeclineWork(actorID, laborID, now)
	if say == "" {
		return decline, nil
	}
	return sim.Command{Fn: func(w *sim.World) (any, error) {
		to := laborCounterpartyName(w, actorID, laborID)
		res, err := decline.Fn(w)
		if err != nil {
			return nil, err
		}
		declined, ok := res.(sim.LaborDeclineResult)
		if !ok {
			return res, nil
		}
		declined.Announced, declined.SayRefused = sim.SpeakAlongside(
			w, actorID, say, to, hasNewNews, now,
			fmt.Sprintf("decline_work refused offer %d", laborID),
		)
		return declined, nil
	}}, nil
}

// ---- shared helpers --------------------------------------------------

// laborCounterpartyName resolves the display name of the party on the other side
// of laborID from callerID. Empty when it can't be resolved, which addresses the
// utterance to the whole huddle rather than failing — SpeakTo's vocative gate
// must never cost the caller their answer.
func laborCounterpartyName(w *sim.World, callerID sim.ActorID, laborID sim.LaborID) string {
	offer, ok := w.LaborLedger[laborID]
	if !ok || offer == nil {
		return ""
	}
	other := offer.Initiator()
	if other == callerID {
		other = offer.Responder()
	}
	peer, ok := w.Actors[other]
	if !ok || peer == nil {
		return ""
	}
	return peer.DisplayName
}

// decodeLaborIDAndSay handles the strict-object / no-trailing / unknown-
// fields / minimum-1 boilerplate for the two tools (accept_work,
// decline_work) that take a labor_id and an optional spoken line. The labor
// analog of decodeLedgerOnly; LaborID is decoded via LenientID so the same
// weak-model "null" / numeric-string tolerance applies (LLM-42 readback).
//
// say shares speak's rune cap — it lands on the same utterance path, so a line
// speak would refuse must not sneak in through a labor response (mirrors sell).
func decodeLaborIDAndSay(raw json.RawMessage, toolName string) (LenientID, string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return 0, "", modelSafef("%s: arguments must be a JSON object", toolName)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args struct {
		LaborID LenientID `json:"labor_id"`
		Say     string    `json:"say"`
	}
	if err := dec.Decode(&args); err != nil {
		return 0, "", fmt.Errorf("%s: malformed arguments: %w", toolName, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return 0, "", modelSafef("%s: trailing data after JSON object", toolName)
		}
		return 0, "", fmt.Errorf("%s: malformed trailing data: %w", toolName, err)
	}
	if args.LaborID < 1 {
		return 0, "", modelSafef("%s: labor_id must be at least 1 (got %d)", toolName, args.LaborID)
	}
	if n := utf8.RuneCountInString(args.Say); n > MaxSpeakTextChars {
		return 0, "", modelSafef(
			"%s: say exceeds %d-character cap (got %d characters)", toolName, MaxSpeakTextChars, n)
	}
	// Same utterance path ⇒ same mojibake guard as speak (LLM-235). toolName
	// scopes the error (accept_work / decline_work share this decoder).
	if err := checkUtteranceText(toolName, "say", args.Say); err != nil {
		return 0, "", err
	}
	return args.LaborID, args.Say, nil
}
