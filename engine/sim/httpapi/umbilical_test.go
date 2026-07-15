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

// The pull view behind the world_command_stalled alarm (LLM-402). It reads the
// world's own recorder rather than asking the world goroutine anything — a route
// that had to send a command to report on the command loop would hang in exactly
// the incident it exists to describe, so the healthy-path shape here is load-bearing.
func TestUmbilical_WorldCommandHealth(t *testing.T) {
	h := umbilicalServer(t, operatorPerms, telemetry.New(8))

	rec := req(t, h, "/api/village/umbilical/world-command-health", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("world-command-health = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalWorldCommandHealthDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ContractVersion != ContractVersion {
		t.Errorf("contract_version = %d, want %d", out.ContractVersion, ContractVersion)
	}
	if out.Now.IsZero() {
		t.Error("now is zero, want server wall-clock")
	}
	// No prober runs against a seeded test world, so the health is the zero value —
	// which must read as HEALTHY (no timeout streak), never as a stall.
	if out.Health.ConsecutiveTimeouts != 0 {
		t.Errorf("consecutive_timeouts = %d on a world with no prober, want 0", out.Health.ConsecutiveTimeouts)
	}
	// The route is self-describing: the reader should not have to know the engine's
	// constants by heart to judge the numbers above.
	if out.Health.ProbeIntervalSeconds != sim.WorldCommandProbeInterval.Seconds() {
		t.Errorf("probe_interval_seconds = %v, want %v", out.Health.ProbeIntervalSeconds, sim.WorldCommandProbeInterval.Seconds())
	}
	if out.Health.ProbeTimeoutSeconds != sim.WorldCommandProbeTimeout.Seconds() {
		t.Errorf("probe_timeout_seconds = %v, want %v", out.Health.ProbeTimeoutSeconds, sim.WorldCommandProbeTimeout.Seconds())
	}

	// Gating: engine liveness is operator-only, same boundary as every other health
	// read. Non-operator 403, no token 401, 404 when the umbilical is off.
	hNonOp := umbilicalServer(t, nil, telemetry.New(8))
	if rec := req(t, hNonOp, "/api/village/umbilical/world-command-health", "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("non-operator = %d, want 403", rec.Code)
	}
	if rec := req(t, hNonOp, "/api/village/umbilical/world-command-health", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}
	hOffWC := NewServer(seededWorld(t), permAuth{operatorPerms}).Handler()
	if rec := req(t, hOffWC, "/api/village/umbilical/world-command-health", "tok"); rec.Code != http.StatusNotFound {
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
	// Attributes (LLM-421): the actor's role-marker slugs, sorted. seededWorld
	// gives hannah {tavernkeeper, businessowner}; the wire set is sorted.
	if got := strings.Join(out.Attributes, ","); got != "businessowner,tavernkeeper" {
		t.Errorf("attributes = %v, want [businessowner tavernkeeper] (sorted)", out.Attributes)
	}

	// An actor with no attributes emits an empty array `[]` (not null/absent), so
	// the operator can tell "no markers" from "field missing" (LLM-421 AC).
	rec = req(t, h, "/api/village/umbilical/agent?id=bram", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("agent bram = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"attributes":[]`) {
		t.Errorf("bram body = %s, want attributes:[] (empty array, not null)", rec.Body.String())
	}
	var bram UmbilicalAgentDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &bram); err != nil {
		t.Fatalf("decode bram: %v", err)
	}
	if bram.Attributes == nil || len(bram.Attributes) != 0 {
		t.Errorf("bram attributes = %v, want non-nil empty slice", bram.Attributes)
	}

	// Missing id → 400; unknown actor → 404.
	if rec := req(t, h, "/api/village/umbilical/agent", "tok"); rec.Code != http.StatusBadRequest {
		t.Errorf("missing id = %d, want 400", rec.Code)
	}
	if rec := req(t, h, "/api/village/umbilical/agent?id=nobody", "tok"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown actor = %d, want 404", rec.Code)
	}
}

// TestUmbilical_AgentRestockProduce: the agent view surfaces an actor's restock
// policy (item/source/cap, in policy order, LLM-111) and the in-flight one-shot
// production cycle (LLM-319), and omits both for an actor that manages nothing.
func TestUmbilical_AgentRestockProduce(t *testing.T) {
	w := seededWorld(t)
	anchor := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["hannah"]
		a.RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "horseshoe", Source: sim.RestockSourceProduce, Max: 5},
			{Item: "ale", Source: sim.RestockSourceBuy, Max: 12},
		}}
		a.ProductionActivity = &sim.ProductionActivity{
			Item: "horseshoe", BatchQty: 4, RemainingSeconds: 900, LastProgressAt: anchor,
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed restock/production: %v", err)
	}
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	h := srv.Handler()

	rec := req(t, h, "/api/village/umbilical/agent?id=hannah", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("agent = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalAgentDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.RestockPolicy) != 2 {
		t.Fatalf("restock_policy = %+v, want 2 entries", out.RestockPolicy)
	}
	// Policy order preserved (produce horseshoe first, buy ale second).
	if out.RestockPolicy[0].Item != "horseshoe" || out.RestockPolicy[0].Source != "produce" || out.RestockPolicy[0].Cap != 5 {
		t.Errorf("entry[0] = %+v, want horseshoe/produce/5", out.RestockPolicy[0])
	}
	if out.RestockPolicy[1].Item != "ale" || out.RestockPolicy[1].Source != "buy" || out.RestockPolicy[1].Cap != 12 {
		t.Errorf("entry[1] = %+v, want ale/buy/12", out.RestockPolicy[1])
	}
	if out.Production == nil || out.Production.Item != "horseshoe" ||
		out.Production.BatchQty != 4 || out.Production.RemainingSeconds != 900 {
		t.Fatalf("production = %+v, want horseshoe/4/900 (LLM-319)", out.Production)
	}
	if out.Production.LastProgressAt == nil || !out.Production.LastProgressAt.Equal(anchor) {
		t.Errorf("last_progress_at = %v, want %v", out.Production.LastProgressAt, anchor)
	}

	// A zero LastProgressAt (the post-restart posture — LoadAll leaves it
	// unstamped so downtime never counts as work) is omitted from the wire, not
	// rendered as a zero timestamp.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"].ProductionActivity.LastProgressAt = time.Time{}
		return nil, nil
	}}); err != nil {
		t.Fatalf("clear progress anchor: %v", err)
	}
	rec = req(t, h, "/api/village/umbilical/agent?id=hannah", "tok")
	out = UmbilicalAgentDTO{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode hannah (zero anchor): %v", err)
	}
	if out.Production == nil || out.Production.LastProgressAt != nil {
		t.Errorf("zero-anchor production = %+v, want non-nil with last_progress_at omitted", out.Production)
	}

	// An actor with no policy and nothing in the works omits both fields.
	rec = req(t, h, "/api/village/umbilical/agent?id=bram", "tok")
	out = UmbilicalAgentDTO{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode bram: %v", err)
	}
	if len(out.RestockPolicy) != 0 || out.Production != nil {
		t.Errorf("bram (no policy) = restock %v production %v, want both absent", out.RestockPolicy, out.Production)
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
		"/api/village/umbilical/structures",
		"/api/village/umbilical/objects",
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

// manifestRouteKeys indexes a manifest's routes by "METHOD path" so presence
// checks assert the real route identity (method + path), not just the path.
func manifestRouteKeys(m UmbilicalManifestDTO) map[string]bool {
	out := make(map[string]bool, len(m.Routes))
	for _, r := range m.Routes {
		out[r.Method+" "+r.Path] = true
	}
	return out
}

func TestUmbilical_Manifest(t *testing.T) {
	// Read-only: umbilical on, control off.
	h := umbilicalServer(t, operatorPerms, telemetry.New(8))
	rec := req(t, h, "/api/village/umbilical", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var ro UmbilicalManifestDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &ro); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ro.ContractVersion != ContractVersion {
		t.Errorf("contract_version = %d, want %d", ro.ContractVersion, ContractVersion)
	}
	if !ro.Enabled || ro.ControlEnabled {
		t.Errorf("enabled=%v control_enabled=%v, want true/false", ro.Enabled, ro.ControlEnabled)
	}
	roRoutes := manifestRouteKeys(ro)
	if !roRoutes["GET /api/village/umbilical"] {
		t.Error("manifest must list itself")
	}
	if !roRoutes["GET /api/village/umbilical/telemetry"] {
		t.Error("manifest must list the telemetry read route")
	}
	for _, r := range ro.Routes {
		if r.Control {
			t.Errorf("control route %q listed while control disabled", r.Path)
		}
	}
	// The gating invariant the refactor must preserve: a control route is not
	// just absent from the manifest — it isn't registered at all (404). Guards
	// against manifest filtering and registration drifting apart.
	if rec := postReq(t, h, "/api/village/umbilical/nudge", "tok", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("control route registered with control disabled = %d, want 404", rec.Code)
	}

	// Control enabled: the whitelist appears and control_enabled flips.
	_, hc := controlServer(t, operatorPerms)
	rec = req(t, hc, "/api/village/umbilical", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest (control) = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var co UmbilicalManifestDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &co); err != nil {
		t.Fatalf("decode control: %v", err)
	}
	if !co.ControlEnabled {
		t.Error("control_enabled = false, want true")
	}
	coRoutes := manifestRouteKeys(co)
	if !coRoutes["POST /api/village/umbilical/nudge"] || !coRoutes["POST /api/village/umbilical/grant"] {
		t.Errorf("control manifest missing control routes: %v", coRoutes)
	}
	if len(co.Routes) <= len(ro.Routes) {
		t.Errorf("control manifest (%d routes) should exceed read-only (%d)", len(co.Routes), len(ro.Routes))
	}

	// The manifest can't lie: every route it advertises is actually registered
	// on the live mux (a non-404 response). GET routes are probed with GET, POST
	// (control) routes with POST so the method matches the registration.
	for _, r := range co.Routes {
		var probe *httptest.ResponseRecorder
		switch r.Method {
		case http.MethodGet:
			probe = req(t, hc, r.Path, "tok")
		case http.MethodPost:
			probe = postReq(t, hc, r.Path, "tok", `{}`)
		default:
			t.Fatalf("unexpected manifest method %q for route %q", r.Method, r.Path)
		}
		if probe.Code == http.StatusNotFound {
			t.Errorf("manifest advertises %s %s but it is not registered (404)", r.Method, r.Path)
		}
	}

	// Off by default (no telemetry) → 404, like every umbilical route.
	off := NewServer(seededWorld(t), permAuth{operatorPerms}).Handler()
	if rec := req(t, off, "/api/village/umbilical", "tok"); rec.Code != http.StatusNotFound {
		t.Errorf("manifest with umbilical off = %d, want 404", rec.Code)
	}
	// Enabled but non-operator → 403; no token → 401.
	nonOp := umbilicalServer(t, nil, telemetry.New(8))
	if rec := req(t, nonOp, "/api/village/umbilical", "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("manifest non-operator = %d, want 403", rec.Code)
	}
	if rec := req(t, nonOp, "/api/village/umbilical", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("manifest no token = %d, want 401", rec.Code)
	}
}

func TestUmbilical_Actors(t *testing.T) {
	w := seededWorld(t)
	// Seed live needs on hannah (the NPC); bram (the PC) stays needs-less to
	// prove a nil Needs map serializes as an omitted field, not a panic.
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"].Needs = map[sim.NeedKey]int{"hunger": sim.NeedMax, "thirst": 12, "tiredness": 0}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed needs: %v", err)
	}
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	h := srv.Handler()

	rec := req(t, h, "/api/village/umbilical/actors", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("actors = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalActorsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != 2 || len(out.Actors) != 2 {
		t.Fatalf("total/len = %d/%d, want 2/2", out.Total, len(out.Actors))
	}
	// Sorted by id: bram (PC, no needs) then hannah (NPC, needs present).
	if out.Actors[0].ID != "bram" || out.Actors[1].ID != "hannah" {
		t.Errorf("order = %s,%s, want bram,hannah", out.Actors[0].ID, out.Actors[1].ID)
	}
	if out.Actors[0].Needs != nil {
		t.Errorf("bram needs = %v, want nil (needs-less PC omits the field)", out.Actors[0].Needs)
	}
	if out.Actors[1].Needs["hunger"] != sim.NeedMax || out.Actors[1].Needs["thirst"] != 12 {
		t.Errorf("hannah needs = %v, want hunger=%d thirst=12", out.Actors[1].Needs, sim.NeedMax)
	}
	// Attributes (LLM-421): roster rows carry the sorted role-marker slugs, and
	// omitempty means a marker-less actor omits the field. bram (PC) has none;
	// hannah has {businessowner, tavernkeeper}.
	if out.Actors[0].Attributes != nil {
		t.Errorf("bram attributes = %v, want nil (omitempty, no markers)", out.Actors[0].Attributes)
	}
	if got := strings.Join(out.Actors[1].Attributes, ","); got != "businessowner,tavernkeeper" {
		t.Errorf("hannah attributes = %v, want [businessowner tavernkeeper]", out.Actors[1].Attributes)
	}
	// Assert the omitempty behavior on the actual response bytes, not just the
	// decoded slice: exactly one of the two rows (hannah) carries the field, so
	// `"attributes"` appears once and bram's row omits it. Guards against a future
	// marshaler / DTO change that keeps the decode assertions green while altering
	// the wire contract.
	if n := strings.Count(rec.Body.String(), `"attributes"`); n != 1 {
		t.Errorf("attributes field count = %d, want 1 (only hannah's row; bram omits it)", n)
	}
	if !strings.Contains(rec.Body.String(), `"attributes":["businessowner","tavernkeeper"]`) {
		t.Errorf("body = %s, want hannah attributes:[businessowner,tavernkeeper] on the wire", rec.Body.String())
	}

	// Gating mirrors the read surface: 404 when the umbilical is off, 403 for a
	// non-operator.
	if rec := req(t, NewServer(seededWorld(t), permAuth{operatorPerms}).Handler(), "/api/village/umbilical/actors", "tok"); rec.Code != http.StatusNotFound {
		t.Errorf("actors umbilical-off = %d, want 404", rec.Code)
	}
	if rec := req(t, umbilicalServer(t, nil, telemetry.New(4)), "/api/village/umbilical/actors", "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("actors non-operator = %d, want 403", rec.Code)
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
	// Coin supply (LLM-410): hannah holds 25, bram (PC) 0 — both non-decorative,
	// neither a visitor, so 25 total across 2 resident holders.
	if out.CoinSupply != (sim.CoinSupply{Total: 25, Resident: 25, Visitor: 0, Holders: 2}) {
		t.Errorf("coin_supply = %+v, want {Total:25 Resident:25 Visitor:0 Holders:2}", out.CoinSupply)
	}
}

// TestUmbilical_State_CoinSupply: the /state mapper carries the money-supply
// gauge, excluding decoratives and splitting a transient visitor's purse out of
// the resident total. Exercises the pure snapshot→DTO mapper directly.
func TestUmbilical_State_CoinSupply(t *testing.T) {
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"keeper": {Kind: sim.KindNPCStateful, Coins: 40},
			"factor": {Kind: sim.KindNPCShared, Coins: 200, VisitorState: &sim.VisitorState{Archetype: "peddler"}},
			"statue": {Kind: sim.KindDecorative, Coins: 999},
		},
	}
	out := umbilicalStateFromSnapshot(snap, telemetry.Stats{})
	if out.CoinSupply != (sim.CoinSupply{Total: 240, Resident: 40, Visitor: 200, Holders: 2}) {
		t.Errorf("coin_supply = %+v, want {Total:240 Resident:40 Visitor:200 Holders:2}", out.CoinSupply)
	}
}

// TestUmbilical_State_ConfigWarnings: /state surfaces the LLM-60 data-config
// audit — a nameless gather/eat source is flagged (the resolver can't reach it),
// a named one is not. Exercises the pure snapshot→DTO mapper directly.
func TestUmbilical_State_ConfigWarnings(t *testing.T) {
	snap := &sim.Snapshot{
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"nameless-bush": {ID: "nameless-bush", Refreshes: []*sim.ObjectRefresh{{GatherItem: "blueberries"}}},
			"named-bush":    {ID: "named-bush", DisplayName: "Raspberry Bush", Refreshes: []*sim.ObjectRefresh{{GatherItem: "raspberries"}}},
		},
	}
	out := umbilicalStateFromSnapshot(snap, telemetry.Stats{})
	if len(out.ConfigWarnings) != 1 {
		t.Fatalf("config_warnings = %v, want exactly 1 (the nameless gatherable bush)", out.ConfigWarnings)
	}
	if !strings.Contains(out.ConfigWarnings[0], "nameless-bush") {
		t.Errorf("config_warnings[0] = %q, want it to name nameless-bush", out.ConfigWarnings[0])
	}
}
