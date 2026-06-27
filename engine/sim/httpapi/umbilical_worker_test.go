package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// seedWorkerProvisioning adds the `worker` attribute definition and a
// sprite-only decorative ("statue") to the running control world — the
// preconditions handleUmbilicalProvisionWorker needs for a 200.
func seedWorkerProvisioning(t *testing.T, srv *Server) {
	t.Helper()
	if _, err := srv.world.Send(sim.Command{Fn: func(wd *sim.World) (any, error) {
		wd.AttributeDefinitions[sim.AttrWorker] = &sim.AttributeDefinition{Slug: sim.AttrWorker, DisplayName: "Worker"}
		wd.Actors["statue"] = &sim.Actor{ID: "statue", DisplayName: "Statue", Kind: sim.KindDecorative}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestUmbilicalProvisionWorker_DefaultsAgentAndComesOnline: the happy path —
// an omitted agent defaults to salem-vendor, the decorative reclassifies to
// npc_shared live, and the response carries the new driver state.
func TestUmbilicalProvisionWorker_DefaultsAgentAndComesOnline(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	seedWorkerProvisioning(t, srv)

	rec := postReq(t, h, "/api/village/umbilical/worker/provision", "tok", `{"actor_id":"statue"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("provision = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out provisionWorkerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Agent != sim.VendorAgentName {
		t.Errorf("agent = %q, want %q (defaulted)", out.Agent, sim.VendorAgentName)
	}
	if out.Kind != "npc_shared" {
		t.Errorf("kind = %q, want npc_shared", out.Kind)
	}
	if len(out.Attributes) != 1 || out.Attributes[0] != sim.AttrWorker {
		t.Errorf("attributes = %v, want [worker]", out.Attributes)
	}

	// The live actor is reclassified — it will now tick.
	res, err := srv.world.Send(sim.Command{Fn: func(wd *sim.World) (any, error) {
		return wd.Actors["statue"].Kind, nil
	}})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if k, _ := res.(sim.ActorKind); k != sim.KindNPCShared {
		t.Errorf("live Kind = %v, want KindNPCShared", k)
	}
}

// TestUmbilicalProvisionWorker_ExplicitAgent: an explicit agent overrides the
// default.
func TestUmbilicalProvisionWorker_ExplicitAgent(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	seedWorkerProvisioning(t, srv)

	rec := postReq(t, h, "/api/village/umbilical/worker/provision", "tok", `{"actor_id":"statue","agent":"zbbs-statue"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("provision = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out provisionWorkerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Agent != "zbbs-statue" || out.Kind != "npc_stateful" {
		t.Errorf("response = %+v, want agent=zbbs-statue kind=npc_stateful", out)
	}
}

// TestUmbilicalProvisionWorker_MissingActorID: a body without actor_id is 400.
func TestUmbilicalProvisionWorker_MissingActorID(t *testing.T) {
	_, h := controlServer(t, operatorPerms)
	if rec := postReq(t, h, "/api/village/umbilical/worker/provision", "tok", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("missing actor_id = %d, want 400", rec.Code)
	}
}

// TestUmbilicalProvisionWorker_UnknownActor: a missing actor id is 404.
func TestUmbilicalProvisionWorker_UnknownActor(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	seedWorkerProvisioning(t, srv)
	if rec := postReq(t, h, "/api/village/umbilical/worker/provision", "tok", `{"actor_id":"ghost"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown actor = %d, want 404", rec.Code)
	}
}

// TestUmbilicalProvisionWorker_PCRejected: a human player (bram, seeded as a PC)
// is not provisionable — editableNPC resolves it to ErrActorNotFound (404).
func TestUmbilicalProvisionWorker_PCRejected(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	seedWorkerProvisioning(t, srv)
	if rec := postReq(t, h, "/api/village/umbilical/worker/provision", "tok", `{"actor_id":"bram"}`); rec.Code != http.StatusNotFound {
		t.Errorf("PC provision = %d, want 404 (editableNPC rejects PCs)", rec.Code)
	}
}

// TestUmbilicalProvisionWorker_Gated: the route honors the control surface gate
// (403 without plugins/administer, 401 with no token).
func TestUmbilicalProvisionWorker_Gated(t *testing.T) {
	_, h := controlServer(t, nil) // authed, no plugins/administer
	if rec := postReq(t, h, "/api/village/umbilical/worker/provision", "tok", `{"actor_id":"statue"}`); rec.Code != http.StatusForbidden {
		t.Errorf("non-operator = %d, want 403", rec.Code)
	}
	if rec := postReq(t, h, "/api/village/umbilical/worker/provision", "", `{"actor_id":"statue"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}
}
