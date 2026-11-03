package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// LLM-892 — the player's story on the wire: the login-scoped story_changed
// frame, story on /pc/me, POST /pc/story/seen, and the umbilical story routes.

func TestTranslateEvent_StoryChanged(t *testing.T) {
	view := sim.StoryView{
		Standing: 20,
		Rank:     sim.StandingRank{ID: "stranger", Name: "Stranger", At: 0},
		NextRank: &sim.StandingRank{ID: "sojourner", Name: "Sojourner", At: 50},
		Prose:    "A few villagers have started to know your face.",
		Mission:  &sim.StoryMissionView{ID: "arrival-3", Chapter: "Arrival", Title: "A Night Under Canvas", Text: "sleep", OfferedAt: time.Date(2026, 11, 2, 21, 30, 0, 0, time.UTC)},
		Unseen: []sim.StoryUnseenItem{
			{Key: "catch_up", Kind: "catch_up", Chapter: "Arrival", Titles: []string{"Make Camp", "A Fire of Your Own"}, Text: "You have already made a start here: your tent stands and your fire burns.", StandingGain: 20, At: time.Date(2026, 11, 2, 21, 30, 0, 0, time.UTC)},
			{Key: "offer:arrival-3", Kind: "offer", Chapter: "Arrival", Title: "A Night Under Canvas", Text: "sleep", At: time.Date(2026, 11, 2, 21, 30, 0, 0, time.UTC)},
		},
	}
	frame, ok := TranslateEvent(&sim.StoryChanged{ActorID: "bram", Login: "alice", View: view})
	if !ok || frame.Type != "story_changed" || frame.Target != "alice" {
		t.Fatalf("frame = %+v ok=%v, want story_changed to alice", frame, ok)
	}
	b, _ := json.Marshal(frame)
	want := `{"type":"story_changed","data":{"standing":20,"rank":{"id":"stranger","name":"Stranger","at":0},"next_rank":{"id":"sojourner","name":"Sojourner","at":50},"prose":"A few villagers have started to know your face.","mission":{"id":"arrival-3","chapter":"Arrival","title":"A Night Under Canvas","text":"sleep","offered_at":"2026-11-02T21:30:00Z"},"unseen":[{"key":"catch_up","kind":"catch_up","chapter":"Arrival","titles":["Make Camp","A Fire of Your Own"],"text":"You have already made a start here: your tent stands and your fire burns.","standing_gain":20,"at":"2026-11-02T21:30:00Z"},{"key":"offer:arrival-3","kind":"offer","chapter":"Arrival","title":"A Night Under Canvas","text":"sleep","at":"2026-11-02T21:30:00Z"}]}}`
	if string(b) != want {
		t.Errorf("wire =\n%s\nwant\n%s", b, want)
	}

	if _, ok := TranslateEvent(&sim.StoryChanged{ActorID: "bram", View: view}); ok {
		t.Error("StoryChanged with no login translated; want dropped (an empty Target broadcasts)")
	}
}

func TestStoryWire_TopRankAndNoMission(t *testing.T) {
	b, _ := json.Marshal(storyWire(sim.StoryView{Standing: 700, Rank: sim.StandingRank{ID: "freeman", Name: "Freeman", At: 600}}))
	if !strings.Contains(string(b), `"next_rank":null`) || !strings.Contains(string(b), `"mission":null`) || !strings.Contains(string(b), `"unseen":[]`) {
		t.Errorf("wire = %s, want next_rank null, mission null, unseen []", b)
	}
}

// storyWorld is triggerWorld (bram = alice, cara = bob) with a two-beat
// catalog, alice started on it, and hannah as a villager.
func storyWorld(t *testing.T) *sim.World {
	t.Helper()
	w := triggerWorld(t)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.StoryMissions = map[sim.StoryMissionID]*sim.StoryMission{
			"arrival-1": {ID: "arrival-1", Chapter: "Arrival", Seq: 1, Title: "Make Camp", OfferText: "pitch it", DoneText: "it stands",
				Trigger: sim.StoryTriggerCampPitched, StandingReward: 10, NextID: "arrival-5", IsStart: true},
			"arrival-5": {ID: "arrival-5", Chapter: "Arrival", Seq: 5, Title: "A Word with Your Neighbors", OfferText: "talk", DoneText: "word travels",
				Trigger: sim.StoryTriggerSpokeWithVillager, StandingReward: 10},
		}
		world.Actors["hannah"] = &sim.Actor{ID: "hannah", DisplayName: "Hannah", Kind: sim.KindNPCShared, LLMAgent: "hannah-va", Pos: sim.TilePos{X: 3, Y: 3}}
		world.StartStoryForPC(world.Actors["bram"], time.Now().UTC())
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed story: %v", err)
	}
	return w
}

