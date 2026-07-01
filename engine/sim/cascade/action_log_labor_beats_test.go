package cascade

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// action_log_labor_beats_test.go — LLM-213: the two labor CONVERSATIONAL beats
// (solicit_work → hired) now append action-log rows, so a live hire is visible
// in the operator/talk-panel conversation views the moment it happens, instead
// of only surfacing hours later as the `labored` payout row. Each handler is
// tested directly (constructed event + invocation on the world goroutine),
// mirroring the settle-time `labored` tests.

// --- TestHandleSolicitedWorkActionLog_AppendsWorkerSideRow -----------
// A LaborOfferReceived (the live-pending solicit) appends a worker-side
// `solicited_work` row: ActorID = worker, counterparty = employer, amount =
// reward, huddle from the event.
func TestHandleSolicitedWorkActionLog_AppendsWorkerSideRow(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		handleSolicitedWorkActionLog(world, &sim.LaborOfferReceived{
			LaborID:     11,
			WorkerID:    "hannah",
			EmployerID:  "bob",
			Reward:      4,
			DurationMin: 240,
			HuddleID:    "h1",
			At:          at,
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1", len(got))
	}
	e := got[0]
	if e.ActorID != "hannah" {
		t.Errorf("ActorID = %q, want hannah (worker)", e.ActorID)
	}
	if e.ActionType != sim.ActionTypeSolicitedWork {
		t.Errorf("ActionType = %q, want %q", e.ActionType, sim.ActionTypeSolicitedWork)
	}
	if e.CounterpartyName != "Bob" {
		t.Errorf("CounterpartyName = %q, want Bob (employer)", e.CounterpartyName)
	}
	if e.Amount != 4 {
		t.Errorf("Amount = %d, want 4 (reward)", e.Amount)
	}
	if e.HuddleID != "h1" {
		t.Errorf("HuddleID = %q, want h1 (from the event)", e.HuddleID)
	}
}

// --- TestHandleHiredActionLog_AppendsEmployerSideRow ----------------
// A LaborOfferAccepted (the all-gates-pass accept) appends an employer-side
// `hired` row: ActorID = employer, counterparty = worker, amount = reward.
func TestHandleHiredActionLog_AppendsEmployerSideRow(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		handleHiredActionLog(world, &sim.LaborOfferAccepted{
			LaborID:     11,
			WorkerID:    "hannah",
			EmployerID:  "bob",
			Reward:      4,
			DurationMin: 240,
			HuddleID:    "h1",
			At:          at,
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1", len(got))
	}
	e := got[0]
	if e.ActorID != "bob" {
		t.Errorf("ActorID = %q, want bob (employer)", e.ActorID)
	}
	if e.ActionType != sim.ActionTypeHired {
		t.Errorf("ActionType = %q, want %q", e.ActionType, sim.ActionTypeHired)
	}
	if e.CounterpartyName != "Hannah" {
		t.Errorf("CounterpartyName = %q, want Hannah (worker)", e.CounterpartyName)
	}
	if e.Amount != 4 {
		t.Errorf("Amount = %d, want 4 (reward)", e.Amount)
	}
	if e.HuddleID != "h1" {
		t.Errorf("HuddleID = %q, want h1 (from the event)", e.HuddleID)
	}
}

// --- TestHandleLaborBeats_IgnoreUnrelatedEvents ---------------------
// The subscriber is fanned out to every event; the type assertion is the
// filter. Neither labor-beat handler fires on an unrelated event.
func TestHandleLaborBeats_IgnoreUnrelatedEvents(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	invokeOnWorld(t, w, func(world *sim.World) {
		evt := &sim.ActorMoved{ActorID: "hannah", At: time.Now().UTC()}
		handleSolicitedWorkActionLog(world, evt)
		handleHiredActionLog(world, evt)
	})

	if got := readActionLog(t, w); len(got) != 0 {
		t.Errorf("len(ActionLog) = %d, want 0 (no labor-beat handler should fire on unrelated events)", len(got))
	}
}

// --- TestHandleLaborBeats_EmitDurableRows ---------------------------
// Each labor-beat subscriber mirrors a structured DurableActionLogRow to the
// installed sink: counterparty (employer/worker) + amount + duration_min +
// labor_id, with the acting actor's speaker-name + source. Marshals + re-parses
// the payload to catch writer/reader key drift.
func TestHandleLaborBeats_EmitDurableRows(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	rec := &recordingActionLogSink{}
	invokeOnWorld(t, w, func(world *sim.World) { world.SetActionLogSink(rec) })

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		handleSolicitedWorkActionLog(world, &sim.LaborOfferReceived{
			LaborID: 11, WorkerID: "hannah", EmployerID: "bob", Reward: 4, DurationMin: 240, HuddleID: "h1", At: at,
		})
		handleHiredActionLog(world, &sim.LaborOfferAccepted{
			LaborID: 11, WorkerID: "hannah", EmployerID: "bob", Reward: 4, DurationMin: 240, HuddleID: "h1", At: at,
		})
	})

	rows := rec.snapshot()
	if len(rows) != 2 {
		t.Fatalf("recorded %d durable rows, want 2", len(rows))
	}

	type parsed struct {
		Employer    string `json:"employer"`
		Worker      string `json:"worker"`
		Amount      int    `json:"amount"`
		DurationMin int    `json:"duration_min"`
		LaborID     uint64 `json:"labor_id"`
	}
	decode := func(p map[string]any) parsed {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		var out parsed
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		return out
	}

	// 0: solicited_work — worker acts; counterparty key is "employer".
	if rows[0].ActorID != "hannah" || rows[0].ActionType != sim.ActionTypeSolicitedWork ||
		rows[0].SpeakerName != "Hannah" || rows[0].Source != "agent" || rows[0].HuddleID != "h1" {
		t.Errorf("row 0 solicited_work header = %+v", rows[0])
	}
	s := decode(rows[0].Payload)
	if s.Employer != "Bob" || s.Amount != 4 || s.DurationMin != 240 || s.LaborID != 11 {
		t.Errorf("solicited_work payload = %+v, want {employer:Bob amount:4 duration_min:240 labor_id:11}", s)
	}

	// 1: hired — employer acts; counterparty key is "worker".
	if rows[1].ActorID != "bob" || rows[1].ActionType != sim.ActionTypeHired ||
		rows[1].SpeakerName != "Bob" || rows[1].Source != "agent" || rows[1].HuddleID != "h1" {
		t.Errorf("row 1 hired header = %+v", rows[1])
	}
	h := decode(rows[1].Payload)
	if h.Worker != "Hannah" || h.Amount != 4 || h.DurationMin != 240 || h.LaborID != 11 {
		t.Errorf("hired payload = %+v, want {worker:Hannah amount:4 duration_min:240 labor_id:11}", h)
	}
}
