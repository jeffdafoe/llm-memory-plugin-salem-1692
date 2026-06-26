package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// labor_commands_test.go — LLM-26 coverage of the three Command Fns
// (SolicitWork, AcceptWork, DeclineWork) and the completion/expiry sweep
// (EvaluateLaborLedgerSweep). Self-contained fixtures: unlike the pay
// fixtures, a labor actor needs the AttrWorker marker and the tests assert
// on the per-actor LaboringUntil mirror + settle-at-completion coin movement.

// laborActor — minimal actor seed for labor Command tests.
type laborActor struct {
	id            sim.ActorID
	displayName   string
	coins         int
	huddleID      sim.HuddleID
	worker        bool // seeds Attributes[AttrWorker]
	moveInFlight  bool
	laboringUntil *time.Time
}

// buildLaborWorld constructs a world with the given actors, one huddle, and
// one scene observing it (mirrors buildPayWithItemWorld). Actors whose
// huddleID matches join the huddle.
func buildLaborWorld(t *testing.T, huddleID sim.HuddleID, sceneID sim.SceneID, actors []laborActor) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()

	now := time.Now().UTC()
	seed := make(map[sim.ActorID]*sim.Actor, len(actors))
	members := make(map[sim.ActorID]struct{}, len(actors))
	for _, s := range actors {
		a := &sim.Actor{
			ID:              s.id,
			DisplayName:     s.displayName,
			Kind:            sim.KindNPCShared,
			State:           sim.StateIdle,
			Coins:           s.coins,
			CurrentHuddleID: s.huddleID,
			LaboringUntil:   s.laboringUntil,
			RecentActions:   sim.NewRingBuffer[sim.Action](4),
		}
		if s.worker {
			a.Attributes = map[string][]byte{sim.AttrWorker: {}}
		}
		if s.laboringUntil != nil {
			a.State = sim.StateLaboring
		}
		if s.moveInFlight {
			a.MoveIntent = &sim.MoveIntent{AttemptID: sim.MovementAttemptID(1)}
		}
		seed[s.id] = a
		if s.huddleID == huddleID && huddleID != "" {
			members[s.id] = struct{}{}
		}
	}
	handles.Actors.Seed(seed)

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	if huddleID != "" {
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Huddles[huddleID] = &sim.Huddle{
				ID:        huddleID,
				Members:   members,
				StartedAt: now,
			}
			world.Scenes[sceneID] = &sim.Scene{
				ID:       sceneID,
				OriginAt: now,
				Bound:    sim.NewUnboundedBound(),
				Huddles:  map[sim.HuddleID]struct{}{huddleID: {}},
			}
			sim.RebuildIndicesForTest(world)
			return nil, nil
		}}); err != nil {
			cancel()
			<-done
			t.Fatalf("seed scene+huddle: %v", err)
		}
	}
	return w, func() { cancel(); <-done }
}

// laborEvents accumulates the three labor event families as they emit.
type laborEvents struct {
	Received []sim.LaborOfferReceived
	Accepted []sim.LaborOfferAccepted
	Resolved []sim.LaborResolved
}

func captureLaborEvents(t *testing.T, w *sim.World) *laborEvents {
	t.Helper()
	out := &laborEvents{}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			switch e := evt.(type) {
			case *sim.LaborOfferReceived:
				out.Received = append(out.Received, *e)
			case *sim.LaborOfferAccepted:
				out.Accepted = append(out.Accepted, *e)
			case *sim.LaborResolved:
				out.Resolved = append(out.Resolved, *e)
			}
		}))
		return nil, nil
	}}); err != nil {
		t.Fatalf("captureLaborEvents subscribe: %v", err)
	}
	return out
}

// readLaborLedger snapshots World.LaborLedger on the world goroutine.
func readLaborLedger(t *testing.T, w *sim.World) map[sim.LaborID]sim.LaborOffer {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		out := make(map[sim.LaborID]sim.LaborOffer, len(world.LaborLedger))
		for id, o := range world.LaborLedger {
			if o == nil {
				continue
			}
			out[id] = *o
		}
		return out, nil
	}})
	if err != nil {
		t.Fatalf("readLaborLedger: %v", err)
	}
	return res.(map[sim.LaborID]sim.LaborOffer)
}

// actorSnap is the slice of actor state the labor tests assert on.
type actorSnap struct {
	Coins         int
	State         sim.ActorState
	LaboringUntil *time.Time
}

