package telemetry

import (
	"fmt"
	"sync"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func rec(kind string) sim.TickTelemetryRecord {
	return sim.TickTelemetryRecord{Kind: kind}
}

func kinds(recs []sim.TickTelemetryRecord) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Kind
	}
	return out
}

func TestRingSink_DefaultCapacity(t *testing.T) {
	r := New(0)
	if got := r.Stats().Capacity; got != DefaultCapacity {
		t.Errorf("New(0) capacity = %d, want %d", got, DefaultCapacity)
	}
	if got := New(-5).Stats().Capacity; got != DefaultCapacity {
		t.Errorf("New(-5) capacity = %d, want %d", got, DefaultCapacity)
	}
}

func TestRingSink_EmptySnapshot(t *testing.T) {
	if got := New(4).Snapshot(); len(got) != 0 {
		t.Errorf("empty snapshot len = %d, want 0", len(got))
	}
}

func TestRingSink_FillsWithoutDropping(t *testing.T) {
	r := New(3)
	r.WriteTickTelemetry(rec("a"))
	r.WriteTickTelemetry(rec("b"))
	r.WriteTickTelemetry(rec("c"))

	got := kinds(r.Snapshot())
	want := []string{"a", "b", "c"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("snapshot = %v, want %v", got, want)
	}
	st := r.Stats()
	if st.Size != 3 || st.Written != 3 || st.Dropped != 0 {
		t.Errorf("stats = %+v, want size=3 written=3 dropped=0", st)
	}
}

func TestRingSink_WrapAroundEvictsOldest(t *testing.T) {
	r := New(3)
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		r.WriteTickTelemetry(rec(k))
	}
	// Capacity 3, wrote 5 → oldest two (a, b) evicted; chronological order kept.
	got := kinds(r.Snapshot())
	want := []string{"c", "d", "e"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("snapshot = %v, want %v", got, want)
	}
	st := r.Stats()
	if st.Size != 3 || st.Written != 5 || st.Dropped != 2 {
		t.Errorf("stats = %+v, want size=3 written=5 dropped=2", st)
	}
}

// TestRingSink_SnapshotIsolated proves Snapshot returns a copy that a later
// write cannot mutate (no aliasing of the backing array).
func TestRingSink_SnapshotIsolated(t *testing.T) {
	r := New(2)
	r.WriteTickTelemetry(rec("a"))
	snap := r.Snapshot()
	r.WriteTickTelemetry(rec("b"))
	r.WriteTickTelemetry(rec("c")) // wraps, overwrites slot "a" held
	if len(snap) != 1 || snap[0].Kind != "a" {
		t.Errorf("earlier snapshot mutated by later writes: %v", kinds(snap))
	}
}

// TestRingSink_Concurrent exercises the mutex under the race detector: many
// concurrent writers plus concurrent readers, asserting the accounting stays
// consistent and no read ever observes a torn slice.
func TestRingSink_Concurrent(t *testing.T) {
	r := New(64)
	const writers = 8
	const perWriter = 500

	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				r.WriteTickTelemetry(rec(fmt.Sprintf("w%d-%d", id, i)))
			}
		}(w)
	}

	// Concurrent readers while writes are in flight.
	stop := make(chan struct{})
	var rwg sync.WaitGroup
	rwg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer rwg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if got := r.Snapshot(); len(got) > 64 {
						t.Errorf("snapshot len %d exceeds capacity", len(got))
						return
					}
				}
			}
		}()
	}

	wg.Wait()
	close(stop)
	rwg.Wait()

	st := r.Stats()
	if st.Written != writers*perWriter {
		t.Errorf("written = %d, want %d", st.Written, writers*perWriter)
	}
	if st.Size != 64 {
		t.Errorf("size = %d, want 64 (saturated)", st.Size)
	}
	if st.Dropped != st.Written-uint64(st.Size) {
		t.Errorf("dropped = %d, want written-size = %d", st.Dropped, st.Written-uint64(st.Size))
	}
}
