package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// bring_before_magistrates.go — the constable's filing tool (LLM-695).
//
// The model emits {"parties": ["Josiah Thorne"], "complaint": "..."}. The
// decoder bounds the shape; sim.FileCourtCase re-validates against the world
// (constable attribute, party names, the daily limit) and puts the matter on the
// docket. The magistrates hear it at the next sitting, off the map.
//
// NOT terminal: sending a matter to the court is not an exchange with anyone
// present, and the natural turn is to file and then tell the people in front of
// him that the magistrates will hear it — speak, which is terminal, has to stay
// reachable after it. Gated to a constable under today's limit by tool_gating.go
// (payload.Court.OffersFiling), in lockstep with the "## The magistrates"
// section.

// BringBeforeMagistratesArgs is the decoded argument shape.
type BringBeforeMagistratesArgs struct {
	Parties   []string `json:"parties"`
	Complaint string   `json:"complaint"`
}

var bringBeforeMagistratesSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "parties": {
            "type": "array",
            "items": {"type": "string", "maxLength": 80},
            "minItems": 1,
            "maxItems": 4,
            "description": "The villagers the matter concerns, by name — the one who says they were wronged and anyone they lay it against."
        },
        "complaint": {
            "type": "string",
            "maxLength": 600,
            "description": "What the matter is, in your own words: what is said to have happened, to whom, and when."
        }
    },
    "required": ["parties", "complaint"],
    "additionalProperties": false
}`)

const bringBeforeMagistratesDescription = "Bring a matter you cannot settle yourself before the magistrates in Salem Town. Name the villagers it concerns and say what the matter is. The court reads what passed in the village and sends its ruling to everyone the matter concerns; the ruling ends it."

// DecodeBringBeforeMagistratesArgs parses and bounds the arguments.
func DecodeBringBeforeMagistratesArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("bring_before_magistrates: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args BringBeforeMagistratesArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("bring_before_magistrates: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("bring_before_magistrates: trailing data after JSON object")
		}
		return nil, fmt.Errorf("bring_before_magistrates: malformed trailing data: %w", err)
	}
	if len(args.Parties) == 0 {
		return nil, modelSafef("bring_before_magistrates: name at least one villager the matter concerns")
	}
	if len(args.Parties) > sim.MaxCourtParties {
		return nil, modelSafef("bring_before_magistrates: a matter may name at most %d villagers", sim.MaxCourtParties)
	}
	if strings.TrimSpace(args.Complaint) == "" {
		return nil, modelSafef("bring_before_magistrates: say what the matter is")
	}
	return args, nil
}

// HandleBringBeforeMagistrates is the CommitFn. Pure builder; the world-state
// checks run inside sim.FileCourtCase.
func HandleBringBeforeMagistrates(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(BringBeforeMagistratesArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("bring_before_magistrates: handler received unexpected args type %T", in.Args)
	}
	complaint := strings.TrimSpace(args.Complaint)
	if i := indexInvalidControlChar(complaint); i >= 0 {
		return sim.Command{}, modelSafef("bring_before_magistrates: complaint contains a disallowed control character at byte offset %d", i)
	}
	for _, p := range args.Parties {
		if i := indexInvalidControlChar(p); i >= 0 {
			return sim.Command{}, modelSafef("bring_before_magistrates: a party name contains a disallowed control character at byte offset %d", i)
		}
	}
	return sim.FileCourtCase(in.ActorID, args.Parties, complaint, time.Now().UTC(), false), nil
}

// RegisterBringBeforeMagistrates adds the tool as a ClassCommit entry,
// non-terminal (see the file comment).
func RegisterBringBeforeMagistrates(r *Registry) error {
	return r.RegisterCommit(
		bringBeforeMagistratesToolName,
		bringBeforeMagistratesSchema,
		DecodeBringBeforeMagistratesArgs,
		HandleBringBeforeMagistrates,
		false, // not terminal: he may tell those present after sending it
		WithDescription(bringBeforeMagistratesDescription),
	)
}
