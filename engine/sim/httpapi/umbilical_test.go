package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// operatorPerms is the permission map an operator (plugins/administer holder)
// carries; permAuth (operator_gate_test.go) turns it into a salem-realm principal.
var operatorPerms = map[string][]string{"plugins": {"administer"}}

// umbilicalServer builds a Server over the seeded world with a telemetry ring
// attached (= umbilical enabled), authenticating via permAuth{perms}.
func umbilicalServer(t *testing.T, perms map[string][]string, ring *telemetry.RingSink) http.Handler {
	t.Helper()
	srv := NewServer(seededWorld(t), permAuth{perms})
	srv.SetTelemetry(ring)
	return srv.Handler()
}

func req(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// TestUmbilical_OffByDefault: with no telemetry ring attached, the umbilical
// routes are not registered — even an operator gets 404 (the surface doesn't exist).
func TestUmbilical_OffByDefault(t *testing.T) {
	srv := NewServer(seededWorld(t), permAuth{operatorPerms})
	h := srv.Handler() // SetTelemetry NOT called
	for _, p := range []string{"/api/village/umbilical/telemetry", "/api/village/umbilical/state"} {
		if rec := req(t, h, p, "tok"); rec.Code != http.StatusNotFound {
			t.Errorf("%s with umbilical disabled = %d, want 404", p, rec.Code)
		}
	}
}

// TestUmbilical_GateEnforced: when enabled, the routes still require the operator
// capability — a non-operator is 403, a missing token is 401.
func TestUmbilical_GateEnforced(t *testing.T) {
	paths := []string{"/api/village/umbilical/telemetry", "/api/village/umbilical/state"}

	hNonOp := umbilicalServer(t, nil, telemetry.New(8)) // authed, no plugins/administer
	for _, p := range paths {
		if rec := req(t, hNonOp, p, "tok"); rec.Code != http.StatusForbidden {
			t.Errorf("%s as non-operator = %d, want 403", p, rec.Code)
		}
		if rec := req(t, hNonOp, p, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with no token = %d, want 401", p, rec.Code)
		}
	}
}

func TestUmbilical_Telemetry(t *testing.T) {
	ring := telemetry.New(8)
	ring.WriteTickTelemetry(sim.TickTelemetryRecord{ActorID: "hannah", AttemptID: "att-1", Kind: "started"})
	ring.WriteTickTelemetry(sim.TickTelemetryRecord{ActorID: "hannah", AttemptID: "att-1", Kind: "completed", Detail: map[string]string{"ms": "42"}})
	h := umbilicalServer(t, operatorPerms, ring)

	rec := req(t, h, "/api/village/umbilical/telemetry", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("telemetry = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalTelemetryDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ContractVersion != ContractVersion {
		t.Errorf("contract_version = %d, want %d", out.ContractVersion, ContractVersion)
	}
	if out.Stats.Written != 2 || out.Stats.Size != 2 || out.Stats.Dropped != 0 {
		t.Errorf("stats = %+v, want written=2 size=2 dropped=0", out.Stats)
	}
	if len(out.Records) != 2 {
		t.Fatalf("records len = %d, want 2", len(out.Records))
	}
	// Chronological order (oldest first).
	if out.Records[0].Kind != "started" || out.Records[1].Kind != "completed" {
		t.Errorf("record order = [%s, %s], want [started, completed]", out.Records[0].Kind, out.Records[1].Kind)
	}
	if out.Records[1].Detail["ms"] != "42" {
		t.Errorf("detail not carried: %+v", out.Records[1].Detail)
	}
}

func TestUmbilical_TickerHealth(t *testing.T) {
	h := umbilicalServer(t, operatorPerms, telemetry.New(8))

	rec := req(t, h, "/api/village/umbilical/ticker-health", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("ticker-health = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalTickerHealthDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ContractVersion != ContractVersion {
		t.Errorf("contract_version = %d, want %d", out.ContractVersion, ContractVersion)
	}
	if out.Now.IsZero() {
		t.Error("now is zero, want server wall-clock")
	}
	// The seeded world doesn't run tickers, so Tickers is an empty-but-present
	// array (the registry-beat→entry path is covered in sim/ticker_health_test.go).
	if out.Tickers == nil {
		t.Error("tickers is nil, want a (possibly empty) array")
	}

	// Gating: non-operator 403, no token 401, and 404 when the umbilical is off.
	hNonOp := umbilicalServer(t, nil, telemetry.New(8))
	if rec := req(t, hNonOp, "/api/village/umbilical/ticker-health", "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("non-operator = %d, want 403", rec.Code)
	}
	if rec := req(t, hNonOp, "/api/village/umbilical/ticker-health", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}
	hOff := NewServer(seededWorld(t), permAuth{operatorPerms}).Handler() // no SetTelemetry
	if rec := req(t, hOff, "/api/village/umbilical/ticker-health", "tok"); rec.Code != http.StatusNotFound {
		t.Errorf("umbilical off = %d, want 404", rec.Code)
	}
}

func TestUmbilical_Actions(t *testing.T) {
	w := seededWorld(t)
	t0 := time.Date(2026, 5, 24, 9, 0, 0, 0, time.UTC)
	// Seed a committed-action trail with the three v1 nonsense shapes in mind:
	// bram oscillating (walked farm → walked home) and hannah speaking in a huddle.
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ActionLog = []sim.ActionLogEntry{
			{ActorID: "bram", OccurredAt: t0, ActionType: sim.ActionTypeWalked, Text: "walked to the farm"},
			{ActorID: "bram", OccurredAt: t0.Add(time.Minute), ActionType: sim.ActionTypeWalked, Text: "walked back home"},
			{ActorID: "hannah", OccurredAt: t0.Add(2 * time.Minute), ActionType: sim.ActionTypeSpoke, Text: "good morning", HuddleID: "h1"},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed action log: %v", err)
	}

	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	h := srv.Handler()

	// Full tail.
	var all UmbilicalActionsDTO
	rec := req(t, h, "/api/village/umbilical/actions", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("actions = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if all.Total != 3 || all.Returned != 3 {
		t.Errorf("total/returned = %d/%d, want 3/3", all.Total, all.Returned)
	}
	// Chronological (oldest first) + content carried through.
	if all.Actions[0].ActorID != "bram" || all.Actions[0].ActionType != "walked" {
		t.Errorf("first action = %+v, want bram/walked", all.Actions[0])
	}
	if all.Actions[2].ActionType != "spoke" || all.Actions[2].HuddleID != "h1" {
		t.Errorf("last action = %+v, want spoke in huddle h1", all.Actions[2])
	}

	// Actor filter isolates one NPC's recent behavior (the oscillation view).
	var bram UmbilicalActionsDTO
	rec = req(t, h, "/api/village/umbilical/actions?actor=bram", "tok")
	if err := json.Unmarshal(rec.Body.Bytes(), &bram); err != nil {
		t.Fatalf("decode bram: %v", err)
	}
	if bram.Total != 3 || bram.Returned != 2 {
		t.Errorf("bram total/returned = %d/%d, want 3/2", bram.Total, bram.Returned)
	}
	for _, a := range bram.Actions {
		if a.ActorID != "bram" {
			t.Errorf("actor filter leaked %q", a.ActorID)
		}
	}

	// Limit keeps the most-recent N (chronological tail).
	var lim UmbilicalActionsDTO
	rec = req(t, h, "/api/village/umbilical/actions?limit=1", "tok")
	if err := json.Unmarshal(rec.Body.Bytes(), &lim); err != nil {
		t.Fatalf("decode limit: %v", err)
	}
	if lim.Returned != 1 || lim.Actions[0].ActorID != "hannah" {
		t.Errorf("limit=1 → %d entries, first=%v; want 1 entry (hannah, the latest)", lim.Returned, lim.Actions)
	}

	// Gate: registered with the read surface, so off-by-default 404 and
	// non-operator 403 hold (mirrors the other umbilical read routes).
	srvNoTel := NewServer(seededWorld(t), permAuth{operatorPerms})
	if rec := req(t, srvNoTel.Handler(), "/api/village/umbilical/actions", "tok"); rec.Code != http.StatusNotFound {
		t.Errorf("actions with umbilical disabled = %d, want 404", rec.Code)
	}
	if rec := req(t, umbilicalServer(t, nil, telemetry.New(4)), "/api/village/umbilical/actions", "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("actions as non-operator = %d, want 403", rec.Code)
	}
}

func TestUmbilical_TelemetrySummary(t *testing.T) {
	ring := telemetry.New(16)
	ring.WriteTickTelemetry(sim.TickTelemetryRecord{ActorID: "hannah", Kind: "deferred", Detail: map[string]string{"gate": "admission"}})
	ring.WriteTickTelemetry(sim.TickTelemetryRecord{ActorID: "hannah", Kind: "completed", Detail: map[string]string{"terminal_status": "success", "duration_ms": "100"}})
	ring.WriteTickTelemetry(sim.TickTelemetryRecord{ActorID: "bram", Kind: "failed", Detail: map[string]string{"terminal_status": "failed_before_render", "llm_error_class": "timeout", "duration_ms": "50"}})
	h := umbilicalServer(t, operatorPerms, ring)

	rec := req(t, h, "/api/village/umbilical/telemetry/summary", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("summary = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out TelemetrySummaryDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ByKind["completed"] != 1 || out.ByKind["failed"] != 1 || out.ByKind["deferred"] != 1 {
		t.Errorf("by_kind = %v, want completed/failed/deferred each 1", out.ByKind)
	}
	if out.ByLLMErrorClass["timeout"] != 1 {
		t.Errorf("by_llm_error_class = %v, want timeout:1", out.ByLLMErrorClass)
	}
	if out.DurationSamples != 2 || out.DurationMsMean != 75 {
		t.Errorf("durations: samples=%d mean=%d, want 2 / 75", out.DurationSamples, out.DurationMsMean)
	}
}

func TestUmbilical_Agent(t *testing.T) {
	ring := telemetry.New(8)
	ring.WriteTickTelemetry(sim.TickTelemetryRecord{ActorID: "hannah", Kind: "started"})
	w := seededWorld(t)
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(ring)
	h := srv.Handler()

	// Known actor.
	rec := req(t, h, "/api/village/umbilical/agent?id=hannah", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("agent = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalAgentDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID != "hannah" || out.DisplayName != "Hannah" || out.WorkStructureID != "tavern" {
		t.Errorf("agent = %+v, want hannah/Hannah/tavern", out)
	}
	if out.TileX != 3 || out.TileY != 4 {
		t.Errorf("tile = %d,%d, want 3,4", out.TileX, out.TileY)
	}

	// Missing id → 400; unknown actor → 404.
	if rec := req(t, h, "/api/village/umbilical/agent", "tok"); rec.Code != http.StatusBadRequest {
		t.Errorf("missing id = %d, want 400", rec.Code)
	}
	if rec := req(t, h, "/api/village/umbilical/agent?id=nobody", "tok"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown actor = %d, want 404", rec.Code)
	}
}

func TestUmbilical_Reactor(t *testing.T) {
	w := seededWorld(t)
	due := time.Now().Add(-time.Second) // past → due now
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		since := time.Now().Add(-time.Minute)
		world.Actors["hannah"].WarrantedSince = &since
		world.Actors["hannah"].WarrantDueAt = &due
		world.Actors["bram"].TickInFlight = true
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed reactor state: %v", err)
	}
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	h := srv.Handler()

	rec := req(t, h, "/api/village/umbilical/reactor", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("reactor = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalReactorDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Warranted != 1 || out.DueNow != 1 || out.InFlight != 1 {
		t.Errorf("reactor counts: warranted=%d due_now=%d in_flight=%d, want 1/1/1", out.Warranted, out.DueNow, out.InFlight)
	}
	// hannah (warranted) + bram (in-flight) both listed.
	if len(out.WarrantedActors) != 2 {
		t.Fatalf("warranted_actors = %d, want 2 (hannah + bram)", len(out.WarrantedActors))
	}
}

func TestUmbilical_ViewsGated(t *testing.T) {
	paths := []string{
		"/api/village/umbilical/telemetry/summary",
		"/api/village/umbilical/agent?id=hannah",
		"/api/village/umbilical/reactor",
	}
	// Off by default (no telemetry attached) → 404.
	off := NewServer(seededWorld(t), permAuth{operatorPerms}).Handler()
	for _, p := range paths {
		if rec := req(t, off, p, "tok"); rec.Code != http.StatusNotFound {
			t.Errorf("%s disabled = %d, want 404", p, rec.Code)
		}
	}
	// Enabled but non-operator → 403.
	nonOp := umbilicalServer(t, nil, telemetry.New(4))
	for _, p := range paths {
		if rec := req(t, nonOp, p, "tok"); rec.Code != http.StatusForbidden {
			t.Errorf("%s non-operator = %d, want 403", p, rec.Code)
		}
	}
}

func TestUmbilical_State(t *testing.T) {
	h := umbilicalServer(t, operatorPerms, telemetry.New(8))
	rec := req(t, h, "/api/village/umbilical/state", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("state = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalStateDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// seededWorld sets night + two actors (hannah, bram) + one object, none in flight.
	if out.World.Phase != "night" {
		t.Errorf("world.phase = %q, want night", out.World.Phase)
	}
	if out.Counts.Actors != 2 {
		t.Errorf("counts.actors = %d, want 2", out.Counts.Actors)
	}
	if out.Counts.VillageObjects != 1 {
		t.Errorf("counts.village_objects = %d, want 1", out.Counts.VillageObjects)
	}
	if out.TicksInFlight != 0 {
		t.Errorf("ticks_in_flight = %d, want 0", out.TicksInFlight)
	}
	if out.Telemetry.Capacity != 8 {
		t.Errorf("telemetry.capacity = %d, want 8", out.Telemetry.Capacity)
	}
}
