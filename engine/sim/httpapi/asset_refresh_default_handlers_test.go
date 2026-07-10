package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// stubAssetRefreshDefaultWriter is a test AssetRefreshDefaultWriter: it records the
// last call and can be made to fail (failErr != nil) to exercise the durable-write
// failure path. calls lets a test assert the writer was NOT reached (e.g. a 403 or
// 400 short-circuits before persistence).
type stubAssetRefreshDefaultWriter struct {
	failErr error

	calls int
	id    sim.AssetID
	rows  []*sim.ObjectRefresh
}

func (s *stubAssetRefreshDefaultWriter) UpdateAssetRefreshDefaults(_ context.Context, id sim.AssetID, rows []*sim.ObjectRefresh) error {
	s.calls++
	s.id, s.rows = id, rows
	return s.failErr
}

// newRefreshDefaultServer builds an admin-authed server over a seeded world (asset
// "asset-x" exists) with the refresh-default writer wired.
func newRefreshDefaultServer(t *testing.T) (*sim.World, *Server, *stubAssetRefreshDefaultWriter) {
	t.Helper()
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})
	writer := &stubAssetRefreshDefaultWriter{}
	srv.SetAssetRefreshDefaultWriter(writer)
	return w, srv, writer
}

// assetDefaultCount reads the seeded asset's RefreshDefaults length off the live
// catalog through the command channel, so a test can assert the in-memory catalog
// was mutated (not just that the writer was called + the response echoed).
func assetDefaultCount(t *testing.T, w *sim.World, id sim.AssetID) int {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Assets[id]
		if a == nil {
			return -1, nil
		}
		return len(a.RefreshDefaults), nil
	}})
	if err != nil {
		t.Fatalf("read defaults: %v", err)
	}
	return res.(int)
}

func TestHandleAssetSetRefreshDefault_Accepted(t *testing.T) {
	w, srv, writer := newRefreshDefaultServer(t)

	// Author from a DEPLETED source (available 2 of 10): the command normalizes to
	// a full supply, so the echo + the persisted rows carry available == max == 10.
	body := `{"asset_id":"asset-x","rows":[` +
		`{"attribute":"","amount":0,"available_quantity":2,"max_quantity":10,"refresh_mode":"continuous","refresh_period_hours":24,"gather_item":"sage"}` +
		`]}`
	rec := post(t, srv, "/api/village/admin/asset/set-refresh-default", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res adminObjectRefreshResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.ID != "asset-x" || len(res.Rows) != 1 {
		t.Fatalf("response = %+v, want asset-x with 1 row", res)
	}
	if res.Rows[0].GatherItem != "sage" || res.Rows[0].AvailableQuantity == nil ||
		*res.Rows[0].AvailableQuantity != 10 {
		t.Errorf("row = %+v, want gather sage normalized to full (10)", res.Rows[0])
	}
	// Durable writer reached with the normalized set.
	if writer.calls != 1 || writer.id != "asset-x" || len(writer.rows) != 1 ||
		writer.rows[0].AvailableQuantity == nil || *writer.rows[0].AvailableQuantity != 10 {
		t.Errorf("writer = %+v (calls=%d), want 1 call with a full-supply row", writer.rows, writer.calls)
	}
	// Live catalog mutated.
	if n := assetDefaultCount(t, w, "asset-x"); n != 1 {
		t.Errorf("live default count = %d, want 1", n)
	}
}

// TestHandleAssetSetRefreshDefault_Clears: an empty rows clears the template and the
// body carries "rows":[] (not null), matching the per-object route.
func TestHandleAssetSetRefreshDefault_Clears(t *testing.T) {
	w, srv, writer := newRefreshDefaultServer(t)

	rec := post(t, srv, "/api/village/admin/asset/set-refresh-default", `{"asset_id":"asset-x","rows":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, `"rows":[]`) {
		t.Errorf("body = %s, want rows:[]", got)
	}
	if writer.calls != 1 {
		t.Errorf("writer calls = %d, want 1 (clear still persists)", writer.calls)
	}
	if n := assetDefaultCount(t, w, "asset-x"); n != 0 {
		t.Errorf("live default count = %d, want 0", n)
	}
}

func TestHandleAssetSetRefreshDefault_Invalid(t *testing.T) {
	_, srv, writer := newRefreshDefaultServer(t)
	// Positive amount violates the amount_negative CHECK → 400, no persistence.
	rec := post(t, srv, "/api/village/admin/asset/set-refresh-default",
		`{"asset_id":"asset-x","rows":[{"attribute":"thirst","amount":5}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if writer.calls != 0 {
		t.Errorf("writer calls = %d, want 0 on invalid input", writer.calls)
	}
}

func TestHandleAssetSetRefreshDefault_NotFound(t *testing.T) {
	_, srv, writer := newRefreshDefaultServer(t)
	rec := post(t, srv, "/api/village/admin/asset/set-refresh-default",
		`{"asset_id":"ghost","rows":[{"attribute":"thirst","amount":-1}]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if writer.calls != 0 {
		t.Errorf("writer calls = %d, want 0 for unknown asset", writer.calls)
	}
}

func TestHandleAssetSetRefreshDefault_MissingAssetID(t *testing.T) {
	_, srv, _ := newRefreshDefaultServer(t)
	rec := post(t, srv, "/api/village/admin/asset/set-refresh-default", `{"rows":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleAssetSetRefreshDefault_Forbidden: a non-admin caller is refused by the
// adminCommand gate (the writer is wired, so 503 doesn't mask the 403).
func TestHandleAssetSetRefreshDefault_Forbidden(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	srv.SetAssetRefreshDefaultWriter(&stubAssetRefreshDefaultWriter{})
	rec := post(t, srv, "/api/village/admin/asset/set-refresh-default",
		`{"asset_id":"asset-x","rows":[]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleAssetSetRefreshDefault_Unwired: no durable writer → 503 before any apply.
func TestHandleAssetSetRefreshDefault_Unwired(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{}) // SetAssetRefreshDefaultWriter deliberately not called
	rec := post(t, srv, "/api/village/admin/asset/set-refresh-default",
		`{"asset_id":"asset-x","rows":[]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleAssetSetRefreshDefault_DurableWriteFails: the in-memory apply lands but
// the durable write errors → 500 (live is ahead of durable, reverts on restart).
func TestHandleAssetSetRefreshDefault_DurableWriteFails(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})
	srv.SetAssetRefreshDefaultWriter(&stubAssetRefreshDefaultWriter{failErr: errors.New("pg down")})

	rec := post(t, srv, "/api/village/admin/asset/set-refresh-default",
		`{"asset_id":"asset-x","rows":[{"attribute":"","amount":0,"available_quantity":5,"max_quantity":5,"refresh_mode":"continuous","refresh_period_hours":24,"gather_item":"sage"}]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	// The in-memory apply still landed (live ahead of durable).
	if n := assetDefaultCount(t, w, "asset-x"); n != 1 {
		t.Errorf("live default count = %d, want 1 (applied before the durable write failed)", n)
	}
}
