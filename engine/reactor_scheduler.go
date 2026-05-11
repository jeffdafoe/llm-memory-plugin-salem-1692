package main

// Cascade-reactor pacing scheduler (ZBBS-HOME-263).
//
// Replaces inline `triggerImmediateTick` for cascade-triggered reactor
// ticks (`heard-speech`, `saw-action`, `npc-paid-you`,
// `arrival-into-populated-huddle`) with an in-memory min-heap delay
// queue. PC-initiated ticks (`pc-spoke`, `pc-knocked`), self-ticks
// (idle-sweep, return-to-work), and direct-API ticks continue to fire
// inline.
//
// Why: prior to HOME-263, reactor ticks fired inline as fast as the LLM
// returned (~2-3s between turns). Conversations had no pauses; multiple
// cascades fanning into the same listener stacked instantly. Adding
// 1-4s of jitter between cascade trigger and reactor fire produces
// natural conversational rhythm AND makes "John speaks twice in 200ms"
// merge into a single queued reaction (the second trigger merges into
// the existing pending entry instead of firing a second tick).
//
// The merge semantics also let chunk 4 (work) cleanly remove the
// same-trigger-actor rule from claimSceneTick — cost protection now
// comes from (a) merging on the schedule side and (b) the existing
// maxReactionsPerSceneActor=4 cap, which keeps a chatty scene bounded.
//
// Restart-loss is by design. The queue is in-memory; engine restarts
// drop pending ticks. Per shared/GUIDELINES "Postgres is for durable
// storage, not infrastructure substitute" — the v1 design with a
// pending_reactor_tick table was rejected because it conceded
// restart-loss anyway via a 10-min stale safety sweep, so the
// durability bought nothing concrete. A cascade interrupted by a
// restart will be re-engaged by the next external trigger (PC speak,
// idle-sweep, arrival) with a fresh sceneID — the conversational
// moment passed.

import (
	"container/heap"
	"context"
	"log"
	mathrand "math/rand/v2"
	"sync"
	"time"
)

// pendingTick is one queued reactor tick waiting to fire. Mirrors the
// argument set of triggerImmediateTick so the scheduler can fire it
// verbatim when due_at lands. force carries the original cascade
// callsite's intent — heard-speech / saw-action force=true so the
// addressee/witness isn't cost-gated out of reacting; arrival force=
// false so a recently-ticked NPC doesn't re-tick on every passerby.
type pendingTick struct {
	actorID, sceneID, reason, triggerActorID string
	force                                    bool
	dueAt                                    time.Time
	index                                    int // heap.Interface bookkeeping
}

// reactorHeap is a min-heap of *pendingTick keyed by dueAt.
type reactorHeap []*pendingTick

func (h reactorHeap) Len() int           { return len(h) }
func (h reactorHeap) Less(i, j int) bool { return h[i].dueAt.Before(h[j].dueAt) }
func (h reactorHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *reactorHeap) Push(x any) {
	pt := x.(*pendingTick)
	pt.index = len(*h)
	*h = append(*h, pt)
}
func (h *reactorHeap) Pop() any {
	old := *h
	n := len(old)
	pt := old[n-1]
	pt.index = -1
	old[n-1] = nil
	*h = old[:n-1]
	return pt
}

// reactorScheduler holds the in-memory queue of pending reactor ticks.
// One instance per engine, owned by App.ReactorScheduler. Concurrent-
// safe via mu; the run goroutine reads through wakeCh signals from
// schedule callers when the heap head changes.
type reactorScheduler struct {
	mu     sync.Mutex
	heap   reactorHeap
	index  map[string]*pendingTick // key: actorID|sceneID, for O(1) merge lookup
	wakeCh chan struct{}           // cap=1, signal-on-new-head
}

func newReactorScheduler() *reactorScheduler {
	return &reactorScheduler{
		heap:   reactorHeap{},
		index:  make(map[string]*pendingTick),
		wakeCh: make(chan struct{}, 1),
	}
}

// reactorScheduleKey is the merge dedup key for the index map. Must
// match the (actor, scene) tuple — two cascade triggers for the same
// listener in the same scene merge into one pending entry.
func reactorScheduleKey(actorID, sceneID string) string {
	return actorID + "|" + sceneID
}

// Reactor jitter band. v1 uses a single band for all cascade reasons;
// chunk 5 will add per-reason tuning if motivated by observation.
const (
	reactorJitterMin = 1 * time.Second
	reactorJitterMax = 4 * time.Second
)

