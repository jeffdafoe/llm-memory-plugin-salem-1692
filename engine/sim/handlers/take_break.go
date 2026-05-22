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

// take_break.go — production take_break tool registration + handler,
// ZBBS-HOME-284 #4.
//
// The model emits {"reason": "feeling unwell", "until_hour": 13} (until_hour
// optional). DecodeTakeBreakArgs parses + applies schema-bounded length + the
// numeric range; HandleTakeBreak normalizes the reason (trim + control-char
// reject); the returned sim.TakeBreak Command runs on the world goroutine and
// does the world-state-dependent work: already-on-break reject, timezone-
// anchored until_hour resolution (past-hour reject / 4h default / 24h cap),
// the BreakUntil + recovery-cursor + StateResting stamp, occupancy refresh,
// and the TookBreak emit (see sim/take_break.go).

// TakeBreakArgs is the decoded shape of the take_break tool's arguments.
//
//   - reason:     required, minLength 1, maxLength MaxTakeBreakReasonChars.
//   - until_hour: optional integer 1..23. Absent decodes to 0, which the
//     Command reads as "use the default break length".
type TakeBreakArgs struct {
	Reason    string `json:"reason"`
	UntilHour int    `json:"until_hour"`
}

// MaxTakeBreakReasonChars caps the reason length on the model-facing schema.
// 200 leaves room for a short sentence ("feeling unwell and need to rest a
// while") without letting a pathological input bloat the action-log Text field
// (which AppendActionLogEntry rune-truncates at MaxActionLogTextLen anyway).
const MaxTakeBreakReasonChars = 200

// takeBreakSchema is the JSON Schema bytes shipped to the LLM provider. The
// `until_hour` bounds (1..23) are restated as literals because schema bytes are
// static — keep them in sync with DecodeTakeBreakArgs's defensive range check.
var takeBreakSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "reason": {
            "type": "string",
            "minLength": 1,
            "maxLength": 200,
            "description": "Short reason you are stepping away from your post (e.g. \"feeling unwell\", \"need to rest a while\"). Recorded in the action log."
        },
        "until_hour": {
            "type": "integer",
            "minimum": 1,
            "maximum": 23,
            "description": "Optional. Hour on the 24-hour clock to stay on break until (e.g. 13 means 1pm). Must be later today than the current hour. Omit for a 4-hour break. take_break is for stepping away during the day — overnight rest is handled automatically by the sleep cycle."
        }
    },
    "required": ["reason"],
    "additionalProperties": false
}`)

// takeBreakDescription is the tool description advertised to the model. The
// schema's per-field descriptions carry the detailed guidance; this frames when
// to reach for the tool and what it costs (you stop being open for business).
const takeBreakDescription = "Step away from your post to rest for a while — you close up and stop being counted as open for business, so others won't expect service from you. Your tiredness slowly recovers while you are on break. Use this when you are too tired or unwell to keep working your shift. For overnight rest, the sleep cycle handles it automatically — take_break is for stepping away during the day."

// DecodeTakeBreakArgs parses the raw tool-call arguments into a TakeBreakArgs.
// Errors are typed validation failures the harness surfaces to the model as
// tool errors (so the model can retry with corrected args).
//
// Checks:
//
//   - JSON parses, no trailing data, no unknown fields
//   - reason present and within the character cap
//   - until_hour in [0, 23] (0 = omitted → default; 1..23 = a target hour)
//
// What DecodeTakeBreakArgs does NOT check (handled in HandleTakeBreak /
// TakeBreak Command):
//
//   - Trim-emptiness + control-character scan of reason: HandleTakeBreak's job.
//   - until_hour past-today / 4h default / 24h cap: needs the world clock +
//     timezone, so it runs inside sim.TakeBreak on the world goroutine.
//   - already-on-break reject: world-state, done in the Command.
func DecodeTakeBreakArgs(raw json.RawMessage) (any, error) {
	// Reject non-object payloads early — same rationale as consume/pay: a bare
	// null / number / string decodes quietly to zero values, producing a
	// misleading "reason is required" instead of a crisp "must be a JSON
	// object".
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("take_break: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args TakeBreakArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("take_break: malformed arguments: %w", err)
	}
	// Trailing-data check — matches the consume/pay/speak pattern.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("take_break: trailing data after JSON object")
		}
		return nil, fmt.Errorf("take_break: malformed trailing data: %w", err)
	}
	if args.Reason == "" {
		return nil, errors.New("take_break: reason is required")
	}
	if n := utf8.RuneCountInString(args.Reason); n > MaxTakeBreakReasonChars {
		return nil, fmt.Errorf(
			"take_break: reason exceeds %d-character cap (got %d characters)",
			MaxTakeBreakReasonChars, n,
		)
	}
	// until_hour is optional; absent decodes to 0 (→ default break length). The
	// schema bounds a present value to 1..23, but a non-handler caller (tests,
	// future in-engine paths) could pass out of range, so defend here too.
	if args.UntilHour < 0 || args.UntilHour > 23 {
		return nil, fmt.Errorf(
			"take_break: until_hour must be between 1 and 23 (got %d); omit it for a default-length break",
			args.UntilHour,
		)
	}
	return args, nil
}

// HandleTakeBreak is the CommitFn for the take_break tool. Pure builder — does
// NOT touch the world. Static validation that JSON Schema cannot express runs
// here (trim-empty reason, control-char scan); world-state validation
// (already-on-break, until_hour resolution, mutation + emit) runs inside the
// returned sim.TakeBreak Command on the world goroutine.
func HandleTakeBreak(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(TakeBreakArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("take_break: handler received unexpected args type %T", in.Args)
	}
	reason := strings.TrimSpace(args.Reason)
	if reason == "" {
		return sim.Command{}, errors.New("take_break: reason is empty after trim")
	}
	// reason is freeform prose recorded in the action log — allow the same
	// \n/\r/\t set the speak/pay freeform text fields allow, reject other
	// control characters (typos at best, prompt-forge attempts at worst).
	if i := indexInvalidControlChar(reason); i >= 0 {
		return sim.Command{}, fmt.Errorf(
			"take_break: reason contains a disallowed control character at byte offset %d", i)
	}
	// until_hour resolution (timezone-anchored, past-hour reject, default + cap)
	// happens inside sim.TakeBreak on the world goroutine, where the commit
	// clock + WorldSettings.Location are available.
	return sim.TakeBreak(in.ActorID, reason, args.UntilHour, time.Now().UTC()), nil
}
