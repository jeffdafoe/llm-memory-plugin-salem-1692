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

// move_to.go — production move_to tool registration + handler, ZBBS-HOME-285.
//
// The model emits {"destination": "<a place name, a home/work keyword, or an id
// shown in perception>"}. DecodeMoveToArgs parses + applies the schema-bounded
// length check; HandleMoveTo normalizes the value (trim + control-char reject)
// and returns the sim.MoveToDestination Command, which runs on the world
// goroutine and does the world-state-dependent work: id-first-else-name
// resolution, structure-exists / already-there / already-walking rejects, the
// enter-vs-visit derivation, and the MoveActor dispatch (see sim/move_to.go).

// MoveToArgs is the decoded shape of the move_to tool's arguments.
//
//   - destination: required, minLength 1, maxLength MaxMoveToDestinationChars.
//     Where the NPC wants to walk — a place NAME it can see in perception ("the
//     Tavern", "the Well"), a "home"/"work" keyword, or the id shown for a place
//     in a perception cue. The Command resolves id-first, else by name (LLM-320).
//     The former split structure_id / structure_name fields collapsed into this
//     one generic arg: varied models fumbled the two-field oneOf and the
//     engine-jargon names, reaching for "location"/"place" unprompted.
type MoveToArgs struct {
	Destination string `json:"destination"`
}

// MaxMoveToDestinationChars caps the destination length on the model-facing
// schema. Structure ids are UUIDs (36 chars) or short slugs; place names are
// short; 128 leaves generous headroom while bounding a pathological input
// before it reaches the world lookup (which would reject it as unknown anyway).
const MaxMoveToDestinationChars = 128

