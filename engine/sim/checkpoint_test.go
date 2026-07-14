package sim_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// checkpoint_test.go — coverage for the full-fidelity checkpoint snapshot
// and the periodic checkpoint driver (engine/sim/checkpoint.go).
//
// The load-bearing property is the deep-clone isolation in
// BuildCheckpointSnapshot: it is the whole reason the checkpoint reads a
// dedicated full clone rather than the slim Snapshot. The CheckpointNow /
// RunCheckpointer tests exercise the clone-on-world-goroutine then
// write-off-goroutine composition with a fake CheckpointFunc.

// runningWorld stands up a mem-backed world, starts its Run loop, and
// returns the world plus a cancel that stops the world goroutine.
func runningWorld(t *testing.T, actors map[sim.ActorID]*sim.Actor) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	if actors != nil {
		handles.Actors.Seed(actors)
	}
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// TestBuildCheckpointSnapshot_FullFidelityAndIsolation proves the snapshot
// (a) carries the full *Actor fields the slim Snapshot drops (Inventory,
// Attributes) and (b) is a deep, independent clone — mutating the world
// after the build, including byte-level edits to nested slices and deleting
// map entries, does not bleed into the snapshot.
//
// The world goroutine is not started here, so calling BuildCheckpointSnapshot
// directly is safe (nothing else mutates the maps).
func TestBuildCheckpointSnapshot_FullFidelityAndIsolation(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:        "hannah",
			Kind:      sim.KindNPCShared,
			Inventory: map[sim.ItemKind]int{"bread": 2},
			Attributes: map[string][]byte{
				"mood": []byte("calm"),
			},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	ts := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	w.Phase = sim.PhaseDay
	w.Environment = sim.WorldEnvironment{Now: ts}
	w.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{
		"obj1": {ID: "obj1", AssetID: "asset-x", EntryPolicy: sim.EntryPolicyOpen},
	}
	// Order is the one checkpoint aggregate without a repo/mem roundtrip
	// aliasing test, so cover its nested refs (ConsumerIDs slice +
	// DeliveredAt *time.Time) directly at this boundary.
	deliveredAt := time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC)
	w.Orders = map[sim.OrderID]*sim.Order{
		1: {ID: 1, ConsumerIDs: []sim.ActorID{"buyer1"}, DeliveredAt: &deliveredAt},
	}

	cp := w.BuildCheckpointSnapshot()

	// Full fidelity — fields the slim ActorSnapshot would have dropped.
	a := cp.Actors["hannah"]
	if a == nil {
		t.Fatal("checkpoint missing actor hannah")
	}
	if a.Inventory["bread"] != 2 {
		t.Errorf("Inventory[bread] = %d, want 2 (full-fidelity actor lost inventory)", a.Inventory["bread"])
	}
	if string(a.Attributes["mood"]) != "calm" {
		t.Errorf("Attributes[mood] = %q, want %q", a.Attributes["mood"], "calm")
	}
	if cp.VillageObjects["obj1"] == nil {
		t.Fatal("checkpoint missing village_object obj1")
	}
	ord := cp.Orders[1]
	if ord == nil {
		t.Fatal("checkpoint missing order ord1")
	}
	if len(ord.ConsumerIDs) != 1 || ord.ConsumerIDs[0] != "buyer1" {
		t.Errorf("Order.ConsumerIDs = %v, want [buyer1]", ord.ConsumerIDs)
	}
	if ord.DeliveredAt == nil || !ord.DeliveredAt.Equal(deliveredAt) {
		t.Errorf("Order.DeliveredAt = %v, want %v", ord.DeliveredAt, deliveredAt)
	}
	if cp.Phase != sim.PhaseDay {
		t.Errorf("Phase = %q, want %q", cp.Phase, sim.PhaseDay)
	}
	if !cp.Environment.Now.Equal(ts) {
		t.Errorf("Environment.Now = %v, want %v", cp.Environment.Now, ts)
	}

	// Isolation — mutate the live world every way that would alias a shallow
	// copy: value in a map, a byte inside a nested slice, a field on a
	// pointed-to aggregate, the time a *time.Time points at, and deleting a
	// whole map entry. wantDelivered captures the original time value before
	// the mutation zeroes it through the (aliased) source pointer.
	wantDelivered := deliveredAt
	w.Actors["hannah"].Inventory["bread"] = 99
	w.Actors["hannah"].Attributes["mood"][0] = 'X'
	w.VillageObjects["obj1"].EntryPolicy = sim.EntryPolicyClosed
	w.Orders[1].ConsumerIDs[0] = "mutated"
	*w.Orders[1].DeliveredAt = time.Time{}
	delete(w.Actors, "hannah")

	if cp.Actors["hannah"] == nil {
		t.Fatal("snapshot actor vanished after deleting from the live world (map not cloned)")
	}
	if cp.Actors["hannah"].Inventory["bread"] != 2 {
		t.Errorf("snapshot Inventory[bread] = %d after live mutation, want 2 (not isolated)", cp.Actors["hannah"].Inventory["bread"])
	}
	if string(cp.Actors["hannah"].Attributes["mood"]) != "calm" {
		t.Errorf("snapshot Attributes[mood] = %q after live byte mutation, want %q (nested slice not cloned)", cp.Actors["hannah"].Attributes["mood"], "calm")
	}
	if cp.VillageObjects["obj1"].EntryPolicy != sim.EntryPolicyOpen {
		t.Errorf("snapshot village_object EntryPolicy changed after live mutation (aggregate not cloned)")
	}
	if cp.Orders[1].ConsumerIDs[0] != "buyer1" {
		t.Errorf("snapshot Order.ConsumerIDs[0] = %q after live mutation, want buyer1 (slice not cloned)", cp.Orders[1].ConsumerIDs[0])
	}
	if !cp.Orders[1].DeliveredAt.Equal(wantDelivered) {
		t.Errorf("snapshot Order.DeliveredAt changed after live mutation (*time.Time not cloned)")
	}
}

