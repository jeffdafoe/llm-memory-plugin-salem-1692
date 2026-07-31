package pg

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// settings_ownership_test.go — LLM-577. The blast radius of the persist policy.
//
// Making every registered key Persist:true is an ownership change: the engine
// now overwrites a direct DB edit to any of those keys at the next checkpoint,
// where before it only did so for 28 of them. That trade is deliberate and
// documented on SettingSpec.Persist, but its BOUNDARY needs pinning, because the
// failure mode if it slipped would be silent and total — SaveMutableSettings
// turning into a full settings replace would clobber every operator-owned row in
// the table, including keys the engine has no business writing.
//
// So: exactly the registered keys are written, and nothing else is touched.

// recordingTx captures the upserts SaveMutableSettings issues. Only Exec is
// exercised — the settings writeback issues no queries.
type recordingTx struct {
	upserts map[string]string
}

func (tx *recordingTx) Exec(_ context.Context, _ string, args ...any) (sim.CommandTag, error) {
	if len(args) == 2 {
		key, _ := args[0].(string)
		val, _ := args[1].(string)
		tx.upserts[key] = val
	}
	return nil, nil
}
func (tx *recordingTx) Query(context.Context, string, ...any) (sim.Rows, error) { return nil, nil }
func (tx *recordingTx) QueryRow(context.Context, string, ...any) sim.Row        { return nil }
func (tx *recordingTx) Commit(context.Context) error                            { return nil }
func (tx *recordingTx) Rollback(context.Context) error                          { return nil }

func TestSaveMutableSettings_LeavesUnregisteredRowsAlone(t *testing.T) {
	repo := NewEnvironmentRepo(nil)
	tx := &recordingTx{upserts: map[string]string{}}

	settings := buildSettings(map[string]string{})
	rows := sim.PersistableSettingRows(&settings)
	if err := repo.SaveMutableSettings(context.Background(), tx, sim.MutableWorldSettings{Rows: rows}); err != nil {
		t.Fatalf("SaveMutableSettings: %v", err)
	}

	// A registered key IS written — the engine owns it now.
	if _, ok := tx.upserts["world_dusk_time"]; !ok {
		t.Error("world_dusk_time was not written — a registered key must be engine-owned and durable")
	}

	// Rows that are not registered settings must never be touched.
	// sim_conversation_last_pushed_day is a real example: it lives in the same
	// table, is written by a different subsystem entirely, and a full replace
	// would destroy it.
	unregistered := []string{
		"sim_conversation_last_pushed_day",
		"eco_conversation_max_seconds", // the retired LLM-334 key; inert, must stay inert
		"some_operator_scratch_key",
	}
	for _, key := range unregistered {
		if got, ok := tx.upserts[key]; ok {
			t.Errorf("unregistered key %q was written as %q — SaveMutableSettings has become a full settings replace and is clobbering rows the engine does not own", key, got)
		}
	}

	// And the write set is EXACTLY the registered keys, neither more nor less.
	registered := make(map[string]bool)
	for _, k := range sim.SettingKeys() {
		registered[k] = true
	}
	for key := range tx.upserts {
		if !registered[key] {
			t.Errorf("wrote unregistered key %q", key)
		}
	}
	for key := range registered {
		if _, ok := tx.upserts[key]; !ok {
			t.Errorf("registered key %q was not written — a live tune of it would silently revert on restart", key)
		}
	}
}
