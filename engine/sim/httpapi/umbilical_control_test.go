package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
