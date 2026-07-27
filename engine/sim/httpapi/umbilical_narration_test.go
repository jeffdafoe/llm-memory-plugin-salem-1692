package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// umbilical_narration_test.go — LLM-538 coverage for GET /umbilical/narration.

// narrationTestKey is the pool these tests drive: the reserved farewell, the one
// LLM-535 actually drifted. Deriving it rather than hardcoding the string keeps
// the test bound to BusinessownerNarrationKey's shape.
var narrationTestKey = sim.BusinessownerNarrationKey("reserved", sim.BusinessownerTriggerFarewell)

// seedNarrationExpansions puts the world into the LLM-535 shape: the reserved
// farewell carrying two generated lines on top of its seeds, mid-cycle draws,
// and an expansion attempt in flight.
func seedNarrationExpansions(t *testing.T, w *sim.World) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		p := world.NarrationPools[narrationTestKey]
		if p == nil {
			t.Fatalf("fixture invalid: %q is not in the seeded registry", narrationTestKey)
		}
		p.Extra = append(p.Extra, "Off with you, now.", "That will do.")
		p.Draws = 7
		p.InFlight = true
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed narration expansions: %v", err)
	}
}

func TestUmbilical_Narration(t *testing.T) {
	w := seededWorld(t)
	seedNarrationExpansions(t, w)
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	h := srv.Handler()

	rec := req(t, h, "/api/village/umbilical/narration", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("narration = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalNarrationDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ContractVersion != ContractVersion {
		t.Errorf("contract_version = %d, want %d", out.ContractVersion, ContractVersion)
	}
	// Every pool in the registry, not just the ones with expansions.
	if out.Total != 10 || len(out.Pools) != 10 {
		t.Fatalf("total/len = %d/%d, want 10/10 (6 businessowner + 2 lodging + retire + establishment-closing)",
			out.Total, len(out.Pools))
	}
	for i := 1; i < len(out.Pools); i++ {
		if out.Pools[i-1].Key >= out.Pools[i].Key {
			t.Fatalf("not sorted by key: %q before %q", out.Pools[i-1].Key, out.Pools[i].Key)
		}
	}

	var drifted UmbilicalNarrationPoolDTO
	for _, p := range out.Pools {
		if p.Key == narrationTestKey {
			drifted = p
		}
	}
	if drifted.Key == "" {
		t.Fatalf("%q missing from the response", narrationTestKey)
	}

	// The point of the route: the two generated lines are in `extra`, and the
	// seeds are not — this is what "is this line ours or the model's" reads as.
	if drifted.ExtraCount != 2 || len(drifted.Extra) != 2 {
		t.Fatalf("extra = %d lines %v, want the 2 generated ones", drifted.ExtraCount, drifted.Extra)
	}
	if drifted.Extra[0] != "Off with you, now." || drifted.Extra[1] != "That will do." {
		t.Errorf("extra = %v, want the seeded expansion lines in merge order", drifted.Extra)
	}
	for _, line := range drifted.Extra {
		for _, seed := range drifted.Seed {
			if line == seed {
				t.Errorf("%q appears in BOTH seed and extra — the split is broken", line)
			}
		}
	}
	if drifted.SeedCount != len(drifted.Seed) || drifted.SeedCount == 0 {
		t.Errorf("seed_count = %d against %d seed lines", drifted.SeedCount, len(drifted.Seed))
	}
	if drifted.TotalCount != drifted.SeedCount+drifted.ExtraCount {
		t.Errorf("total_count = %d, want seed_count+extra_count = %d",
			drifted.TotalCount, drifted.SeedCount+drifted.ExtraCount)
	}

	// The description is what the expansions were generated against — the field
	// that makes a drifted pool diagnosable rather than merely visible.
	if !strings.Contains(drifted.Description, "shopkeeper") {
		t.Errorf("description = %q, want the reserved-farewell meta prose", drifted.Description)
	}
	if !drifted.CustomerToken {
		t.Error("customer_token = false for a businessowner pool, which interpolates {customer}")
	}

	// Draw/cap state.
	if drifted.Draws != 7 || !drifted.InFlight {
		t.Errorf("draws/in_flight = %d/%v, want 7/true", drifted.Draws, drifted.InFlight)
	}
	if drifted.MaxCount != sim.NarrationPoolMaxPhrases {
		t.Errorf("max_count = %d, want %d", drifted.MaxCount, sim.NarrationPoolMaxPhrases)
	}
	if drifted.AtCap {
		t.Error("at_cap = true for a pool well under the cap")
	}
	want := sim.NarrationExpansionCycleFactor * drifted.TotalCount
	if drifted.ExpandsAtDraws == nil || *drifted.ExpandsAtDraws != want {
		t.Errorf("expands_at_draws = %v, want %d (cycle factor x total)", drifted.ExpandsAtDraws, want)
	}

	// Gating mirrors the rest of the read surface: 404 when the umbilical is off,
	// 403 for a non-operator.
	if rec := req(t, NewServer(seededWorld(t), permAuth{operatorPerms}).Handler(), "/api/village/umbilical/narration", "tok"); rec.Code != http.StatusNotFound {
		t.Errorf("narration umbilical-off = %d, want 404", rec.Code)
	}
	if rec := req(t, umbilicalServer(t, nil, telemetry.New(4)), "/api/village/umbilical/narration", "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("narration non-operator = %d, want 403", rec.Code)
	}
}

// TestUmbilical_NarrationEmptyExtraIsArray is the LLM-535 acceptance criterion
// that had no read: after the drifted rows are deleted from
// narration_pool_expansion and the engine restarts, the live pool must show an
// EMPTY extra — and it has to serialize as [] rather than null, since that is
// the difference between "confirmed clean" and "field absent, can't tell".
//
// A freshly seeded world is exactly the post-restart-with-no-rows state.
func TestUmbilical_NarrationEmptyExtraIsArray(t *testing.T) {
	srv := NewServer(seededWorld(t), permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(4))

	rec := req(t, srv.Handler(), "/api/village/umbilical/narration?key="+narrationTestKey, "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("narration = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalNarrationDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Pools) != 1 {
		t.Fatalf("pools = %d, want 1", len(out.Pools))
	}
	if out.Pools[0].ExtraCount != 0 || len(out.Pools[0].Extra) != 0 {
		t.Fatalf("extra = %v, want none — no expansion rows were merged", out.Pools[0].Extra)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"extra":[]`)) {
		t.Errorf("empty extra serialized as null, want []: %s", rec.Body.String())
	}
}

// TestUmbilical_NarrationKeyFilter: the filter narrows to one pool, matches
// case-insensitively, and an unknown key yields an EMPTY list rather than a 404
// (the /recipes + /items optional-filter posture).
func TestUmbilical_NarrationKeyFilter(t *testing.T) {
	w := seededWorld(t)
	seedNarrationExpansions(t, w)
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	h := srv.Handler()

	// Returns the raw body alongside the decoded DTO: a decoded nil slice and a
	// decoded empty slice are indistinguishable in Go, so the []-not-null
	// assertion has to read the actual response bytes (code_review).
	decode := func(t *testing.T, path string) (UmbilicalNarrationDTO, []byte) {
		t.Helper()
		rec := req(t, h, path, "tok")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200; body=%s", path, rec.Code, rec.Body.String())
		}
		var out UmbilicalNarrationDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out, rec.Body.Bytes()
	}

	for _, key := range []string{narrationTestKey, strings.ToUpper(narrationTestKey)} {
		one, _ := decode(t, "/api/village/umbilical/narration?key="+key)
		if one.Total != 1 || len(one.Pools) != 1 || one.Pools[0].Key != narrationTestKey {
			t.Fatalf("key=%q returned %d pools %+v, want just the reserved farewell", key, one.Total, one.Pools)
		}
	}

	out, body := decode(t, "/api/village/umbilical/narration?key=no-such-pool")
	if out.Total != 0 || len(out.Pools) != 0 {
		t.Errorf("unknown key returned %d pools, want an empty list", out.Total)
	}
	if !bytes.Contains(body, []byte(`"pools":[]`)) {
		t.Errorf("empty pools serialized as null, want []: %s", body)
	}
}

// TestUmbilical_NarrationAtCap pins the cap fields: a pool at MaxCount reports
// at_cap and OMITS expands_at_draws, because narrationDraw's cap gate stops it
// nudging however high the draw counter climbs. Reporting a threshold it will
// never reach would read as "an expansion is due" for a pool that is done.
//
// The over-cap row is here for the same reason `at_cap` is a `>=` test: nothing
// bounds the SEED table, so a pool authored with more than MaxCount seed lines
// lands over the cap at boot. It must read the same as exactly-at-cap, not fall
// through to a threshold it can never reach (code_review).
func TestUmbilical_NarrationAtCap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		total int
	}{
		{"exactly at cap", sim.NarrationPoolMaxPhrases},
		{"over cap", sim.NarrationPoolMaxPhrases + 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := seededWorld(t)
			_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
				p := world.NarrationPools[narrationTestKey]
				// Straight onto the slice, not through appendNarrationPhrases — that
				// helper enforces the cap, which is exactly what this fixture needs to
				// bypass to build an over-cap pool.
				for i := len(p.Seed) + len(p.Extra); i < tc.total; i++ {
					p.Extra = append(p.Extra, "filler line "+strconv.Itoa(i))
				}
				p.Draws = 500
				return nil, nil
			}})
			if err != nil {
				t.Fatalf("fill pool: %v", err)
			}
			srv := NewServer(w, permAuth{operatorPerms})
			srv.SetTelemetry(telemetry.New(4))

			rec := req(t, srv.Handler(), "/api/village/umbilical/narration?key="+narrationTestKey, "tok")
			if rec.Code != http.StatusOK {
				t.Fatalf("narration = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			var out UmbilicalNarrationDTO
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(out.Pools) != 1 {
				t.Fatalf("pools = %d, want 1", len(out.Pools))
			}
			p := out.Pools[0]
			if p.TotalCount != tc.total || !p.AtCap {
				t.Errorf("total_count/at_cap = %d/%v, want %d/true", p.TotalCount, p.AtCap, tc.total)
			}
			if p.ExpandsAtDraws != nil {
				t.Errorf("expands_at_draws = %d for a capped pool, want it omitted — it never expands again",
					*p.ExpandsAtDraws)
			}
			if bytes.Contains(rec.Body.Bytes(), []byte(`"expands_at_draws"`)) {
				t.Errorf("expands_at_draws present on the wire for a capped pool: %s", rec.Body.String())
			}
		})
	}
}

// TestUmbilical_NarrationInManifest pins the route into the READ (non-control)
// whitelist — the sibling reads all carry this assertion, and it is what proves
// the route is reachable without control armed.
func TestUmbilical_NarrationInManifest(t *testing.T) {
	srv := NewServer(seededWorld(t), permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(4))

	rec := req(t, srv.Handler(), "/api/village/umbilical", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest = %d, want 200", rec.Code)
	}
	var dto UmbilicalManifestDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.ControlEnabled {
		t.Fatal("fixture invalid: control is armed, so this cannot prove the route is a READ")
	}
	if !manifestRouteKeys(dto)["GET /api/village/umbilical/narration"] {
		t.Errorf("/umbilical/narration missing from the read manifest: %+v", dto.Routes)
	}
}
