package pg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestIntegration_StandingLadder_Seeded (LLM-895): the migration seeds the nine
// ranks, and they load as an ascending ladder from Stranger at 0 to Selectman.
func TestIntegration_StandingLadder_Seeded(t *testing.T) {
	f := newFixture(t)
	repo := NewStoryRepo(f.Pool)
	raw, err := repo.LoadRanks(t.Context())
	if err != nil {
		t.Fatalf("LoadRanks: %v", err)
	}
	ranks := sim.NormalizeStandingRanks(raw)
	want := []string{"stranger", "sojourner", "inhabitant", "householder", "goodman", "freeman", "church_member", "tithingman", "selectman"}
	if len(ranks) != len(want) {
		t.Fatalf("loaded %d ranks, want %d: %+v", len(ranks), len(want), ranks)
	}
	for i, id := range want {
		r := ranks[i]
		if r.ID != id || r.Name == "" || len(r.Prose) == 0 {
			t.Errorf("rank %d = %+v, want %s with a name and prose", i, r, id)
		}
		if i > 0 && (r.Text == "" || r.DownText == "") {
			t.Errorf("%s has no rank-up or rank-down line", id)
		}
	}
	if ranks[0].At != 0 || ranks[1].At != 50 || ranks[len(ranks)-1].At != 1700 {
		t.Errorf("thresholds = %d, %d … %d, want 0, 50 … 1700", ranks[0].At, ranks[1].At, ranks[len(ranks)-1].At)
	}
}

// TestIntegration_StandingLadder_SetThreshold (LLM-895): the operator's write
// moves a rank's threshold and refuses the bottom rank or an unknown id.
func TestIntegration_StandingLadder_SetThreshold(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewStoryRepo(f.Pool)
	if err := repo.SetStandingRankThreshold(ctx, "sojourner", 60); err != nil {
		t.Fatalf("SetStandingRankThreshold: %v", err)
	}
	raw, err := repo.LoadRanks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range raw {
		if r.ID == "sojourner" && r.At != 60 {
			t.Errorf("sojourner at %d, want 60", r.At)
		}
	}
	for _, id := range []string{"stranger", "nobody"} {
		if err := repo.SetStandingRankThreshold(ctx, id, 10); !errors.Is(err, sim.ErrStandingRankUnknown) {
			t.Errorf("%s: err = %v, want ErrStandingRankUnknown", id, err)
		}
	}
}

// TestIntegration_StandingLadder_MigrationConvertsRankSeen (LLM-895): the up
// turns the LLM-892 position into the rank id, the down turns it back, and a
// rerun of either changes nothing.
func TestIntegration_StandingLadder_MigrationConvertsRankSeen(t *testing.T) {
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
	up, down := read("LLM-895-standing-ladder_up.sql"), read("LLM-895-standing-ladder_down.sql")
	run := func(label, sql string) {
		t.Helper()
		if _, err := f.Pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	ids := []string{
		"66666666-6666-6666-6666-666666666660", "66666666-6666-6666-6666-666666666661",
		"66666666-6666-6666-6666-666666666662", "66666666-6666-6666-6666-666666666663",
	}
	// Back to the LLM-892 shape, with one player at each old position.
	run("down", down)
	run("down again", down)
	for i, id := range ids {
		if _, err := f.Pool.Exec(ctx, `INSERT INTO actor (id, display_name, current_x, current_y, standing_rank_seen)
			VALUES ($1, $2, 0, 0, $3)`, id, "Player "+id[len(id)-1:], i); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	run("up", up)
	run("up again", up)
	want := []string{"stranger", "sojourner", "inhabitant", "freeman"}
	for i, id := range ids {
		var got string
		if err := f.Pool.QueryRow(ctx, `SELECT standing_rank_seen FROM actor WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if got != want[i] {
			t.Errorf("position %d became %q, want %q", i, got, want[i])
		}
	}
	var def string
	if err := f.Pool.QueryRow(ctx, `SELECT column_default FROM information_schema.columns
		WHERE table_name = 'actor' AND column_name = 'standing_rank_seen'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	if def != "'stranger'::text" {
		t.Errorf("default = %s, want 'stranger'", def)
	}
	run("down", down)
	for i, id := range ids {
		var got int
		// A different text from the read above: the driver caches a statement
		// per text, and the column's type has changed under it.
		if err := f.Pool.QueryRow(ctx, `SELECT standing_rank_seen AS position FROM actor WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatalf("read %s after down: %v", id, err)
		}
		if got != i {
			t.Errorf("%s back to %d, want %d", want[i], got, i)
		}
	}
	run("up to leave the fixture as found", up)
}
