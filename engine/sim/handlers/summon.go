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
type SummonArgs struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

// MaxSummonTargetChars caps the target length on the model-facing schema.
// Targets are display names or ids; 128 leaves generous headroom while
// bounding a pathological input before the world lookup (which would reject
// an unknown target anyway). Mirrors MaxMoveToStructureIDChars.
const MaxSummonTargetChars = 128

// MaxSummonReasonChars caps the optional reason. The reason rides into a
// canned Spoke line (bounded at MaxActionLogTextLen = 220 runes downstream);
// 220 here keeps the model-facing bound aligned with the speech truncation
// so the model isn't told it can write more than the engine will speak.
const MaxSummonReasonChars = 220

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
        }
    },
    "required": ["target"],
    "additionalProperties": false
}`)

// summonDescription is the tool description advertised to the model.
const summonDescription = "Send for another villager. A messenger will walk to them, deliver your summons, and return — this is NOT a teleport and takes time. You will walk to the summoning place to wait. Summoning ENDS your turn, so say anything you want the people around you to hear BEFORE you call summon. If the messenger cannot find them, it returns and tells you so."

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
		return nil, decodeErrf("summon: arguments must be a JSON object")
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
			return nil, decodeErrf("summon: trailing data after JSON object")
		}
		return nil, fmt.Errorf("summon: malformed trailing data: %w", err)
	}
	if args.Target == "" {
		return nil, decodeErrf("summon: target is required")
	}
	if n := utf8.RuneCountInString(args.Target); n > MaxSummonTargetChars {
		return nil, decodeErrf(
			"summon: target exceeds %d-character cap (got %d characters)", MaxSummonTargetChars, n)
	}
	if n := utf8.RuneCountInString(args.Reason); n > MaxSummonReasonChars {
		return nil, decodeErrf(
			"summon: reason exceeds %d-character cap (got %d characters)", MaxSummonReasonChars, n)
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
		return sim.Command{}, errors.New("summon: target is empty after trim")
	}
	if i := indexInvalidControlChar(target); i >= 0 {
		return sim.Command{}, fmt.Errorf(
			"summon: target contains a disallowed control character at byte offset %d", i)
	}
	reason := strings.TrimSpace(args.Reason)
	if reason != "" {
		if i := indexInvalidControlChar(reason); i >= 0 {
			return sim.Command{}, fmt.Errorf(
				"summon: reason contains a disallowed control character at byte offset %d", i)
		}
	}
	return sim.DispatchSummon(in.ActorID, sim.ActorID(target), reason, time.Now().UTC()), nil
}
