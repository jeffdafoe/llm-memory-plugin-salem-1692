package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// seededWorld stands up a running mem-backed world and applies a command that
// sets world state + a couple of actors and an object. Because Run republishes
// before replying to a command, world.Published() reflects these once Send
// returns, so the handlers (which read Published()) see them.
func seededWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)

	_, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Phase = sim.PhaseNight
		world.Environment.Weather = "clear"
		world.Environment.Atmosphere = "a hush over the square"
		world.Actors["hannah"] = &sim.Actor{
			ID: "hannah", DisplayName: "Hannah", Kind: sim.KindNPCShared,
			State: sim.StateIdle, Role: "innkeeper", CurrentX: 3, CurrentY: 4,
			InsideStructureID: "tavern",
		}
		world.Actors["bram"] = &sim.Actor{
			ID: "bram", DisplayName: "Bram", Kind: sim.KindPC,
			State: sim.StateWalking, CurrentX: 1, CurrentY: 1,
		}
		world.VillageObjects["obj1"] = &sim.VillageObject{
			ID: "obj1", AssetID: "asset-x", X: 5.5, Y: 6.5,
			CurrentState: "lit", DisplayName: "Tavern", Tags: []string{"vendor"},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed command: %v", err)
	}
	return w
}

func get(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200; body=%s", path, rec.Code, rec.Body.String())
	}
	return rec
}

func TestHandleWorld(t *testing.T) {
	srv := NewServer(seededWorld(t))
	rec := get(t, srv, "/api/village/world")

	var dto WorldStateDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.ContractVersion != ContractVersion {
		t.Errorf("contract_version = %d, want %d", dto.ContractVersion, ContractVersion)
	}
	if dto.Phase != "night" {
		t.Errorf("phase = %q, want night", dto.Phase)
	}
	if dto.Weather != "clear" {
		t.Errorf("weather = %q, want clear", dto.Weather)
	}
	if dto.Atmosphere != "a hush over the square" {
		t.Errorf("atmosphere = %q", dto.Atmosphere)
	}
}

func TestHandleAgents(t *testing.T) {
	srv := NewServer(seededWorld(t))
	rec := get(t, srv, "/api/village/agents")

	var agents []AgentDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &agents); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("len(agents) = %d, want 2", len(agents))
	}
	// Sorted by ID: bram < hannah.
	if agents[0].ID != "bram" || agents[1].ID != "hannah" {
		t.Fatalf("order = [%s %s], want [bram hannah]", agents[0].ID, agents[1].ID)
	}
	bram, hannah := agents[0], agents[1]
	if bram.Kind != "pc" {
		t.Errorf("bram.kind = %q, want pc", bram.Kind)
	}
	if bram.State != "walking" || bram.X != 1 || bram.Y != 1 {
		t.Errorf("bram state/pos = %q (%d,%d)", bram.State, bram.X, bram.Y)
	}
	if hannah.Kind != "npc_shared" {
		t.Errorf("hannah.kind = %q, want npc_shared", hannah.Kind)
	}
	if hannah.Role != "innkeeper" || hannah.X != 3 || hannah.Y != 4 || hannah.InsideStructureID != "tavern" {
		t.Errorf("hannah fields wrong: %+v", hannah)
	}
}

func TestHandleObjects(t *testing.T) {
	srv := NewServer(seededWorld(t))
	rec := get(t, srv, "/api/village/objects")

	var objs []ObjectDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &objs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("len(objects) = %d, want 1", len(objs))
	}
	o := objs[0]
	if o.ID != "obj1" || o.AssetID != "asset-x" || o.X != 5.5 || o.Y != 6.5 {
		t.Errorf("object identity/pos wrong: %+v", o)
	}
	if o.CurrentState != "lit" || o.DisplayName != "Tavern" || len(o.Tags) != 1 || o.Tags[0] != "vendor" {
		t.Errorf("object fields wrong: %+v", o)
	}
}

func TestNewServer_NilWorldPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic on nil world")
		}
	}()
	NewServer(nil)
}
