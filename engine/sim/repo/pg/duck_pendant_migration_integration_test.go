package pg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDuckPendantMigration_Integration (LLM-800): the up and down migrations
// are each other's inverse, refuse every starting state but the expected one
// (iron-only for the up; duck-only after the up for the down), refuse while
// anyone holds the good being renamed, and are no-ops on a rerun.
func TestDuckPendantMigration_Integration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	dir, err := findMigrationsDir()
	if err != nil {
		t.Fatalf("findMigrationsDir: %v", err)
	}
	// One connection throughout, so a refused migration's aborted transaction
	// is rolled back on the connection that holds it.
	conn, err := f.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(b)
	}
	up, down := read("LLM-800-duck-pendant_up.sql"), read("LLM-800-duck-pendant_down.sql")
	run := func(label, sql string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	refuse := func(label, sql, want string) {
		t.Helper()
		_, err := conn.Exec(ctx, sql)
		_, _ = conn.Exec(ctx, "ROLLBACK")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want one naming %q", label, err, want)
		}
	}
	// state is the catalog's shape: which of the two names exist, whether
	// actor.wearing exists, and how many goods carry `wear`.
	state := func() string {
		t.Helper()
		var s string
		if err := conn.QueryRow(ctx, `
			SELECT concat_ws(',',
			    CASE WHEN EXISTS (SELECT 1 FROM item_kind WHERE name = 'iron_ward') THEN 'iron' END,
			    CASE WHEN EXISTS (SELECT 1 FROM item_kind WHERE name = 'duck_pendant') THEN 'duck' END,
			    CASE WHEN EXISTS (SELECT 1 FROM information_schema.columns
			                       WHERE table_schema = current_schema() AND table_name = 'actor' AND column_name = 'wearing') THEN 'wearing' END,
			    'wear=' || (SELECT count(*) FROM item_kind WHERE 'wear' = ANY(capabilities)))`).Scan(&s); err != nil {
			t.Fatalf("state: %v", err)
		}
		return s
	}
	label := func(name string) string {
		t.Helper()
		var s string
		if err := conn.QueryRow(ctx, `SELECT display_label || '|' || description FROM item_kind WHERE name = $1`, name).Scan(&s); err != nil {
			t.Fatalf("label %s: %v", name, err)
		}
		return s
	}

	// The fixture is migrated: the up has run.
	if got := state(); got != "duck,wearing,wear=3" {
		t.Fatalf("migrated fixture = %s, want duck,wearing,wear=3", got)
	}
	run("down", down)
	if got := state(); got != "iron,wear=0" {
		t.Fatalf("after down = %s, want iron,wear=0", got)
	}
	if got := label("iron_ward"); got != "Iron ward|A twist of cold iron worn against the skin, to turn away ill wishes." {
		t.Errorf("iron ward after down = %s", got)
	}
	ironWard := label("iron_ward")
	run("down rerun", down)
	if got := state(); got != "iron,wear=0" {
		t.Errorf("down rerun = %s, want unchanged", got)
	}

	// The up refuses every starting state but iron-only.
	run("make duck-only", `UPDATE item_kind SET name = 'duck_pendant' WHERE name = 'iron_ward'`)
	refuse("up over duck-only", up, "duck_pendant already exists")
	run("make both", `INSERT INTO item_kind (name, display_label, category, capabilities) VALUES ('iron_ward', 'x', 'charm', '{}')`)
	refuse("up over both", up, "duck_pendant already exists")
	run("make neither", `DELETE FROM item_kind WHERE name = 'iron_ward'; UPDATE item_kind SET name = 'spare_charm' WHERE name = 'duck_pendant'`)
	refuse("up over neither", up, "iron_ward does not exist")
	run("back to iron-only", `UPDATE item_kind SET name = 'iron_ward' WHERE name = 'spare_charm'`)
	const holder = "22222222-2222-2222-2222-222222222222"
	run("seed holder", `INSERT INTO actor (id, display_name, current_x, current_y) VALUES ('`+holder+`', 'Holder', 0, 0);
	                    INSERT INTO actor_inventory (actor_id, item_kind, quantity) VALUES ('`+holder+`', 'iron_ward', 1)`)
	refuse("up while an iron ward is held", up, "holds an iron ward")
	run("drop holding", `DELETE FROM actor_inventory WHERE actor_id = '`+holder+`'`)
	if got := state(); got != "iron,wear=0" {
		t.Fatalf("refusals changed the catalog: %s", got)
	}

	// Iron-only: the up runs, and a rerun changes nothing.
	run("up", up)
	if got := state(); got != "duck,wearing,wear=3" {
		t.Fatalf("after up = %s, want duck,wearing,wear=3", got)
	}
	if got := label("duck_pendant"); !strings.HasPrefix(got, "Duck pendant|A little duck carved") {
		t.Errorf("duck pendant after up = %s", got)
	}
	run("up rerun", up)
	if got := state(); got != "duck,wearing,wear=3" {
		t.Errorf("up rerun = %s, want unchanged", got)
	}

	// The down refuses every state but duck-only, and while a pendant is held.
	run("hold a pendant", `INSERT INTO actor_inventory (actor_id, item_kind, quantity) VALUES ('`+holder+`', 'duck_pendant', 1)`)
	refuse("down while a duck pendant is held", down, "holds a duck pendant")
	run("drop holding", `DELETE FROM actor_inventory WHERE actor_id = '`+holder+`'`)
	run("add an iron ward", `INSERT INTO item_kind (name, display_label, category, capabilities) VALUES ('iron_ward', 'x', 'charm', '{}')`)
	refuse("down over both", down, "iron_ward already exists")
	run("make neither", `DELETE FROM item_kind WHERE name = 'iron_ward'; UPDATE item_kind SET name = 'spare_charm' WHERE name = 'duck_pendant'`)
	refuse("down over neither", down, "duck_pendant does not exist")
	run("back to duck-only", `UPDATE item_kind SET name = 'duck_pendant' WHERE name = 'spare_charm'`)

	// Duck-only: the down restores the iron ward exactly.
	run("down", down)
	if got := state(); got != "iron,wear=0" {
		t.Errorf("final down = %s, want iron,wear=0", got)
	}
	if got := label("iron_ward"); got != ironWard {
		t.Errorf("iron ward round trip = %s, want %s", got, ironWard)
	}
}
