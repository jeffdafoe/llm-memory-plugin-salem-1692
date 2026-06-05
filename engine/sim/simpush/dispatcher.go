// Package simpush ports v1's daily sim-conversation push (engine/
// sim_conversation_push.go, package main) into the v2 engine (ZBBS-WORK-376
// piece 3). Once per real UTC day it collects each agentized actor's
// just-completed-day agent_action_log rows and POSTs them to llm-memory-api's
// /v1/sim/conversation-day endpoint, which distills them into the per-day
// narrative note the four stateful NPCs' nightly dream cron reads. Without this
// the durable NPC memory has been frozen since the v2 cutover.
//
// The Dispatcher owns only the rollover/catch-up control flow; the DB reads
// (cursor, actor roster, day events) and the HTTP POST are injected as Store /
// Poster so the control flow is unit-testable with a fake clock and no real PG
// or network (pg-backed Store = repo/pg/sim_push.go; HTTP Poster = poster.go).
package simpush

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// dayFormat is the cursor / window date layout — a bare UTC calendar day.
const dayFormat = "2006-01-02"

// defaultInterval is how often the dispatcher checks for a rolled-over day.
// Each check is cheap when caught up (one cursor read + a date compare), so the
// cadence only bounds how soon after UTC midnight a completed day is flushed.
// 30m is well inside any nightly dream-cron window; the push is idempotent
// (the API upserts on (agent, day)) so an extra tick never double-writes.
const defaultInterval = 30 * time.Minute

// Store is the durable read/write surface the push needs. The pg
// implementation is repo/pg/SimPushStore.
type Store interface {
	// LastPushedDay returns the push cursor (YYYY-MM-DD), or "" when never set.
	LastPushedDay(ctx context.Context) (string, error)
	// SetLastPushedDay stamps the cursor after a day's push completes.
	SetLastPushedDay(ctx context.Context, day string) error
	// AgentizedActors lists the actors to build a day-note for (non-empty
	// llm_memory_agent).
	AgentizedActors(ctx context.Context) ([]sim.AgentActor, error)
	// DayEvents returns one actor's [dayStart, dayEnd) events — own actions plus
	// overheard co-present speech.
	DayEvents(ctx context.Context, actorID sim.ActorID, dayStart, dayEnd time.Time) ([]sim.SimDayEvent, error)
}

// Poster ships one (agent, day, events) batch to llm-memory-api.
// Implementations MUST treat "agent is not dream_mode=sim" and "agent unknown"
// as non-fatal (return nil): the engine pushes for every agentized actor and
// the API filters, so those responses are expected, not errors.
type Poster interface {
	PostDay(ctx context.Context, agent, day string, events []sim.SimDayEvent) error
}

// Dispatcher runs the daily push loop. Construct with NewDispatcher and run
// Run(ctx) in a goroutine for the life of the process.
type Dispatcher struct {
	store    Store
	poster   Poster
	interval time.Duration
	now      func() time.Time
}

// Option tunes a Dispatcher at construction.
type Option func(*Dispatcher)

// WithInterval overrides the default rollover-check cadence. Ignored if <= 0.
func WithInterval(d time.Duration) Option {
	return func(disp *Dispatcher) {
		if d > 0 {
			disp.interval = d
		}
	}
}

// WithClock overrides the time source (tests inject a fixed clock). Ignored if
// nil.
func WithClock(now func() time.Time) Option {
	return func(disp *Dispatcher) {
		if now != nil {
			disp.now = now
		}
	}
}

// NewDispatcher wires the dispatcher. store and poster are required (panics on
// nil — a wiring bug, surfaced loudly at startup).
func NewDispatcher(store Store, poster Poster, opts ...Option) *Dispatcher {
	if store == nil {
		panic("simpush: NewDispatcher requires a non-nil Store")
	}
	if poster == nil {
		panic("simpush: NewDispatcher requires a non-nil Poster")
	}
	d := &Dispatcher{
		store:    store,
		poster:   poster,
		interval: defaultInterval,
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Run drives the push on a ticker until ctx is cancelled. An immediate first
// pass runs on entry (initializes the cursor on a fresh deploy, or flushes any
// already-completed days), then one pass per interval. Bind ctx to the world
// lifecycle; on cancellation Run returns after the in-flight pass unwinds.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	d.dispatchOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.dispatchOnce(ctx)
		}
	}
}

