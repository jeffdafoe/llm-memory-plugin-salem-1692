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

// summon.go — the summon tool's handler half (ZBBS-HOME-311). Mirrors
// move_to.go's shape: DecodeSummonArgs validates the JSON-schema-bounded
// args; HandleSummon normalizes them and returns the sim.DispatchSummon
// Command, which runs on the world goroutine and does all world-state-
// dependent work (target exists, a messenger is free, a summon_point exists,
// the summoner can path there) — dispatching the errand or returning a
// rejection error the model sees as a tool error.
//
// What summon is: an NPC asks the engine to fetch another villager. It is
// NOT a teleport — the engine runs a messenger errand (summoner walks to a
// summon point, a messenger NPC carries the summons to the target and
// returns). The model only names a target and an optional reason; the legs,
// the messenger selection, and the canned speech are all engine-driven.
//
// Terminal: summon ends the tick (registered terminalOnSuccess=true), like
// move_to — committing to send for someone is the actor's action this turn.

// SummonArgs is the decoded shape of the summon tool's arguments.
//
//   - target: required, the display name / id of the villager to summon.
//   - reason: optional, a short in-character reason carried into the
//     delivery line ("<target>, <summoner> summons you. <reason>").
//   - say: optional (LLM-414), the summoner's own spoken acknowledgement,
//     emitted through the real speak pipeline right before the errand
//     dispatches. summon and speak are both terminal-on-success, so without
//     this the model had to choose between agreeing out loud and actually
//     summoning — the live incident chose the words and lost the deed.
type SummonArgs struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
	Say    string `json:"say"`
}

// MaxSummonTargetChars caps the target length on the model-facing schema.
// Targets are display names or ids; 128 leaves generous headroom while
// bounding a pathological input before the world lookup (which would reject
// an unknown target anyway). Mirrors MaxMoveToStructureIDChars.
const MaxSummonTargetChars = 128

// MaxSummonReasonChars caps the optional reason. The reason rides into a
// canned Spoke line, staying well within the MaxSpokenActionLogTextLen bound
// on spoken text downstream; 220 here keeps the model-facing bound aligned
// with the MaxSalientFactTextLen excerpt a peer forms from the line, so the
// model isn't told it can write more than a listener will remember.
const MaxSummonReasonChars = 220

// MaxSummonSayChars caps the optional say beat. It is a real utterance
// through the real speak pipeline, so it carries speak's own cap
// (MaxSpeakTextChars) — the schema restates the literal below.
const MaxSummonSayChars = MaxSpeakTextChars

// summonSchema is the JSON Schema bytes shipped to the LLM provider. The
// length bounds are restated as literals because schema bytes are static —
// keep them in sync with DecodeSummonArgs's defensive range checks.
var summonSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "target": {
            "type": "string",
            "minLength": 1,
            "maxLength": 128,
            "description": "The villager you want fetched — use a name of someone you know is in the village. A messenger will carry your summons to them; this is not a teleport."
        },
        "reason": {
            "type": "string",
            "maxLength": 220,
            "description": "Optional. A short reason the messenger relays, e.g. 'There is news of the trial.' Leave empty if you have none."
        },
        "say": {
            "type": "string",
            "maxLength": 1000,
            "description": "Optional. What you say aloud as you agree to send for them, spoken to whoever is with you before you step out — e.g. 'Aye, I'll have a messenger fetch him over for you.'"
        }
    },
    "required": ["target"],
    "additionalProperties": false
}`)

// summonDescription is the tool description advertised to the model. LLM-414
// rewrote it: the old text told the model to SAY its piece BEFORE calling
// summon, which under terminal-on-success speak guaranteed the summon never
// happened — the speak ended the turn and the agreement evaporated. Now the
// say argument carries the spoken reply inside the one call.
const summonDescription = "Send for another villager: a messenger will carry your summons to them and they will be asked to come to YOUR place. When someone asks you to fetch or summon a person, call THIS tool — do not merely say you will. Put your spoken reply in `say`; it is said aloud before you step out to dispatch the messenger. This is NOT a teleport and takes time. You will walk to the summoning place to send the messenger, then return to your business. If the messenger cannot find them, it returns and tells you so."

// DecodeSummonArgs parses the raw tool-call arguments into a SummonArgs.
// Typed validation failures surface to the model as tool errors.
//
// Checks: JSON parses, no trailing data, no unknown fields; target present
// and within its cap; reason within its cap (optional). Trim-emptiness and
// control-char scans run in HandleSummon; world-state checks run in the
// Command.
func DecodeSummonArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("summon: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args SummonArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("summon: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("summon: trailing data after JSON object")
		}
		return nil, fmt.Errorf("summon: malformed trailing data: %w", err)
	}
	if args.Target == "" {
		return nil, modelSafef("summon: target is required")
	}
	if n := utf8.RuneCountInString(args.Target); n > MaxSummonTargetChars {
		return nil, modelSafef(
			"summon: target exceeds %d-character cap (got %d characters)", MaxSummonTargetChars, n)
	}
	if n := utf8.RuneCountInString(args.Reason); n > MaxSummonReasonChars {
		return nil, modelSafef(
			"summon: reason exceeds %d-character cap (got %d characters)", MaxSummonReasonChars, n)
	}
	if n := utf8.RuneCountInString(args.Say); n > MaxSummonSayChars {
		return nil, modelSafef(
			"summon: say exceeds %d-character cap (got %d characters)", MaxSummonSayChars, n)
	}
	return args, nil
}

// HandleSummon is the CommitFn for the summon tool. Pure builder — does NOT
// touch the world. Static validation JSON Schema can't express runs here
// (trim-empty target, control-char scan); world-state validation (target
// exists, messenger free, summon_point exists, reachability) runs inside the
// returned sim.DispatchSummon Command on the world goroutine.
func HandleSummon(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(SummonArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("summon: handler received unexpected args type %T", in.Args)
	}
	target := strings.TrimSpace(args.Target)
	if target == "" {
		return sim.Command{}, modelSafef("summon: target is empty after trim")
	}
	if i := indexInvalidControlChar(target); i >= 0 {
		return sim.Command{}, modelSafef(
			"summon: target contains a disallowed control character at byte offset %d", i)
	}
	reason := strings.TrimSpace(args.Reason)
	if reason != "" {
		if i := indexInvalidControlChar(reason); i >= 0 {
			return sim.Command{}, modelSafef(
				"summon: reason contains a disallowed control character at byte offset %d", i)
		}
	}
	say := strings.TrimSpace(args.Say)
	if say != "" {
		if i := indexInvalidControlChar(say); i >= 0 {
			return sim.Command{}, modelSafef(
				"summon: say contains a disallowed control character at byte offset %d", i)
		}
	}
	// Pass the raw target STRING through to DispatchSummon, which resolves it
	// to an actor id on the world goroutine (name → id; the handler is a pure
	// builder with no world access). Before LLM-323 this cast the display name
	// straight to an ActorID, so the world's exact-id lookup never matched.
	return sim.DispatchSummon(in.ActorID, target, reason, say, time.Now().UTC()), nil
}
