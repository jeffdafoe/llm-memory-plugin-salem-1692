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

// fakeSpriteWriter records upserts. inCatalog reports whether the world
// already held the sprite when the row was written — the row must come first.
type fakeSpriteWriter struct {
	world     *sim.World
	written   []*sim.Sprite
	inCatalog bool
	fail      error
}

func (f *fakeSpriteWriter) UpsertRigSprite(_ context.Context, sp *sim.Sprite) error {
	if f.fail != nil {
		return f.fail
	}
	f.written = append(f.written, sp)
	f.inCatalog = f.world.Published().Sprites[sp.ID] != nil
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
	want := sim.PCOutfitSpriteID("pc-tester")
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
