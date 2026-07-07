package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// stubAssetWriter is a test AssetGeometryWriter: it records the last call to each
// method and can be made to fail (failErr != nil) to exercise the durable-write
// failure path. Counts let a test assert the writer was NOT reached (e.g. a 403
// short-circuits before persistence).
type stubAssetWriter struct {
	failErr error

	doorCalls int
	doorID    sim.AssetID
	doorX     *int
	doorY     *int

	footCalls                    int
	footID                       sim.AssetID
	fLeft, fRight, fTop, fBottom int

	standCalls int
	standID    sim.AssetID
	standX     *int
	standY     *int
}

func (s *stubAssetWriter) UpdateAssetDoorOffset(_ context.Context, id sim.AssetID, x, y *int) error {
	s.doorCalls++
	s.doorID, s.doorX, s.doorY = id, x, y
	return s.failErr
}

func (s *stubAssetWriter) UpdateAssetFootprint(_ context.Context, id sim.AssetID, left, right, top, bottom int) error {
	s.footCalls++
	s.footID, s.fLeft, s.fRight, s.fTop, s.fBottom = id, left, right, top, bottom
	return s.failErr
}

func (s *stubAssetWriter) UpdateAssetStandOffset(_ context.Context, id sim.AssetID, x, y *int) error {
	s.standCalls++
	s.standID, s.standX, s.standY = id, x, y
	return s.failErr
}

// patch issues an authenticated PATCH (Bearer testToken) and returns the
// recorder without asserting status — the asset-write tests check varied codes.
func patch(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// copyTestIntPtr copies an int pointer so a test never holds a pointer aliasing
// live World.Assets state (mirrors sim.copyIntPtr, which is unexported).
func copyTestIntPtr(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// assetDoorOffset reads the live door offset off World.Assets["asset-x"] through
// the command channel, so a test can assert the in-memory catalog was mutated
// (not just that the writer was called + the response echoed). Copies the
// pointers on the world goroutine so nothing world-owned escapes to the test.
func assetDoorOffset(t *testing.T, w *sim.World, id sim.AssetID) (*int, *int) {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Assets[id]
		if a == nil {
			return [2]*int{}, nil
		}
		return [2]*int{copyTestIntPtr(a.DoorOffsetX), copyTestIntPtr(a.DoorOffsetY)}, nil
	}})
	if err != nil {
		t.Fatalf("read door offset: %v", err)
	}
	pair := res.([2]*int)
	return pair[0], pair[1]
}

func newAssetServer(t *testing.T) (*sim.World, *Server, *stubAssetWriter) {
	t.Helper()
	w := seededWorld(t) // seeds asset-x (structure) with door offset (1,2)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})
	writer := &stubAssetWriter{}
	srv.SetAssetGeometryWriter(writer)
	return w, srv, writer
}

