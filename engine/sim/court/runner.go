// Package court runs the magistrates' sittings (LLM-695).
//
// Once a day, at the sitting time, every pending case filed before the sitting
// is heard by the magistrate — an Opus virtual agent (sim.CourtMagistrateModel)
// with read-only tools over the engine's records. Each case is its own session
// on its own scene, so one case's reading never bleeds into another's. The
// session ends when he calls `rule`; the engine applies the ruling
// (sim.ApplyCourtRuling) and delivers it to everyone the matter concerns.
//
// He is not ticked. Nothing runs where nothing is pending.
//
// Everything he can do is read, except two writes: the ruling itself (a closed
// set the engine validates and applies) and his own bench book — one note in his
// namespace, loaded into every session, that he rewrites to carry what he has
// learned about the village from one sitting to the next.
package court

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// RecordStore is the durable record the read tools query (agent_action_log,
// through pg.ActionLogRepo). Retained indefinitely.
type RecordStore interface {
	// LoadDayEvents: an actor's own rows plus the speech overheard in the
	// conversations they were part of, in [start, end).
	LoadDayEvents(ctx context.Context, actorID sim.ActorID, start, end time.Time) ([]sim.SimDayEvent, error)
	// LoadDealingsBetween: every conversation both were part of, plus each one's
	// rows naming the other, in [start, end), at most limit rows.
	LoadDealingsBetween(ctx context.Context, a, b sim.ActorID, aName, bName string, start, end time.Time, limit int) ([]sim.SimDayEvent, error)
}

// NoteStore reads and writes the bench book (memory-api documents).
type NoteStore interface {
	ReadNote(ctx context.Context, namespace, slug string) (string, bool, error)
	SaveNote(ctx context.Context, namespace, slug, title, content, cognitiveType string) error
}

const (
	// Namespace is the magistrate's own memory-api namespace (the VA's name).
	Namespace = sim.CourtMagistrateModel
	// BenchBookSlug is the one note he keeps.
	BenchBookSlug = "bench-book"
	// MaxBenchBookRunes caps the bench book so it stays a working note.
	MaxBenchBookRunes = 4000

	// maxRounds bounds one session's model calls.
	maxRounds = 24
	// maxNudges is how many prose-only replies are answered with a reminder to
	// rule before the session gives up.
	maxNudges = 2
	// retryAfter spaces attempts at a case whose session failed.
	retryAfter = time.Hour
	// tickInterval is how often the runner looks for cases to hear.
	tickInterval = time.Minute
	// sessionTimeout bounds one case's whole session.
	sessionTimeout = 15 * time.Minute
	// persistTimeout bounds the trailing tool-result persist.
	persistTimeout = 5 * time.Second
	// tickerName is the ticker-health name.
	tickerName = "court"
)

// ErrSitting is returned by SitNow while a sitting is already in progress.
var ErrSitting = errors.New("court: a sitting is already in progress")

// Runner owns the sittings.
type Runner struct {
	ctx     context.Context
	w       *sim.World
	client  llm.Client
	records RecordStore
	notes   NoteStore

	mu          sync.Mutex
	sitting     bool
	lastAttempt map[sim.CourtCaseID]time.Time
}

// Register starts the runner's ticker goroutine and returns the runner (the
// umbilical's sit-now route holds it). The goroutine and every sitting run on
// ctx — the engine's lifecycle, never a request's.
func Register(ctx context.Context, w *sim.World, client llm.Client, records RecordStore, notes NoteStore) *Runner {
	if w == nil || client == nil || records == nil || notes == nil {
		panic("court: Register requires a world, an LLM client, a record store and a note store")
	}
	r := &Runner{
		ctx:         ctx,
		w:           w,
		client:      client,
		records:     records,
		notes:       notes,
		lastAttempt: make(map[sim.CourtCaseID]time.Time),
	}
	w.RegisterTicker(tickerName, tickInterval)
	go r.loop()
	return r
}

func (r *Runner) loop() {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	if _, err := r.sit(false); err != nil && !errors.Is(err, ErrSitting) {
		log.Printf("court: %v", err)
	}
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.w.BeatTicker(tickerName)
			if _, err := r.sit(false); err != nil && !errors.Is(err, ErrSitting) {
				log.Printf("court: %v", err)
			}
		}
	}
}

// SitNow hears every pending case at once, regardless of the sitting time or a
// recent failed attempt — the umbilical's test route. The sitting runs in the
// background; the ids it will hear are returned.
func (r *Runner) SitNow() ([]sim.CourtCaseID, error) {
	return r.sit(true)
}

