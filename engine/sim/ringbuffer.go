package sim

// RingBuffer is a fixed-capacity FIFO of T. Pushes at capacity drop the
// oldest entry. Snapshot returns entries in chronological order (oldest
// first), which is what loop-detection and diff-against-previous want.
//
// Generic over T so the same primitive backs RecentActions (Action) and
// RecentReactorTicks (time.Time) on each Actor, PriceBook
// (PriceObservation), and any future per-actor history channels we add.
type RingBuffer[T any] struct {
	buf  []T
	head int  // index of next write
	full bool // wrapped past capacity at least once
}

// NewRingBuffer constructs a buffer with the given capacity. Capacity
// must be > 0; zero or negative panics rather than silently misbehaving.
func NewRingBuffer[T any](capacity int) *RingBuffer[T] {
	if capacity <= 0 {
		panic("sim: RingBuffer capacity must be > 0")
	}
	return &RingBuffer[T]{buf: make([]T, capacity)}
}

// Push appends v, dropping the oldest entry if the buffer is full.
func (r *RingBuffer[T]) Push(v T) {
	r.buf[r.head] = v
	r.head++
	if r.head == len(r.buf) {
		r.head = 0
		r.full = true
	}
}

// Len returns the number of entries currently stored.
func (r *RingBuffer[T]) Len() int {
	if r.full {
		return len(r.buf)
	}
	return r.head
}

// Cap returns the buffer's capacity.
func (r *RingBuffer[T]) Cap() int { return len(r.buf) }

// Clone returns a deep copy of the buffer (internal slice and indices).
// Used by repo/mem to deep-clone Actor entities across the serialization
// boundary so a mutation on one side doesn't leak to the other.
func (r *RingBuffer[T]) Clone() *RingBuffer[T] {
	if r == nil {
		return nil
	}
	cp := &RingBuffer[T]{
		buf:  append([]T(nil), r.buf...),
		head: r.head,
		full: r.full,
	}
	return cp
}

// Snapshot returns a slice copy of the buffer's contents in chronological
// order (oldest first, newest last). Safe to retain; not aliased to the
// buffer's internal storage.
func (r *RingBuffer[T]) Snapshot() []T {
	n := r.Len()
	out := make([]T, n)
	if !r.full {
		copy(out, r.buf[:r.head])
		return out
	}
	copy(out, r.buf[r.head:])
	copy(out[len(r.buf)-r.head:], r.buf[:r.head])
	return out
}