// TestCheckpointSnapshotCommand_ReturnsSnapshot — the command builds and
// returns a *CheckpointSnapshot on the world goroutine.
func TestCheckpointSnapshotCommand_ReturnsSnapshot(t *testing.T) {
	w, cancel := runningWorld(t, map[sim.ActorID]*sim.Actor{
		"hannah": {ID: "hannah", Kind: sim.KindNPCShared},
	})
	defer cancel()

	res, err := w.Send(sim.CheckpointSnapshotCommand())
	if err != nil {
		t.Fatalf("Send(CheckpointSnapshotCommand): %v", err)
	}
	cp, ok := res.(*sim.CheckpointSnapshot)
	if !ok {
		t.Fatalf("command returned %T, want *sim.CheckpointSnapshot", res)
	}
	if cp.Actors["hannah"] == nil {
		t.Error("snapshot missing seeded actor")
	}
}

// TestCheckpointNow_BuildsAndSaves — CheckpointNow builds the clone on the
// world goroutine and hands it to the CheckpointFunc, which sees the world's
// actors.
func TestCheckpointNow_BuildsAndSaves(t *testing.T) {
	w, cancel := runningWorld(t, map[sim.ActorID]*sim.Actor{
		"hannah": {ID: "hannah", Kind: sim.KindNPCShared},
	})
	defer cancel()

	var got *sim.CheckpointSnapshot
	save := func(_ context.Context, cp *sim.CheckpointSnapshot) (*sim.Quarantine, error) {
		got = cp
		return nil, nil
	}

	if _, err := sim.CheckpointNow(context.Background(), w, save); err != nil {
		t.Fatalf("CheckpointNow: %v", err)
	}
	if got == nil {
		t.Fatal("CheckpointFunc was not called with a snapshot")
	}
	if got.Actors["hannah"] == nil {
		t.Error("saved snapshot missing seeded actor")
	}
}

// TestCheckpointNow_SaveError — a CheckpointFunc failure surfaces from
// CheckpointNow unchanged.
func TestCheckpointNow_SaveError(t *testing.T) {
	w, cancel := runningWorld(t, nil)
	defer cancel()

	sentinel := errors.New("disk full")
	save := func(_ context.Context, _ *sim.CheckpointSnapshot) (*sim.Quarantine, error) { return nil, sentinel }

	_, err := sim.CheckpointNow(context.Background(), w, save)
	if !errors.Is(err, sentinel) {
		t.Fatalf("CheckpointNow error = %v, want it to wrap %v", err, sentinel)
	}
}

// TestCheckpointNow_BuildError — a cancelled context fails the snapshot-build
// SendContext before the CheckpointFunc is ever reached.
func TestCheckpointNow_BuildError(t *testing.T) {
	w, cancel := runningWorld(t, nil)
	defer cancel()

	saveCalled := false
	save := func(_ context.Context, _ *sim.CheckpointSnapshot) (*sim.Quarantine, error) {
		saveCalled = true
		return nil, nil
	}

	ctx, cancelCtx := context.WithCancel(context.Background())
	cancelCtx()

	if _, err := sim.CheckpointNow(ctx, w, save); err == nil {
		t.Fatal("expected error from cancelled-context build")
	}
	if saveCalled {
		t.Error("CheckpointFunc must not run when the snapshot build fails")
	}
}

