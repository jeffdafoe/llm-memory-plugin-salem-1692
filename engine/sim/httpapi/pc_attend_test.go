package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_attend_test.go — LLM-466. The candle-prompt ack route.

// idlePC seeds a PC whose client is connected (fresh presence) but whose player
// has been idle for hours — the shape the sweep prompts.
func idlePC(t *testing.T, w *sim.World, login string) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		now := time.Now().UTC()
		seen := now.Add(-5 * time.Second)
		activity := now.Add(-6 * time.Hour)
		world.Actors["pc-idle"] = &sim.Actor{
			ID: "pc-idle", DisplayName: "Idler", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: login,
			LastPCSeenAt: &seen, LastPCActivityAt: &activity,
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("idlePC: %v", err)
	}
}

// idlePCState is the slice of PC state these cases assert on, read off the
// world goroutine so nothing races the live actor.
type idlePCState struct {
	PromptPending bool
	LastInputAt   *time.Time
}

func attendedPC(t *testing.T, w *sim.World) idlePCState {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["pc-idle"]
		return idlePCState{PromptPending: a.IdlePromptPending, LastInputAt: a.LastPCInputAt}, nil
	}})
	if err != nil {
		t.Fatalf("read PC: %v", err)
	}
	return res.(idlePCState)
}

// The ack restores audience: after the click the village is no longer
// throttled, which is the entire point of the route.
func TestHandlePCAttend_RestoresAudience(t *testing.T) {
	w := seededWorld(t)
	idlePC(t, w, "tester")
	srv := NewServer(w, okAuth{})

	audience, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.AudienceActive(world, time.Now().UTC()), nil
	}})
	if err != nil {
		t.Fatalf("audience precondition: %v", err)
	}
	if audience.(bool) {
		t.Fatal("precondition: an idle connected PC must not read as an audience")
	}

	rec := post(t, srv, "/api/village/pc/attend", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	audience, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.AudienceActive(world, time.Now().UTC()), nil
	}})
	if err != nil {
		t.Fatalf("audience after ack: %v", err)
	}
	if !audience.(bool) {
		t.Error("the ack must restore audience")
	}
}

// The ack reports whether it was the call that dismissed a live prompt, and
// leaves the in-world input cursor alone (it is not an act of the character).
func TestHandlePCAttend_ClearsPromptWithoutTouchingInputCursor(t *testing.T) {
	w := seededWorld(t)
	idlePC(t, w, "tester")
	srv := NewServer(w, okAuth{})

	if _, err := w.Send(sim.SweepPCIdleAudience(time.Now().UTC())); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !attendedPC(t, w).PromptPending {
		t.Fatal("precondition: the sweep should have raised the prompt")
	}

	rec := post(t, srv, "/api/village/pc/attend", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp pcAttendResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if !resp.Cleared {
		t.Error("cleared = false, want true — a prompt was pending")
	}

	pc := attendedPC(t, w)
	if pc.PromptPending {
		t.Error("the ack must clear the pending prompt")
	}
	if pc.LastInputAt != nil {
		t.Error("the ack must not stamp the in-world input cursor (it would defer idle auto-bed)")
	}
}

// Answering with nothing pending is a 200 with cleared=false, not an error.
func TestHandlePCAttend_IdempotentWithoutPendingPrompt(t *testing.T) {
	w := seededWorld(t)
	idlePC(t, w, "tester")
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/pc/attend", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp pcAttendResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if resp.Cleared {
		t.Error("cleared = true with no prompt pending, want false")
	}
}

func TestHandlePCAttend_PCNotFound(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/attend", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// Both candle frames translate — without this the engine would raise a prompt
// no client ever hears about.
func TestTranslateEvent_PCIdlePrompt(t *testing.T) {
	at := time.Date(2026, 7, 19, 4, 0, 0, 0, time.UTC)

	frame, ok := TranslateEvent(&sim.PCIdlePromptShown{ActorID: "pc-idle", At: at})
	if !ok {
		t.Fatal("PCIdlePromptShown should translate")
	}
	if frame.Type != "pc_idle_prompt" {
		t.Errorf("type = %q, want pc_idle_prompt", frame.Type)
	}
	data, isPrompt := frame.Data.(pcIdlePromptWireDTO)
	if !isPrompt {
		t.Fatalf("data = %T, want pcIdlePromptWireDTO", frame.Data)
	}
	if data.ActorID != "pc-idle" {
		t.Errorf("actor_id = %q, want pc-idle", data.ActorID)
	}

	cleared, ok := TranslateEvent(&sim.PCIdlePromptCleared{ActorID: "pc-idle", At: at})
	if !ok {
		t.Fatal("PCIdlePromptCleared should translate")
	}
	if cleared.Type != "pc_idle_prompt_cleared" {
		t.Errorf("type = %q, want pc_idle_prompt_cleared", cleared.Type)
	}
}
