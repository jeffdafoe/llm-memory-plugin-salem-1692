package pg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestStoryChapterSignsMigration_Integration (LLM-898): after the replay
// chapter 2 follows chapter 1 and the new trigger kinds pass the CHECK; the up
// reruns as a no-op, and the down then the up come back to the same catalog.
// The down also clears players' rows on the chapter, and the CHECK refuses the
// new kinds again.
func TestStoryChapterSignsMigration_Integration(t *testing.T) {
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
	up, down := read("LLM-898-story-chapter-signs_up.sql"), read("LLM-898-story-chapter-signs_down.sql")
	state := func() string {
		t.Helper()
		var s string
		if err := f.Pool.QueryRow(ctx, `
			SELECT concat_ws(',',
			    (SELECT coalesce(next_mission_id, '-') FROM story_mission WHERE id = 'arrival-5'),
			    (SELECT coalesce(string_agg(id || ':' || trigger_kind, '+' ORDER BY id), '-') FROM story_mission WHERE chapter = 'Signs'),
			    (SELECT count(*)::text FROM pc_story_mission WHERE mission_id LIKE 'signs-%'))`).Scan(&s); err != nil {
			t.Fatalf("state: %v", err)
		}
		return s
	}
	const chapter = "signs-1:omen_witnessed+signs-2:spoke_with_villager+signs-3:item_acquired+signs-4:ward_placed"
	want := "signs-1," + chapter + ",0"
	if got := state(); got != want {
		t.Fatalf("after the replay: %s, want %s", got, want)
	}
	if _, err := f.Pool.Exec(ctx, `INSERT INTO pc_story_mission (actor_id, mission_id, offered_at, snapshot_gen)
		VALUES ('pc', 'signs-1', now(), 1)`); err != nil {
		t.Fatalf("seed a progress row: %v", err)
	}
	for _, step := range []struct{ label, sql, want string }{
		{"up again", up, "signs-1," + chapter + ",1"},
		{"down", down, "-,-,0"},
		{"down again", down, "-,-,0"},
		{"up", up, want},
	} {
		if _, err := f.Pool.Exec(ctx, step.sql); err != nil {
			t.Fatalf("%s: %v", step.label, err)
		}
		if got := state(); got != step.want {
			t.Errorf("%s: %s, want %s", step.label, got, step.want)
		}
		if step.label == "down" {
			if _, err := f.Pool.Exec(ctx, `INSERT INTO story_mission (id, chapter, seq, title, offer_text, done_text, trigger_kind)
				VALUES ('x-1', 'C', 1, 't', 'o', 'd', 'ward_placed')`); err == nil {
				t.Error("after the down the CHECK still takes ward_placed")
			}
		}
	}
}