func TestHandleAssetSetDoor_Accepted(t *testing.T) {
	w, srv, writer := newAssetServer(t)

	rec := patch(t, srv, "/api/assets/asset-x/door", `{"x":3,"y":4}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res assetDoorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.AssetID != "asset-x" || res.X == nil || *res.X != 3 || res.Y == nil || *res.Y != 4 {
		t.Errorf("response = %+v, want asset-x (3,4)", res)
	}
	// Durable write reached with the applied values.
	if writer.doorCalls != 1 || writer.doorID != "asset-x" || writer.doorX == nil || *writer.doorX != 3 || writer.doorY == nil || *writer.doorY != 4 {
		t.Errorf("writer door call = {n:%d id:%s x:%v y:%v}, want 1 asset-x 3 4", writer.doorCalls, writer.doorID, writer.doorX, writer.doorY)
	}
	// In-memory catalog mutated too.
	x, y := assetDoorOffset(t, w, "asset-x")
	if x == nil || *x != 3 || y == nil || *y != 4 {
		t.Errorf("in-memory door offset = (%v,%v), want (3,4)", x, y)
	}
}

func TestHandleAssetSetDoor_ClearsWhenNull(t *testing.T) {
	w, srv, writer := newAssetServer(t)

	rec := patch(t, srv, "/api/assets/asset-x/door", `{"x":null,"y":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if writer.doorCalls != 1 || writer.doorX != nil || writer.doorY != nil {
		t.Errorf("writer door call = {n:%d x:%v y:%v}, want 1 nil nil (cleared)", writer.doorCalls, writer.doorX, writer.doorY)
	}
	x, y := assetDoorOffset(t, w, "asset-x")
	if x != nil || y != nil {
		t.Errorf("in-memory door offset = (%v,%v), want cleared (nil,nil)", x, y)
	}
}

func TestHandleAssetSetDoor_HalfPairRejected(t *testing.T) {
	_, srv, writer := newAssetServer(t)

	rec := patch(t, srv, "/api/assets/asset-x/door", `{"x":3,"y":null}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if writer.doorCalls != 0 {
		t.Errorf("writer called %d times on a rejected half-pair, want 0", writer.doorCalls)
	}
}

// A missing field must be rejected, not read as "clear" — an empty body or a
// one-field body must never silently wipe the door offset.
func TestHandleAssetSetDoor_MissingFieldRejected(t *testing.T) {
	for _, body := range []string{`{}`, `{"x":1}`, `{"y":2}`} {
		_, srv, writer := newAssetServer(t)
		rec := patch(t, srv, "/api/assets/asset-x/door", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400; body=%s", body, rec.Code, rec.Body.String())
		}
		if writer.doorCalls != 0 {
			t.Errorf("body %s: writer called %d times, want 0 (missing field must not persist)", body, writer.doorCalls)
		}
	}
}

func TestHandleAssetSetDoor_NotFound(t *testing.T) {
	_, srv, writer := newAssetServer(t)

	rec := patch(t, srv, "/api/assets/ghost/door", `{"x":1,"y":2}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if writer.doorCalls != 0 {
		t.Errorf("writer called %d times for an unknown asset, want 0 (no durable write)", writer.doorCalls)
	}
}

// A caller with a valid session but no admin actor → 403, and the durable write
// never runs (the admin gate is inside the command, before persistence).
func TestHandleAssetSetDoor_Forbidden(t *testing.T) {
	w := seededWorld(t) // no admin actor seeded
	srv := NewServer(w, okAuth{})
	writer := &stubAssetWriter{}
	srv.SetAssetGeometryWriter(writer)

	rec := patch(t, srv, "/api/assets/asset-x/door", `{"x":3,"y":4}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if writer.doorCalls != 0 {
		t.Errorf("writer called %d times for a non-admin, want 0", writer.doorCalls)
	}
}

// No writer wired → 503, before any mutation/broadcast.
func TestHandleAssetSetDoor_WriterUnwired(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{}) // SetAssetGeometryWriter deliberately not called

	rec := patch(t, srv, "/api/assets/asset-x/door", `{"x":3,"y":4}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

// The in-memory apply + broadcast land, then the durable write fails → 500. The
// live catalog is ahead of the DB (reverts on restart); the handler says so.
func TestHandleAssetSetDoor_DurableFailure(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})
	srv.SetAssetGeometryWriter(&stubAssetWriter{failErr: errors.New("pg down")})

	rec := patch(t, srv, "/api/assets/asset-x/door", `{"x":7,"y":8}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	// Documented behavior: the live catalog was already mutated before the
	// durable write was attempted.
	x, y := assetDoorOffset(t, w, "asset-x")
	if x == nil || *x != 7 || y == nil || *y != 8 {
		t.Errorf("in-memory door offset = (%v,%v), want (7,8) applied-live", x, y)
	}
}

func TestHandleAssetSetDoor_MissingToken(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "admin-tester", "tester")
	srv := NewServer(w, okAuth{})
	srv.SetAssetGeometryWriter(&stubAssetWriter{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/assets/asset-x/door", strings.NewReader(`{"x":1,"y":2}`))
	srv.Handler().ServeHTTP(rec, req) // no Authorization header
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAssetSetFootprint_Accepted(t *testing.T) {
	_, srv, writer := newAssetServer(t)

	rec := patch(t, srv, "/api/assets/asset-x/footprint", `{"left":2,"right":3,"top":1,"bottom":4}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res assetFootprintResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Left != 2 || res.Right != 3 || res.Top != 1 || res.Bottom != 4 {
		t.Errorf("response = %+v, want (2,3,1,4)", res)
	}
	if writer.footCalls != 1 || writer.fLeft != 2 || writer.fRight != 3 || writer.fTop != 1 || writer.fBottom != 4 {
		t.Errorf("writer footprint call = {n:%d %d,%d,%d,%d}, want 1 (2,3,1,4)", writer.footCalls, writer.fLeft, writer.fRight, writer.fTop, writer.fBottom)
	}
}

func TestHandleAssetSetFootprint_NegativeRejected(t *testing.T) {
	_, srv, writer := newAssetServer(t)

	rec := patch(t, srv, "/api/assets/asset-x/footprint", `{"left":2,"right":-1,"top":0,"bottom":0}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if writer.footCalls != 0 {
		t.Errorf("writer called %d times on a negative footprint, want 0", writer.footCalls)
	}
}

// A missing (or typo'd) footprint side must be rejected, not persisted as a zero.
func TestHandleAssetSetFootprint_MissingFieldRejected(t *testing.T) {
	for _, body := range []string{`{"left":2}`, `{"left":2,"right":3,"top":1}`, `{"left":2,"right":3,"top":1,"botom":4}`} {
		_, srv, writer := newAssetServer(t)
		rec := patch(t, srv, "/api/assets/asset-x/footprint", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400; body=%s", body, rec.Code, rec.Body.String())
		}
		if writer.footCalls != 0 {
			t.Errorf("body %s: writer called %d times, want 0 (missing field must not persist)", body, writer.footCalls)
		}
	}
}

func TestHandleAssetSetStand_Accepted(t *testing.T) {
	_, srv, writer := newAssetServer(t)

	rec := patch(t, srv, "/api/assets/asset-x/stand", `{"x":0,"y":-1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res assetStandResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.X == nil || *res.X != 0 || res.Y == nil || *res.Y != -1 {
		t.Errorf("response = %+v, want (0,-1)", res)
	}
	if writer.standCalls != 1 || writer.standX == nil || *writer.standX != 0 || writer.standY == nil || *writer.standY != -1 {
		t.Errorf("writer stand call = {n:%d x:%v y:%v}, want 1 (0,-1)", writer.standCalls, writer.standX, writer.standY)
	}
}

// --- translator (event -> WS frame) ---------------------------------------

func TestTranslateEvent_AssetDoorOffsetChanged(t *testing.T) {
	x, y := 3, 4
	frame, ok := TranslateEvent(&sim.AssetDoorOffsetChanged{AssetID: "asset-x", X: &x, Y: &y})
	if !ok {
		t.Fatal("AssetDoorOffsetChanged should translate")
	}
	if frame.Type != "asset_door_updated" {
		t.Fatalf("type = %q, want asset_door_updated", frame.Type)
	}
	d, isType := frame.Data.(assetDoorUpdatedWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want assetDoorUpdatedWireDTO", frame.Data)
	}
	if d.AssetID != "asset-x" || d.X == nil || *d.X != 3 || d.Y == nil || *d.Y != 4 {
		t.Errorf("door payload = %+v, want asset-x (3,4)", d)
	}
	// A cleared door marshals x/y as JSON null (the client reads null as "unset").
	cleared, _ := TranslateEvent(&sim.AssetDoorOffsetChanged{AssetID: "asset-x"})
	b, err := json.Marshal(cleared.Data)
	if err != nil {
		t.Fatalf("marshal cleared: %v", err)
	}
	if got := string(b); !strings.Contains(got, `"x":null`) || !strings.Contains(got, `"y":null`) {
		t.Errorf("cleared door JSON = %s, want x/y null", got)
	}
}

func TestTranslateEvent_AssetFootprintChanged(t *testing.T) {
	frame, ok := TranslateEvent(&sim.AssetFootprintChanged{AssetID: "asset-x", Left: 2, Right: 3, Top: 1, Bottom: 4})
	if !ok {
		t.Fatal("AssetFootprintChanged should translate")
	}
	if frame.Type != "asset_footprint_updated" {
		t.Fatalf("type = %q, want asset_footprint_updated", frame.Type)
	}
	d, isType := frame.Data.(assetFootprintUpdatedWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want assetFootprintUpdatedWireDTO", frame.Data)
	}
	if d.AssetID != "asset-x" || d.Left != 2 || d.Right != 3 || d.Top != 1 || d.Bottom != 4 {
		t.Errorf("footprint payload = %+v, want asset-x (2,3,1,4)", d)
	}
}

func TestTranslateEvent_AssetStandOffsetChanged(t *testing.T) {
	x, y := 0, -1
	frame, ok := TranslateEvent(&sim.AssetStandOffsetChanged{AssetID: "asset-x", X: &x, Y: &y})
	if !ok {
		t.Fatal("AssetStandOffsetChanged should translate")
	}
	if frame.Type != "asset_stand_updated" {
		t.Fatalf("type = %q, want asset_stand_updated", frame.Type)
	}
	d, isType := frame.Data.(assetStandUpdatedWireDTO)
	if !isType {
		t.Fatalf("data type = %T, want assetStandUpdatedWireDTO", frame.Data)
	}
	if d.AssetID != "asset-x" || d.X == nil || *d.X != 0 || d.Y == nil || *d.Y != -1 {
		t.Errorf("stand payload = %+v, want asset-x (0,-1)", d)
	}
}
