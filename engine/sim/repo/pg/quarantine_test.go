package pg

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// assertQuarantinedRow asserts the quarantine recorded an entry for the given
// table whose reason contains wantSubstr — the LLM-392 contract that bad row
// content is reported and left behind, never allowed to abort the checkpoint.
func assertQuarantinedRow(t *testing.T, q *sim.Quarantine, wantTable, wantSubstr string) {
	t.Helper()
	for _, r := range q.Rows() {
		if r.Table == wantTable && strings.Contains(r.Reason, wantSubstr) {
			return
		}
	}
	t.Fatalf("quarantine rows = %+v, want one on table %q whose reason contains %q", q.Rows(), wantTable, wantSubstr)
}
