package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// seedActivityLog stages a five-entry action log exercising every render
// path the Village tab cares about: speech, a counterparty act, a renderer
// fallback type (stayed_open), and an orphan actor id missing from the
// snapshot.
func seedActivityLog(t *testing.T, w *sim.World, t0 time.Time) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ActionLog = []sim.ActionLogEntry{
			{ActorID: "hannah", OccurredAt: t0, ActionType: sim.ActionTypeSpoke, Text: "good morning", HuddleID: "h1"},
			{ActorID: "bram", OccurredAt: t0.Add(time.Minute), ActionType: sim.ActionTypeWalked, Text: "the tavern"},
			{ActorID: "bram", OccurredAt: t0.Add(2 * time.Minute), ActionType: sim.ActionTypePaid, Text: "stew", CounterpartyName: "Hannah", Amount: 3},
			{ActorID: "hannah", OccurredAt: t0.Add(3 * time.Minute), ActionType: sim.ActionTypeStayedOpen, Text: "a late customer lingers"},
			{ActorID: "ghost-7", OccurredAt: t0.Add(4 * time.Minute), ActionType: sim.ActionTypeConsumed, Text: "ale"},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed action log: %v", err)
	}
}

func TestVillageActivity_RenderAndOrder(t *testing.T) {
	w := seededWorld(t)
	t0 := time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)
	seedActivityLog(t, w, t0)
	h := NewServer(w, permAuth{operatorPerms}).Handler() // no SetTelemetry: route must work with the umbilical off

	rec := req(t, h, "/api/village/activity/recent", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("activity = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out VillageActivityDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != 5 || out.Returned != 5 {
		t.Fatalf("total/returned = %d/%d, want 5/5", out.Total, out.Returned)
	}

	// Chronological, oldest first.
	for i := 1; i < len(out.Entries); i++ {
		if out.Entries[i].OccurredAt.Before(out.Entries[i-1].OccurredAt) {
			t.Errorf("entries out of order at %d: %v < %v", i, out.Entries[i].OccurredAt, out.Entries[i-1].OccurredAt)
		}
	}

	// Speech: name-prefixed line, speech kind.
	if e := out.Entries[0]; e.Kind != "speech_npc" || e.Line != "Hannah: good morning" || e.ActorName != "Hannah" {
		t.Errorf("speech entry = %+v, want speech_npc 'Hannah: good morning'", e)
	}
	// Act narration embeds names + amount.
	if e := out.Entries[2]; e.Kind != "act" || e.Line != "Bram pays Hannah 3 coins for stew." {
		t.Errorf("paid entry = %+v, want act 'Bram pays Hannah 3 coins for stew.'", e)
	}
	// Renderer-fallback type is carried raw, never dropped.
	if e := out.Entries[3]; e.Kind != "raw" || e.Line != "Hannah — stayed_open: a late customer lingers" {
		t.Errorf("stayed_open entry = %+v, want raw fallback line", e)
	}
	// Orphan actor renders under its raw id — the troubleshooting signal.
	if e := out.Entries[4]; e.Kind != "raw" || e.Line != "ghost-7 — consumed: ale" || e.ActorName != "" {
		t.Errorf("orphan entry = %+v, want raw id-labelled line with empty actor_name", e)
	}
}

func TestVillageActivity_SinceAndLimit(t *testing.T) {
	w := seededWorld(t)
	t0 := time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)
	seedActivityLog(t, w, t0)
	h := NewServer(w, permAuth{operatorPerms}).Handler()

	// since: strictly-after cutoff — the t0+2m entry itself is excluded.
	var out VillageActivityDTO
	rec := req(t, h, "/api/village/activity/recent?since="+t0.Add(2*time.Minute).Format(time.RFC3339), "tok")
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode since: %v", err)
	}
	if out.Total != 5 || out.Returned != 2 {
		t.Errorf("since total/returned = %d/%d, want 5/2", out.Total, out.Returned)
	}
	if out.Returned == 2 && out.Entries[0].ActionType != "stayed_open" {
		t.Errorf("since first entry = %+v, want the t0+3m stayed_open", out.Entries[0])
	}

	// Malformed since is a 400, not a silent full tail.
	if rec := req(t, h, "/api/village/activity/recent?since=yesterday", "tok"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad since = %d, want 400", rec.Code)
	}

	// limit keeps the newest N.
	rec = req(t, h, "/api/village/activity/recent?limit=1", "tok")
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode limit: %v", err)
	}
	if out.Returned != 1 || out.Entries[0].ActorID != "ghost-7" {
		t.Errorf("limit=1 = %+v, want only the newest (ghost-7) entry", out.Entries)
	}
}

func TestVillageActivity_Gate(t *testing.T) {
	w := seededWorld(t)

	// Non-operator (plain salem session): 403. The tab is presentation, the
	// route is the real gate.
	hNonOp := NewServer(w, permAuth{map[string][]string{}}).Handler()
	if rec := req(t, hNonOp, "/api/village/activity/recent", "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("non-operator = %d, want 403", rec.Code)
	}
	// No token: 401.
	if rec := req(t, hNonOp, "/api/village/activity/recent", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}
}