func getAs(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestPCMe_CarriesStory(t *testing.T) {
	w := storyWorld(t)
	h := NewServer(w, loginAuth{}).Handler()

	resp := postReq(t, h, "/api/village/pc/me", "alice", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("pc/me = %d; body=%s", resp.Code, resp.Body.String())
	}
	var me struct {
		Story *storyWireDTO `json:"story"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if me.Story == nil || me.Story.Mission == nil || me.Story.Mission.ID != "arrival-1" || me.Story.Rank.ID != "stranger" {
		t.Fatalf("story = %+v", me.Story)
	}
	if len(me.Story.Unseen) != 1 || me.Story.Unseen[0].Key != "offer:arrival-1" {
		t.Errorf("unseen = %+v", me.Story.Unseen)
	}

	// bob (cara) has no story rows: no story field.
	resp = postReq(t, h, "/api/village/pc/me", "bob", "")
	if strings.Contains(resp.Body.String(), `"story"`) {
		t.Errorf("bob's pc/me carries a story: %s", resp.Body.String())
	}
}

func TestPCStorySeen(t *testing.T) {
	w := storyWorld(t)
	h := NewServer(w, loginAuth{}).Handler()

	resp := postReq(t, h, "/api/village/pc/story/seen", "alice", `{"key":"offer:arrival-1"}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("seen = %d; body=%s", resp.Code, resp.Body.String())
	}
	var story storyWireDTO
	if err := json.Unmarshal(resp.Body.Bytes(), &story); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(story.Unseen) != 0 || story.Mission == nil {
		t.Errorf("after seen: %+v", story)
	}
	// /pc/me agrees once the snapshot republishes.
	if body := postReq(t, h, "/api/village/pc/me", "alice", "").Body.String(); !strings.Contains(body, `"unseen":[]`) {
		t.Errorf("pc/me after seen: %s", body)
	}

	// Again: idempotent 200.
	if resp := postReq(t, h, "/api/village/pc/story/seen", "alice", `{"key":"offer:arrival-1"}`); resp.Code != http.StatusOK {
		t.Errorf("second seen = %d", resp.Code)
	}
	for body, want := range map[string]int{
		`{"key":""}`:        http.StatusBadRequest,
		`{"key":"bogus:x"}`: http.StatusBadRequest,
		`{"key":"x","y":1}`: http.StatusBadRequest,
		`not json`:          http.StatusBadRequest,
	} {
		if resp := postReq(t, h, "/api/village/pc/story/seen", "alice", body); resp.Code != want {
			t.Errorf("seen %s = %d, want %d", body, resp.Code, want)
		}
	}
	if resp := postReq(t, h, "/api/village/pc/story/seen", "nobody", `{"key":"catch_up"}`); resp.Code != http.StatusNotFound {
		t.Errorf("seen with no PC = %d, want 404", resp.Code)
	}
}

func storyOperatorServer(t *testing.T) (http.Handler, *sim.World) {
	t.Helper()
	w := storyWorld(t)
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	srv.SetControlEnabled(true)
	return srv.Handler(), w
}

func TestUmbilicalStory_ReadAndReset(t *testing.T) {
	h, _ := storyOperatorServer(t)

	resp := getAs(t, h, "/api/village/umbilical/story", "tok")
	if resp.Code != http.StatusOK {
		t.Fatalf("story = %d; body=%s", resp.Code, resp.Body.String())
	}
	var out umbilicalStoryResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Players) != 2 || out.Players[0].Name != "Bram" || out.Players[1].Name != "Cara" {
		t.Fatalf("players = %+v, want Bram and Cara by name", out.Players)
	}
	if bram := out.Players[0]; len(bram.Rows) != 1 || bram.Rows[0].MissionID != "arrival-1" || bram.Story.Mission == nil {
		t.Errorf("bram = %+v", bram)
	}

	if resp := getAs(t, h, "/api/village/umbilical/story?id=hannah", "tok"); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("story for a villager = %d, want 422", resp.Code)
	}
	if resp := getAs(t, h, "/api/village/umbilical/story?id=ghost", "tok"); resp.Code != http.StatusNotFound {
		t.Errorf("story for an unknown actor = %d, want 404", resp.Code)
	}

	// Close the offer, then reset: the start beat comes back unseen.
	resp = postReq(t, h, "/api/village/umbilical/story/reset", "tok", `{"actor_id":"bram"}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("reset = %d; body=%s", resp.Code, resp.Body.String())
	}
	var story storyWireDTO
	_ = json.Unmarshal(resp.Body.Bytes(), &story)
	if story.Standing != 0 || len(story.Unseen) != 1 || story.Unseen[0].Key != "offer:arrival-1" {
		t.Errorf("after reset: %+v", story)
	}
	if resp := postReq(t, h, "/api/village/umbilical/story/reset", "tok", `{}`); resp.Code != http.StatusBadRequest {
		t.Errorf("reset with no actor = %d, want 400", resp.Code)
	}
}

// story_changed goes through the hub to every socket of the owner's login and
// to no other player's socket (code_review).
func TestStoryChanged_ReachesOnlyTheOwner(t *testing.T) {
	w := storyWorld(t)
	hub := NewHub(sentinelTranslator)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(hub)
		return nil, nil
	}}); err != nil {
		t.Fatalf("subscribe hub: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)
	srv := NewServer(w, loginAuth{})
	srv.SetEventsHub(hub)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	alice1 := dialEventsAs(t, ts, "alice")
	alice2 := dialEventsAs(t, ts, "alice")
	bob := dialEventsAs(t, ts, "bob")
	waitConnected(t, hub, 3)

	if _, err := w.Send(sim.ResetStory("bram", time.Now().UTC())); err != nil {
		t.Fatalf("ResetStory: %v", err)
	}
	hub.Handle(nil, nil) // sentinel, broadcast to all

	for name, conn := range map[string]*websocket.Conn{"alice socket 1": alice1, "alice socket 2": alice2} {
		if n := countType(framesUntilSentinel(t, conn), "story_changed"); n != 1 {
			t.Errorf("%s got %d story_changed frames, want 1", name, n)
		}
	}
	if n := countType(framesUntilSentinel(t, bob), "story_changed"); n != 0 {
		t.Errorf("bob got %d story_changed frames, want 0", n)
	}
}

// One player's seen POST never touches another player's story (code_review).
func TestPCStorySeen_OnlyTheCallersStory(t *testing.T) {
	w := storyWorld(t)
	h := NewServer(w, loginAuth{}).Handler()

	// bob (cara) has no story rows; alice's key is a no-op for him.
	if resp := postReq(t, h, "/api/village/pc/story/seen", "bob", `{"key":"offer:arrival-1"}`); resp.Code != http.StatusOK {
		t.Fatalf("bob seen = %d; body=%s", resp.Code, resp.Body.String())
	}
	body := postReq(t, h, "/api/village/pc/me", "alice", "").Body.String()
	if !strings.Contains(body, `"key":"offer:arrival-1"`) {
		t.Errorf("alice's offer should still be unseen: %s", body)
	}
}

// The seen route refuses an oversized body and trailing JSON (code_review).
func TestPCStorySeen_BodyBounds(t *testing.T) {
	w := storyWorld(t)
	h := NewServer(w, loginAuth{}).Handler()
	big := `{"key":"` + strings.Repeat("x", maxStorySeenBodyBytes) + `"}`
	for label, body := range map[string]string{
		"oversized": big,
		"trailing":  `{"key":"offer:arrival-1"}{"key":"catch_up"}`,
	} {
		if resp := postReq(t, h, "/api/village/pc/story/seen", "alice", body); resp.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", label, resp.Code)
		}
	}
}