// scheduleReactorTick queues a reactor tick for an actor in the scene.
// The tick fires after a 1-4s jitter via the run goroutine.
//
// Merge semantics: if the (actor, scene) slot is already pending, push
// the dueAt later (max). The latest trigger's reason and triggerActorID
// also overwrite the entry — when the tick eventually fires, the LLM
// sees the freshest cascade context. (Per work mail 6ff60106 — "later
// trigger pushes the fire time back gives the cluster time to settle"
// is the right semantic for a pacing primitive.)
//
// Empty sceneID is dropped with a log line. Cascade origins all carry
// a sceneID after WORK-222 / WORK-225; an empty sceneID would collapse
// every entry for the actor into one slot, which would silently lose
// pending reactions across separate scenes.
func (app *App) scheduleReactorTick(actorID, sceneID, reason, triggerActorID string, force bool) {
	if sceneID == "" {
		log.Printf("scheduleReactorTick: empty sceneID for actor=%s reason=%s — dropping", actorID, reason)
		return
	}
	sched := app.ReactorScheduler
	if sched == nil {
		return
	}

	jitter := reactorJitterMin + time.Duration(mathrand.Int64N(int64(reactorJitterMax-reactorJitterMin)))
	dueAt := time.Now().Add(jitter)
	key := reactorScheduleKey(actorID, sceneID)

	sched.mu.Lock()
	var becameHead bool
	if existing, ok := sched.index[key]; ok {
		// The latest cascade context always wins for reason / trigger,
		// regardless of whether dueAt moved (pacing decision lives in
		// dueAt; identity of the fire context lives in reason/trigger).
		existing.reason = reason
		existing.triggerActorID = triggerActorID
		existing.force = existing.force || force
		// Push dueAt later only if newDueAt is actually later. A second
		// trigger arriving slightly before the existing dueAt doesn't
		// pull the fire forward — that would defeat "let the cluster
		// settle." The merge is one-directional: monotonically later.
		if dueAt.After(existing.dueAt) {
			existing.dueAt = dueAt
			heap.Fix(&sched.heap, existing.index)
		}
		becameHead = sched.heap[0] == existing
	} else {
		pt := &pendingTick{
			actorID:        actorID,
			sceneID:        sceneID,
			reason:         reason,
			triggerActorID: triggerActorID,
			force:          force,
			dueAt:          dueAt,
		}
		heap.Push(&sched.heap, pt)
		sched.index[key] = pt
		becameHead = sched.heap[0] == pt
	}
	sched.mu.Unlock()

	if becameHead {
		// Non-blocking send: if a wake is already pending, the run
		// goroutine will re-read the head when it processes it.
		select {
		case sched.wakeCh <- struct{}{}:
		default:
		}
	}
}

// scheduleCoLocatedReactorTicks is the scheduled-tick analog of
// triggerCoLocatedTicks — discovers co-located reactors via the same
// query, but each listener gets a scheduleReactorTick call instead of
// an inline triggerImmediateTick goroutine.
//
// Used by NPC speech / action commits. PC-initiated callsites
// (pc-knocked, pc-spoke) continue to use triggerCoLocatedTicks for
// inline firing — PC interaction expects immediate response, not
// 1-4s pacing latency.
func (app *App) scheduleCoLocatedReactorTicks(ctx context.Context, structureID, excludeNpcID, reason string, force bool, sceneID, triggerActorID string) {
	ids := app.findCoLocatedReactors(ctx, structureID, excludeNpcID, triggerActorID)
	log.Printf("schedule-cascade reason=%s structure=%s exclude=%q found=%d ids=%v force=%v scene=%s",
		reason, structureID, excludeNpcID, len(ids), ids, force, sceneID)
	for _, id := range ids {
		app.scheduleReactorTick(id, sceneID, reason, triggerActorID, force)
	}
}

// runReactorScheduler is the single goroutine that drains the heap.
// Sleeps on time.NewTimer(time.Until(headDueAt)) until either:
//   - timer fires: pop head, dispatch the tick via go triggerImmediateTick
//   - wakeCh signals: re-read head, re-arm timer (a new schedule made
//     a sooner-due entry the head, or a merge pushed the head later)
//   - ctx.Done: shutdown
//
// No polling; the timer wakes exactly when the head is due. Started at
// engine boot from main.go and canceled on shutdown.
func (app *App) runReactorScheduler(ctx context.Context) {
	sched := app.ReactorScheduler
	if sched == nil {
		return
	}
	log.Printf("reactor-scheduler: started (jitter %s-%s)", reactorJitterMin, reactorJitterMax)
	for {
		sched.mu.Lock()
		var nextDue time.Time
		if len(sched.heap) > 0 {
			nextDue = sched.heap[0].dueAt
		}
		sched.mu.Unlock()

		var timerCh <-chan time.Time
		var timer *time.Timer
		if !nextDue.IsZero() {
			d := time.Until(nextDue)
			if d < 0 {
				d = 0
			}
			timer = time.NewTimer(d)
			timerCh = timer.C
		}

		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			log.Printf("reactor-scheduler: stopping")
			return
		case <-sched.wakeCh:
			if timer != nil {
				timer.Stop()
			}
			// Re-loop to re-read head.
		case <-timerCh:
			sched.mu.Lock()
			if len(sched.heap) > 0 && !time.Now().Before(sched.heap[0].dueAt) {
				pt := heap.Pop(&sched.heap).(*pendingTick)
				delete(sched.index, reactorScheduleKey(pt.actorID, pt.sceneID))
				sched.mu.Unlock()
				// Detached context: triggerImmediateTick spawns its own
				// LLM call; the scheduler context cancellation should not
				// abort an in-flight tick mid-LLM-roundtrip.
				go app.triggerImmediateTick(context.Background(),
					pt.actorID, pt.reason, pt.force,
					pt.sceneID, pt.triggerActorID)
			} else {
				// Head moved since we read it (a wakeCh signal might be
				// queued). Drop the timer and re-loop.
				sched.mu.Unlock()
			}
		}
	}
}
