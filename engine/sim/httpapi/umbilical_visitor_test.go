package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// seedVisitor adds one mid-stay visitor ("vstr-roger") to the running control
// world — the precondition for a 200 from /visitor/depart (LLM-701).
func seedVisitor(t *testing.T, srv *Server) {
	t.Helper()
	if _, err := srv.world.Send(sim.Command{Fn: func(wd *sim.World) (any, error) {
		wd.Actors["vstr-roger"] = &sim.Actor{
			ID:          "vstr-roger",
			DisplayName: "Roger Standish the messenger",
			Kind:        sim.KindNPCShared,
			LLMAgent:    sim.VisitorAgentName,
			Inventory:   map[sim.ItemKind]int{},
			VisitorState: &sim.VisitorState{
				Archetype: "messenger",
				ExpiresAt: time.Now().Add(12 * time.Hour),
				Phase:     sim.VisitorPhaseMakingRounds,
			},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestUmbilicalDepartVisitor_SendsAway: the happy path — 200, the stay ends at the
// call, and the live visitor is departing and silenced.
func TestUmbilicalDepartVisitor_SendsAway(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	seedVisitor(t, srv)

	before := time.Now().UTC()
	rec := postReq(t, h, "/api/village/umbilical/visitor/depart", "tok", `{"actor_id":"vstr-roger"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("depart = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out departVisitorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ActorID != "vstr-roger" || out.DisplayName != "Roger Standish the messenger" {
		t.Errorf("response = %+v, want vstr-roger / Roger Standish the messenger", out)
	}
	if out.Walk != string(sim.VisitorDepartWalkStarted) && out.Walk != string(sim.VisitorDepartWalkNoRoute) {
		t.Errorf("walk = %q, want started or no_route", out.Walk)
	}
	if out.ExpiresAt.Before(before) || out.ExpiresAt.After(time.Now().UTC()) {
		t.Errorf("expires_at = %v, want the moment of the call", out.ExpiresAt)
	}

	res, err := srv.world.Send(sim.Command{Fn: func(wd *sim.World) (any, error) {
		return *wd.Actors["vstr-roger"].VisitorState, nil
	}})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	vs := res.(sim.VisitorState)
	if vs.Phase != sim.VisitorPhaseDeparting || vs.DepartCause != sim.VisitorDepartCauseOperator {
		t.Errorf("live state phase=%q cause=%q, want departing / operator", vs.Phase, vs.DepartCause)
	}
}

// TestUmbilicalDepartVisitor_Errors: missing actor_id 400, unknown actor 404, a
// resident (hannah, seeded KindNPCShared with no VisitorState) 409.
func TestUmbilicalDepartVisitor_Errors(t *testing.T) {
	_, h := controlServer(t, operatorPerms)
	if rec := postReq(t, h, "/api/village/umbilical/visitor/depart", "tok", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("missing actor_id = %d, want 400", rec.Code)
	}
	if rec := postReq(t, h, "/api/village/umbilical/visitor/depart", "tok", `{"actor_id":"ghost"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown actor = %d, want 404", rec.Code)
	}
	if rec := postReq(t, h, "/api/village/umbilical/visitor/depart", "tok", `{"actor_id":"hannah"}`); rec.Code != http.StatusConflict {
		t.Errorf("resident = %d, want 409", rec.Code)
	}
}

// TestUmbilicalDepartVisitor_Gated: the route honors the control surface gate
// (403 without plugins/administer, 401 with no token).
func TestUmbilicalDepartVisitor_Gated(t *testing.T) {
	_, h := controlServer(t, nil)
	if rec := postReq(t, h, "/api/village/umbilical/visitor/depart", "tok", `{"actor_id":"vstr-roger"}`); rec.Code != http.StatusForbidden {
		t.Errorf("non-operator = %d, want 403", rec.Code)
	}
	if rec := postReq(t, h, "/api/village/umbilical/visitor/depart", "", `{"actor_id":"vstr-roger"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}
}
