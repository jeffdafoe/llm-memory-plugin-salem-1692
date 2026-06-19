package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// fakeTranscriptStore is an in-test HuddleTranscriptStore: it records the args it
// was called with and returns canned rows (or an error), so the handler can be
// exercised without a pg pool.
type fakeTranscriptStore struct {
	rows      []sim.HuddleTranscriptRow
	err       error
	gotHuddle string
	gotLimit  int
}

func (f *fakeTranscriptStore) LoadHuddleTranscript(_ context.Context, huddleID string, limit int) ([]sim.HuddleTranscriptRow, error) {
	f.gotHuddle = huddleID
	f.gotLimit = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// transcriptServer builds an umbilical-enabled Server whose /transcript route is
// backed by store. A nil store is left unwired (not stored as a typed-nil
// interface), so the handler's nil-store 503 path is reachable.
func transcriptServer(t *testing.T, store HuddleTranscriptStore) http.Handler {
	t.Helper()
	srv := NewServer(seededWorld(t), permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	if store != nil {
		srv.SetTranscriptStore(store)
	}
	return srv.Handler()
}

// TestUmbilicalTranscript_ReturnsOldestFirst: the happy path. The handler passes
// the huddle and an over-fetch limit (cap+1) to the store and maps every row to
// the wire DTO in order, with has_more false for a sub-cap result.
func TestUmbilicalTranscript_ReturnsOldestFirst(t *testing.T) {
	t0 := time.Date(2026, 6, 18, 21, 21, 0, 0, time.UTC)
	store := &fakeTranscriptStore{rows: []sim.HuddleTranscriptRow{
		{OccurredAt: t0, Source: "player", SpeakerName: "Jefferey", ActionType: sim.ActionTypeSpoke, Text: "Good evening."},
		{OccurredAt: t0.Add(time.Minute), Source: "agent", SpeakerName: "Prudence", ActionType: sim.ActionTypeSpoke, Text: "And to you."},
		{OccurredAt: t0.Add(2 * time.Minute), Source: "agent", SpeakerName: "Prudence", ActionType: sim.ActionType("paid"), Text: ""},
	}}

	h := transcriptServer(t, store)
	rec := req(t, h, "/api/village/umbilical/transcript?huddle=hud-abc", "operator-tok")

	if rec.Code != http.StatusOK {
		t.Fatalf("transcript = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// The store was asked for this huddle, over-fetching one past the cap.
	if store.gotHuddle != "hud-abc" {
		t.Errorf("store huddle = %q, want hud-abc", store.gotHuddle)
	}
	if store.gotLimit != transcriptMaxRows+1 {
		t.Errorf("store limit = %d, want %d (cap+1 over-fetch)", store.gotLimit, transcriptMaxRows+1)
	}

	var out UmbilicalTranscriptDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if out.HuddleID != "hud-abc" || out.Returned != 3 || out.HasMore {
		t.Errorf("DTO huddle/returned/has_more = %q/%d/%v, want hud-abc/3/false", out.HuddleID, out.Returned, out.HasMore)
	}
	if len(out.Transcript) != 3 {
		t.Fatalf("transcript len = %d, want 3", len(out.Transcript))
	}
	// Oldest-first order + field mapping preserved.
	if out.Transcript[0].SpeakerName != "Jefferey" || out.Transcript[0].Source != "player" || out.Transcript[0].Text != "Good evening." {
		t.Errorf("row[0] = %+v, want the player's opening line", out.Transcript[0])
	}
	// A textless action keeps its action_type but omits text on the wire.
	if out.Transcript[2].ActionType != "paid" || out.Transcript[2].Text != "" {
		t.Errorf("row[2] = %+v, want action_type=paid with empty text", out.Transcript[2])
	}
}

// TestUmbilicalTranscript_Truncates: a huddle larger than the cap returns the cap
// rows with has_more true — truncation is reported, never silent.
func TestUmbilicalTranscript_Truncates(t *testing.T) {
	rows := make([]sim.HuddleTranscriptRow, transcriptMaxRows+1)
	for i := range rows {
		rows[i] = sim.HuddleTranscriptRow{Source: "agent", ActionType: sim.ActionTypeSpoke}
	}
	h := transcriptServer(t, &fakeTranscriptStore{rows: rows})
	rec := req(t, h, "/api/village/umbilical/transcript?huddle=hud-big", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("transcript = %d, want 200", rec.Code)
	}
	var out UmbilicalTranscriptDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.HasMore || out.Returned != transcriptMaxRows || len(out.Transcript) != transcriptMaxRows {
		t.Errorf("returned/has_more/len = %d/%v/%d, want %d/true/%d", out.Returned, out.HasMore, len(out.Transcript), transcriptMaxRows, transcriptMaxRows)
	}
}

// TestUmbilicalTranscript_MissingHuddle: the huddle query param is required → 400
// before the store is touched.
func TestUmbilicalTranscript_MissingHuddle(t *testing.T) {
	store := &fakeTranscriptStore{}
	h := transcriptServer(t, store)
	rec := req(t, h, "/api/village/umbilical/transcript", "tok")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("transcript with no huddle = %d, want 400", rec.Code)
	}
	if store.gotLimit != 0 {
		t.Errorf("store was called (limit=%d) despite missing huddle", store.gotLimit)
	}
}

// TestUmbilicalTranscript_NotConfigured: the route registers with the read
// surface even without a store, but can't serve → 503 (not a panic, not a 200).
func TestUmbilicalTranscript_NotConfigured(t *testing.T) {
	h := transcriptServer(t, nil) // umbilical on, but SetTranscriptStore NOT called
	rec := req(t, h, "/api/village/umbilical/transcript?huddle=hud-abc", "tok")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("transcript with no store = %d, want 503", rec.Code)
	}
}

// TestUmbilicalTranscript_StoreError: a store read failure is an honest 500, not
// a partial 200.
func TestUmbilicalTranscript_StoreError(t *testing.T) {
	h := transcriptServer(t, &fakeTranscriptStore{err: errors.New("pg down")})
	rec := req(t, h, "/api/village/umbilical/transcript?huddle=hud-abc", "tok")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("transcript with store error = %d, want 500", rec.Code)
	}
}

// TestUmbilicalTranscript_Gated: the route shares the read-surface gate — 404 when
// the umbilical is off, 403 for a non-operator, 401 with no token.
func TestUmbilicalTranscript_Gated(t *testing.T) {
	const path = "/api/village/umbilical/transcript?huddle=hud-abc"

	// Off by default (no telemetry) → 404 (the route isn't registered).
	off := NewServer(seededWorld(t), permAuth{operatorPerms}).Handler()
	if rec := req(t, off, path, "tok"); rec.Code != http.StatusNotFound {
		t.Errorf("transcript umbilical-off = %d, want 404", rec.Code)
	}
	// Enabled but non-operator → 403; no token → 401. (Gate runs before the
	// store, so no real store is needed here.)
	nonOp := NewServer(seededWorld(t), permAuth{nil})
	nonOp.SetTelemetry(telemetry.New(4))
	nonOp.SetTranscriptStore(&fakeTranscriptStore{})
	hNonOp := nonOp.Handler()
	if rec := req(t, hNonOp, path, "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("transcript non-operator = %d, want 403", rec.Code)
	}
	if rec := req(t, hNonOp, path, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("transcript no token = %d, want 401", rec.Code)
	}
}
