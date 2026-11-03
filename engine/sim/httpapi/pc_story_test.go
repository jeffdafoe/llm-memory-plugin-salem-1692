package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// fakeRankWriter is standing_rank in memory: SetStandingRankThreshold edits a
// row (refusing the bottom rank and unknown ids, as the SQL does) and LoadRanks
// reads them back. gate, when set, holds the first write until it is closed.
type fakeRankWriter struct {
	mu      sync.Mutex
	rows    []sim.StandingRank
	calls   []string
	gate    chan struct{}
	entered chan struct{}
	// afterWrite, when set, runs once the write has committed.
	afterWrite func()
}

func (f *fakeRankWriter) SetStandingRankThreshold(_ context.Context, id string, at int) error {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("%s=%d", id, at))
	gate, entered := f.gate, f.entered
	f.gate = nil
	f.mu.Unlock()
	if gate != nil {
		close(entered)
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.rows {
		if f.rows[i].ID == id && f.rows[i].At > 0 {
			f.rows[i].At = at
			if f.afterWrite != nil {
				f.afterWrite()
			}
			return nil
		}
	}
	return sim.ErrStandingRankUnknown
}

// LoadRanks honours a cancelled context, as the pg pool does.
func (f *fakeRankWriter) LoadRanks(ctx context.Context) ([]sim.StandingRank, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sim.StandingRank(nil), f.rows...), nil
}

