package pg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSnappedFallenTreesMigration_Integration replays LLM-789: the up moves the
// LLM-678 tops to the snapped sheet and adds the SW-pose assets; the down
// refuses while a SW top is placed, then puts the flat tops back; a second up
// converges.
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
		snapped   = "/tilesets/mana-seed/growable-trees/public-works-fallen-trees-snapped.png"
		flat      = "/tilesets/mana-seed/growable-trees/public-works-fallen-trees.png"
		snapAsset = `SELECT count(*) FROM asset WHERE id::text LIKE '019e5f00-c401-7a10-9e00-000000789%'`
	)
	// The art's numbers (engine/sim/testdata, the tools/fallen-trees JSON).
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fallen-trees-snapped.json"))
	if err != nil {
		t.Fatal(err)
	}
	var art struct {
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
	if err := json.Unmarshal(raw, &art); err != nil {
		t.Fatal(err)
	}
	assetOf := map[string]string{
		"maple up": maple, "chestnut up": "019e5f00-c401-7a10-9e00-000000678002", "birch up": "019e5f00-c401-7a10-9e00-000000678003",
		"maple down": mapleSW, "chestnut down": "019e5f00-c401-7a10-9e00-000000789002", "birch down": "019e5f00-c401-7a10-9e00-000000789003",
	}
	matchesArt := func() {
		t.Helper()
		if len(art.Tops) != len(assetOf) {
			t.Fatalf("art has %d tops, want %d", len(art.Tops), len(assetOf))
		}
		for _, top := range art.Tops {
			id := assetOf[top.Species+" "+top.Pose]
			if n := count(`SELECT count(*) FROM asset WHERE id = $1 AND anchor_x = $2 AND anchor_y = $3
			                AND footprint_left = $4 AND footprint_right = $5 AND footprint_top = $6 AND footprint_bottom = $7`,
				id, top.AnchorX, top.AnchorY, top.FL, top.FR, top.FT, top.FB); n != 1 {
				t.Errorf("%s %s: asset row does not match the art's anchor and footprint", top.Species, top.Pose)
			}
			for _, fr := range top.Frames {
				if n := count(`SELECT count(*) FROM asset_state WHERE asset_id = $1 AND state = $2 AND sheet = $3
				                AND src_x = $4 AND src_y = $5 AND src_w = $6 AND src_h = $7`,
					id, fr.State, snapped, fr.Rect[0], fr.Rect[1], fr.Rect[2], fr.Rect[3]); n != 1 {
					t.Errorf("%s %s %s: frame does not match the art's rect", top.Species, top.Pose, fr.State)
				}
			}
		}
	}
	upState := func() {
		t.Helper()
		matchesArt()
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

	// The fixture already ran every up.
	upState()

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
}
