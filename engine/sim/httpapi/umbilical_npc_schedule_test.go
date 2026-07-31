package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_npc_schedule_test.go — LLM-577. The npc/set-schedule control route:
// the operator-gated path to an NPC's shift window that replaces stop-engine →
// UPDATE actor → start-engine.

// scheduleOf reads one actor's live window off the world goroutine.
func scheduleOf(t *testing.T, srv *Server, id sim.ActorID) (*int, *int) {
	t.Helper()
	res, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		if a == nil {
			return nil, nil
		}
		return [2]*int{a.ScheduleStartMin, a.ScheduleEndMin}, nil
	}})
	if err != nil {
		t.Fatalf("read schedule: %v", err)
	}
	pair, ok := res.([2]*int)
	if !ok {
		t.Fatalf("actor %s not found", id)
	}
	return pair[0], pair[1]
}

func TestUmbilicalNPCSetSchedule_AppliesToLiveWorld(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	// The Elizabeth Ellis case that motivated the ticket: move a shift to
	// 9:00 AM–6:30 PM world time, on a running engine.
	rec := postReq(t, h, "/api/village/umbilical/npc/set-schedule", "tok",
		`{"npc_id":"hannah","schedule_start_minute":570,"schedule_end_minute":1110}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set-schedule = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalNPCScheduleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ScheduleStartMin == nil || *out.ScheduleStartMin != 570 ||
		out.ScheduleEndMin == nil || *out.ScheduleEndMin != 1110 {
		t.Errorf("response = %+v, want 570/1110", out)
	}

	start, end := scheduleOf(t, srv, "hannah")
	if start == nil || *start != 570 || end == nil || *end != 1110 {
		t.Errorf("live schedule = %v/%v, want 570/1110", start, end)
	}
}

// Both bounds omitted clears the window, which makes the NPC inherit the world
// dawn/dusk hours — a distinct outcome from "0 to 0", which is a real (empty)
// window meaning never on shift.
func TestUmbilicalNPCSetSchedule_ClearsWindow(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/npc/set-schedule", "tok", `{"npc_id":"hannah"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	start, end := scheduleOf(t, srv, "hannah")
	if start != nil || end != nil {
		t.Errorf("live schedule = %v/%v, want both nil (inherit world dawn/dusk)", start, end)
	}

	// start == end is legal and is NOT the same thing — an empty shift window.
	if rec := postReq(t, h, "/api/village/umbilical/npc/set-schedule", "tok",
		`{"npc_id":"hannah","schedule_start_minute":600,"schedule_end_minute":600}`); rec.Code != http.StatusOK {
		t.Fatalf("empty window = %d, want 200 (start == end means never on shift)", rec.Code)
	}
	start, end = scheduleOf(t, srv, "hannah")
	if start == nil || *start != 600 || end == nil || *end != 600 {
		t.Errorf("live schedule = %v/%v, want 600/600", start, end)
	}
}

// A one-sided pair must be rejected rather than half-applied: a caller who
// forgets a field would otherwise silently move only one edge of the shift.
func TestUmbilicalNPCSetSchedule_Validation(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	startBefore, endBefore := scheduleOf(t, srv, "hannah")

	bad := []struct {
		body string
		want int
	}{
		{`{}`, http.StatusBadRequest}, // no npc_id
		{`{"npc_id":"hannah","schedule_start_minute":570}`, http.StatusBadRequest}, // one-sided
		{`{"npc_id":"hannah","schedule_end_minute":1110}`, http.StatusBadRequest},  // one-sided
		{`{"npc_id":"hannah","schedule_start_minute":-1,"schedule_end_minute":600}`, http.StatusBadRequest},
		{`{"npc_id":"hannah","schedule_start_minute":0,"schedule_end_minute":1440}`, http.StatusBadRequest}, // 1439 is the last minute
	}
	for _, tc := range bad {
		if rec := postReq(t, h, "/api/village/umbilical/npc/set-schedule", "tok", tc.body); rec.Code != tc.want {
			t.Errorf("body %s = %d, want %d", tc.body, rec.Code, tc.want)
		}
	}

	// None of the rejections may have moved the live window.
	start, end := scheduleOf(t, srv, "hannah")
	if !intPtrSame(start, startBefore) || !intPtrSame(end, endBefore) {
		t.Errorf("a rejected request mutated the schedule: %v/%v, was %v/%v", start, end, startBefore, endBefore)
	}

	// An unknown NPC is not a 200.
	if rec := postReq(t, h, "/api/village/umbilical/npc/set-schedule", "tok",
		`{"npc_id":"nobody","schedule_start_minute":570,"schedule_end_minute":1110}`); rec.Code == http.StatusOK {
		t.Error("unknown npc_id returned 200")
	}
}

func intPtrSame(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
