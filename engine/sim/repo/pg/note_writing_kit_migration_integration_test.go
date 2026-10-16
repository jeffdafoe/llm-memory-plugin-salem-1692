package pg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoteWritingKitMigration_Integration replays the LLM-799 migration over a
// seeded distributor. The fixture migrates an EMPTY database, where the
// distributor block returns early, so without this the buy lines and starter
// shelf would ship untested.
func TestNoteWritingKitMigration_Integration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	dir, err := findMigrationsDir()
	if err != nil {
		t.Fatalf("findMigrationsDir: %v", err)
	}
	run := func(name string) {
		t.Helper()
		sqlBytes, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := f.Pool.Exec(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("run %s: %v", name, err)
		}
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", strings.Fields(sql)[0:3], err)
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
	restock := func(actorID string) string {
		t.Helper()
		var lines string
		if err := f.Pool.QueryRow(ctx,
			`SELECT string_agg((e->>'item') || ':' || (e->>'max'), ',' ORDER BY ord)
			   FROM actor_attribute aa,
			        jsonb_array_elements(aa.params->'restock') WITH ORDINALITY AS t(e, ord)
			  WHERE aa.actor_id = $1 AND aa.slug = 'merchant'`, actorID).Scan(&lines); err != nil {
			t.Fatalf("read restock: %v", err)
		}
		return lines
	}

	// The fixture already ran the up migration on an empty catalog; take the
	// goods back out so the seed below is the world before LLM-799.
	run("LLM-799-note-writing-kit_down.sql")
	if n := count(`SELECT count(*) FROM item_kind WHERE name IN ('quill_and_ink', 'parchment')`); n != 0 {
		t.Fatalf("down left %d writing goods", n)
	}

	const shop = "11111111-1111-1111-1111-111111111111"
	exec(`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, 'Josiah', 0, 0)`, shop)
	exec(`INSERT INTO village_object (x, y, tags, owner_actor_id) VALUES (0, 0, '{distributor}', $1)`, shop)
	exec(`INSERT INTO item_kind (name, display_label, category, capabilities) VALUES ('bread', 'bread', 'food', '{}') ON CONFLICT (name) DO NOTHING`)
	exec(`INSERT INTO attribute_definition (slug, display_name, scope) VALUES ('merchant', 'Merchant', 'actor') ON CONFLICT (slug) DO NOTHING`)
	exec(`INSERT INTO actor_attribute (actor_id, slug, params) VALUES ($1, 'merchant',
	      '{"restock":[{"item":"bread","source":"buy","max":5}]}')`, shop)
	exec(`INSERT INTO actor_inventory (actor_id, item_kind, quantity) VALUES ($1, 'bread', 3)`, shop)

	run("LLM-799-note-writing-kit_up.sql")
	if got, want := restock(shop), "bread:5,quill_and_ink:3,parchment:20"; got != want {
		t.Errorf("restock lines = %s, want %s", got, want)
	}
	for kind, want := range map[string]int{"quill_and_ink": 3, "parchment": 12, "bread": 3} {
		if n := count(`SELECT quantity FROM actor_inventory WHERE actor_id = $1 AND item_kind = $2`, shop, kind); n != want {
			t.Errorf("shelf %s = %d, want %d", kind, n, want)
		}
	}
	if n := count(`SELECT count(*) FROM item_recipe WHERE (output_item, wholesale_price, retail_price) IN (('quill_and_ink', 3, 6), ('parchment', 1, 2))`); n != 2 {
		t.Errorf("%d of 2 price anchors", n)
	}

	// Rerun: no second line for either good, and live stock is not overwritten.
	exec(`UPDATE actor_inventory SET quantity = 9 WHERE actor_id = $1 AND item_kind = 'parchment'`, shop)
	run("LLM-799-note-writing-kit_up.sql")
	if got, want := restock(shop), "bread:5,quill_and_ink:3,parchment:20"; got != want {
		t.Errorf("rerun restock lines = %s, want %s", got, want)
	}
	if n := count(`SELECT quantity FROM actor_inventory WHERE actor_id = $1 AND item_kind = 'parchment'`, shop); n != 9 {
		t.Errorf("rerun overwrote live stock: parchment %d, want 9", n)
	}

	run("LLM-799-note-writing-kit_down.sql")
	if got := restock(shop); got != "bread:5" {
		t.Errorf("down restock lines = %s, want bread:5", got)
	}
	if n := count(`SELECT count(*) FROM actor_inventory WHERE actor_id = $1`, shop); n != 1 {
		t.Errorf("down left %d holdings, want only the bread", n)
	}
	if n := count(`SELECT count(*) FROM item_kind WHERE name IN ('quill_and_ink', 'parchment')`); n != 0 {
		t.Errorf("down left %d writing goods", n)
	}
}