// dispatchOnce pushes every UTC day from the cursor up to yesterday. Cheap when
// caught up (one cursor read + a compare). Errors are logged, not returned —
// the next tick retries. Ported from v1's dispatchSimConversationPush.
func (d *Dispatcher) dispatchOnce(ctx context.Context) {
	lastPushed, err := d.store.LastPushedDay(ctx)
	if err != nil {
		log.Printf("simpush: read cursor: %v", err)
		return
	}
	yesterday := d.now().UTC().Add(-24 * time.Hour).Format(dayFormat)

	// First-run init: anchor at yesterday so the NEXT post-midnight tick starts
	// pushing forward, rather than backfilling weeks of pre-existing rows on a
	// fresh deploy.
	if lastPushed == "" {
		if err := d.store.SetLastPushedDay(ctx, yesterday); err != nil {
			log.Printf("simpush: init cursor: %v", err)
		}
		return
	}

	// Validate the stored cursor before comparing / advancing it — a malformed
	// value would lex-compare wrong against yesterday and could spin the
	// catch-up loop (nextDay can't advance it).
	if _, err := time.Parse(dayFormat, lastPushed); err != nil {
		log.Printf("simpush: invalid cursor %q: %v (re-anchoring to yesterday)", lastPushed, err)
		if uerr := d.store.SetLastPushedDay(ctx, yesterday); uerr != nil {
			log.Printf("simpush: re-anchor cursor: %v", uerr)
		}
		return
	}

	if lastPushed >= yesterday {
		return // already caught up
	}

	// Catch-up: push each day from cursor+1 through yesterday, stamping the
	// cursor after each so a mid-window failure resumes rather than repeats.
	day, err := nextDay(lastPushed)
	if err != nil {
		log.Printf("simpush: advance from %q: %v", lastPushed, err)
		return
	}
	for day <= yesterday {
		if ctx.Err() != nil {
			return // shutdown mid-catch-up — next run resumes from the cursor
		}
		if err := d.pushDay(ctx, day); err != nil {
			log.Printf("simpush: %s: %v", day, err)
			return // leave the cursor un-stamped so the next tick retries this day
		}
		if err := d.store.SetLastPushedDay(ctx, day); err != nil {
			log.Printf("simpush: stamp cursor %s: %v", day, err)
			return
		}
		day, err = nextDay(day)
		if err != nil {
			log.Printf("simpush: advance from %q: %v", day, err)
			return
		}
	}
}

// pushDay builds and POSTs one (agent, day) batch per agentized actor. A
// per-actor failure is logged and the rest still run; the day is only counted
// as pushed (cursor stamped by the caller) when every actor succeeded, so a
// retryable blip re-runs the whole day next tick (the API's idempotent upsert
// makes the re-push harmless). Ported from v1's pushSimDay.
func (d *Dispatcher) pushDay(ctx context.Context, day string) error {
	dayStart, err := time.Parse(dayFormat, day)
	if err != nil {
		return fmt.Errorf("parse day %q: %w", day, err)
	}
	dayEnd := dayStart.Add(24 * time.Hour)

	actors, err := d.store.AgentizedActors(ctx)
	if err != nil {
		return fmt.Errorf("list agentized actors: %w", err)
	}

	hadFailure := false
	skippedNonSim := 0
	skippedUnknown := 0
	for _, a := range actors {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		events, err := d.store.DayEvents(ctx, a.ID, dayStart, dayEnd)
		if err != nil {
			log.Printf("simpush: %s %s: load events: %v", a.Agent, day, err)
			hadFailure = true
			continue
		}
		switch err := d.poster.PostDay(ctx, a.Agent, day, events); {
		case errors.Is(err, errSkippedNonSim):
			skippedNonSim++
		case errors.Is(err, errSkippedUnknown):
			skippedUnknown++
		case err != nil:
			log.Printf("simpush: %s %s: post: %v", a.Agent, day, err)
			hadFailure = true
		}
	}
	// Fold the contract-expected non-sim / unknown skips into one benign line per
	// day rather than one alarming "api 400" line per actor: a backlog boot pushes
	// every agentized actor for every caught-up day, and most are non-dreaming
	// shared VAs whose 400 is by-design (see Poster).
	if skippedNonSim > 0 || skippedUnknown > 0 {
		log.Printf("simpush: %s: skipped %d non-sim, %d unknown agent(s) (expected, not pushed)", day, skippedNonSim, skippedUnknown)
	}
	if hadFailure {
		return fmt.Errorf("one or more agent pushes failed for %s", day)
	}
	return nil
}

// nextDay returns the YYYY-MM-DD day after dayStr. Errors (rather than echoing
// the input) on a parse failure so the catch-up loop aborts cleanly instead of
// spinning on a malformed cursor.
func nextDay(dayStr string) (string, error) {
	t, err := time.Parse(dayFormat, dayStr)
	if err != nil {
		return "", err
	}
	return t.AddDate(0, 0, 1).Format(dayFormat), nil
}