func readActor(t *testing.T, w *sim.World, id sim.ActorID) actorSnap {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a, ok := world.Actors[id]
		if !ok {
			return actorSnap{}, nil
		}
		return actorSnap{Coins: a.Coins, State: a.State, LaboringUntil: a.LaboringUntil}, nil
	}})
	if err != nil {
		t.Fatalf("readActor %q: %v", id, err)
	}
	return res.(actorSnap)
}

// seedLaborOffer inserts a LaborOffer directly for tests exercising
// AcceptWork / DeclineWork / the sweep against a pre-seeded offer.
func seedLaborOffer(t *testing.T, w *sim.World, offer sim.LaborOffer) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.LaborLedger[offer.ID] = sim.CloneLaborOffer(&offer)
		return nil, nil
	}}); err != nil {
		t.Fatalf("seedLaborOffer: %v", err)
	}
}

// ---- SolicitWork -----------------------------------------------------

func TestSolicitWork_MintsPendingOffer(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()
	events := captureLaborEvents(t, w)

	now := time.Now().UTC()
	res, err := w.Send(sim.SolicitWork("ezekiel", "Josiah", 10, 30, now))
	if err != nil {
		t.Fatalf("SolicitWork: %v", err)
	}
	out, ok := res.(sim.LaborSolicitResult)
	if !ok {
		t.Fatalf("result type = %T, want LaborSolicitResult", res)
	}
	if out.State != sim.LaborStatePending {
		t.Errorf("result State = %q, want pending", out.State)
	}
	if out.EmployerName != "Josiah" {
		t.Errorf("result EmployerName = %q, want Josiah", out.EmployerName)
	}

	ledger := readLaborLedger(t, w)
	if len(ledger) != 1 {
		t.Fatalf("ledger size = %d, want 1", len(ledger))
	}
	o := ledger[out.ID]
	if o.WorkerID != "ezekiel" || o.EmployerID != "josiah" {
		t.Errorf("offer parties = %q/%q, want ezekiel/josiah", o.WorkerID, o.EmployerID)
	}
	if o.Reward != 10 || o.DurationMin != 30 {
		t.Errorf("offer terms reward=%d dur=%d, want 10/30", o.Reward, o.DurationMin)
	}
	if o.State != sim.LaborStatePending {
		t.Errorf("offer State = %q, want pending", o.State)
	}
	if o.HuddleID != "h1" || o.SceneID != "sc1" {
		t.Errorf("offer anchor = %q/%q, want h1/sc1", o.HuddleID, o.SceneID)
	}
	wantExpiry := now.Add(3 * time.Minute)
	if !o.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("offer ExpiresAt = %v, want %v", o.ExpiresAt, wantExpiry)
	}

	// No coins move on solicit.
	if got := readActor(t, w, "josiah").Coins; got != 50 {
		t.Errorf("employer coins = %d after solicit, want 50 (no move)", got)
	}
	if len(events.Received) != 1 || events.Received[0].LaborID != out.ID {
		t.Errorf("LaborOfferReceived = %+v", events.Received)
	}
}

func TestSolicitWork_RejectsNonWorker(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1"}, // no worker attribute
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()

	now := time.Now().UTC()
	if _, err := w.Send(sim.SolicitWork("ezekiel", "Josiah", 10, 30, now)); err == nil {
		t.Fatal("SolicitWork by non-worker: want error, got nil")
	}
	if n := len(readLaborLedger(t, w)); n != 0 {
		t.Errorf("ledger size = %d after rejected solicit, want 0", n)
	}
}

func TestSolicitWork_RejectsUnknownEmployer(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()

	now := time.Now().UTC()
	if _, err := w.Send(sim.SolicitWork("ezekiel", "Nobody", 10, 30, now)); err == nil {
		t.Fatal("SolicitWork naming absent employer: want error, got nil")
	}
}

func TestSolicitWork_RejectsDuplicatePending(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()

	now := time.Now().UTC()
	if _, err := w.Send(sim.SolicitWork("ezekiel", "Josiah", 10, 30, now)); err != nil {
		t.Fatalf("first SolicitWork: %v", err)
	}
	if _, err := w.Send(sim.SolicitWork("ezekiel", "Josiah", 8, 20, now)); err == nil {
		t.Fatal("second SolicitWork to same employer: want duplicate error, got nil")
	}
	if n := len(readLaborLedger(t, w)); n != 1 {
		t.Errorf("ledger size = %d, want 1 (duplicate rejected)", n)
	}
}

