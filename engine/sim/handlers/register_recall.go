package handlers

import (
	"errors"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// register_recall.go — production registration helper for the recall tool
// (ZBBS-WORK-321). Same opt-in-piecewise pattern as register_take_break.go,
// but via RegisterObservation: recall is the first production ClassObservation
// tool (non-terminal, no world mutation, reads the actor's own memory).
//
// Unlike the commit-tool registrars, RegisterRecall takes a searcher — the
// observation handler closes over it because observation handlers run off the
// world goroutine and reach llm-memory-api directly (see recall.go). The
// entrypoint passes the production memapi client (cmd/engine/main.go
// registerTools), which implements llm.MemorySearcher.
func RegisterRecall(r *Registry, searcher llm.MemorySearcher) error {
	if searcher == nil {
		return errors.New("RegisterRecall: nil searcher")
	}
	return r.RegisterObservation(
		"recall",
		recallSchema,
		DecodeRecallArgs,
		makeRecallHandler(searcher),
		WithDescription(recallDescription),
	)
}
