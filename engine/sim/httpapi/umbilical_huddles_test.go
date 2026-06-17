package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// seedHuddles stamps a structure + three huddles onto the seeded world:
//
//	h1 — active, in the tavern, members hannah (2 lines) + bram (0 lines: the
//	     one-sided/silent-member signal).
//	h-out — active, structureless (outdoor), member ezekiel, no lines.
//	h2 — concluded (excluded from the list, still fetchable by id).
func seedHuddles(t *testing.T, w *sim.World) {
	t.Helper()
	t0 := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	concluded := t0.Add(20 * time.Minute)
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["ezekiel"] = &sim.Actor{
			ID: "ezekiel", DisplayName: "Ezekiel", Kind: sim.KindNPCShared, State: sim.StateIdle,
		}
		world.Structures["tavern"] = &sim.Structure{ID: "tavern", DisplayName: "The Salty Mare"}
		world.Huddles["h1"] = &sim.Huddle{
			ID:             "h1",
			Members:        map[sim.ActorID]struct{}{"hannah": {}, "bram": {}},
			StructureID:    "tavern",
			StartedAt:      t0,
			LastActivityAt: t0.Add(5 * time.Minute),
			RecentUtterances: []sim.Utterance{
				{SpeakerID: "hannah", SpeakerName: "Hannah", Text: "A room for the night?", At: t0.Add(1 * time.Minute)},
				{SpeakerID: "hannah", SpeakerName: "Hannah", Text: "Two coins and it's yours.", At: t0.Add(2 * time.Minute)},
			},
		}
		world.Huddles["h-out"] = &sim.Huddle{
			ID:             "h-out",
			Members:        map[sim.ActorID]struct{}{"ezekiel": {}},
			StartedAt:      t0,
			LastActivityAt: t0.Add(10 * time.Minute), // later than h1 → sorts first
		}
		world.Huddles["h2"] = &sim.Huddle{
			ID:             "h2",
			Members:        map[sim.ActorID]struct{}{"hannah": {}},
			StructureID:    "tavern",
			StartedAt:      t0,
			LastActivityAt: t0.Add(3 * time.Minute),
			ConcludedAt:    &concluded,
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed huddles: %v", err)
	}
}