func TestSolicitWork_RejectsWhenNotHuddled(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "", worker: true}, // not in a conversation
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()

	now := time.Now().UTC()
	if _, err := w.Send(sim.SolicitWork("ezekiel", "Josiah", 10, 30, now)); err == nil {
		t.Fatal("SolicitWork while not huddled: want error, got nil")
	}
}

func TestSolicitWork_RejectsBadTerms(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()
	now := time.Now().UTC()

	if _, err := w.Send(sim.SolicitWork("ezekiel", "Josiah", 0, 30, now)); err == nil {
		t.Error("reward 0: want error, got nil")
	}
	if _, err := w.Send(sim.SolicitWork("ezekiel", "Josiah", 10, 0, now)); err == nil {
		t.Error("duration 0: want error, got nil")
	}
	if _, err := w.Send(sim.SolicitWork("ezekiel", "Josiah", 10, 9999, now)); err == nil {
		t.Error("duration over cap: want error, got nil")
	}
}

// ---- AcceptWork ------------------------------------------------------

func TestAcceptWork_StartsWindowNoCoinsMove(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()
	events := captureLaborEvents(t, w)

	now := time.Now().UTC()
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30,
		State:    sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1",
		CreatedAt: now.Add(-time.Minute),
		ExpiresAt: now.Add(2 * time.Minute),
	})

	res, err := w.Send(sim.AcceptWork("josiah", 1, now))
	if err != nil {
		t.Fatalf("AcceptWork: %v", err)
	}
	out := res.(sim.LaborAcceptResult)
	if out.State != sim.LaborStateWorking {
		t.Errorf("result State = %q, want working", out.State)
	}
	wantUntil := now.Add(30 * time.Minute)
	if !out.WorkingUntil.Equal(wantUntil) {
		t.Errorf("result WorkingUntil = %v, want %v", out.WorkingUntil, wantUntil)
	}

	o := readLaborLedger(t, w)[1]
	if o.State != sim.LaborStateWorking {
		t.Errorf("offer State = %q, want working", o.State)
	}
	if o.AcceptedAt == nil || !o.AcceptedAt.Equal(now) {
		t.Errorf("offer AcceptedAt = %v, want %v", o.AcceptedAt, now)
	}
	if o.WorkingUntil == nil || !o.WorkingUntil.Equal(wantUntil) {
		t.Errorf("offer WorkingUntil = %v, want %v", o.WorkingUntil, wantUntil)
	}

	// No coins move at accept (settle-at-completion).
	if got := readActor(t, w, "josiah").Coins; got != 50 {
		t.Errorf("employer coins = %d, want 50 (no coins move at accept)", got)
	}
	// Worker mirror: laboring window + state, NOT yet paid.
	ws := readActor(t, w, "ezekiel")
	if ws.Coins != 0 {
		t.Errorf("worker coins = %d, want 0 (paid at completion, not accept)", ws.Coins)
	}
	if ws.State != sim.StateLaboring {
		t.Errorf("worker State = %q, want laboring", ws.State)
	}
	if ws.LaboringUntil == nil || !ws.LaboringUntil.Equal(wantUntil) {
		t.Errorf("worker LaboringUntil = %v, want %v", ws.LaboringUntil, wantUntil)
	}
	if len(events.Accepted) != 1 || events.Accepted[0].LaborID != 1 {
		t.Errorf("LaborOfferAccepted = %+v", events.Accepted)
	}
}

func TestAcceptWork_RejectsNonEmployer(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
		{id: "mary", displayName: "Mary", huddleID: "h1", coins: 50},
	})
	defer stop()
	now := time.Now().UTC()
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30, State: sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1", ExpiresAt: now.Add(2 * time.Minute),
	})

	// Mary is not the employer — idempotent reject, no transition.
	if _, err := w.Send(sim.AcceptWork("mary", 1, now)); err == nil {
		t.Fatal("AcceptWork by non-employer: want error, got nil")
	}
	if got := readLaborLedger(t, w)[1].State; got != sim.LaborStatePending {
		t.Errorf("offer State = %q after non-employer accept, want still pending", got)
	}
	if got := readActor(t, w, "josiah").Coins; got != 50 {
		t.Errorf("employer coins = %d, want 50 (no debit on rejected accept)", got)
	}
}

