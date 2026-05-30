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

// move_to.go — production move_to tool registration + handler, ZBBS-HOME-285.
//
// The model emits {"structure_id": "<id from perception>"}. DecodeMoveToArgs
// parses + applies the schema-bounded length check; HandleMoveTo normalizes
// the id (trim + control-char reject) and returns the sim.MoveToStructure
// Command, which runs on the world goroutine and does the world-state-dependent
// work: structure-exists / already-there / already-walking rejects, the
// enter-vs-visit derivation, and the MoveActor dispatch (see sim/move_to.go).

// MoveToArgs is the decoded shape of the move_to tool's arguments.
//
//   - structure_id: required, minLength 1, maxLength MaxMoveToStructureIDChars.
//     The id of a structure the NPC can see in its perception (its own
//     home/work, a shop, a place nearby).
//   - structure_name: optional ALTERNATIVE to structure_id — a place name the
//     NPC can see in its perception (its own home/work, a landmark nearby). The
//     engine resolves the name to a structure it could plausibly reach
//     (anchors + structures within scene radius, nearest-wins on duplicates,
//     ZBBS-HOME-356). Exactly one of structure_id / structure_name is required;
//     structure_id wins if both are somehow provided (the precise form).
type MoveToArgs struct {
	StructureID   string `json:"structure_id"`
	StructureName string `json:"structure_name"`
}

// MaxMoveToStructureIDChars caps the structure_id length on the model-facing
// schema. Structure ids are UUIDs (36 chars) or short slugs in tests; 128
// leaves generous headroom while bounding a pathological input before it
// reaches the world lookup (which would reject it as unknown anyway).
const MaxMoveToStructureIDChars = 128

// moveToSchema is the JSON Schema bytes shipped to the LLM provider. The
// structure_id length bound is restated as a literal because schema bytes are
// static — keep it in sync with DecodeMoveToArgs's defensive range check.
var moveToSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "structure_id": {
            "type": "string",
            "minLength": 1,
            "maxLength": 128,
            "description": "The id of the structure to walk to, taken from your perception (your home or workplace, or a place nearby). Preferred when you have it. The engine handles pathfinding and decides whether you go inside or stand just outside."
        },
        "structure_name": {
            "type": "string",
            "minLength": 1,
            "maxLength": 128,
            "description": "Alternative to structure_id: the NAME of a place you can see in your perception (e.g. \"the Tavern\", your home). Use this when you know the place by name but not its id. The engine resolves it to the nearest matching place you could reach. Provide structure_id OR structure_name, not both."
        }
    },
    "additionalProperties": false
}`)

// moveToDescription is the tool description advertised to the model. The
// schema's per-field description carries the arg guidance; this frames when to
// reach for the tool and the two things the model must understand about it:
// walking ends the turn, and it leaves any conversation.
const moveToDescription = "Walk to a structure you can see in your perception — your home, your workplace, a shop, the meeting house, or another place nearby. Give its structure_id if you have it, or its name (structure_name) — e.g. \"the Tavern\" — if you only know what it's called. The engine handles pathfinding and decides whether you go inside (your own home or work, an open shop) or stand just outside (a well, a closed building). Walking ENDS your turn, so say anything you want the people around you to hear BEFORE you call move_to. If you are in a conversation, choosing to walk away leaves it."

// DecodeMoveToArgs parses the raw tool-call arguments into a MoveToArgs.
// Errors are typed validation failures the harness surfaces to the model as
// tool errors (so the model can retry with corrected args).
//
// Checks:
//
//   - JSON parses, no trailing data, no unknown fields
//   - structure_id present and within the character cap
//
// What DecodeMoveToArgs does NOT check (handled in HandleMoveTo /
// MoveToStructure Command):
//
//   - Trim-emptiness + control-character scan of structure_id: HandleMoveTo.
//   - structure exists / already-there / already-walking / enter-vs-visit /
//     reachability: world state, done in the Command.
func DecodeMoveToArgs(raw json.RawMessage) (any, error) {
	// Reject non-object payloads early — same rationale as take_break/consume:
	// a bare null / number / string decodes quietly to zero values, producing
	// a misleading "structure_id is required" instead of a crisp "must be a
	// JSON object".
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("move_to: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args MoveToArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("move_to: malformed arguments: %w", err)
	}
	// Trailing-data check — matches the take_break/consume/pay pattern.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("move_to: trailing data after JSON object")
		}
		return nil, fmt.Errorf("move_to: malformed trailing data: %w", err)
	}
	// Exactly one of structure_id / structure_name. Neither → the model gave no
	// destination; both → ambiguous intent (rather than silently preferring one,
	// reject so the model picks the precise form). ZBBS-HOME-356.
	hasID := args.StructureID != ""
	hasName := args.StructureName != ""
	switch {
	case !hasID && !hasName:
		return nil, errors.New("move_to: provide structure_id or structure_name")
	case hasID && hasName:
		return nil, errors.New("move_to: provide structure_id OR structure_name, not both")
	}
	if n := utf8.RuneCountInString(args.StructureID); n > MaxMoveToStructureIDChars {
		return nil, fmt.Errorf(
			"move_to: structure_id exceeds %d-character cap (got %d characters)",
			MaxMoveToStructureIDChars, n,
		)
	}
	if n := utf8.RuneCountInString(args.StructureName); n > MaxMoveToStructureIDChars {
		return nil, fmt.Errorf(
			"move_to: structure_name exceeds %d-character cap (got %d characters)",
			MaxMoveToStructureIDChars, n,
		)
	}
	return args, nil
}

// HandleMoveTo is the CommitFn for the move_to tool. Pure builder — does NOT
// touch the world. Static validation that JSON Schema cannot express runs here
// (trim-empty id, control-char scan); world-state validation (structure
// exists, already-there, enter-vs-visit, MoveActor dispatch) runs inside the
// returned sim.MoveToStructure Command on the world goroutine.
func HandleMoveTo(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(MoveToArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("move_to: handler received unexpected args type %T", in.Args)
	}

	// Name path (ZBBS-HOME-356): resolve a perceivable place name engine-side.
	// Decode guarantees not-both; structure_id still wins when present (the
	// precise form) so this only runs for a name-only call.
	if args.StructureID == "" && args.StructureName != "" {
		name := strings.TrimSpace(args.StructureName)
		if name == "" {
			return sim.Command{}, errors.New("move_to: structure_name is empty after trim")
		}
		if i := indexInvalidControlChar(name); i >= 0 {
			return sim.Command{}, fmt.Errorf(
				"move_to: structure_name contains a disallowed control character at byte offset %d", i)
		}
		return sim.MoveToStructureByName(in.ActorID, name, time.Now().UTC()), nil
	}

	structureID := strings.TrimSpace(args.StructureID)
	if structureID == "" {
		return sim.Command{}, errors.New("move_to: structure_id is empty after trim")
	}
	// structure_id is an identifier — reject embedded control characters
	// (the same scan speak/take_break apply; \n\r\t pass through it, but a real
	// structure_id never contains whitespace, so the world lookup in the
	// Command rejects any that slip past as unknown). This catches the
	// genuinely malformed bytes (NUL, escape codes) early.
	if i := indexInvalidControlChar(structureID); i >= 0 {
		return sim.Command{}, fmt.Errorf(
			"move_to: structure_id contains a disallowed control character at byte offset %d", i)
	}
	return sim.MoveToStructure(in.ActorID, sim.StructureID(structureID), time.Now().UTC()), nil
}
