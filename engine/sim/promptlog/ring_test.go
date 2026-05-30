package promptlog

import (
	"fmt"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func rec(actor sim.ActorID, attempt, prompt string) sim.PromptRecord {
	return sim.PromptRecord{
		At:        time.Unix(0, 0).UTC(),
		ActorID:   actor,
		AttemptID: sim.TickAttemptID(attempt),
		Prompt:    prompt,
	}
}

func TestNew_NonPositiveCapacityFallsBack(t *testing.T) {
	if got := New(0).Stats().PerActorCapacity; got != DefaultPerActorCapacity {
		t.Errorf("cap=0 → PerActorCapacity %d, want default %d", got, DefaultPerActorCapacity)
	}
	if got := New(-5).Stats().PerActorCapacity; got != DefaultPerActorCapacity {
		t.Errorf("cap=-5 → PerActorCapacity %d, want default %d", got, DefaultPerActorCapacity)
	}
}

func TestWriteAndRecent_OldestFirst(t *testing.T) {
	r := New(4)
	r.WritePrompt(rec("a", "t1", "first"))
	r.WritePrompt(rec("a", "t2", "second"))
	r.WritePrompt(rec("a", "t3", "third"))

	got := r.Recent("a", 0) // all
	if len(got) != 3 {
		t.Fatalf("want 3 prompts, got %d", len(got))
	}
	if got[0].Prompt != "first" || got[2].Prompt != "third" {
		t.Errorf("not oldest-first: %q .. %q", got[0].Prompt, got[2].Prompt)
	}
}

func TestPerActorIsolation(t *testing.T) {
	r := New(4)
	r.WritePrompt(rec("a", "t1", "a-one"))
	r.WritePrompt(rec("b", "t1", "b-one"))
	if a := r.Recent("a", 0); len(a) != 1 || a[0].Prompt != "a-one" {
		t.Errorf("actor a leaked/missing: %+v", a)
	}
	if b := r.Recent("b", 0); len(b) != 1 || b[0].Prompt != "b-one" {
		t.Errorf("actor b leaked/missing: %+v", b)
	}
}

func TestEviction_DropsOldestPerActor(t *testing.T) {
	r := New(2)
	r.WritePrompt(rec("a", "t1", "one"))
	r.WritePrompt(rec("a", "t2", "two"))
	r.WritePrompt(rec("a", "t3", "three")) // evicts "one"

	got := r.Recent("a", 0)
	if len(got) != 2 || got[0].Prompt != "two" || got[1].Prompt != "three" {
		t.Errorf("ring did not drop oldest: %+v", promptTexts(got))
	}
	st := r.Stats()
	if st.Dropped != 1 {
		t.Errorf("Dropped = %d, want 1", st.Dropped)
	}
	if st.Written != 3 {
		t.Errorf("Written = %d, want 3", st.Written)
	}
	if st.Buffered != 2 {
		t.Errorf("Buffered = %d, want 2 (bounded by cap)", st.Buffered)
	}
	if st.Actors != 1 {
		t.Errorf("Actors = %d, want 1", st.Actors)
	}
}

func TestRecent_LimitKeepsMostRecentOldestFirst(t *testing.T) {
	r := New(8)
	for _, p := range []string{"p1", "p2", "p3", "p4"} {
		r.WritePrompt(rec("a", "t", p))
	}
	got := r.Recent("a", 2)
	if len(got) != 2 || got[0].Prompt != "p3" || got[1].Prompt != "p4" {
		t.Errorf("limit=2 should yield the 2 most recent, oldest-first; got %+v", promptTexts(got))
	}
}

func TestRecent_UnknownActorEmptyNonNil(t *testing.T) {
	r := New(4)
	got := r.Recent("nobody", 0)
	if got == nil {
		t.Fatal("Recent for unknown actor returned nil; want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %d", len(got))
	}
}

func TestWritePrompt_EmptyActorDropped(t *testing.T) {
	r := New(4)
	r.WritePrompt(rec("", "t1", "orphan"))
	if st := r.Stats(); st.Written != 0 || st.Actors != 0 {
		t.Errorf("empty-actor prompt should be dropped, got Written=%d Actors=%d", st.Written, st.Actors)
	}
}

func TestWritePrompt_NilReceiverNoPanic(t *testing.T) {
	var r *RingSink
	r.WritePrompt(rec("a", "t1", "x")) // must not panic
}

// Recent and Stats must honor the nil-receiver contract too (not just
// WritePrompt) — a future caller shouldn't have to nil-check the ring.
func TestRecentAndStats_NilReceiverSafe(t *testing.T) {
	var r *RingSink
	if got := r.Recent("a", 0); got == nil || len(got) != 0 {
		t.Errorf("nil Recent should return empty non-nil, got %v", got)
	}
	if got := r.Stats(); got != (Stats{}) {
		t.Errorf("nil Stats should return the zero value, got %+v", got)
	}
}

// The distinct-actor count is bounded: a new actor arriving at the cap evicts
// the least-recently-active actor's ring, so transient-actor churn can't grow
// the map without bound.
func TestEviction_BoundsDistinctActors(t *testing.T) {
	r := New(2)
	base := time.Unix(0, 0).UTC()
	// Fill exactly maxActors with increasing last-write times, so a000 is the
	// stalest (oldest most-recent prompt).
	for i := 0; i < DefaultMaxActors; i++ {
		r.WritePrompt(sim.PromptRecord{
			At:      base.Add(time.Duration(i) * time.Second),
			ActorID: sim.ActorID(fmt.Sprintf("a%03d", i)),
			Prompt:  "p",
		})
	}
	if got := r.Stats().Actors; got != DefaultMaxActors {
		t.Fatalf("Actors = %d, want %d (at cap)", got, DefaultMaxActors)
	}
	// A new actor pushes over the cap → the stalest (a000) is evicted.
	r.WritePrompt(sim.PromptRecord{At: base.Add(time.Hour), ActorID: "newcomer", Prompt: "p"})
	if got := r.Stats().Actors; got != DefaultMaxActors {
		t.Errorf("Actors after overflow = %d, want bounded at %d", got, DefaultMaxActors)
	}
	if len(r.Recent("a000", 0)) != 0 {
		t.Errorf("stalest actor a000 should have been evicted")
	}
	if len(r.Recent("newcomer", 0)) != 1 {
		t.Errorf("newcomer should be retained after eviction")
	}
	// An EXISTING actor writing again must never trigger eviction.
	r.WritePrompt(sim.PromptRecord{At: base.Add(2 * time.Hour), ActorID: "newcomer", Prompt: "p2"})
	if got := r.Stats().Actors; got != DefaultMaxActors {
		t.Errorf("existing-actor write changed actor count to %d, want %d", got, DefaultMaxActors)
	}
}

func TestRecent_ReturnsCopy(t *testing.T) {
	r := New(4)
	r.WritePrompt(rec("a", "t1", "orig"))
	got := r.Recent("a", 0)
	got[0].Prompt = "mutated"
	if again := r.Recent("a", 0); again[0].Prompt != "orig" {
		t.Errorf("Recent must return a copy; store was mutated to %q", again[0].Prompt)
	}
}

func promptTexts(recs []sim.PromptRecord) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Prompt
	}
	return out
}
