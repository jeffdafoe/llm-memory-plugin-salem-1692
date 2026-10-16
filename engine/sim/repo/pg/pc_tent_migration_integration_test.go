package pg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPCTentMigration_Integration replays the LLM-798 migration over a seeded
// player and villager. The fixture migrates an EMPTY database, where no actor
// exists, so without this the tent grant and the down's camp teardown would
// ship untested.
func TestPCTentMigration_Integration(t *testing.T) {
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
	const tentAsset = "019e5f00-c401-7a10-9e00-000000798001"

	// The fixture already ran the up on an empty database; take it back out so
	// the seed below is the world before LLM-798.
	run("LLM-798-pc-tent_down.sql")
	if n := count(`SELECT count(*) FROM asset WHERE id::text LIKE '019e5f00-c401-7a10-9e00-000000798%'`); n != 0 {
		t.Fatalf("down left %d camp assets", n)
	}

	const player = "22222222-2222-2222-2222-222222222222"
	const villager = "33333333-3333-3333-3333-333333333333"
	exec(`INSERT INTO actor (id, display_name, current_x, current_y, login_username) VALUES ($1, 'Jefferey', 0, 0, 'jeff')`, player)
	exec(`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, 'Josiah', 0, 0)`, villager)

	run("LLM-798-pc-tent_up.sql")
	if n := count(`SELECT quantity FROM actor_inventory WHERE actor_id = $1 AND item_kind = 'tent'`, player); n != 1 {
		t.Errorf("player tents = %d, want 1", n)
	}
	if n := count(`SELECT count(*) FROM actor_inventory WHERE actor_id = $1`, villager); n != 0 {
		t.Errorf("villager holds %d rows, want none (tents are for players)", n)
	}
	if n := count(`SELECT count(*) FROM asset_state WHERE asset_id = $1`, tentAsset); n != 3 {
		t.Errorf("tent states = %d, want packed, closed, open", n)
	}

	// Rerun converges and never overwrites a live count.
	exec(`UPDATE actor_inventory SET quantity = 2 WHERE actor_id = $1 AND item_kind = 'tent'`, player)
	run("LLM-798-pc-tent_up.sql")
	if n := count(`SELECT quantity FROM actor_inventory WHERE actor_id = $1 AND item_kind = 'tent'`, player); n != 2 {
		t.Errorf("rerun overwrote the live count: %d, want 2", n)
	}
	if n := count(`SELECT count(*) FROM asset_state_tag x JOIN asset_state s ON s.id = x.state_id WHERE s.asset_id = $1`, tentAsset); n != 2 {
		t.Errorf("rerun left %d tent state tags, want 2", n)
	}

	// A pitched camp: the tent is the player's home and they are inside it.
	const tent = "44444444-4444-4444-4444-444444444444"
	exec(`INSERT INTO village_object (id, x, y, asset_id, owner_actor_id) VALUES ($1, 0, 0, $2, $3)`, tent, tentAsset, player)
	exec(`INSERT INTO village_object (x, y, asset_id, owner_actor_id) VALUES (0, 0, '019e5f00-c401-7a10-9e00-000000798002', $1)`, player)
	exec(`INSERT INTO structure (id, display_name) VALUES ($1, 'Jefferey''s Tent')`, tent)
	exec(`UPDATE actor SET home_structure_id = $1, inside_structure_id = $1 WHERE id = $2`, tent, player)
	exec(`INSERT INTO door_note (id, structure_id, recipient_actor_id, author_actor_id, author_name, body, left_at)
	      VALUES ('note-0000798a', $1, $2, $3, 'Josiah', 'Called by.', now())`, tent, player, villager)

	run("LLM-798-pc-tent_down.sql")
	if n := count(`SELECT count(*) FROM door_note WHERE structure_id = $1`, tent); n != 0 {
		t.Error("down left a door note at a tent that is gone")
	}
	if n := count(`SELECT count(*) FROM actor WHERE id = $1 AND home_structure_id IS NULL AND inside_structure_id IS NULL`, player); n != 1 {
		t.Error("down left the player homed in, or inside, a tent that is gone")
	}
	if n := count(`SELECT count(*) FROM structure WHERE id = $1`, tent); n != 0 {
		t.Error("down left the tent's structure")
	}
	if n := count(`SELECT count(*) FROM village_object WHERE owner_actor_id = $1`, player); n != 0 {
		t.Errorf("down left %d camp objects", n)
	}
	if n := count(`SELECT count(*) FROM item_kind WHERE name = 'tent'`); n != 0 {
		t.Error("down left the tent item")
	}
}
