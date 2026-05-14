package handlers

import (
	"context"
	"testing"
	"time"
)

// pool_test.go — TickWorkerPool: CanAdmit semantics, the job-buffer size
// derivation, and clean worker shutdown.

func TestCanAdmitTracksCapacity(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	p := NewTickWorkerPool(w, tel) // buffer = clamp(2*1, 2, 16) = 2

	if !p.CanAdmit() {
		t.Fatal("CanAdmit should be true on an empty pool")
	}
	p.jobs <- tickJob{}
	p.jobs <- tickJob{}
	if p.CanAdmit() {
		t.Fatal("CanAdmit should be false when the buffer is full")
	}
	<-p.jobs
	if !p.CanAdmit() {
		t.Fatal("CanAdmit should be true again after a job drains")
	}
}

func TestCanAdmitFalseWhenStopping(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	p := NewTickWorkerPool(w, tel)

	if !p.CanAdmit() {
		t.Fatal("precondition: an empty pool should admit")
	}
	p.Stop() // no Start — Stop still flips the stopping flag
	if p.CanAdmit() {
		t.Fatal("CanAdmit must be false once Stop has begun, even with an empty buffer")
	}
}

func TestBufferSizeDerivation(t *testing.T) {
	cases := []struct {
		workerCount int
		wantBuffer  int
	}{
		{workerCount: 0, wantBuffer: 2},    // unset → default count 1 → 2*1
		{workerCount: 1, wantBuffer: 2},    // 2*1
		{workerCount: 5, wantBuffer: 10},   // 2*5
		{workerCount: 100, wantBuffer: 16}, // 2*100, clamped down to the max
	}
	for _, tc := range cases {
		w, tel, cancel := newTestWorld(t, tc.workerCount)
		p := NewTickWorkerPool(w, tel)
		if got := cap(p.jobs); got != tc.wantBuffer {
			t.Errorf("workerCount %d: buffer cap = %d, want %d", tc.workerCount, got, tc.wantBuffer)
		}
		cancel()
	}
}

func TestNewTickWorkerPoolRejectsNilWorld(t *testing.T) {
	assertPanics(t, "a nil world", func() {
		NewTickWorkerPool(nil, &recordingTelemetry{})
	})
}

func TestStartRejectsNilContext(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	p := NewTickWorkerPool(w, tel)
	assertPanics(t, "a nil context", func() { p.Start(nil) }) //nolint:staticcheck // intentional nil-ctx misuse
}

func TestStartAfterStopPanics(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	p := NewTickWorkerPool(w, tel)
	p.Stop() // permanently disables the pool — no restart semantics
	assertPanics(t, "Start after Stop", func() { p.Start(context.Background()) })
}

func TestStartStopJoinsWorkers(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 3)
	defer cancel()
	p := NewTickWorkerPool(w, tel)

	p.Start(context.Background())
	p.Stop()

	done := make(chan struct{})
	go func() { p.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("workers did not exit after Stop — goroutine leak")
	}
}