// moveToSchema is the JSON Schema bytes shipped to the LLM provider. The
// destination length bound is restated as a literal because schema bytes are
// static — keep it in sync with DecodeMoveToArgs's defensive range check.
var moveToSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "destination": {
            "type": "string",
            "minLength": 1,
            "maxLength": 128,
            "description": "Where to walk — a place you can see in your perception: your home or workplace, a shop, the meeting house, or a free source nearby like a well or fruit tree. Give the place's name (\"the Tavern\", \"the Well\"), or say simply \"home\" or \"work\" for your own; you can also use the id shown for a place in your perception. The engine handles pathfinding and decides whether you go inside or stand just outside."
        }
    },
    "required": ["destination"],
    "additionalProperties": false
}`)

// moveToDescription is the tool description advertised to the model. The
// schema's per-field description carries the arg guidance; this frames when to
// reach for the tool and the two things the model must understand about it:
// walking ends the turn, and it leaves any conversation.
const moveToDescription = "Walk to a place you can see in your perception — your home, your workplace, a shop, the meeting house, or a free source nearby like a well or fruit tree. Give its destination — the place's name (e.g. \"the Tavern\", \"the Well\"), \"home\"/\"work\" for your own, or the id shown for it in your perception. The engine handles pathfinding and decides whether you go inside (your own home or work, an open shop) or stand just outside (a well, a closed building). Walking ENDS your turn, so say anything you want the people around you to hear BEFORE you call move_to. If you are in a conversation, choosing to walk away leaves it."

// DecodeMoveToArgs parses the raw tool-call arguments into a MoveToArgs.
// Errors are typed validation failures the harness surfaces to the model as
// tool errors (so the model can retry with corrected args).
//
// Aliases (LLM-320): the canonical field is `destination`, but varied models
// reach for natural synonyms (`location`, `place`) or the engine's older jargon
// fields (`structure_id`, `structure_name`) — and loop on the same wrong shape
// when the "unknown field" rejection is fed back (the live qwen `location`
// case). Tolerating them as decode-only aliases lets the walk land. Precedence:
// a non-empty `destination` always wins; else the first non-empty alias in a
// stable order. Mirrors the speak `message`→`text` alias (LLM-315), and is
// likewise per-tool by design — NOT a global decoder alias.
//
// Checks:
//
//   - JSON parses, no trailing data, no unknown fields (beyond the aliases)
//   - destination (or an alias) present and within the character cap
//
// What DecodeMoveToArgs does NOT check (handled in HandleMoveTo /
// MoveToDestination Command):
//
//   - Trim-emptiness + control-character scan: HandleMoveTo (defensive re-check).
//   - id-vs-name resolution, structure exists / already-there / already-walking /
//     enter-vs-visit / reachability: world state, done in the Command.
func DecodeMoveToArgs(raw json.RawMessage) (any, error) {
	// Reject non-object payloads early — same rationale as speak/take_break: a
	// bare null / number / string decodes quietly to zero values, producing a
	// misleading "destination is required" instead of a crisp "must be a JSON
	// object".
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("move_to: arguments must be a JSON object")
	}
	// moveToArgsWire is MoveToArgs plus the accepted aliases. Embedding MoveToArgs
	// promotes its `destination` key so DisallowUnknownFields still rejects
	// everything outside {destination + the aliases}.
	type moveToArgsWire struct {
		MoveToArgs
		Location      string `json:"location"`
		Place         string `json:"place"`
		StructureID   string `json:"structure_id"`
		StructureName string `json:"structure_name"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var wire moveToArgsWire
	if err := dec.Decode(&wire); err != nil {
		return nil, fmt.Errorf("move_to: malformed arguments: %w", err)
	}
	// Trailing-data check — matches the speak/take_break/consume pattern.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("move_to: trailing data after JSON object")
		}
		return nil, fmt.Errorf("move_to: malformed trailing data: %w", err)
	}
	// Validate EVERY field the model actually sent (any non-empty-after-trim
	// value), then select canonical-first. Validating all present fields — not
	// just the selected one — keeps the engine's strict-decode posture: a
	// malformed alias (a NUL, an over-cap value) is a malformed tool call even
	// when another field would win, so we surface it rather than silently
	// ignoring it. A whitespace-only field is treated as ABSENT (trimmed empty →
	// skipped), so a blank canonical alongside a real alias still lands the walk —
	// strict on content, forgiving on emptiness. Canonical `destination` wins;
	// else the first non-empty alias in order (natural synonyms before jargon).
	fields := []struct {
		name string
		val  string
	}{
		{"destination", wire.Destination},
		{"location", wire.Location},
		{"place", wire.Place},
		{"structure_id", wire.StructureID},
		{"structure_name", wire.StructureName},
	}
	dest := ""
	for _, f := range fields {
		trimmed := strings.TrimSpace(f.val)
		if trimmed == "" {
			continue
		}
		if n := utf8.RuneCountInString(trimmed); n > MaxMoveToDestinationChars {
			return nil, modelSafef(
				"move_to: %s exceeds %d-character cap (got %d characters)",
				f.name, MaxMoveToDestinationChars, n,
			)
		}
		// Control-char scan (an identifier / a name never contains C0 controls).
		if i := indexInvalidControlChar(trimmed); i >= 0 {
			return nil, modelSafef("move_to: %s contains a disallowed control character at byte offset %d", f.name, i)
		}
		if dest == "" {
			dest = trimmed
		}
	}
	if dest == "" {
		return nil, modelSafef("move_to: destination is required")
	}
	return MoveToArgs{Destination: dest}, nil
}

// HandleMoveTo is the CommitFn for the move_to tool. Pure builder — does NOT
// touch the world. Static validation that JSON Schema cannot express runs here
// (trim-empty, control-char scan); world-state validation (id-vs-name
// resolution, structure exists, already-there, enter-vs-visit, MoveActor
// dispatch) runs inside the returned sim.MoveToDestination Command on the world
// goroutine.
func HandleMoveTo(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(MoveToArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("move_to: handler received unexpected args type %T", in.Args)
	}
	dest := strings.TrimSpace(args.Destination)
	if dest == "" {
		return sim.Command{}, modelSafef("move_to: destination is empty after trim")
	}
	// destination is a name or identifier — reject embedded control characters
	// (the same scan speak/take_break apply). Real names/ids never carry C0
	// controls; the world lookup rejects anything else as unknown.
	if i := indexInvalidControlChar(dest); i >= 0 {
		return sim.Command{}, modelSafef(
			"move_to: destination contains a disallowed control character at byte offset %d", i)
	}
	return sim.MoveToDestination(in.ActorID, dest, in.PerceivedObjectIDs, in.RememberedPlaces, time.Now().UTC()), nil
}