func TestAcceptWork_InsufficientFundsFlipsFailed(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 5}, // can't afford 10
	})
	defer stop()
	events := captureLaborEvents(t, w)
	now := time.Now().UTC()
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30, State: sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1", ExpiresAt: now.Add(2 * time.Minute),
	})

	// Gate-driven terminal flip — NOT a tool error.
	if _, err := w.Send(sim.AcceptWork("josiah", 1, now)); err != nil {
		t.Fatalf("AcceptWork (insufficient funds) returned tool error, want terminal flip: %v", err)
	}
	o := readLaborLedger(t, w)[1]
	if o.State != sim.LaborStateFailedUnavailable {
		t.Errorf("offer State = %q, want failed_unavailable", o.State)
	}
	if got := readActor(t, w, "josiah").Coins; got != 5 {
		t.Errorf("employer coins = %d, want 5 (no debit on failed accept)", got)
	}
	if ws := readActor(t, w, "ezekiel"); ws.LaboringUntil != nil || ws.State == sim.StateLaboring {
		t.Errorf("worker entered laboring on failed accept: state=%q until=%v", ws.State, ws.LaboringUntil)
	}
	if len(events.Resolved) != 1 || events.Resolved[0].TerminalState != sim.LaborTerminalStateFailedUnavailable {
		t.Errorf("LaborResolved = %+v", events.Resolved)
	}
}

func TestAcceptWork_CoPresenceLostFlipsFailed(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h2", worker: true}, // worker drifted to another huddle
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()
	now := time.Now().UTC()
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30, State: sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1", ExpiresAt: now.Add(2 * time.Minute),
	})

	if _, err := w.Send(sim.AcceptWork("josiah", 1, now)); err != nil {
		t.Fatalf("AcceptWork (co-presence lost): %v", err)
	}
	if got := readLaborLedger(t, w)[1].State; got != sim.LaborStateFailedUnavailable {
		t.Errorf("offer State = %q, want failed_unavailable", got)
	}
	if got := readActor(t, w, "josiah").Coins; got != 50 {
		t.Errorf("employer coins = %d, want 50 (no debit)", got)
	}
}

func TestAcceptWork_PastTTLFlipsExpired(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()
	now := time.Now().UTC()
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30, State: sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1", ExpiresAt: now.Add(-time.Minute), // already expired
	})

	if _, err := w.Send(sim.AcceptWork("josiah", 1, now)); err != nil {
		t.Fatalf("AcceptWork (past TTL): %v", err)
	}
	if got := readLaborLedger(t, w)[1].State; got != sim.LaborStateExpired {
		t.Errorf("offer State = %q, want expired", got)
	}
	if got := readActor(t, w, "josiah").Coins; got != 50 {
		t.Errorf("employer coins = %d, want 50 (no debit on expired)", got)
	}
}

// ---- DeclineWork -----------------------------------------------------

func TestDeclineWork_FlipsDeclined(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()
	events := captureLaborEvents(t, w)
	now := time.Now().UTC()
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30, State: sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1", ExpiresAt: now.Add(2 * time.Minute),
	})

	res, err := w.Send(sim.DeclineWork("josiah", 1, now))
	if err != nil {
		t.Fatalf("DeclineWork: %v", err)
	}
	if out := res.(sim.LaborDeclineResult); out.State != sim.LaborStateDeclined {
		t.Errorf("result State = %q, want declined", out.State)
	}
	if got := readLaborLedger(t, w)[1].State; got != sim.LaborStateDeclined {
		t.Errorf("offer State = %q, want declined", got)
	}
	if got := readActor(t, w, "josiah").Coins; got != 50 {
		t.Errorf("employer coins = %d, want 50 (no coins move on decline)", got)
	}
	if len(events.Resolved) != 1 || events.Resolved[0].TerminalState != sim.LaborTerminalStateDeclined {
		t.Errorf("LaborResolved = %+v", events.Resolved)
	}
}

func TestDeclineWork_RejectsNonEmployer(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()
	now := time.Now().UTC()
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30, State: sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1", ExpiresAt: now.Add(2 * time.Minute),
	})

	// The worker can't decline their own offer — only the employer.
	if _, err := w.Send(sim.DeclineWork("ezekiel", 1, now)); err == nil {
		t.Fatal("DeclineWork by non-employer: want error, got nil")
	}
	if got := readLaborLedger(t, w)[1].State; got != sim.LaborStatePending {
		t.Errorf("offer State = %q, want still pending", got)
	}
}

