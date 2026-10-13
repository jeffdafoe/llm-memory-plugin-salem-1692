package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/roomlayout"
)

// pc_room_test.go — LLM-768 slice (ii): POST /api/village/pc/move_inside. The
// walking rules are covered in the sim package (room_square_test.go); these
// cover the HTTP surface: body shape, session→PC, status mapping.

const tavernRoom sim.StructureID = "019dbcd2-c0b1-7bf9-98c2-0610cfb7f5e9"

// seedTavernPC puts a login-bound PC inside the Tavern on its door square.
func seedTavernPC(t *testing.T, w *sim.World, login string) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		door := roomlayout.For(string(tavernRoom)).Door.Square
		world.Actors["pc-guest"] = &sim.Actor{
			ID: "pc-guest", DisplayName: "Guest", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: login,
			InsideStructureID: tavernRoom, RoomSquare: &door,
			Needs: map[sim.NeedKey]int{"tiredness": 20},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seedTavernPC: %v", err)
	}
}

func TestHandlePCMoveInside(t *testing.T) {
	w := seededWorld(t)
	seedTavernPC(t, w, "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/pc/move_inside", `{"square": [3, 6]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got pcMoveInsideResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	want := []roomlayout.Square{{X: 4, Y: 6}, {X: 3, Y: 6}}
	if got.Square != (roomlayout.Square{X: 3, Y: 6}) || len(got.Path) != 2 || got.Path[0] != want[0] || got.Path[1] != want[1] {
		t.Errorf("response = %+v, want square [3,6] by %v", got, want)
	}

	// The table is not floor: refused, not moved.
	if rec := post(t, srv, "/api/village/pc/move_inside", `{"square": [2, 6]}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("onto the table: status = %d, want 422", rec.Code)
	}
	for _, body := range []string{``, `{}`, `{"square": [1]}`, `{"square": [1, 2], "x": 1}`} {
		if rec := post(t, srv, "/api/village/pc/move_inside", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestHandlePCMoveInside_PCNotFound(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	if rec := post(t, srv, "/api/village/pc/move_inside", `{"square": [3, 6]}`); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// A sleeping PC is refused before the input stamp: the tap neither wakes them
// nor moves them (code_review).
func TestHandlePCMoveInside_SleepingRefused(t *testing.T) {
	w := seededWorld(t)
	seedTavernPC(t, w, "tester")
	_, _ = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		pc := world.Actors["pc-guest"]
		until := time.Now().UTC().Add(time.Hour)
		pc.State, pc.SleepingUntil, pc.RoomSquare = sim.StateSleeping, &until, nil
		return nil, nil
	}})
	srv := NewServer(w, okAuth{})
	if rec := post(t, srv, "/api/village/pc/move_inside", `{"square": [3, 6]}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	res, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		pc := world.Actors["pc-guest"]
		return []any{pc.State, pc.SleepingUntil != nil, pc.RoomSquare == nil}, nil
	}})
	if got := res.([]any); got[0] != sim.StateSleeping || got[1] != true || got[2] != true {
		t.Errorf("after the refused tap: state %v, sleeping-until set %v, no square %v — want still asleep, no square", got[0], got[1], got[2])
	}
}
