package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_outhouse_test.go — LLM-806: a player goes into an outhouse.

// seedOuthouse places an object whose loiter pin is (pinX, pinY), tagged
// outhouse when tagged is set.
func seedOuthouse(t *testing.T, w *sim.World, id string, pinX, pinY int, tagged bool) {
	t.Helper()
	zero := 0
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		asset := &sim.Asset{ID: "asset-outhouse", Name: "Outhouse 1", Category: "prop"}
		world.Assets[asset.ID] = asset
		obj := &sim.VillageObject{
			ID: sim.VillageObjectID(id), AssetID: asset.ID,
			Pos:           sim.WorldPos{X: float64(pinX-sim.PadX)*sim.TileSize + 1, Y: float64(pinY-sim.PadY)*sim.TileSize + 1},
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
		}
		if tagged {
			obj.Tags = []string{sim.TagOuthouse}
		}
		world.VillageObjects[obj.ID] = obj
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seedOuthouse: %v", err)
	}
}

func TestHandlePCOuthouse(t *testing.T) {
	w := seededWorld(t)
	seedPC(t, w, "pc-tester", "tester", 10, 10)
	seedPC(t, w, "pc-other", "other", 30, 30)
	seedOuthouse(t, w, "outhouse", 11, 10, true)
	seedOuthouse(t, w, "barrel", 9, 10, false)
	var used []*sim.OuthouseUsed
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, e sim.Event) {
			if ev, ok := e.(*sim.OuthouseUsed); ok {
				used = append(used, ev)
			}
		}))
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(w, okAuth{})

	for _, tc := range []struct{ body, why string }{
		{`{}`, "no object_id"},
		{`{"object_id":"outhouse","x":1}`, "an unknown field"},
	} {
		if rec := post(t, srv, "/api/village/pc/outhouse", tc.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.why, rec.Code)
		}
	}
	if rec := post(t, srv, "/api/village/pc/outhouse", `{"object_id":"barrel"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("not an outhouse: status = %d, want 422", rec.Code)
	}
	if _, err := w.Send(sim.UseOuthouse("pc-other", "outhouse", time.Now().UTC())); !errors.Is(err, sim.ErrOuthouseOutOfReach) {
		t.Errorf("from across the village: err = %v, want ErrOuthouseOutOfReach", err)
	}

	rec := post(t, srv, "/api/village/pc/outhouse", `{"object_id":"outhouse"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"seconds":10`) {
		t.Errorf("body = %s, want seconds 10", rec.Body.String())
	}
	if _, err := w.Send(sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}); err != nil {
		t.Fatal(err)
	}
	if len(used) != 1 || used[0].ObjectID != "outhouse" || used[0].ActorID != "pc-tester" || used[0].Stay != sim.OuthouseStay {
		t.Fatalf("OuthouseUsed events = %+v, want one for pc-tester", used)
	}

	// The other player walks up while the tester is inside.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["pc-other"].Pos = sim.TilePos{X: 12, Y: 10}
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Send(sim.UseOuthouse("pc-other", "outhouse", time.Now().UTC())); !errors.Is(err, sim.ErrOuthouseInUse) {
		t.Errorf("while occupied: err = %v, want ErrOuthouseInUse", err)
	}
	// Once the stay is over the other player goes in.
	if _, err := w.Send(sim.UseOuthouse("pc-other", "outhouse", time.Now().UTC().Add(sim.OuthouseStay))); err != nil {
		t.Errorf("after the stay: err = %v, want nil", err)
	}
	// The tester's own second click while inside answers with the player-facing line.
	if rec := post(t, srv, "/api/village/pc/outhouse", `{"object_id":"outhouse"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Someone is in there.") {
		t.Errorf("while occupied: status = %d body = %s, want 422 Someone is in there.", rec.Code, rec.Body.String())
	}
}

func TestTranslateOuthouseUsed(t *testing.T) {
	frame, ok := TranslateEvent(&sim.OuthouseUsed{ObjectID: "o1", ActorID: "pc-1", Stay: sim.OuthouseStay})
	if !ok || frame.Type != "outhouse_used" {
		t.Fatalf("frame = %+v ok=%v, want outhouse_used", frame, ok)
	}
	dto, _ := frame.Data.(outhouseUsedWireDTO)
	if dto.ObjectID != "o1" || dto.ActorID != "pc-1" || dto.Seconds != 10 {
		t.Errorf("payload = %+v", dto)
	}
}

// The client's path end to end: the click walks the player to the outhouse
// (object_visit), and the arrival's /pc/outhouse goes in — from across the
// green and from its door.
func TestPCOuthouse_WalkThenGoIn(t *testing.T) {
	for _, tc := range []struct {
		name string
		x    int
	}{{"from across the green", 4}, {"from the door", 10}} {
		t.Run(tc.name, func(t *testing.T) {
			w := seededWorld(t)
			seedPC(t, w, "pc-tester", "tester", tc.x, 10)
			seedOuthouse(t, w, "outhouse", 11, 10, true)
			arrived := false
			if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
				world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, e sim.Event) {
					if ev, ok := e.(*sim.ActorArrived); ok && ev.ActorID == "pc-tester" {
						arrived = true
					}
				}))
				return nil, nil
			}}); err != nil {
				t.Fatal(err)
			}
			srv := NewServer(w, okAuth{})
			if rec := post(t, srv, "/api/village/pc/move", `{"destination":{"kind":"object_visit","object_id":"outhouse"}}`); rec.Code != http.StatusOK {
				t.Fatalf("move: status = %d body = %s", rec.Code, rec.Body.String())
			}
			start := time.Now().UTC()
			for i := 0; i < 60 && !arrived; i++ {
				if _, err := w.Send(sim.EvaluateLocomotion(start.Add(time.Duration(i) * time.Second))); err != nil {
					t.Fatal(err)
				}
			}
			if !arrived {
				t.Fatal("the player never arrived at the outhouse")
			}
			if rec := post(t, srv, "/api/village/pc/outhouse", `{"object_id":"outhouse"}`); rec.Code != http.StatusOK {
				t.Errorf("go in on arrival: status = %d body = %s, want 200", rec.Code, rec.Body.String())
			}
		})
	}
}
