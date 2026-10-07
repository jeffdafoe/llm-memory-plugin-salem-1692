package pg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWardrobeGoodsMigration_Integration replays the LLM-710 migration over a
// seeded copy of the old clothing catalog. The fixture migrates an EMPTY
// database, where the rename and the distributor block both return early, so
// without this they would ship untested. Each step runs the real file.
func TestWardrobeGoodsMigration_Integration(t *testing.T) {
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
	restockItems := func(actorID string) string {
		t.Helper()
		var items string
		if err := f.Pool.QueryRow(ctx,
			`SELECT string_agg(e->>'item', ',' ORDER BY ord)
			   FROM actor_attribute aa,
			        jsonb_array_elements(aa.params->'restock') WITH ORDINALITY AS t(e, ord)
			  WHERE aa.actor_id = $1 AND aa.slug = 'merchant'`, actorID).Scan(&items); err != nil {
			t.Fatalf("read restock: %v", err)
		}
		return items
	}

	// The fixture already ran the up migration on an empty catalog; take the
	// new goods back out so the seed below is the pre-LLM-710 world.
	run("LLM-710-wardrobe-goods_down.sql")
	if n := count(`SELECT count(*) FROM item_kind WHERE name IN ('headscarf', 'indigo')`); n != 0 {
		t.Fatalf("down left %d new goods", n)
	}

	const shop = "11111111-1111-1111-1111-111111111111"
	const buyer = "22222222-2222-2222-2222-222222222222"
	exec(`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, 'Josiah', 0, 0), ($2, 'Gideon', 0, 0)`, shop, buyer)
	exec(`INSERT INTO village_object (x, y, tags, owner_actor_id) VALUES (0, 0, '{distributor}', $1)`, shop)
	for _, k := range []struct{ name, caps string }{
		{"coat", "{warms}"}, {"cloak", "{warms}"}, {"linens", "{}"}, {"woolens", "{}"}, {"homespun", "{}"}, {"bread", "{}"},
	} {
		exec(`INSERT INTO item_kind (name, display_label, category, capabilities) VALUES ($1, $1, 'clothing', $2::text[]) ON CONFLICT (name) DO NOTHING`, k.name, k.caps)
		exec(`INSERT INTO item_recipe (output_item, output_qty, rate_qty, rate_per_hours, inputs, wholesale_price, retail_price)
		      VALUES ($1, 1, 1, 1, '[]'::jsonb, 8, 14) ON CONFLICT (output_item) DO NOTHING`, k.name)
	}
	exec(`INSERT INTO attribute_definition (slug, display_name, scope) VALUES ('merchant', 'Merchant', 'actor') ON CONFLICT (slug) DO NOTHING`)
	exec(`INSERT INTO actor_attribute (actor_id, slug, params) VALUES ($1, 'merchant',
	      '{"restock":[{"item":"bread","source":"buy","max":5},{"item":"coat","source":"buy","max":4},{"item":"homespun","source":"buy","max":6}]}')`, shop)
	exec(`INSERT INTO actor_inventory (actor_id, item_kind, quantity) VALUES ($1, 'coat', 2), ($1, 'homespun', 1), ($2, 'linens', 1)`, shop, buyer)

	run("LLM-710-wardrobe-goods_up.sql")

	if n := count(`SELECT count(*) FROM item_kind WHERE name IN ('coat', 'linens', 'woolens', 'homespun')`); n != 0 {
		t.Errorf("%d old garment names survive the rename", n)
	}
	if n := count(`SELECT count(*) FROM item_kind WHERE name = 'mantled_cloak' AND 'warms' = ANY(capabilities)`); n != 1 {
		t.Error("coat did not become a warm mantled_cloak")
	}
	if n := count(`SELECT quantity FROM actor_inventory WHERE actor_id = $1 AND item_kind = 'linen_shirt'`, buyer); n != 1 {
		t.Errorf("the villager's linens became %d linen shirts, want 1", n)
	}
	if n := count(`SELECT quantity FROM actor_inventory WHERE actor_id = $1 AND item_kind = 'mantled_cloak'`, shop); n != 2 {
		t.Errorf("the shop's coats became %d mantled cloaks, want 2", n)
	}
	if n := count(`SELECT retail_price FROM item_recipe WHERE output_item = 'stockings'`); n != 5 {
		t.Errorf("stockings retail %d, want 5", n)
	}
	items := restockItems(shop)
	if !strings.HasPrefix(items, "bread,mantled_cloak,stockings,headscarf,") || !strings.Contains(items, "logwood") {
		t.Errorf("restock lines = %s", items)
	}
	if n := count(`SELECT count(*) FROM actor_inventory WHERE actor_id = $1 AND item_kind IN ('headscarf', 'boots', 'gloves')`, shop); n != 3 {
		t.Errorf("starter shelf has %d of 3 sampled garments", n)
	}
	if n := count(`SELECT quantity FROM actor_inventory WHERE actor_id = $1 AND item_kind = 'indigo'`, shop); n != 2 {
		t.Errorf("starter shelf has %d indigo, want 2", n)
	}

	// Rerun: the rename sees an applied catalog, the lines and shelf are not
	// added twice, and live stock is not overwritten.
	exec(`UPDATE actor_inventory SET quantity = 7 WHERE actor_id = $1 AND item_kind = 'indigo'`, shop)
	run("LLM-710-wardrobe-goods_up.sql")
	if got := restockItems(shop); got != items {
		t.Errorf("rerun changed restock lines: %s", got)
	}
	if n := count(`SELECT quantity FROM actor_inventory WHERE actor_id = $1 AND item_kind = 'indigo'`, shop); n != 7 {
		t.Errorf("rerun overwrote live stock: indigo %d, want 7", n)
	}

	run("LLM-710-wardrobe-goods_down.sql")
	if n := count(`SELECT count(*) FROM item_kind WHERE name IN ('coat', 'cloak', 'linens', 'woolens', 'homespun')`); n != 5 {
		t.Errorf("down restored %d of 5 old garment names", n)
	}
	if got := restockItems(shop); got != "bread,coat,homespun" {
		t.Errorf("down restock lines = %s", got)
	}
	if n := count(`SELECT quantity FROM actor_inventory WHERE actor_id = $1 AND item_kind = 'linens'`, buyer); n != 1 {
		t.Errorf("down: the villager holds %d linens, want 1", n)
	}
}