// ---- Sweep -----------------------------------------------------------

func TestEvaluateLaborLedgerSweep_ExpiresPending(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()
	events := captureLaborEvents(t, w)
	now := time.Now().UTC()
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30, State: sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1", ExpiresAt: now.Add(-time.Minute),
	})

	if _, err := w.Send(sim.EvaluateLaborLedgerSweep(now)); err != nil {
		t.Fatalf("EvaluateLaborLedgerSweep: %v", err)
	}
	o := readLaborLedger(t, w)[1]
	if o.State != sim.LaborStateExpired {
		t.Errorf("offer State = %q, want expired", o.State)
	}
	if o.ResolvedAt == nil || !o.ResolvedAt.Equal(now) {
		t.Errorf("offer ResolvedAt = %v, want %v", o.ResolvedAt, now)
	}
	if len(events.Resolved) != 1 || events.Resolved[0].TerminalState != sim.LaborTerminalStateExpired {
		t.Errorf("LaborResolved = %+v", events.Resolved)
	}
}

func TestEvaluateLaborLedgerSweep_CompletesWorking(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50}, // full purse — debited at completion, not accept
	})
	defer stop()
	events := captureLaborEvents(t, w)
	now := time.Now().UTC()
	accepted := now.Add(-31 * time.Minute)
	until := now.Add(-time.Minute) // window elapsed
	// Worker carries the mirror, as AcceptWork would have set it.
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30, State: sim.LaborStateWorking,
		HuddleID: "h1", SceneID: "sc1",
		AcceptedAt: &accepted, WorkingUntil: &until,
	})
	// Mirror the worker's laboring state for the completion path to clear.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["ezekiel"]
		u := until
		a.LaboringUntil = &u
		a.State = sim.StateLaboring
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed worker mirror: %v", err)
	}

	if _, err := w.Send(sim.EvaluateLaborLedgerSweep(now)); err != nil {
		t.Fatalf("EvaluateLaborLedgerSweep: %v", err)
	}
	o := readLaborLedger(t, w)[1]
	if o.State != sim.LaborStateCompleted {
		t.Errorf("offer State = %q, want completed", o.State)
	}
	// Worker paid the reward at completion; mirror cleared.
	ws := readActor(t, w, "ezekiel")
	if ws.Coins != 10 {
		t.Errorf("worker coins = %d, want 10 (paid at completion)", ws.Coins)
	}
	if ws.LaboringUntil != nil {
		t.Errorf("worker LaboringUntil = %v, want nil (cleared)", ws.LaboringUntil)
	}
	if ws.State == sim.StateLaboring {
		t.Errorf("worker State still laboring after completion, want cleared")
	}
	// Employer debited the reward at completion (the atomic transfer).
	if got := readActor(t, w, "josiah").Coins; got != 40 {
		t.Errorf("employer coins = %d, want 40 (debited reward at completion)", got)
	}
	if len(events.Resolved) != 1 || events.Resolved[0].TerminalState != sim.LaborTerminalStateCompleted {
		t.Errorf("LaborResolved = %+v", events.Resolved)
	}
}

// TestEvaluateLaborLedgerSweep_EmployerBrokeAtCompletionFails — under
// settle-at-completion the employer's balance can drift below the reward
// during a long window. At completion the sweep re-checks funds: if the
// employer can't pay, the finished work resolves FailedUnavailable, no coins
// move, and the worker is freed unpaid.
func TestEvaluateLaborLedgerSweep_EmployerBrokeAtCompletionFails(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 5}, // spent down below the 10 reward
	})
	defer stop()
	events := captureLaborEvents(t, w)
	now := time.Now().UTC()
	until := now.Add(-time.Minute) // window elapsed
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30, State: sim.LaborStateWorking,
		HuddleID: "h1", SceneID: "sc1", WorkingUntil: &until,
	})
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["ezekiel"]
		u := until
		a.LaboringUntil = &u
		a.State = sim.StateLaboring
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed worker mirror: %v", err)
	}

	if _, err := w.Send(sim.EvaluateLaborLedgerSweep(now)); err != nil {
		t.Fatalf("EvaluateLaborLedgerSweep: %v", err)
	}
	o := readLaborLedger(t, w)[1]
	if o.State != sim.LaborStateFailedUnavailable {
		t.Errorf("offer State = %q, want failed_unavailable (employer broke)", o.State)
	}
	// No coins move on an unpaid completion.
	if got := readActor(t, w, "josiah").Coins; got != 5 {
		t.Errorf("employer coins = %d, want 5 (no debit on unpaid)", got)
	}
	ws := readActor(t, w, "ezekiel")
	if ws.Coins != 0 {
		t.Errorf("worker coins = %d, want 0 (unpaid)", ws.Coins)
	}
	// Worker is still freed — the work is finished regardless of payment.
	if ws.LaboringUntil != nil || ws.State == sim.StateLaboring {
		t.Errorf("worker not freed after unpaid completion: state=%q until=%v", ws.State, ws.LaboringUntil)
	}
	if len(events.Resolved) != 1 || events.Resolved[0].TerminalState != sim.LaborTerminalStateFailedUnavailable {
		t.Errorf("LaborResolved = %+v", events.Resolved)
	}
}

