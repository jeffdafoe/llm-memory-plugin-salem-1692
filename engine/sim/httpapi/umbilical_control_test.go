package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// controlServer builds a Server over the seeded world with the umbilical AND
// control both enabled, returning the server (for direct world inspection) and
// its handler.
func controlServer(t *testing.T, perms map[string][]string) (*Server, http.Handler) {
	t.Helper()
	srv := NewServer(seededWorld(t), permAuth{perms})
	srv.SetTelemetry(telemetry.New(8))
	srv.SetControlEnabled(true)
	return srv, srv.Handler()
}

func postReq(t *testing.T, h http.Handler, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// TestUmbilicalControl_OffByDefault: with the umbilical enabled but control NOT
// enabled, the mutating routes are not registered (404), while the read routes
// still work. Proves the independent second opt-in.
func TestUmbilicalControl_OffByDefault(t *testing.T) {
	srv := NewServer(seededWorld(t), permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8)) // umbilical on, control NOT enabled
	h := srv.Handler()

	for _, p := range []string{"/api/village/umbilical/nudge", "/api/village/umbilical/phase"} {
		if rec := postReq(t, h, p, "tok", `{}`); rec.Code != http.StatusNotFound {
			t.Errorf("%s with control disabled = %d, want 404", p, rec.Code)
		}
	}
	// Read surface unaffected.
	if rec := req(t, h, "/api/village/umbilical/state", "tok"); rec.Code != http.StatusOK {
		t.Errorf("state route = %d, want 200 (read unaffected by control flag)", rec.Code)
	}
}

func TestUmbilicalControl_GateEnforced(t *testing.T) {
	_, h := controlServer(t, nil) // authed, no plugins/administer
	for _, p := range []string{"/api/village/umbilical/nudge", "/api/village/umbilical/phase"} {
		if rec := postReq(t, h, p, "tok", `{}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s as non-operator = %d, want 403", p, rec.Code)
		}
		if rec := postReq(t, h, p, "", `{}`); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with no token = %d, want 401", p, rec.Code)
		}
	}
}

// TestUmbilicalNudge_StampsWarrant: a nudge stamps an admin warrant on the
// target actor in the live (mem-backed, running) world — verified by reading
// the actor's warrant state back through the world command channel.
func TestUmbilicalNudge_StampsWarrant(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/nudge", "tok", `{"actor_id":"hannah"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("nudge = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalNudgeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ActorID != "hannah" || !out.Stamped {
		t.Errorf("response = %+v, want actor=hannah stamped=true", out)
	}
	if out.Directive {
		t.Errorf("bare nudge (no message) reported directive=true, want false")
	}

	// Confirm the warrant actually landed on the live actor.
	res, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["hannah"].WarrantedSince != nil, nil
	}})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if warranted, _ := res.(bool); !warranted {
		t.Error("hannah has no warrant after nudge — StampWarrant did not take effect")
	}
}

