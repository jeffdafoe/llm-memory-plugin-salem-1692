package cascade

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// register_production_test.go — covers the production cascade
// composition helper. The per-subsystem Register* helpers have their
// own subscriber tests; this file's job is to assert the composition
// runs end-to-end without panicking and rejects nil inputs.

func buildRegisterProductionWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	return w
}

// TestRegisterProductionCascades_ComposesWithoutPanic exercises the
// full composition end-to-end with real LoadWorld'd state + a FakeClient.
// A panic from any per-subsystem Register* helper would surface here —
// the actual subscriber semantics are covered by the per-subsystem
// test files in this package.
func TestRegisterProductionCascades_ComposesWithoutPanic(t *testing.T) {
	w := buildRegisterProductionWorld(t)
	client := llm.NewFakeClient()
	// Nothing to assert beyond "it didn't panic" — see file-level comment.
	RegisterProductionCascades(context.Background(), w, client)
}

// TestRegisterProductionCascades_PanicsOnNilWorld — wiring guard.
func TestRegisterProductionCascades_PanicsOnNilWorld(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("RegisterProductionCascades(nil world) should panic")
		}
	}()
	RegisterProductionCascades(context.Background(), nil, llm.NewFakeClient())
}

// TestRegisterProductionCascades_PanicsOnNilClient — production requires
// a real LLM adapter. Tests wanting a partial composition without LLM
// cascades should call the non-LLM Register* helpers directly.
func TestRegisterProductionCascades_PanicsOnNilClient(t *testing.T) {
	w := buildRegisterProductionWorld(t)
	defer func() {
		if r := recover(); r == nil {
			t.Error("RegisterProductionCascades(nil client) should panic")
		}
	}()
	RegisterProductionCascades(context.Background(), w, nil)
}
