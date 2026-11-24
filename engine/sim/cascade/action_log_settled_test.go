package cascade

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// action_log_settled_test.go — LLM-1018. A completed wage also writes the
// employer's own `settled` entry and row, so the employer's day push and
// consolidation keep the payment past the one-turn settle warrant.

func ringEntriesOfType(entries []sim.ActionLogEntry, t sim.ActionType) []sim.ActionLogEntry {
	var out []sim.ActionLogEntry
	for _, e := range entries {
		if e.ActionType == t {
			out = append(out, e)
		}
	}
	return out
}

func durableRowsOfType(rows []sim.DurableActionLogRow, t sim.ActionType) []sim.DurableActionLogRow {
	var out []sim.DurableActionLogRow
	for _, r := range rows {
		if r.ActionType == t {
			out = append(out, r)
		}
	}
	return out
}

func TestHandleLaborResolvedActionLog_EmployerGetsSettledRow(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	rec := &recordingActionLogSink{}
	invokeOnWorld(t, w, func(world *sim.World) { world.SetActionLogSink(rec) })

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		handleLaborResolvedActionLog(world, &sim.LaborResolved{
			LaborID: 42, WorkerID: "hannah", EmployerID: "bob", Reward: 4,
			RewardItems: []sim.ItemKindQty{{Kind: "porridge", Qty: 2}}, DurationMin: 45,
			TerminalState: sim.LaborTerminalStateCompleted, HuddleID: "h1", At: at,
		})
	})

	want := "You paid Hannah 2 porridge and 4 coins for the work done for you, as agreed."
	ring := ringEntriesOfType(readActionLog(t, w), sim.ActionTypeSettled)
	if len(ring) != 1 {
		t.Fatalf("settled ring entries = %+v, want 1", ring)
	}
	e := ring[0]
	if e.ActorID != "bob" || e.CounterpartyName != "Hannah" || e.Amount != 4 || e.Text != want {
		t.Errorf("ring entry = %+v\nwant text %q", e, want)
	}
	if e.HuddleID != "" {
		t.Errorf("ring HuddleID = %q, want empty — the settlement is not part of a conversation", e.HuddleID)
	}

	rows := durableRowsOfType(rec.snapshot(), sim.ActionTypeSettled)
	if len(rows) != 1 {
		t.Fatalf("durable settled rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.ActorID != "bob" || r.HuddleID != "" || r.Source != "engine" || r.SpeakerName != "Bob" {
		t.Errorf("durable row = %+v", r)
	}
	p := r.Payload
	if p["settlement"] != "wage" || p["counterparty"] != "Hannah" || p["counterparty_actor_id"] != "hannah" ||
		p["amount"] != 4 || p["labor_id"] != uint64(42) || p["duration_min"] != 45 || p["text"] != want {
		t.Errorf("payload = %+v", p)
	}
	if p["reward_items"] == nil {
		t.Errorf("payload = %+v, want reward_items for the in-kind leg", p)
	}

	// The worker's row and the coin record are as before: one labored row, one
	// wage in the pair record.
	if n := len(durableRowsOfType(rec.snapshot(), sim.ActionTypeLabored)); n != 1 {
		t.Errorf("durable labored rows = %d, want 1", n)
	}
	invokeOnWorld(t, w, func(world *sim.World) {
		snap := &sim.Snapshot{
			CoinRecord:       sim.CloneCoinRecord(world.CoinRecord),
			CoinRecordWindow: world.CoinRecordWindow(),
		}
		d := snap.CoinDealingsFor("bob", "hannah", at.Add(time.Minute))
		if d.PaidCount != 1 || d.PaidTotal != 4 || d.PaidWorkCount != 1 || d.ReceivedCount != 0 {
			t.Errorf("bob->hannah coin record = %+v, want one 4-coin wage and nothing back", d)
		}
	})
}

func TestHandleLaborResolvedActionLog_NonCompletedWritesNoSettledRow(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	rec := &recordingActionLogSink{}
	invokeOnWorld(t, w, func(world *sim.World) { world.SetActionLogSink(rec) })
	invokeOnWorld(t, w, func(world *sim.World) {
		handleLaborResolvedActionLog(world, &sim.LaborResolved{
			LaborID: 43, WorkerID: "hannah", EmployerID: "bob", Reward: 4, DurationMin: 45,
			TerminalState: sim.LaborTerminalStateFailedUnavailable, WorkPerformed: true, At: time.Now().UTC(),
		})
	})
	if n := len(ringEntriesOfType(readActionLog(t, w), sim.ActionTypeSettled)); n != 0 {
		t.Errorf("settled ring entries = %d, want 0 — no wage moved", n)
	}
	if n := len(durableRowsOfType(rec.snapshot(), sim.ActionTypeSettled)); n != 0 {
		t.Errorf("settled rows = %d, want 0", n)
	}
}

// The consolidation prompt renders a settled entry as a readable line.
func TestBuildSoulDaySnapshot_RendersSettledLine(t *testing.T) {
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	got := buildSoulDaySnapshot(sim.NarrativeCandidate{
		ActorID:   "hannah",
		ActorName: "Hannah Boggs",
		Events: []sim.ActionLogEntry{{
			ActorID: "hannah", OccurredAt: at, ActionType: sim.ActionTypeSettled,
			Text: "Thomas Hale paid you 3 coins for another night's lodging.",
		}},
	})
	want := "- [Nov 24] settled: Thomas Hale paid you 3 coins for another night's lodging."
	if !strings.Contains(got, want) {
		t.Errorf("snapshot = %q\nwant it to contain %q", got, want)
	}
}