// TestUmbilicalNudge_Directive: a nudge carrying a message stamps an
// AdminDirectiveWarrantReason (WarrantKindImpulse) on the target — the operator
// directive that surfaces in the forced tick's perception as an in-world felt
// impulse (ZBBS-WORK-329). Verified by reading the live actor's warrant reason
// back through the world command channel.
func TestUmbilicalNudge_Directive(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	rec := postReq(t, h, "/api/village/umbilical/nudge", "tok", `{"actor_id":"hannah","message":"return home and rest"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("directive nudge = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalNudgeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Directive || !out.Stamped {
		t.Errorf("response = %+v, want stamped=true directive=true", out)
	}

	// Confirm the directive reason (with the message) landed on the live actor.
	res, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		for _, m := range world.Actors["hannah"].Warrants {
			if r, ok := m.Reason.(sim.AdminDirectiveWarrantReason); ok {
				return r.Message, nil
			}
		}
		return "", nil
	}})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if msg, _ := res.(string); msg != "return home and rest" {
		t.Errorf("warrant directive message = %q, want %q", msg, "return home and rest")
	}
}

func TestUmbilicalNudge_BadInput(t *testing.T) {
	_, h := controlServer(t, operatorPerms)

	if rec := postReq(t, h, "/api/village/umbilical/nudge", "tok", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("missing actor_id = %d, want 400", rec.Code)
	}
	// Unknown actor → the command rejects → 422.
	if rec := postReq(t, h, "/api/village/umbilical/nudge", "tok", `{"actor_id":"nobody"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown actor = %d, want 422", rec.Code)
	}
}

func TestUmbilicalSettle(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	// Warrant hannah so there's something to settle.
	_, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		now := time.Now()
		world.Actors["hannah"].WarrantedSince = &now
		world.Actors["hannah"].WarrantDueAt = &now
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("warrant hannah: %v", err)
	}

	rec := postReq(t, h, "/api/village/umbilical/settle", "tok", `{"actor_id":"hannah"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("settle = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalSettleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.WasWarranted {
		t.Errorf("was_warranted = false, want true")
	}
	// Confirm the warrant is actually cleared on the live actor.
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["hannah"].WarrantedSince == nil, nil
	}})
	if cleared, _ := res.(bool); !cleared {
		t.Error("hannah still warranted after settle")
	}

	if rec := postReq(t, h, "/api/village/umbilical/settle", "tok", `{"actor_id":"nobody"}`); rec.Code != http.StatusNotFound {
		t.Errorf("settle unknown actor = %d, want 404", rec.Code)
	}
}

func TestUmbilicalRotate(t *testing.T) {
	_, h := controlServer(t, operatorPerms)
	rec := postReq(t, h, "/api/village/umbilical/rotate", "tok", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalRotateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// seededWorld has no rotation-tagged objects → 0 flips, but a valid result.
	if out.ObjectsAffected != 0 {
		t.Errorf("objects_affected = %d, want 0 (no rotation objects seeded)", out.ObjectsAffected)
	}
}

func TestUmbilicalNeedThreshold(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	// Configure a threshold so there's a tunable key (mem-loaded world may have none).
	_, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.NeedThresholds = sim.NeedThresholds{"tiredness": 20}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed threshold: %v", err)
	}

	// Valid tune.
	rec := postReq(t, h, "/api/village/umbilical/settings/need-threshold", "tok", `{"need":"tiredness","value":15}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tune = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Settings.NeedThresholds["tiredness"], nil
	}})
	if v, _ := res.(int); v != 15 {
		t.Errorf("threshold after tune = %d, want 15", v)
	}

	// Out of range → 400.
	if rec := postReq(t, h, "/api/village/umbilical/settings/need-threshold", "tok", `{"need":"tiredness","value":99}`); rec.Code != http.StatusBadRequest {
		t.Errorf("out-of-range = %d, want 400", rec.Code)
	}
	// Unknown (unconfigured) need → 400.
	if rec := postReq(t, h, "/api/village/umbilical/settings/need-threshold", "tok", `{"need":"ennui","value":10}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown need = %d, want 400", rec.Code)
	}
}

func TestUmbilicalControl_NewRoutesGated(t *testing.T) {
	paths := []string{
		"/api/village/umbilical/settle",
		"/api/village/umbilical/rotate",
		"/api/village/umbilical/settings/need-threshold",
	}
	// Umbilical on but control NOT enabled → 404.
	srv := NewServer(seededWorld(t), permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(4))
	off := srv.Handler()
	for _, p := range paths {
		if rec := postReq(t, off, p, "tok", `{}`); rec.Code != http.StatusNotFound {
			t.Errorf("%s control-disabled = %d, want 404", p, rec.Code)
		}
	}
	// Control on but non-operator → 403.
	_, nonOp := controlServer(t, nil)
	for _, p := range paths {
		if rec := postReq(t, nonOp, p, "tok", `{}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s non-operator = %d, want 403", p, rec.Code)
		}
	}
}

func TestUmbilicalPhase_Flips(t *testing.T) {
	_, h := controlServer(t, operatorPerms)

	// seededWorld starts at night; force day.
	rec := postReq(t, h, "/api/village/umbilical/phase", "tok", `{"phase":"day"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("phase = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalPhaseResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.From != "night" || out.To != "day" {
		t.Errorf("transition = %s→%s, want night→day", out.From, out.To)
	}

	// Bad phase → 400.
	if rec := postReq(t, h, "/api/village/umbilical/phase", "tok", `{"phase":"dusk"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad phase = %d, want 400", rec.Code)
	}
}
