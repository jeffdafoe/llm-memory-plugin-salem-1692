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

// fakeSpriteWriter records upserts. inCatalog reports whether the world
// already held the sprite when the row was written — the row must come first.
type fakeSpriteWriter struct {
	world     *sim.World
	written   []*sim.Sprite
	inCatalog bool
	fail      error
	// afterWrite runs once the row is "written" — a test cancels the request here.
	afterWrite func()
}

func (f *fakeSpriteWriter) UpsertRigSprite(_ context.Context, sp *sim.Sprite) error {
	if f.fail != nil {
		return f.fail
	}
	f.written = append(f.written, sp)
	f.inCatalog = f.world.Published().Sprites[sp.ID] != nil
	if f.afterWrite != nil {
		f.afterWrite()
	}
	return nil
}

const testOutfitBody = `{"layers":[` +
	`{"sheet":"/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png","ramps":{"skin":4}},` +
	`{"sheet":"/tilesets/mana-seed/farmer/sheets/05shrt/fbas_05shrt_longshirt_00a.png","ramps":{"c3":0}}]}`

func TestHandlePCWardrobe(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/wardrobe", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got sim.FarmerWardrobe
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) == 0 || len(got.Categories) == 0 || len(got.Colours["skin"]) == 0 {
		t.Fatalf("wardrobe is empty: %+v", got)
	}
}