func TestUmbilical_Huddles(t *testing.T) {
	w := seededWorld(t)
	seedHuddles(t, w)
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	h := srv.Handler()

	rec := req(t, h, "/api/village/umbilical/huddles", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("huddles = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalHuddlesDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ContractVersion != ContractVersion {
		t.Errorf("contract_version = %d, want %d", out.ContractVersion, ContractVersion)
	}
	// Active-only: h2 (concluded) is excluded; h1 + h-out remain.
	if out.Total != 2 || len(out.Huddles) != 2 {
		t.Fatalf("total/len = %d/%d, want 2/2 (concluded h2 excluded)", out.Total, len(out.Huddles))
	}
	// Most-recently-active first: h-out (t0+10m) before h1 (t0+5m).
	if out.Huddles[0].ID != "h-out" || out.Huddles[1].ID != "h1" {
		t.Errorf("order = %s,%s, want h-out,h1 (most-recently-active first)", out.Huddles[0].ID, out.Huddles[1].ID)
	}
	// h-out is structureless (outdoor) → empty structure id/name.
	if out.Huddles[0].StructureID != "" || out.Huddles[0].StructureName != "" {
		t.Errorf("outdoor huddle structure = %q/%q, want empty", out.Huddles[0].StructureID, out.Huddles[0].StructureName)
	}
	// h1 row: structure resolved, 2 members sorted by id, per-member silence visible.
	h1 := out.Huddles[1]
	if h1.StructureName != "The Salty Mare" {
		t.Errorf("h1 structure_name = %q, want The Salty Mare", h1.StructureName)
	}
	if h1.MemberCount != 2 || h1.RecentUtteranceCount != 2 {
		t.Errorf("h1 member_count/recent_utterance_count = %d/%d, want 2/2", h1.MemberCount, h1.RecentUtteranceCount)
	}
	if len(h1.Members) != 2 || h1.Members[0].ID != "bram" || h1.Members[1].ID != "hannah" {
		t.Fatalf("h1 members = %+v, want [bram, hannah] sorted by id", h1.Members)
	}
	// The one-sided signal: bram silent (0), hannah spoke (2).
	if h1.Members[0].RecentUtterances != 0 || h1.Members[1].RecentUtterances != 2 {
		t.Errorf("h1 per-member counts = bram:%d hannah:%d, want 0/2", h1.Members[0].RecentUtterances, h1.Members[1].RecentUtterances)
	}
	if h1.Members[1].Name != "Hannah" {
		t.Errorf("h1 member name = %q, want Hannah", h1.Members[1].Name)
	}

	// Gating mirrors the read surface: 404 when off, 403 for a non-operator.
	if rec := req(t, NewServer(seededWorld(t), permAuth{operatorPerms}).Handler(), "/api/village/umbilical/huddles", "tok"); rec.Code != http.StatusNotFound {
		t.Errorf("huddles umbilical-off = %d, want 404", rec.Code)
	}
	if rec := req(t, umbilicalServer(t, nil, telemetry.New(4)), "/api/village/umbilical/huddles", "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("huddles non-operator = %d, want 403", rec.Code)
	}
}

func TestUmbilical_Huddle(t *testing.T) {
	w := seededWorld(t)
	seedHuddles(t, w)
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	h := srv.Handler()

	// Active huddle: detail + the recent-conversation ring (oldest first).
	rec := req(t, h, "/api/village/umbilical/huddle?id=h1", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("huddle h1 = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalHuddleDetailDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID != "h1" || out.ConversationID != "h1" {
		t.Errorf("id/conversation_id = %q/%q, want h1/h1 (the /turns?conversation= pivot)", out.ID, out.ConversationID)
	}
	if out.StructureName != "The Salty Mare" || out.MemberCount != 2 {
		t.Errorf("structure_name/member_count = %q/%d, want The Salty Mare/2", out.StructureName, out.MemberCount)
	}
	if out.ConcludedAt != nil {
		t.Errorf("active huddle concluded_at = %v, want nil", out.ConcludedAt)
	}
	if len(out.RecentUtterances) != 2 {
		t.Fatalf("recent_utterances = %d, want 2", len(out.RecentUtterances))
	}
	if out.RecentUtterances[0].Text != "A room for the night?" || out.RecentUtterances[1].Text != "Two coins and it's yours." {
		t.Errorf("utterances out of order: %+v", out.RecentUtterances)
	}
	if out.RecentUtterances[0].SpeakerName != "Hannah" {
		t.Errorf("utterance speaker = %q, want Hannah", out.RecentUtterances[0].SpeakerName)
	}

	// A concluded-but-not-cleared huddle is still fetchable, with concluded_at set.
	rec = req(t, h, "/api/village/umbilical/huddle?id=h2", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("huddle h2 = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var concluded UmbilicalHuddleDetailDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &concluded); err != nil {
		t.Fatalf("decode h2: %v", err)
	}
	if concluded.ConcludedAt == nil {
		t.Error("h2 concluded_at = nil, want a timestamp")
	}

	// Missing id → 400; unknown id → 404.
	if rec := req(t, h, "/api/village/umbilical/huddle", "tok"); rec.Code != http.StatusBadRequest {
		t.Errorf("missing id = %d, want 400", rec.Code)
	}
	if rec := req(t, h, "/api/village/umbilical/huddle?id=nope", "tok"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown huddle = %d, want 404", rec.Code)
	}

	// Gating: 404 when off, 403 for a non-operator.
	if rec := req(t, NewServer(seededWorld(t), permAuth{operatorPerms}).Handler(), "/api/village/umbilical/huddle?id=h1", "tok"); rec.Code != http.StatusNotFound {
		t.Errorf("huddle umbilical-off = %d, want 404", rec.Code)
	}
	if rec := req(t, umbilicalServer(t, nil, telemetry.New(4)), "/api/village/umbilical/huddle?id=h1", "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("huddle non-operator = %d, want 403", rec.Code)
	}
}
