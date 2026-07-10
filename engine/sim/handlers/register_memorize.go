package handlers

import (
	"errors"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// register_memorize.go — production registration helper for the memorize tool
// (LLM-356). Same RegisterObservation pattern as register_recall.go: memorize
// is a ClassObservation tool (non-terminal, no world mutation) whose handler
// runs off the world goroutine and reaches llm-memory-api directly, so it closes
// over a writer capability the entrypoint supplies (cmd/engine/main.go
// registerTools passes the production memapi client, which implements
// llm.MemoryWriter).
func RegisterMemorize(r *Registry, writer llm.MemoryWriter) error {
	if writer == nil {
		return errors.New("RegisterMemorize: nil writer")
	}
	return r.RegisterObservation(
		"memorize",
		memorizeSchema,
		DecodeMemorizeArgs,
		makeMemorizeHandler(writer),
		WithDescription(memorizeDescription),
	)
}
