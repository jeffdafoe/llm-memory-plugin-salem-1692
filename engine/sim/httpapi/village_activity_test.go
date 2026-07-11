package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// seedActivityLog stages a five-entry action log exercising every render
// path the Village tab cares about: speech, a counterparty act, a renderer
// fallback type (stayed_open), and an orphan actor id missing from the
// snapshot. Seeded through the real AppendActionLogEntry funnel so each
// entry gets its Seq assigned exactly as production appends do. Note: two
// entries deliberately share the t0+2m timestamp — the seq cursor must
// page across them where a time cursor would drop one.
func seedActivityLog(t *testing.T, w *sim.World, t0 time.Time) {
	t.Helper()
	entries := []sim.ActionLogEntry{
		{ActorID: "hannah", OccurredAt: t0, ActionType: sim.ActionTypeSpoke, Text: "good morning", HuddleID: "h1"},
		{ActorID: "bram", OccurredAt: t0.Add(2 * time.Minute), ActionType: sim.ActionTypeWalked, Text: "the tavern"},
		{ActorID: "bram", OccurredAt: t0.Add(2 * time.Minute), ActionType: sim.ActionTypePaid, Text: "stew", CounterpartyName: "Hannah", Amount: 3},
		{ActorID: "hannah", OccurredAt: t0.Add(3 * time.Minute), ActionType: sim.ActionTypeStayedOpen, Text: "a late customer lingers"},
		{ActorID: "ghost-7", OccurredAt: t0.Add(4 * time.Minute), ActionType: sim.ActionTypeConsumed, Text: "ale"},
	}
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		for _, e := range entries {
			if _, appendErr := sim.AppendActionLogEntry(e).Fn(world); appendErr != nil {
				return nil, appendErr
			}
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
	if out.LatestSeq == 0 || out.LatestSeq != out.Entries[4].Seq {
		t.Errorf("latest_seq = %d, want the newest entry's seq %d", out.LatestSeq, out.Entries[4].Seq)
	}

	// Seqs strictly increasing (append order) — the cursor's invariant.
	for i := range out.Entries {
		if out.Entries[i].Seq == 0 {
			t.Errorf("entry %d has zero seq — append funnel didn't assign", i)
		}
		if i > 0 && out.Entries[i].Seq <= out.Entries[i-1].Seq {
			t.Errorf("seqs not strictly increasing at %d: %d <= %d", i, out.Entries[i].Seq, out.Entries[i-1].Seq)
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

// TestRenderActionLogEntry_BarterShowsGoods (LLM-374): a pay_with_item settlement
// paid with a mix of coins AND goods narrates BOTH legs in the Village feed.
// Before the fix the line dropped the goods and read as coins-only, making a
// value-matched barter look like a shortchange. A pure-coin pay is unchanged
// (covered by TestVillageActivity_RenderAndOrder's "3 coins for stew" line).
func TestRenderActionLogEntry_BarterShowsGoods(t *testing.T) {
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"josiah": {DisplayName: "Josiah Thorne"},
		},
	}
	e := sim.ActionLogEntry{
		ActorID:          "josiah",
		ActionType:       sim.ActionTypePaid,
		CounterpartyName: "Joseph Scott",
		Amount:           4,
		PayItems:         []sim.ItemKindQty{{Kind: "cheese", Qty: 3}},
		Text:             "5x flour",
	}
	_, line, kind, ok := renderActionLogEntry(snap, e)
	if !ok || kind != "act" {
		t.Fatalf("render ok/kind = %v/%q, want true/act", ok, kind)
	}
	if want := "Josiah Thorne pays Joseph Scott 3 cheese and 4 coins for 5x flour."; line != want {
		t.Errorf("barter line = %q, want %q", line, want)
	}
}

func TestVillageActivity_CursorAndLimit(t *testing.T) {
	w := seededWorld(t)
	t0 := time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)
	seedActivityLog(t, w, t0)
	h := NewServer(w, permAuth{operatorPerms}).Handler()

	// Grab real seqs from a full fetch first.
	var all VillageActivityDTO
	rec := req(t, h, "/api/village/activity/recent", "tok")
	if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode all: %v", err)
	}
	if all.Returned != 5 {
		t.Fatalf("full fetch returned %d, want 5", all.Returned)
	}

	// Cursor at the walked entry (t0+2m): the same-timestamp paid sibling
	// MUST still come back — the seq cursor pages across timestamp
	// collisions where a time cursor would drop the straggler.
	var out VillageActivityDTO
	cursor := strconv.FormatUint(all.Entries[1].Seq, 10)
	rec = req(t, h, "/api/village/activity/recent?since_seq="+cursor, "tok")
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode since_seq: %v", err)
	}
	if out.Total != 5 || out.Returned != 3 {
		t.Errorf("since_seq total/returned = %d/%d, want 5/3", out.Total, out.Returned)
	}
	if out.Returned == 3 && (out.Entries[0].ActionType != "paid" || out.Entries[0].OccurredAt != all.Entries[1].OccurredAt) {
		t.Errorf("since_seq first entry = %+v, want the same-timestamp paid sibling", out.Entries[0])
	}

	// Cursor + limit keeps the OLDEST of the remainder (catch-up: the
	// client converges over successive polls, never skipping rows).
	rec = req(t, h, "/api/village/activity/recent?since_seq="+cursor+"&limit=2", "tok")
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode catch-up: %v", err)
	}
	if out.Returned != 2 || out.Entries[0].ActionType != "paid" || out.Entries[1].ActionType != "stayed_open" {
		t.Errorf("catch-up page = %+v, want [paid, stayed_open] (oldest first, ghost-7 next poll)", out.Entries)
	}
	if out.LatestSeq != all.LatestSeq {
		t.Errorf("catch-up latest_seq = %d, want %d (full-log newest regardless of paging)", out.LatestSeq, all.LatestSeq)
	}

	// Malformed since_seq is a 400, not a silent full tail.
	if rec := req(t, h, "/api/village/activity/recent?since_seq=yesterday", "tok"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad since_seq = %d, want 400", rec.Code)
	}

	// Cursorless limit keeps the newest N (backload mode).
	rec = req(t, h, "/api/village/activity/recent?limit=1", "tok")
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode limit: %v", err)
	}
	if out.Returned != 1 || out.Entries[0].ActorID != "ghost-7" {
		t.Errorf("limit=1 = %+v, want only the newest (ghost-7) entry", out.Entries)
	}

	// Cursor already at the head: empty page, latest_seq still reported —
	// the pair a live client sees between events.
	rec = req(t, h, "/api/village/activity/recent?since_seq="+strconv.FormatUint(all.LatestSeq, 10), "tok")
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode head: %v", err)
	}
	if out.Returned != 0 || out.LatestSeq != all.LatestSeq {
		t.Errorf("head poll = returned %d latest %d, want 0/%d", out.Returned, out.LatestSeq, all.LatestSeq)
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
