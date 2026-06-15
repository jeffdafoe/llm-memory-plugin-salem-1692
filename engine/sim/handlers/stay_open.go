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

// stay_open.go — production stay_open tool registration + handler (ZBBS-WORK-387).
//
// The wind-down-side twin of take_break. The model emits
// {"reason": "an order I still owe", "until_hour": 23}. DecodeStayOpenArgs parses
// + applies the schema-bounded length + the numeric range; HandleStayOpen
// normalizes the reason (trim + control-char reject); the returned sim.StayOpen
// Command runs on the world goroutine and does the world-state-dependent work:
// already-committed reject, timezone-anchored until_hour resolution (next
// occurrence / 24h cap), the OpenUntil stamp, and the StayingOpen emit (see
// sim/stay_open.go).

// StayOpenArgs is the decoded shape of the stay_open tool's arguments.
//
//   - reason:     required, minLength 1, maxLength MaxStayOpenReasonChars.
//   - until_hour: REQUIRED integer 0..23. A POINTER so decode can reject an
//     OMITTED value distinctly — "stay open until <unspecified>" is exactly the
//     vague non-decision the commit window replaces, and a plain int would alias
//     an omitted field with an explicit 0 (midnight), which is itself a valid
//     close hour here. So omitted → error, explicit 0 → midnight.
type StayOpenArgs struct {
	Reason    string `json:"reason"`
	UntilHour *int   `json:"until_hour,omitempty"`
}

// MaxStayOpenReasonChars caps the reason length on the model-facing schema.
// Matches MaxTakeBreakReasonChars — room for a short sentence without letting a
// pathological input bloat the action-log Text field (which AppendActionLogEntry
// rune-truncates anyway).
const MaxStayOpenReasonChars = 200

// stayOpenSchema is the JSON Schema bytes shipped to the LLM provider. The
// `until_hour` bounds (0..23) are restated as literals because schema bytes are
// static — keep them in sync with DecodeStayOpenArgs's defensive range check.
var stayOpenSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "reason": {
            "type": "string",
            "minLength": 1,
            "maxLength": 200,
            "description": "Short reason you are keeping your business open late (e.g. \"an order I still owe\", \"a customer is still here\"). Recorded in the action log."
        },
        "until_hour": {
            "type": "integer",
            "minimum": 0,
            "maximum": 23,
            "description": "Required. Hour on the 24-hour clock you will keep your business open until (e.g. 23 for 11pm, 1 for 1am the next morning). An hour earlier than now rolls to the next day, so you can commit to staying open past midnight."
        }
    },
    "required": ["reason", "until_hour"],
    "additionalProperties": false
}`)

// stayOpenDescription is the tool description advertised to the model. The
// schema's per-field descriptions carry the detailed guidance; this frames when
// to reach for the tool and what it costs/promises.
const stayOpenDescription = "Commit to keeping your business open past the end of your shift instead of closing up and heading off for the night. You MUST say what hour you will stay open until — the until_hour argument, on the 24-hour clock (e.g. 23 for 11pm, 1 for 1am the next morning). While committed you won't be nudged to wind down — but if you grow exhausted you will close early regardless. Reach for this when you have a concrete reason to stay open late (an order you still owe, a customer still present, work you want to finish). For stepping away to rest DURING the day, use take_break instead."

// DecodeStayOpenArgs parses the raw tool-call arguments into a StayOpenArgs.
// Errors are typed validation failures the harness surfaces to the model as
// tool errors (so the model can retry with corrected args).
//
// Checks:
//
//   - JSON parses, no trailing data, no unknown fields
//   - reason present and within the character cap
//   - until_hour PRESENT (required) and in [0, 23]
//
// What DecodeStayOpenArgs does NOT check (handled in HandleStayOpen / StayOpen
// Command): trim-emptiness + control-char scan of reason (HandleStayOpen's job);
// until_hour next-occurrence resolution + 24h cap (needs the world clock +
// timezone, runs inside sim.StayOpen); already-committed reject (world-state).
func DecodeStayOpenArgs(raw json.RawMessage) (any, error) {
	// Reject non-object payloads early — same rationale as take_break: a bare
	// null / number / string decodes quietly to zero values, producing a
	// misleading "reason is required" instead of a crisp "must be a JSON object".
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("stay_open: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args StayOpenArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("stay_open: malformed arguments: %w", err)
	}
	// Trailing-data check — matches the take_break/consume/pay pattern.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("stay_open: trailing data after JSON object")
		}
		return nil, fmt.Errorf("stay_open: malformed trailing data: %w", err)
	}
	if args.Reason == "" {
		return nil, modelSafef("stay_open: reason is required")
	}
	if n := utf8.RuneCountInString(args.Reason); n > MaxStayOpenReasonChars {
		return nil, modelSafef(
			"stay_open: reason exceeds %d-character cap (got %d characters)",
			MaxStayOpenReasonChars, n,
		)
	}
	// until_hour is REQUIRED — committing to stay open means committing to a
	// closing hour. An omitted hour is a contract violation, not a default request.
	if args.UntilHour == nil {
		return nil, modelSafef("stay_open: until_hour is required (the hour you will close, 0..23)")
	}
	if *args.UntilHour < 0 || *args.UntilHour > 23 {
		return nil, modelSafef(
			"stay_open: until_hour must be between 0 and 23 (got %d)",
			*args.UntilHour,
		)
	}
	return args, nil
}

// HandleStayOpen is the CommitFn for the stay_open tool. Pure builder — does NOT
// touch the world. Static validation that JSON Schema cannot express runs here
// (trim-empty reason, control-char scan); world-state validation
// (already-committed, until_hour resolution, mutation + emit) runs inside the
// returned sim.StayOpen Command on the world goroutine.
func HandleStayOpen(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(StayOpenArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("stay_open: handler received unexpected args type %T", in.Args)
	}
	reason := strings.TrimSpace(args.Reason)
	if reason == "" {
		return sim.Command{}, modelSafef("stay_open: reason is empty after trim")
	}
	// reason is freeform prose recorded in the action log — allow the same
	// \n/\r/\t set the speak/pay/take_break freeform text fields allow, reject
	// other control characters (typos at best, prompt-forge attempts at worst).
	if i := indexInvalidControlChar(reason); i >= 0 {
		return sim.Command{}, modelSafef(
			"stay_open: reason contains a disallowed control character at byte offset %d", i)
	}
	// until_hour is required; DecodeStayOpenArgs guarantees a non-nil, in-range
	// pointer, but guard defensively for direct/test callers.
	if args.UntilHour == nil {
		return sim.Command{}, modelSafef("stay_open: until_hour is required")
	}
	// until_hour resolution (timezone-anchored, next-occurrence, 24h cap) happens
	// inside sim.StayOpen on the world goroutine, where the commit clock +
	// WorldSettings.Location are available.
	return sim.StayOpen(in.ActorID, reason, *args.UntilHour, time.Now().UTC()), nil
}
