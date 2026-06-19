package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_sleep_test.go — ZBBS-WORK-324 httpapi plumbing for the PC sleep routes.
// The sleep mechanics themselves are covered in the sim package
// (pc_sleep_test.go); these tests exercise the HTTP surface: session→PC
// resolution, status mapping, and the input-wake wiring on the action routes.

// seedLodgerPC stands up an inn with one private bedroom (room 1) and a
// login-bound PC standing in it with an active ledger grant — the paid-bedroom
// state pc/sleep's gate requires. tiredness 20 (above the auto-bed floor; not
// that the route uses it).
func seedLodgerPC(t *testing.T, w *sim.World, login string) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		if world.Structures == nil {
			world.Structures = make(map[sim.StructureID]*sim.Structure)
		}
		world.Structures["inn"] = &sim.Structure{
			ID: "inn", DisplayName: "Inn", Rooms: []*sim.Room{
				{ID: 1, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
			},
		}
		expires := time.Now().UTC().Add(72 * time.Hour)
		world.Actors["pc-lodger"] = &sim.Actor{
			ID: "pc-lodger", DisplayName: "Lodger", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: login,
			InsideStructureID: "inn", InsideRoomID: 1,
			Needs: map[sim.NeedKey]int{"tiredness": 20},
			RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 1, Source: sim.AccessSourceLedger}: {
					RoomID: 1, Source: sim.AccessSourceLedger, Active: true, ExpiresAt: &expires,
				},
			},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seedLodgerPC: %v", err)
	}
}

// pcSleepingUntil reads the lodger PC's SleepingUntil off the running world.
func pcSleepingUntil(t *testing.T, w *sim.World) *time.Time {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["pc-lodger"].SleepingUntil, nil
	}})
	if err != nil {
		t.Fatalf("read SleepingUntil: %v", err)
	}
	su, _ := res.(*time.Time)
	return su
}

func TestHandlePCSleep_Bedded(t *testing.T) {
	w := seededWorld(t)
	seedLodgerPC(t, w, "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/pc/sleep", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if pcSleepingUntil(t, w) == nil {
		t.Error("PC should be sleeping after /pc/sleep")
	}
}

func TestHandlePCSleep_NoGrant(t *testing.T) {
	w := seededWorld(t)
	seedLodgerPC(t, w, "tester")
	// Drop the PC's lodging grant → no private room to bed into → gate fails
	// (LLM-14: the gate is grant-based, not physical-InsideRoomID-based, so an
	// awake lodger at the bar can still bed — but a PC with NO grant cannot).
	_, _ = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["pc-lodger"].RoomAccess = nil
		return nil, nil
	}})
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/pc/sleep", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCSleep_PCNotFound(t *testing.T) {
	// seededWorld has no PC bound to login "tester".
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/sleep", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCWake_OK(t *testing.T) {
	w := seededWorld(t)
	seedLodgerPC(t, w, "tester")
	srv := NewServer(w, okAuth{})

	// Bed, then wake.
	if rec := post(t, srv, "/api/village/pc/sleep", ""); rec.Code != http.StatusOK {
		t.Fatalf("sleep status = %d; body=%s", rec.Code, rec.Body.String())
	}
	rec := post(t, srv, "/api/village/pc/wake", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("wake status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if pcSleepingUntil(t, w) != nil {
		t.Error("PC should be awake after /pc/wake")
	}
}

func TestHandlePCWake_PCNotFound(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/wake", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandlePCInputWake verifies the TouchPCInput wiring on an action route: a
// sleeping PC who issues a /pc/speak is woken by the action (input-wake) and the
// speak still proceeds. Guards the desync the client can't prevent (it doesn't
// gate movement/speech while the sleep overlay is up).
func TestHandlePCInputWake(t *testing.T) {
	w := seededWorld(t)
	seedLodgerPC(t, w, "tester")
	srv := NewServer(w, okAuth{})

	if rec := post(t, srv, "/api/village/pc/sleep", ""); rec.Code != http.StatusOK {
		t.Fatalf("sleep status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if pcSleepingUntil(t, w) == nil {
		t.Fatal("precondition: PC should be sleeping")
	}
	// Speak while asleep — the action should wake the PC and proceed.
	rec := post(t, srv, "/api/village/pc/speak", `{"text":"hello there"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("speak status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if pcSleepingUntil(t, w) != nil {
		t.Error("acting (/pc/speak) while asleep should input-wake the PC")
	}
}

// TestHandlePCInputWake_RejectedActionStillWakes locks the intended v1-faithful
// behavior: a deliberate, well-formed action that the engine ultimately rejects
// STILL input-wakes the PC (TouchPCInput fires before the action runs). Here a
// move to a non-existent structure 404s, but the PC is woken — the player took a
// real action; the wake is correct even though that specific move couldn't land.
// Malformed bodies never reach this path (the handler 400s before the command).
func TestHandlePCInputWake_RejectedActionStillWakes(t *testing.T) {
	w := seededWorld(t)
	seedLodgerPC(t, w, "tester")
	srv := NewServer(w, okAuth{})

	if rec := post(t, srv, "/api/village/pc/sleep", ""); rec.Code != http.StatusOK {
		t.Fatalf("sleep status = %d; body=%s", rec.Code, rec.Body.String())
	}
	rec := post(t, srv, "/api/village/pc/move",
		`{"destination":{"kind":"structure_enter","structure_id":"no-such-structure"}}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("move status = %d, want 404 (unknown structure); body=%s", rec.Code, rec.Body.String())
	}
	if pcSleepingUntil(t, w) != nil {
		t.Error("a well-formed but rejected action should still input-wake the PC")
	}
}
