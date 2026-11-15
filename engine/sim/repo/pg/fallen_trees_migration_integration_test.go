package pg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSnappedFallenTreesMigration_Integration replays the fallen trees'
// migrations. The fixture ran every up, so it starts on the painted art
// (LLM-967f), which must match tools/tree-art/fallen.py's geometry
// (testdata/fallen-trees-snapped.json). LLM-967f's down puts back the LLM-789
// snapped pack art. Then LLM-789: its down refuses while a SW top is placed,
// then puts the flat tops back, and its up converges. LLM-967f's up then
// paints them again, twice, to the same place.
func TestSnappedFallenTreesMigration_Integration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	dir, err := findMigrationsDir()
	if err != nil {
		t.Fatalf("findMigrationsDir: %v", err)
	}
	read := func(name string) string {
		t.Helper()
		sqlBytes, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(sqlBytes)
	}
	run := func(name string) {
		t.Helper()
		if _, err := f.Pool.Exec(ctx, read(name)); err != nil {
			t.Fatalf("run %s: %v", name, err)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	const (
		maple     = "019e5f00-c401-7a10-9e00-000000678001"
		mapleSW   = "019e5f00-c401-7a10-9e00-000000789001"
		stump     = "019e5f00-c401-7a10-9e00-000000678004"
		trunk     = "019e5f00-c401-7a10-9e00-000000678005"
		snapped   = "/tilesets/mana-seed/growable-trees/public-works-fallen-trees-snapped.png"
		flat      = "/tilesets/mana-seed/growable-trees/public-works-fallen-trees.png"
		painted   = "res://assets/trees/fallen-tops.png"
		snapAsset = `SELECT count(*) FROM asset WHERE id::text LIKE '019e5f00-c401-7a10-9e00-000000789%'`
	)
	// The art's numbers: the painted art (testdata/fallen-trees-snapped.json,
	// written by tools/tree-art/fallen.py) and, frozen, the pack art LLM-789
	// placed (testdata/fallen-trees-snapped-llm789.json, written by the tiles
	// repo's tools/fallen-trees).
	type artFile struct {
		Stumps []struct {
			State   string
			Rect    [4]int
			AnchorX float64 `json:"anchor_x"`
			AnchorY float64 `json:"anchor_y"`
		}
		Trunk struct {
			Looks []struct {
				State string
				Rect  [4]int
			}
		}
		Tops []struct {
			Species, Pose string
			AnchorX       float64 `json:"anchor_x"`
			AnchorY       float64 `json:"anchor_y"`
			FL            int     `json:"footprint_left"`
			FR            int     `json:"footprint_right"`
			FT            int     `json:"footprint_top"`
			FB            int     `json:"footprint_bottom"`
			Frames        []struct {
				State string
				Rect  [4]int
			}
		}
	}
	load := func(name string) artFile {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		var a artFile
		if err := json.Unmarshal(raw, &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	art, pack := load("fallen-trees-snapped.json"), load("fallen-trees-snapped-llm789.json")
	assetOf := map[string]string{
		"maple up": maple, "chestnut up": "019e5f00-c401-7a10-9e00-000000678002", "birch up": "019e5f00-c401-7a10-9e00-000000678003",
		"maple down": mapleSW, "chestnut down": "019e5f00-c401-7a10-9e00-000000789002", "birch down": "019e5f00-c401-7a10-9e00-000000789003",
	}
	// topsMatch: every top asset carries a's anchor and footprint, and every
	// frame a's rect on sheet; and the stumps a's rects on stumpSheet, the
	// Storm Stump a's anchor.
	topsMatch := func(a artFile, sheet, stumpSheet, label string) {
		t.Helper()
		if len(a.Tops) != len(assetOf) || len(a.Stumps) != 6 {
			t.Fatalf("%s art has %d tops and %d stumps, want %d and 6", label, len(a.Tops), len(a.Stumps), len(assetOf))
		}
		for _, top := range a.Tops {
			id := assetOf[top.Species+" "+top.Pose]
			if n := count(`SELECT count(*) FROM asset WHERE id = $1 AND anchor_x = $2 AND anchor_y = $3
			                AND footprint_left = $4 AND footprint_right = $5 AND footprint_top = $6 AND footprint_bottom = $7`,
				id, top.AnchorX, top.AnchorY, top.FL, top.FR, top.FT, top.FB); n != 1 {
				t.Errorf("%s %s %s: asset row does not match the art's anchor and footprint", label, top.Species, top.Pose)
			}
			for _, fr := range top.Frames {
				if n := count(`SELECT count(*) FROM asset_state WHERE asset_id = $1 AND state = $2 AND sheet = $3
				                AND src_x = $4 AND src_y = $5 AND src_w = $6 AND src_h = $7`,
					id, fr.State, sheet, fr.Rect[0], fr.Rect[1], fr.Rect[2], fr.Rect[3]); n != 1 {
					t.Errorf("%s %s %s %s: frame does not match the art's rect", label, top.Species, top.Pose, fr.State)
				}
			}
		}
		for _, st := range a.Stumps {
			if n := count(`SELECT count(*) FROM asset_state WHERE asset_id = $1 AND state = $2 AND sheet = $3
			                AND src_x = $4 AND src_y = $5 AND src_w = $6 AND src_h = $7`,
				stump, st.State, stumpSheet, st.Rect[0], st.Rect[1], st.Rect[2], st.Rect[3]); n != 1 {
				t.Errorf("%s stump %s: frame does not match the art's rect", label, st.State)
			}
			if n := count(`SELECT count(*) FROM asset WHERE id = $1 AND anchor_x = $2 AND anchor_y = $3`, stump, st.AnchorX, st.AnchorY); n != 1 {
				t.Errorf("%s stump %s: the Storm Stump is not anchored where the art's foot is", label, st.State)
			}
		}
	}
	matchesArt := func() {
		t.Helper()
		topsMatch(art, painted, "res://assets/trees/storm-stumps.png", "painted")
		for _, lk := range art.Trunk.Looks {
			if n := count(`SELECT count(*) FROM asset_state WHERE asset_id = $1 AND state = $2 AND sheet = 'res://assets/trees/fallen-trunk.png'
			                AND src_x = $3 AND src_y = $4 AND src_w = $5 AND src_h = $6`,
				trunk, lk.State, lk.Rect[0], lk.Rect[1], lk.Rect[2], lk.Rect[3]); n != 1 {
				t.Errorf("log %s: frame does not match the art's rect", lk.State)
			}
		}
		if len(art.Trunk.Looks) != 6 {
			t.Errorf("art has %d log looks, want 6", len(art.Trunk.Looks))
		}
		if n := count(`SELECT count(*) FROM asset_state WHERE asset_id IN ($1, $2) AND sheet LIKE '/tilesets/%'`, stump, trunk); n != 0 {
			t.Errorf("%d stump or log states are still on pack art", n)
		}
	}
	// The LLM-789 state: the pack's snapped tops, every one as the frozen art
	// has it, and the stumps on the flat sheet.
	upState := func() {
		t.Helper()
		topsMatch(pack, snapped, flat, "pack")
		if n := count(snapAsset); n != 3 {
			t.Errorf("SW-pose assets = %d, want 3", n)
		}
		if n := count(`SELECT count(*) FROM asset_state WHERE sheet = $1`, snapped); n != 12 {
			t.Errorf("states on the snapped sheet = %d, want 12", n)
		}
		if n := count(`SELECT count(*) FROM asset WHERE id = $1 AND name = 'Fallen Maple (NW)'
		                AND anchor_x = 0.5797101449275363 AND footprint_left = 1 AND footprint_bottom = 1`, maple); n != 1 {
			t.Error("the LLM-678 maple does not carry its up-pose anchor and footprint")
		}
		if n := count(`SELECT count(*) FROM asset_state WHERE asset_id = $1 AND state = 'winter'
		                AND src_x = 71 AND src_y = 0 AND src_w = 69 AND src_h = 72`, maple); n != 1 {
			t.Error("the maple's winter frame is not the snapped sheet's")
		}
		// The stumps stay on the flat sheet.
		if n := count(`SELECT count(*) FROM asset_state WHERE asset_id = '019e5f00-c401-7a10-9e00-000000678004' AND sheet = $1`, flat); n != 6 {
			t.Errorf("stump states on the flat sheet = %d, want 6", n)
		}
	}

	// The fixture already ran every up: the painted art.
	matchesArt()
	run("LLM-967f-fallen-trees_down.sql")
	upState()
	if n := count(`SELECT count(*) FROM asset_state WHERE asset_id = $1 AND sheet LIKE '/tilesets/mana-seed/%'`, trunk); n != 6 {
		t.Errorf("LLM-967f down left %d log states on the pack sheets, want 6", n)
	}

	// A tree in the road across the art change: a maple (crown up-left) with
	// its stump at the pack art's offset (67, 40) moves with LLM-967f to the
	// painted art's (64, 64), and back on the down. A stump already cleared
	// (it has an expiry) stays put, though it stands at a pack offset from
	// another tree (a chestnut, crown down-left: 103, -42).
	place := func(x, y float64, asset, state string, expires bool) string {
		t.Helper()
		var id string
		q := `INSERT INTO village_object (x, y, asset_id, current_state, expires_at)
		      VALUES ($1, $2, $3, $4, CASE WHEN $5 THEN now() + interval '7 days' END) RETURNING id::text`
		if err := f.Pool.QueryRow(ctx, q, x, y, asset, state, expires).Scan(&id); err != nil {
			t.Fatalf("place %s: %v", asset, err)
		}
		return id
	}
	pos := func(id string) (x, y float64) {
		t.Helper()
		if err := f.Pool.QueryRow(ctx, `SELECT x, y FROM village_object WHERE id = $1`, id).Scan(&x, &y); err != nil {
			t.Fatal(err)
		}
		return x, y
	}
	const chestnutSW = "019e5f00-c401-7a10-9e00-000000789002"
	place(1008, 2016, maple, "bare", false)
	linked := place(1008+67, 2016+40, stump, "maple-bare", false)
	place(3024, 3024, chestnutSW, "winter", false)
	cleared := place(3024+103, 3024-42, stump, "chestnut-winter", true)
	run("LLM-967f-fallen-trees_up.sql")
	if x, y := pos(linked); x != 1008+64 || y != 2016+64 {
		t.Errorf("up: the maple's stump stands at (%v, %v), want the painted offset (%v, %v)", x, y, 1008+64, 2016+64)
	}
	if x, y := pos(cleared); x != 3024+103 || y != 3024-42 {
		t.Errorf("up: the cleared stump moved to (%v, %v)", x, y)
	}
	matchesArt()
	run("LLM-967f-fallen-trees_down.sql")
	if x, y := pos(linked); x != 1008+67 || y != 2016+40 {
		t.Errorf("down: the maple's stump stands at (%v, %v), want the pack offset (%v, %v)", x, y, 1008+67, 2016+40)
	}
	if x, y := pos(cleared); x != 3024+103 || y != 3024-42 {
		t.Errorf("down: the cleared stump moved to (%v, %v)", x, y)
	}
	upState()
	if _, err := f.Pool.Exec(ctx, `DELETE FROM village_object WHERE asset_id IN ($1, $2, $3)`, maple, chestnutSW, stump); err != nil {
		t.Fatal(err)
	}

	// A SW top in the road holds the down back.
	if _, err := f.Pool.Exec(ctx, `INSERT INTO village_object (x, y, asset_id) VALUES (0, 0, $1)`, mapleSW); err != nil {
		t.Fatalf("place a SW maple: %v", err)
	}
	if _, err := f.Pool.Exec(ctx, read("LLM-789-snapped-fallen-trees_down.sql")); err == nil || !strings.Contains(err.Error(), "still in the village") {
		t.Fatalf("down with a SW top placed: err = %v, want the refusal", err)
	}
	if _, err := f.Pool.Exec(ctx, `DELETE FROM village_object WHERE asset_id = $1`, mapleSW); err != nil {
		t.Fatal(err)
	}

	run("LLM-789-snapped-fallen-trees_down.sql")
	if n := count(snapAsset); n != 0 {
		t.Errorf("down left %d SW-pose assets", n)
	}
	if n := count(`SELECT count(*) FROM asset_state WHERE sheet = $1`, snapped); n != 0 {
		t.Errorf("down left %d states on the snapped sheet", n)
	}
	if n := count(`SELECT count(*) FROM asset WHERE id = $1 AND name = 'Fallen Maple'
	                AND anchor_x = 0.42857142857142855 AND anchor_y = 0.5333333333333333`, maple); n != 1 {
		t.Error("down did not put back the flat maple's anchor")
	}
	if n := count(`SELECT count(*) FROM asset WHERE id = '019e5f00-c401-7a10-9e00-000000678003'
	                AND footprint_left = 2 AND footprint_right = 1 AND footprint_top = 0 AND footprint_bottom = 1`); n != 1 {
		t.Error("down did not put back the flat birch's footprint")
	}
	if n := count(`SELECT count(*) FROM asset_state WHERE asset_id = $1 AND state = 'bare' AND sheet = $2
	                AND src_x = 0 AND src_y = 0 AND src_w = 56 AND src_h = 45`, maple, flat); n != 1 {
		t.Error("down did not put back the flat maple's bare frame")
	}

	run("LLM-789-snapped-fallen-trees_up.sql")
	upState()
	run("LLM-789-snapped-fallen-trees_up.sql")
	upState()

	run("LLM-967f-fallen-trees_up.sql")
	matchesArt()
	run("LLM-967f-fallen-trees_up.sql")
	matchesArt()
}
