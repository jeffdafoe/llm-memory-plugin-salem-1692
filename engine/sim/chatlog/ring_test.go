package chatlog

import (
	"fmt"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func rec(scene, dir, content string) sim.ChatRecord {
	return sim.ChatRecord{At: time.Now().UTC(), SceneID: scene, ActorID: "actor", Direction: dir, Content: content}
}

func TestWriteAndRecent_OldestFirst(t *testing.T) {
	r := New(8)
	r.WriteChat(rec("s1", "perception", "the prompt"))
	r.WriteChat(rec("s1", "response", "first reply"))
	r.WriteChat(rec("s1", "response", "second reply"))

	got := r.Recent("s1", 0) // all
	if len(got) != 3 {
		t.Fatalf("want 3 records, got %d", len(got))
	}
	if got[0].Content != "the prompt" || got[2].Content != "second reply" {
		t.Errorf("not oldest-first: %+v", got)
	}
}

func TestPerSceneIsolation(t *testing.T) {
	r := New(8)
	r.WriteChat(rec("s1", "response", "s1-one"))
	r.WriteChat(rec("s2", "response", "s2-one"))
	if a := r.Recent("s1", 0); len(a) != 1 || a[0].Content != "s1-one" {
		t.Errorf("scene s1 leaked/missing: %+v", a)
	}
	if b := r.Recent("s2", 0); len(b) != 1 || b[0].Content != "s2-one" {
		t.Errorf("scene s2 leaked/missing: %+v", b)
	}
}

func TestEviction_DropsOldestPerScene(t *testing.T) {
	r := New(2)
	r.WriteChat(rec("s1", "response", "one"))
	r.WriteChat(rec("s1", "response", "two"))
	r.WriteChat(rec("s1", "response", "three")) // evicts "one"

	got := r.Recent("s1", 0)
	if len(got) != 2 || got[0].Content != "two" || got[1].Content != "three" {
		t.Errorf("per-scene eviction wrong: %+v", got)
	}
}

func TestRecent_LimitKeepsMostRecentOldestFirst(t *testing.T) {
	r := New(8)
	for _, c := range []string{"c1", "c2", "c3", "c4"} {
		r.WriteChat(rec("s1", "response", c))
	}
	got := r.Recent("s1", 2)
	if len(got) != 2 || got[0].Content != "c3" || got[1].Content != "c4" {
		t.Errorf("limit wrong: %+v", got)
	}
}

func TestWriteChat_EmptySceneDropped(t *testing.T) {
	r := New(4)
	r.WriteChat(rec("", "response", "orphan"))
	if st := r.Stats(); st.Written != 0 || st.Scenes != 0 {
		t.Errorf("empty-scene record should be dropped, got Written=%d Scenes=%d", st.Written, st.Scenes)
	}
}

func TestNilReceiver_NoPanic(t *testing.T) {
	var r *RingSink
	r.WriteChat(rec("s1", "response", "x")) // must not panic
	if got := r.Recent("s1", 0); len(got) != 0 {
		t.Errorf("nil-receiver Recent should be empty, got %+v", got)
	}
	_ = r.Stats() // must not panic
}

func TestSceneCapEviction(t *testing.T) {
	r := New(4)
	base := time.Unix(1700000000, 0).UTC()
	// Fill exactly maxScenes, increasing last-write times so s0000 is stalest.
	for i := 0; i < DefaultMaxScenes; i++ {
		r.WriteChat(sim.ChatRecord{
			At:        base.Add(time.Duration(i) * time.Second),
			SceneID:   fmt.Sprintf("s%04d", i),
			Direction: "response",
			Content:   "c",
		})
	}
	if got := r.Stats().Scenes; got != DefaultMaxScenes {
		t.Fatalf("Scenes = %d, want %d (at cap)", got, DefaultMaxScenes)
	}
	// A new scene pushes over the cap -> the stalest (s0000) is evicted.
	r.WriteChat(sim.ChatRecord{At: base.Add(time.Hour), SceneID: "newcomer", Direction: "response", Content: "c"})
	if got := r.Stats().Scenes; got != DefaultMaxScenes {
		t.Errorf("Scenes after overflow = %d, want bounded at %d", got, DefaultMaxScenes)
	}
	if got := r.Recent("s0000", 0); len(got) != 0 {
		t.Errorf("stalest scene s0000 should have been evicted, got %+v", got)
	}
	if got := r.Recent("newcomer", 0); len(got) != 1 {
		t.Errorf("newcomer should be retained after eviction")
	}
}

// All-equal (zero) timestamps still evict SOME scene and keep the count bounded
// — ties broken by map order, which is intentional for a bounded debug surface
// (production At is the distinct-per-tick clock). Documents the eviction
// behavior under a degenerate/injected clock.
func TestSceneCapEviction_EqualTimestamps(t *testing.T) {
	r := New(4)
	zero := time.Time{}
	for i := 0; i < DefaultMaxScenes; i++ {
		r.WriteChat(sim.ChatRecord{At: zero, SceneID: fmt.Sprintf("s%04d", i), Direction: "response", Content: "c"})
	}
	r.WriteChat(sim.ChatRecord{At: zero, SceneID: "newcomer", Direction: "response", Content: "c"})
	if got := r.Stats().Scenes; got != DefaultMaxScenes {
		t.Errorf("Scenes after equal-timestamp overflow = %d, want bounded at %d", got, DefaultMaxScenes)
	}
	if got := r.Recent("newcomer", 0); len(got) != 1 {
		t.Errorf("newcomer should be retained after equal-timestamp eviction")
	}
}

func TestRecent_ReturnsCopy(t *testing.T) {
	r := New(4)
	r.WriteChat(rec("s1", "response", "orig"))
	got := r.Recent("s1", 0)
	got[0].Content = "mutated"
	if again := r.Recent("s1", 0); again[0].Content != "orig" {
		t.Errorf("Recent must return a copy; ring was mutated to %q", again[0].Content)
	}
}