func TestEvaluateLaborLedgerSweep_SkipsActive(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()
	events := captureLaborEvents(t, w)
	now := time.Now().UTC()
	until := now.Add(20 * time.Minute)
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30, State: sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1", ExpiresAt: now.Add(2 * time.Minute),
	})
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 2, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 5, DurationMin: 30, State: sim.LaborStateWorking,
		HuddleID: "h1", SceneID: "sc1", WorkingUntil: &until,
	})

	if _, err := w.Send(sim.EvaluateLaborLedgerSweep(now)); err != nil {
		t.Fatalf("EvaluateLaborLedgerSweep: %v", err)
	}
	ledger := readLaborLedger(t, w)
	if ledger[1].State != sim.LaborStatePending {
		t.Errorf("offer 1 State = %q, want still pending (within TTL)", ledger[1].State)
	}
	if ledger[2].State != sim.LaborStateWorking {
		t.Errorf("offer 2 State = %q, want still working (within window)", ledger[2].State)
	}
	if len(events.Resolved) != 0 {
		t.Errorf("emitted Resolved on active offers: %+v", events.Resolved)
	}
}

func TestEvaluateLaborLedgerSweep_ReapsOldTerminal(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()
	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour) // older than the 1h retention
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30, State: sim.LaborStateDeclined,
		HuddleID: "h1", SceneID: "sc1", ResolvedAt: &old,
	})

	if _, err := w.Send(sim.EvaluateLaborLedgerSweep(now)); err != nil {
		t.Fatalf("EvaluateLaborLedgerSweep: %v", err)
	}
	if _, present := readLaborLedger(t, w)[1]; present {
		t.Error("terminal offer past retention was not reaped")
	}
}

// ---- end-to-end ------------------------------------------------------

func TestLaborLifecycle_EndToEndConservesCoins(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true, coins: 3},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()

	now := time.Now().UTC()
	res, err := w.Send(sim.SolicitWork("ezekiel", "Josiah", 10, 30, now))
	if err != nil {
		t.Fatalf("SolicitWork: %v", err)
	}
	id := res.(sim.LaborSolicitResult).ID

	if _, err := w.Send(sim.AcceptWork("josiah", id, now)); err != nil {
		t.Fatalf("AcceptWork: %v", err)
	}
	// Mid-window: no coins have moved yet (settle-at-completion).
	if got := readActor(t, w, "josiah").Coins; got != 50 {
		t.Fatalf("employer coins mid-window = %d, want 50 (nothing moves until completion)", got)
	}
	if got := readActor(t, w, "ezekiel").Coins; got != 3 {
		t.Fatalf("worker coins mid-window = %d, want 3 (unpaid)", got)
	}

	// Advance past the window and sweep.
	after := now.Add(31 * time.Minute)
	if _, err := w.Send(sim.EvaluateLaborLedgerSweep(after)); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := readLaborLedger(t, w)[id].State; got != sim.LaborStateCompleted {
		t.Fatalf("offer State = %q, want completed", got)
	}
	// Coins conserved: employer -10, worker +10. Total 53 before and after.
	emp := readActor(t, w, "josiah").Coins
	wrk := readActor(t, w, "ezekiel").Coins
	if emp != 40 || wrk != 13 {
		t.Errorf("final coins employer=%d worker=%d, want 40/13", emp, wrk)
	}
	if emp+wrk != 53 {
		t.Errorf("coin total = %d, want 53 (conserved)", emp+wrk)
	}
}

// ---- code_review regressions ----------------------------------------