func TestHandlePCOutfit_DressesThePC(t *testing.T) {
	w := seededWorld(t)
	seedPC(t, w, "pc-tester", "tester", 5, 5)
	srv := NewServer(w, okAuth{})
	writer := &fakeSpriteWriter{world: w}
	srv.SetSpriteWriter(writer)

	rec := post(t, srv, "/api/village/pc/outfit", testOutfitBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	want := sim.OutfitSpriteID("pc-tester")
	if len(writer.written) != 1 || writer.written[0].ID != want {
		t.Fatalf("written = %+v, want one row %s", writer.written, want)
	}
	if writer.inCatalog {
		t.Fatal("the sprite reached the catalog before its row was written")
	}
	snap := w.Published()
	if snap.Actors["pc-tester"].SpriteID != want {
		t.Fatalf("PC sprite = %q, want %q", snap.Actors["pc-tester"].SpriteID, want)
	}
	sp := snap.Sprites[want]
	if sp == nil || sp.Rig != "farmer_base" || !strings.Contains(string(sp.Layers), `"skin":4`) {
		t.Fatalf("catalog sprite = %+v", sp)
	}

	// A re-save keeps the id and replaces the layers.
	rec = post(t, srv, "/api/village/pc/outfit", strings.Replace(testOutfitBody, `"skin":4`, `"skin":9`, 1))
	if rec.Code != http.StatusOK {
		t.Fatalf("re-save status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if got := w.Published().Sprites[want]; got == nil || !strings.Contains(string(got.Layers), `"skin":9`) {
		t.Fatalf("re-save did not replace the layers: %+v", got)
	}
}

func TestHandlePCOutfit_Refusals(t *testing.T) {
	t.Run("no writer", func(t *testing.T) {
		w := seededWorld(t)
		seedPC(t, w, "pc-tester", "tester", 5, 5)
		rec := post(t, NewServer(w, okAuth{}), "/api/village/pc/outfit", testOutfitBody)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
	})
	t.Run("invalid outfit", func(t *testing.T) {
		w := seededWorld(t)
		seedPC(t, w, "pc-tester", "tester", 5, 5)
		srv := NewServer(w, okAuth{})
		writer := &fakeSpriteWriter{world: w}
		srv.SetSpriteWriter(writer)
		rec := post(t, srv, "/api/village/pc/outfit", `{"layers":[{"sheet":"/elsewhere.png","ramps":{}}]}`)
		if rec.Code != http.StatusBadRequest || len(writer.written) != 0 {
			t.Fatalf("status = %d, writes = %d; want 400 and none", rec.Code, len(writer.written))
		}
	})
	t.Run("no PC", func(t *testing.T) {
		w := seededWorld(t)
		srv := NewServer(w, okAuth{})
		writer := &fakeSpriteWriter{world: w}
		srv.SetSpriteWriter(writer)
		rec := post(t, srv, "/api/village/pc/outfit", testOutfitBody)
		if rec.Code != http.StatusNotFound || len(writer.written) != 0 {
			t.Fatalf("status = %d, writes = %d; want 404 and none", rec.Code, len(writer.written))
		}
	})
	t.Run("write fails", func(t *testing.T) {
		w := seededWorld(t)
		seedPC(t, w, "pc-tester", "tester", 5, 5)
		before := w.Published().Actors["pc-tester"].SpriteID
		srv := NewServer(w, okAuth{})
		srv.SetSpriteWriter(&fakeSpriteWriter{world: w, fail: errors.New("pg down")})
		rec := post(t, srv, "/api/village/pc/outfit", testOutfitBody)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		if got := w.Published().Actors["pc-tester"].SpriteID; got != before {
			t.Fatalf("PC sprite changed to %q after a failed write", got)
		}
	})
}

// TestHandlePCOutfit_CancelAfterWriteStillInstalls: once the row holds the new
// layers, the live catalog must take them too — a client that drops the
// connection at that moment must not leave the table and the world apart
// (the outfit would change on the next restart).
func TestHandlePCOutfit_CancelAfterWriteStillInstalls(t *testing.T) {
	w := seededWorld(t)
	seedPC(t, w, "pc-tester", "tester", 5, 5)
	srv := NewServer(w, okAuth{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.SetSpriteWriter(&fakeSpriteWriter{world: w, afterWrite: cancel})

	req := httptest.NewRequest(http.MethodPost, "/api/village/pc/outfit", strings.NewReader(testOutfitBody)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+testToken)
	srv.Handler().ServeHTTP(httptest.NewRecorder(), req)

	want := sim.OutfitSpriteID("pc-tester")
	if got := w.Published().Actors["pc-tester"].SpriteID; got != want {
		t.Fatalf("PC sprite = %q after a cancel following the write, want %q", got, want)
	}
	if w.Published().Sprites[want] == nil {
		t.Fatal("outfit sprite missing from the catalog")
	}
}

func adminOutfitBody(npcID string) string {
	return `{"npc_id":"` + npcID + `",` + strings.TrimPrefix(testOutfitBody, "{")
}

// TestHandleAdminNPCOutfit_DressesTheVillager: the editor's Dress… button
// writes the villager's own outfit row and puts it on them live.
func TestHandleAdminNPCOutfit_DressesTheVillager(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "pc-admin", "tester")
	srv := NewServer(w, okAuth{})
	writer := &fakeSpriteWriter{world: w}
	srv.SetSpriteWriter(writer)

	rec := post(t, srv, "/api/village/admin/npc/outfit", adminOutfitBody("hannah"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	want := sim.OutfitSpriteID("hannah")
	if len(writer.written) != 1 || writer.written[0].ID != want || writer.written[0].Name != "Hannah" {
		t.Fatalf("written = %+v, want one row %s named Hannah", writer.written, want)
	}
	if writer.inCatalog {
		t.Fatal("the sprite reached the catalog before its row was written")
	}
	snap := w.Published()
	if snap.Actors["hannah"].SpriteID != want || snap.Sprites[want] == nil {
		t.Fatalf("hannah not dressed: sprite %q", snap.Actors["hannah"].SpriteID)
	}
}

func TestHandleAdminNPCOutfit_Refusals(t *testing.T) {
	cases := []struct {
		name  string
		admin bool
		npc   string
		want  int
	}{
		{"not an admin", false, "hannah", http.StatusForbidden},
		{"unknown villager", true, "nobody", http.StatusNotFound},
		{"a player", true, "pc-admin", http.StatusNotFound},
		{"an animal", true, "cow", http.StatusUnprocessableEntity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := seededWorld(t)
			if c.admin {
				seedAdmin(t, w, "pc-admin", "tester")
			} else {
				seedPC(t, w, "pc-admin", "tester", 5, 5)
			}
			if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
				world.InstallSprite(&sim.Sprite{ID: "cow-sprite", Name: "Cow (grey)", Behaviors: []string{sim.BehaviorGrazer, sim.BehaviorAmbient}})
				world.Actors["cow"] = &sim.Actor{ID: "cow", DisplayName: "Daisy", Kind: sim.KindDecorative, SpriteID: "cow-sprite", State: sim.StateIdle}
				return nil, nil
			}}); err != nil {
				t.Fatal(err)
			}
			srv := NewServer(w, okAuth{})
			writer := &fakeSpriteWriter{world: w}
			srv.SetSpriteWriter(writer)
			rec := post(t, srv, "/api/village/admin/npc/outfit", adminOutfitBody(c.npc))
			if rec.Code != c.want || len(writer.written) != 0 {
				t.Fatalf("status = %d, writes = %d; want %d and none (body=%s)", rec.Code, len(writer.written), c.want, rec.Body.String())
			}
		})
	}
}

// TestHandleAdminNPCOutfit_RefusedInstallRestoresRow: a villager already in
// her outfit is re-dressed, but the install is refused after the row was
// written (here: the caller loses admin between the two checks). The row goes
// back to the version she still wears live, so a restart changes nothing.
func TestHandleAdminNPCOutfit_RefusedInstallRestoresRow(t *testing.T) {
	w := seededWorld(t)
	seedAdmin(t, w, "pc-admin", "tester")
	srv := NewServer(w, okAuth{})
	writer := &fakeSpriteWriter{world: w}
	srv.SetSpriteWriter(writer)
	if rec := post(t, srv, "/api/village/admin/npc/outfit", adminOutfitBody("hannah")); rec.Code != http.StatusOK {
		t.Fatalf("first dressing: status = %d; body=%s", rec.Code, rec.Body.String())
	}

	writer.afterWrite = func() {
		writer.afterWrite = nil
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Actors["pc-admin"].IsAdmin = false
			return nil, nil
		}}); err != nil {
			t.Error(err)
		}
	}
	rec := post(t, srv, "/api/village/admin/npc/outfit", strings.Replace(adminOutfitBody("hannah"), `"skin":4`, `"skin":9`, 1))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	id := sim.OutfitSpriteID("hannah")
	last := writer.written[len(writer.written)-1]
	if last.ID != id || !strings.Contains(string(last.Layers), `"skin":4`) {
		t.Fatalf("row left as %s, want it restored to the live outfit (skin 4)", last.Layers)
	}
	if live := w.Published().Sprites[id]; live == nil || !strings.Contains(string(live.Layers), `"skin":4`) {
		t.Fatal("the live outfit changed after a refused install")
	}
}