// sit collects the cases to hear and, unless a sitting is already running,
// hears them one by one in the background. The ticker only dispatches, so a
// long session never stalls its heartbeat.
func (r *Runner) sit(force bool) ([]sim.CourtCaseID, error) {
	if r.ctx.Err() != nil {
		return nil, nil
	}
	now := time.Now().UTC()
	res, err := r.w.SendContext(r.ctx, sim.CourtCasesToHear(now, force))
	if err != nil {
		if r.ctx.Err() != nil {
			return nil, nil
		}
		return nil, err
	}
	cases, _ := res.([]*sim.CourtCase)

	r.mu.Lock()
	if r.sitting {
		r.mu.Unlock()
		if force {
			return nil, ErrSitting
		}
		return nil, nil
	}
	var hear []*sim.CourtCase
	for _, c := range cases {
		if !force {
			if last, ok := r.lastAttempt[c.ID]; ok && now.Sub(last) < retryAfter {
				continue
			}
		}
		r.lastAttempt[c.ID] = now
		hear = append(hear, c)
	}
	if len(hear) == 0 {
		r.mu.Unlock()
		return nil, nil
	}
	r.sitting = true
	r.mu.Unlock()

	ids := make([]sim.CourtCaseID, 0, len(hear))
	for _, c := range hear {
		ids = append(ids, c.ID)
	}
	go func() {
		defer func() {
			r.mu.Lock()
			r.sitting = false
			r.mu.Unlock()
		}()
		log.Printf("court: the magistrates sit — %d matter(s)", len(hear))
		for _, c := range hear {
			if r.ctx.Err() != nil {
				return
			}
			r.hear(c)
		}
	}()
	return ids, nil
}

// hear runs one case's session to a ruling, or gives up and leaves it pending
// for the next attempt.
func (r *Runner) hear(c *sim.CourtCase) {
	ctx, cancel := context.WithTimeout(r.ctx, sessionTimeout)
	defer cancel()

	res, err := r.w.SendContext(ctx, sim.CourtSessionContext(c.ID))
	if err != nil {
		log.Printf("court: %s: session context: %v", c.ID, err)
		return
	}
	info, ok := res.(sim.CourtSessionInfo)
	if !ok {
		log.Printf("court: %s: session context returned %T", c.ID, res)
		return
	}
	if info.Case.Status != sim.CourtCaseStatusPending {
		return
	}
	bench, _, err := r.notes.ReadNote(ctx, Namespace, BenchBookSlug)
	if err != nil {
		log.Printf("court: %s: read bench book: %v (hearing without it)", c.ID, err)
		bench = ""
	}

	s := &session{
		r:       r,
		info:    info,
		sceneID: llm.NewSceneID(),
	}
	// The matter, the roster and the bench book ride as stable context, which
	// memory-api puts in the (cached) system prompt on every call — never in
	// history, where a long session's 50-row replay window could drop it.
	matter := renderOpening(info, bench, time.Now())
	s.transcript = []llm.Message{{Role: llm.RoleUser, Content: openingText}}
	defer s.persistTrailing()

	nudges := 0
	for round := 0; round < maxRounds; round++ {
		resp, err := r.client.Complete(ctx, llm.Request{
			Model:         sim.CourtMagistrateModel,
			SceneID:       s.sceneID,
			Messages:      s.transcript,
			Tools:         toolSpecs,
			StableContext: matter,
			SimActorName:  "the magistrates",
		})
		if err != nil {
			log.Printf("court: %s: round %d: %v", c.ID, round, err)
			return
		}
		if len(resp.ToolCalls) == 0 {
			s.transcript = append(s.transcript, llm.Message{Role: llm.RoleAssistant, Content: resp.Content})
			nudges++
			if nudges > maxNudges {
				log.Printf("court: %s: no ruling — the magistrate answered in prose %d times", c.ID, nudges)
				return
			}
			s.transcript = append(s.transcript, llm.Message{Role: llm.RoleUser, Content: nudgeText})
			continue
		}
		s.transcript = append(s.transcript, llm.Message{Role: llm.RoleAssistant, Content: resp.Content, ToolCalls: resp.ToolCalls})
		for _, call := range resp.ToolCalls {
			content := "[skipped] The ruling is given; the session is over."
			if !s.ruled {
				content = s.run(ctx, call)
			}
			s.transcript = append(s.transcript, llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, Content: content})
		}
		if s.ruled || s.aborted {
			return
		}
	}
	log.Printf("court: %s: no ruling after %d rounds — the case stays pending", c.ID, maxRounds)
}

const openingText = "The court is in session. Hear the matter set out before you and give your ruling."

const nudgeText ="The court is waiting on your ruling. Read what you still need, then give it with the rule tool."

// session is one case's hearing.
type session struct {
	r          *Runner
	info       sim.CourtSessionInfo
	sceneID    string
	transcript []llm.Message
	ruled      bool
	aborted    bool
}

// persistTrailing writes the session's last tool results to the VA's history so
// the scene never ends on a tool call without its result.
func (s *session) persistTrailing() {
	persister, ok := s.r.client.(llm.ToolResultPersister)
	if !ok || s.r.ctx.Err() != nil {
		return
	}
	var results []llm.ToolResult
	for i := len(s.transcript) - 1; i >= 0; i-- {
		m := s.transcript[i]
		if m.Role != llm.RoleTool {
			break
		}
		results = append([]llm.ToolResult{{ID: m.ToolCallID, Content: m.Content}}, results...)
	}
	if len(results) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	if err := persister.PersistToolResults(ctx, llm.PersistRequest{
		Model:   sim.CourtMagistrateModel,
		SceneID: s.sceneID,
		Results: results,
	}); err != nil {
		log.Printf("court: %s: persist tool results: %v", s.info.Case.ID, err)
	}
}