func (f *fakeRankWriter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// LLM-895: the operator moves a player's Standing; the rank follows, and the
// read shows the ladder.
func TestUmbilicalStory_StandingAndLadder(t *testing.T) {
	h, w := storyOperatorServer(t)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.StandingRanks = sim.NormalizeStandingRanks([]sim.StandingRank{
			{ID: "stranger", Name: "Stranger"},
			{ID: "sojourner", Name: "Sojourner", At: 50, Text: "up", DownText: "down"},
		})
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}

	resp := postReq(t, h, "/api/village/umbilical/story/standing", "tok", `{"actor_id":"bram","delta":60,"reason":"test"}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("standing = %d; body=%s", resp.Code, resp.Body.String())
	}
	var out umbilicalStoryStandingResponse
	_ = json.Unmarshal(resp.Body.Bytes(), &out)
	if out.Applied != 60 || out.Story == nil || out.Story.Rank.ID != "sojourner" {
		t.Fatalf("after +60: %+v", out)
	}
	resp = postReq(t, h, "/api/village/umbilical/story/standing", "tok", `{"actor_id":"bram","delta":-100}`)
	_ = json.Unmarshal(resp.Body.Bytes(), &out)
	if out.Applied != -60 || out.Story.Standing != 0 {
		t.Errorf("after -100: applied %d standing %d, want -60 and 0", out.Applied, out.Story.Standing)
	}
	for body, want := range map[string]int{
		`{"delta":5}`:                     http.StatusBadRequest,
		`{"actor_id":"bram","delta":0}`:   http.StatusBadRequest,
		`{"actor_id":"hannah","delta":5}`: http.StatusUnprocessableEntity,
		`{"actor_id":"ghost","delta":5}`:  http.StatusNotFound,
	} {
		if resp := postReq(t, h, "/api/village/umbilical/story/standing", "tok", body); resp.Code != want {
			t.Errorf("%s = %d, want %d", body, resp.Code, want)
		}
	}

	resp = getAs(t, h, "/api/village/umbilical/story", "tok")
	var read umbilicalStoryResponse
	_ = json.Unmarshal(resp.Body.Bytes(), &read)
	if len(read.Ranks) != 2 || read.Ranks[1].ID != "sojourner" || read.Ranks[1].DownText != "down" || read.Ranks[0].Prose == nil {
		t.Errorf("ladder = %+v", read.Ranks)
	}
}

// LLM-895: a threshold edit writes the table, reads it back and rebuilds the
// live ladder from it; it answers 503 unwired, 404 for the bottom or an
// unknown rank.
func TestUmbilicalStory_RankSet(t *testing.T) {
	w := storyWorld(t)
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	srv.SetControlEnabled(true)
	h := srv.Handler()

	if resp := postReq(t, h, "/api/village/umbilical/story/rank/set", "tok", `{"id":"sojourner","at":80}`); resp.Code != http.StatusServiceUnavailable {
		t.Errorf("unwired = %d, want 503", resp.Code)
	}
	wr := &fakeRankWriter{rows: []sim.StandingRank{
		{ID: "stranger", Name: "Stranger"}, {ID: "sojourner", Name: "Sojourner", At: 50},
		{ID: "inhabitant", Name: "Inhabitant", At: 150},
	}}
	srv.SetStandingRankWriter(wr)
	h = srv.Handler()
	var ladder []umbilicalStoryRankDTO
	// Two edits that collide: the live ladder ends as a load of the rows would.
	for _, body := range []string{`{"id":"sojourner","at":150}`, `{"id":"inhabitant","at":200}`} {
		resp := postReq(t, h, "/api/village/umbilical/story/rank/set", "tok", body)
		if resp.Code != http.StatusOK {
			t.Fatalf("%s = %d; body=%s", body, resp.Code, resp.Body.String())
		}
		_ = json.Unmarshal(resp.Body.Bytes(), &ladder)
	}
	rows, _ := wr.LoadRanks(context.Background())
	want := sim.NormalizeStandingRanks(rows)
	if len(ladder) != len(want) {
		t.Fatalf("ladder %+v, want %+v", ladder, want)
	}
	for i := range want {
		if ladder[i].ID != want[i].ID || ladder[i].At != want[i].At {
			t.Errorf("ladder[%d] = %s at %d, a load gives %s at %d", i, ladder[i].ID, ladder[i].At, want[i].ID, want[i].At)
		}
	}
	for body, code := range map[string]int{
		`{"at":80}`:                 http.StatusBadRequest,
		`{"id":"sojourner","at":0}`: http.StatusBadRequest,
		`{"id":"stranger","at":5}`:  http.StatusNotFound,
		`{"id":"nobody","at":5}`:    http.StatusNotFound,
	} {
		if resp := postReq(t, h, "/api/village/umbilical/story/rank/set", "tok", body); resp.Code != code {
			t.Errorf("%s = %d, want %d", body, resp.Code, code)
		}
	}
}

// LLM-895 (code_review R1): two edits cannot interleave. The first is held
// inside its durable write; the second must not reach the table until the
// first has applied, and the live ladder ends as the table says.
func TestUmbilicalStory_RankSetSerialized(t *testing.T) {
	w := storyWorld(t)
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	srv.SetControlEnabled(true)
	wr := &fakeRankWriter{
		rows:    []sim.StandingRank{{ID: "stranger", Name: "Stranger"}, {ID: "sojourner", Name: "Sojourner", At: 50}},
		gate:    make(chan struct{}),
		entered: make(chan struct{}),
	}
	gate := wr.gate
	srv.SetStandingRankWriter(wr)
	h := srv.Handler()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		postReq(t, h, "/api/village/umbilical/story/rank/set", "tok", `{"id":"sojourner","at":80}`)
	}()
	<-wr.entered
	go func() {
		defer wg.Done()
		postReq(t, h, "/api/village/umbilical/story/rank/set", "tok", `{"id":"sojourner","at":90}`)
	}()
	time.Sleep(50 * time.Millisecond)
	if n := wr.callCount(); n != 1 {
		t.Errorf("the second edit reached the table while the first was held: %d writes", n)
	}
	close(gate)
	wg.Wait()

	res, err := w.Send(sim.StoryReadout(""))
	if err != nil {
		t.Fatal(err)
	}
	ranks := res.(sim.StoryReadoutResult).Ranks
	if len(ranks) != 2 || ranks[1].At != 90 {
		t.Errorf("live ladder %+v, want Sojourner at 90 as the table holds", ranks)
	}
}

// LLM-895 (code_review R2): a client that goes away after the write commits
// does not leave the live ladder behind the table.
func TestUmbilicalStory_RankSetFinishesAfterTheClientLeaves(t *testing.T) {
	w := storyWorld(t)
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	srv.SetControlEnabled(true)
	wr := &fakeRankWriter{rows: []sim.StandingRank{{ID: "stranger", Name: "Stranger"}, {ID: "sojourner", Name: "Sojourner", At: 50}}}
	srv.SetStandingRankWriter(wr)
	h := srv.Handler()

	ctx, cancel := context.WithCancel(context.Background())
	wr.afterWrite = cancel
	r := httptest.NewRequest(http.MethodPost, "/api/village/umbilical/story/rank/set", strings.NewReader(`{"id":"sojourner","at":70}`)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer tok")
	h.ServeHTTP(httptest.NewRecorder(), r)

	res, err := w.Send(sim.StoryReadout(""))
	if err != nil {
		t.Fatal(err)
	}
	if ranks := res.(sim.StoryReadoutResult).Ranks; len(ranks) != 2 || ranks[1].At != 70 {
		t.Errorf("live ladder %+v, want Sojourner at 70 as the table holds", ranks)
	}
}

// LLM-895 (code_review R2): an edit whose ties would push a rank past the most
// Standing a player can hold is refused before anything is written.
func TestUmbilicalStory_RankSetRefusesALadderThatDoesNotFit(t *testing.T) {
	w := storyWorld(t)
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	srv.SetControlEnabled(true)
	wr := &fakeRankWriter{rows: []sim.StandingRank{
		{ID: "stranger", Name: "Stranger"}, {ID: "sojourner", Name: "Sojourner", At: 50},
		{ID: "selectman", Name: "Selectman", At: sim.MaxStanding},
	}}
	srv.SetStandingRankWriter(wr)
	body := fmt.Sprintf(`{"id":"sojourner","at":%d}`, sim.MaxStanding)
	if resp := postReq(t, srv.Handler(), "/api/village/umbilical/story/rank/set", "tok", body); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("= %d, want 422; body=%s", resp.Code, resp.Body.String())
	}
	if n := wr.callCount(); n != 0 {
		t.Errorf("%d writes, want none", n)
	}
}
