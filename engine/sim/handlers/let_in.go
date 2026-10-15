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

// let_in.go — LLM-20. The let_in tool: a resident lets someone standing at
// their door in (sim.LetIn). Advertised only while perception names someone at
// the door (payload.AtYourDoor, gateTools), the same list the door cue names.

// LetInArgs is the decoded let_in call.
type LetInArgs struct {
	Name string `json:"name"`
}

var letInSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "name": {
            "type": "string",
            "minLength": 1,
            "maxLength": 100,
            "description": "The person at your door, exactly as named."
        }
    },
    "required": ["name"],
    "additionalProperties": false
}`)

const letInDescription = "Open your door to someone standing at it and let them into your home. They step inside at once."

const letInToolName = "let_in"

// RegisterLetIn adds let_in as a ClassCommit entry. Non-terminal, so the
// villager may pair it with a word of welcome in the same turn.
func RegisterLetIn(r *Registry) error {
	return r.RegisterCommit(
		letInToolName,
		letInSchema,
		DecodeLetInArgs,
		HandleLetIn,
		false,
		WithDescription(letInDescription),
	)
}

func DecodeLetInArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("let_in: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args LetInArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("let_in: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("let_in: trailing data after JSON object")
		}
		return nil, fmt.Errorf("let_in: malformed trailing data: %w", err)
	}
	if strings.TrimSpace(args.Name) == "" {
		return nil, modelSafef("let_in: name is required")
	}
	return args, nil
}

func HandleLetIn(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(LetInArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("let_in: handler received unexpected args type %T", in.Args)
	}
	name := strings.TrimSpace(args.Name)
	if i := indexInvalidControlChar(name); i >= 0 {
		return sim.Command{}, modelSafef("let_in: name contains a disallowed control character at byte offset %d", i)
	}
	return sim.LetIn(in.ActorID, name, time.Now().UTC()), nil
}