// TestSolicitWork_RejectsSecondPendingDifferentEmployer — one pending outgoing
// offer per worker (not per worker+employer): a worker with an offer out to one
// employer can't simultaneously bid a second to another. Prevents the
// multi-employer race where every late acceptor hits failed_unavailable.
func TestSolicitWork_RejectsSecondPendingDifferentEmployer(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
		{id: "john", displayName: "John", huddleID: "h1", coins: 50},
	})
	defer stop()
	now := time.Now().UTC()
	if _, err := w.Send(sim.SolicitWork("ezekiel", "Josiah", 10, 30, now)); err != nil {
		t.Fatalf("first SolicitWork: %v", err)
	}
	if _, err := w.Send(sim.SolicitWork("ezekiel", "John", 8, 20, now)); err == nil {
		t.Fatal("second SolicitWork to a different employer: want error, got nil")
	}
	if n := len(readLaborLedger(t, w)); n != 1 {
		t.Errorf("ledger size = %d, want 1 (one pending offer per worker)", n)
	}
}

// TestAcceptWork_WorkerAlreadyWorkingFlipsFailed — the busy-gate is ledger-
// authoritative: a worker with a live Working offer can't be hired again, even
// if the offer's window has elapsed but the sweep hasn't settled it (the
// sweep-lag overlapping-job hazard code_review caught). Seeded directly since
// SolicitWork's gate would prevent the second pending offer.
func TestAcceptWork_WorkerAlreadyWorkingFlipsFailed(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
		{id: "john", displayName: "John", huddleID: "h1", coins: 50},
	})
	defer stop()
	now := time.Now().UTC()
	elapsed := now.Add(-time.Minute) // window elapsed but not yet swept
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30, State: sim.LaborStateWorking,
		HuddleID: "h1", SceneID: "sc1", WorkingUntil: &elapsed,
	})
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 2, WorkerID: "ezekiel", EmployerID: "john",
		Reward: 5, DurationMin: 15, State: sim.LaborStatePending,
		HuddleID: "h1", SceneID: "sc1", ExpiresAt: now.Add(2 * time.Minute),
	})
	if _, err := w.Send(sim.AcceptWork("john", 2, now)); err != nil {
		t.Fatalf("AcceptWork (worker busy): %v", err)
	}
	if got := readLaborLedger(t, w)[2].State; got != sim.LaborStateFailedUnavailable {
		t.Errorf("offer 2 State = %q, want failed_unavailable (worker already on a job)", got)
	}
	if got := readLaborLedger(t, w)[1].State; got != sim.LaborStateWorking {
		t.Errorf("offer 1 State = %q, want still working (untouched)", got)
	}
}

// TestSettleCompletedLabor_PreservesNewerJobMirror — settling a stale offer must
// clear the worker mirror ONLY if it owns it. Here the worker's mirror points at
// a newer job's window; settling the stale offer must not free the worker.
func TestSettleCompletedLabor_PreservesNewerJobMirror(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "ezekiel", displayName: "Ezekiel", huddleID: "h1", worker: true},
		{id: "josiah", displayName: "Josiah", huddleID: "h1", coins: 50},
	})
	defer stop()
	now := time.Now().UTC()
	staleUntil := now.Add(-time.Minute)     // offer 1's window elapsed
	newerUntil := now.Add(20 * time.Minute) // the worker's current-job mirror
	seedLaborOffer(t, w, sim.LaborOffer{
		ID: 1, WorkerID: "ezekiel", EmployerID: "josiah",
		Reward: 10, DurationMin: 30, State: sim.LaborStateWorking,
		HuddleID: "h1", SceneID: "sc1", WorkingUntil: &staleUntil,
	})
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["ezekiel"]
		u := newerUntil
		a.LaboringUntil = &u
		a.State = sim.StateLaboring
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed worker mirror: %v", err)
	}
	if _, err := w.Send(sim.EvaluateLaborLedgerSweep(now)); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := readLaborLedger(t, w)[1].State; got != sim.LaborStateCompleted {
		t.Errorf("offer 1 State = %q, want completed", got)
	}
	ws := readActor(t, w, "ezekiel")
	if ws.LaboringUntil == nil || !ws.LaboringUntil.Equal(newerUntil) {
		t.Errorf("worker mirror = %v, want preserved at %v (settling stale offer must not clear a newer job)", ws.LaboringUntil, newerUntil)
	}
	if ws.State != sim.StateLaboring {
		t.Errorf("worker State = %q, want still laboring", ws.State)
	}
}
