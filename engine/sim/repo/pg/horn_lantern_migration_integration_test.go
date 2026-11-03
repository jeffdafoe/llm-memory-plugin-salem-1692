package pg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHornLanternMigration_Integration replays the LLM-846 migration over a
// seeded distributor, as LLM-799's test does: the fixture migrates an EMPTY
// database, where the distributor block returns early, so without this the buy
// lines and starter shelf would ship untested.
func TestHornLanternMigration_Integration(t *testing.T) {
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
	lanternColumns := func() int {
		t.Helper()
		return count(`SELECT count(*) FROM information_schema.columns
		               WHERE table_schema = current_schema() AND table_name = 'actor'
		                 AND column_name IN ('lantern_lit_until', 'lantern_candle_left_seconds')`)
	}

	// The fixture already ran the up migration on an empty catalog; take it
	// back out so the seed below is the world before LLM-846.
	run("LLM-846-horn-lantern_down.sql")
	if n := count(`SELECT count(*) FROM item_kind WHERE name IN ('horn_lantern', 'tallow_candle')`); n != 0 {
		t.Fatalf("down left %d lantern goods", n)
	}
	if n := lanternColumns(); n != 0 {
		t.Fatalf("down left %d lantern columns on actor", n)
	}

	const shop = "11111111-1111-1111-1111-111111111111"
	exec(`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, 'Josiah', 0, 0)`, shop)
	exec(`INSERT INTO village_object (x, y, tags, owner_actor_id) VALUES (0, 0, '{distributor}', $1)`, shop)
	exec(`INSERT INTO item_kind (name, display_label, category, capabilities) VALUES ('bread', 'bread', 'food', '{}') ON CONFLICT (name) DO NOTHING`)
	exec(`INSERT INTO attribute_definition (slug, display_name, scope) VALUES ('merchant', 'Merchant', 'actor') ON CONFLICT (slug) DO NOTHING`)
	exec(`INSERT INTO actor_attribute (actor_id, slug, params) VALUES ($1, 'merchant',
	      '{"restock":[{"item":"bread","source":"buy","max":5}]}')`, shop)
	exec(`INSERT INTO actor_inventory (actor_id, item_kind, quantity) VALUES ($1, 'bread', 3)`, shop)

	run("LLM-846-horn-lantern_up.sql")
	if got, want := restock(shop), "bread:5,horn_lantern:3,tallow_candle:20"; got != want {
		t.Errorf("restock lines = %s, want %s", got, want)
	}
	for kind, want := range map[string]int{"horn_lantern": 3, "tallow_candle": 12, "bread": 3} {
		if n := count(`SELECT quantity FROM actor_inventory WHERE actor_id = $1 AND item_kind = $2`, shop, kind); n != want {
			t.Errorf("shelf %s = %d, want %d", kind, n, want)
		}
	}
	if n := count(`SELECT count(*) FROM item_kind WHERE name = 'horn_lantern' AND category = 'gear' AND capabilities = '{portable,light}'`); n != 1 {
		t.Errorf("horn_lantern catalog row missing or wrong shape")
	}
	if n := lanternColumns(); n != 2 {
		t.Errorf("%d of 2 lantern columns on actor", n)
	}
	if n := count(`SELECT lantern_candle_left_seconds FROM actor WHERE id = $1`, shop); n != 0 {
		t.Errorf("existing actor's stub = %d, want the default 0", n)
	}

	// Rerun: no second line for either good, and live stock is not overwritten.
	exec(`UPDATE actor_inventory SET quantity = 9 WHERE actor_id = $1 AND item_kind = 'tallow_candle'`, shop)
	run("LLM-846-horn-lantern_up.sql")
	if got, want := restock(shop), "bread:5,horn_lantern:3,tallow_candle:20"; got != want {
		t.Errorf("rerun restock lines = %s, want %s", got, want)
	}
	if n := count(`SELECT quantity FROM actor_inventory WHERE actor_id = $1 AND item_kind = 'tallow_candle'`, shop); n != 9 {
		t.Errorf("rerun overwrote live stock: tallow_candle %d, want 9", n)
	}

	run("LLM-846-horn-lantern_down.sql")
	if got := restock(shop); got != "bread:5" {
		t.Errorf("down restock lines = %s, want bread:5", got)
	}
	if n := count(`SELECT count(*) FROM actor_inventory WHERE actor_id = $1`, shop); n != 1 {
		t.Errorf("down left %d holdings, want only the bread", n)
	}
	if n := lanternColumns(); n != 0 {
		t.Errorf("down left %d lantern columns on actor", n)
	}
}
