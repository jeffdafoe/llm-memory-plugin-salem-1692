package pg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestWardsMigration_Integration (LLM-845): after the replay both ward goods
// carry `ward` and have their recipes; the up reruns as a no-op, and the down
// then the up come back to the same catalog.
func TestWardsMigration_Integration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	dir, err := findMigrationsDir()
	if err != nil {
		t.Fatalf("findMigrationsDir: %v", err)
	}
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(b)
	}
	up, down := read("LLM-845-wards_up.sql"), read("LLM-845-wards_down.sql")
	state := func() string {
		t.Helper()
		var s string
		if err := f.Pool.QueryRow(ctx, `
			SELECT concat_ws(',',
			    (SELECT string_agg(name, '+' ORDER BY name) FROM item_kind WHERE 'ward' = ANY(capabilities)),
			    (SELECT string_agg(output_item, '+' ORDER BY output_item) FROM item_recipe
			      WHERE output_item IN ('horseshoe', 'witch_bottle')),
			    (SELECT count(*)::text FROM item_kind WHERE name = 'horseshoe' AND 'ward' = ANY(capabilities)
			        AND array_position(capabilities, 'ward') = array_length(capabilities, 1)))`).Scan(&s); err != nil {
			t.Fatalf("state: %v", err)
		}
		return s
	}
	want := "horseshoe+witch_bottle,horseshoe+witch_bottle,1"
	if got := state(); got != want {
		t.Fatalf("after the replay: %s, want %s", got, want)
	}
	for _, step := range []struct{ label, sql, want string }{
		{"up again", up, want},
		{"down", down, ","},
		{"down again", down, ","},
		{"up", up, want},
	} {
		if _, err := f.Pool.Exec(ctx, step.sql); err != nil {
			t.Fatalf("%s: %v", step.label, err)
		}
		got := state()
		if step.want == "," {
			if got != "0" {
				t.Errorf("%s: %s, want no ward goods and no recipes", step.label, got)
			}
			continue
		}
		if got != step.want {
			t.Errorf("%s: %s, want %s", step.label, got, step.want)
		}
	}
}

// TestWardsMigration_ShelfOnlyWithTheLine (LLM-845, code_review): the
// distributor gets the witch bottle line and two bottles; a rerun after the
// bottles sold out (their row gone) adds no line and no bottles.
func TestWardsMigration_ShelfOnlyWithTheLine(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	dir, err := findMigrationsDir()
	if err != nil {
		t.Fatalf("findMigrationsDir: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "LLM-845-wards_up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up := string(b)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
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
	const shop = "22222222-2222-2222-2222-222222222222"
	exec(`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, 'Josiah', 0, 0)`, shop)
	exec(`INSERT INTO village_object (x, y, tags, owner_actor_id) VALUES (0, 0, '{distributor}', $1)`, shop)
	exec(`INSERT INTO attribute_definition (slug, display_name, scope) VALUES ('merchant', 'Merchant', 'actor') ON CONFLICT (slug) DO NOTHING`)
	exec(`INSERT INTO actor_attribute (actor_id, slug, params) VALUES ($1, 'merchant', '{"restock":[]}')`, shop)
	lines := `SELECT count(*) FROM actor_attribute, jsonb_array_elements(params->'restock') e
	           WHERE actor_id = $1 AND e->>'item' = 'witch_bottle'`
	shelf := `SELECT COALESCE(SUM(quantity), 0) FROM actor_inventory WHERE actor_id = $1 AND item_kind = 'witch_bottle'`

	exec(up)
	if l, s := count(lines, shop), count(shelf, shop); l != 1 || s != 2 {
		t.Fatalf("first run: %d lines, %d bottles; want 1 and 2", l, s)
	}
	exec(`DELETE FROM actor_inventory WHERE actor_id = $1 AND item_kind = 'witch_bottle'`, shop)
	exec(up)
	if l, s := count(lines, shop), count(shelf, shop); l != 1 || s != 0 {
		t.Errorf("rerun after selling out: %d lines, %d bottles; want 1 and 0", l, s)
	}
}