// TestRunCheckpointer_PeriodicAndStop — with a small CheckpointInterval the
// driver fires at least one checkpoint, and cancelling its context returns
// the loop. The fast cadence also confirms the interval is read from Settings
// rather than falling back to the 60s default (a save inside 2s is only
// possible at the configured 20ms).
func TestRunCheckpointer_PeriodicAndStop(t *testing.T) {
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	// Set before Run starts — no concurrent reader yet.
	w.Settings.CheckpointInterval = 20 * time.Millisecond

	worldCtx, cancelWorld := context.WithCancel(context.Background())
	go w.Run(worldCtx)
	defer cancelWorld()

	saved := make(chan struct{}, 64)
	save := func(_ context.Context, _ *sim.CheckpointSnapshot) (*sim.Quarantine, error) {
		select {
		case saved <- struct{}{}:
		default:
		}
		return nil, nil
	}

	health := &sim.CheckpointHealth{}
	cpCtx, cancelCP := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sim.RunCheckpointer(cpCtx, w, save, health)
		close(done)
	}()

	select {
	case <-saved:
	case <-time.After(2 * time.Second):
		t.Fatal("no checkpoint fired within 2s at a 20ms interval")
	}

	cancelCP()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunCheckpointer did not return after context cancel")
	}

	// The periodic loop recorded at least one successful checkpoint and no
	// failures (the save func always succeeds).
	snap := health.Snapshot()
	if snap.TotalSuccesses == 0 {
		t.Errorf("CheckpointHealth recorded no successes; want >= 1")
	}
	if snap.TotalFailures != 0 {
		t.Errorf("CheckpointHealth recorded %d failures; want 0", snap.TotalFailures)
	}
	if snap.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d; want 0", snap.ConsecutiveFailures)
	}
}

// TestCheckpointHealth_NilSafe locks the nil-receiver contract: a nil
// *CheckpointHealth must accept Record* calls and Snapshot without panicking,
// so callers (and RunCheckpointer) can pass nil when they don't track health.
func TestCheckpointHealth_NilSafe(t *testing.T) {
	var h *sim.CheckpointHealth
	h.RecordSuccess(time.Now(), nil)
	h.RecordFailure(time.Now(), errors.New("x"))
	snap := h.Snapshot()
	if snap.TotalSuccesses != 0 || snap.TotalFailures != 0 || snap.ConsecutiveFailures != 0 {
		t.Errorf("nil health Snapshot = %+v; want zero value", snap)
	}

	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	w.Settings.CheckpointInterval = 20 * time.Millisecond
	worldCtx, cancelWorld := context.WithCancel(context.Background())
	go w.Run(worldCtx)
	defer cancelWorld()

	saved := make(chan struct{}, 1)
	save := func(_ context.Context, _ *sim.CheckpointSnapshot) (*sim.Quarantine, error) {
		select {
		case saved <- struct{}{}:
		default:
		}
		return nil, nil
	}
	cpCtx, cancelCP := context.WithCancel(context.Background())
	defer cancelCP()
	// nil health must not panic the loop.
	go sim.RunCheckpointer(cpCtx, w, save, nil)
	select {
	case <-saved:
	case <-time.After(2 * time.Second):
		t.Fatal("no checkpoint fired with nil health")
	}
}

// TestCheckpointHealth_RecordsFailureThenRecovery verifies the failure streak
// advances on RecordFailure and resets (with the last-error cleared) on the
// next RecordSuccess.
func TestCheckpointHealth_RecordsFailureThenRecovery(t *testing.T) {
	h := &sim.CheckpointHealth{}
	h.RecordFailure(time.Now(), errors.New("boom"))
	h.RecordFailure(time.Now(), errors.New("boom again"))
	snap := h.Snapshot()
	if snap.ConsecutiveFailures != 2 || snap.TotalFailures != 2 {
		t.Fatalf("after 2 failures: consecutive=%d total=%d; want 2/2", snap.ConsecutiveFailures, snap.TotalFailures)
	}
	if snap.LastError != "boom again" {
		t.Errorf("LastError = %q; want last error message", snap.LastError)
	}
	h.RecordSuccess(time.Now(), nil)
	snap = h.Snapshot()
	if snap.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures after success = %d; want 0", snap.ConsecutiveFailures)
	}
	if snap.TotalFailures != 2 || snap.TotalSuccesses != 1 {
		t.Errorf("totals after recovery: failures=%d successes=%d; want 2/1", snap.TotalFailures, snap.TotalSuccesses)
	}
	if snap.LastError != "" {
		t.Errorf("LastError after success = %q; want cleared", snap.LastError)
	}
}
