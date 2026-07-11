package sim

import (
	"context"
	"log"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// recurring_visitor.go — the returning-traveler tier (LLM-372).
//
// A transient visitor (engine/sim/visitor.go) who actually deals with a player
// during a visit is PROMOTED to a durable RecurringVisitor: a stable persona
// (name / archetype / origin / disposition) plus per-PC familiarity plus a
// next_return_at. The engine brings that same persona back across the seasons and
// injects the continuity as engine-authored prose in the traveler's perception
// preface ("you've passed through Salem before; you know Sarah Hale here"). The
// returner stays on the shared, stateless salem-visitor VA — it is NOT promoted
// to a stateful zbbs-<name> agent with its own soul/dreams. All continuity lives
// engine-side in these structs (persisted in the recurring_visitor tables) and is
// rendered per call.
//
// Scope (Tier 1): returners + coarse per-pair familiarity (met-before + recency).
// The staged romance arc (acquaintance→…→committed) is a deferred follow-up — its
// hard part (what advances a stage) is unvalidated in play.
//
// Lifecycle:
//   - PROMOTION (earned) — handleVisitorReturnerMeet, an ActorMet subscriber:
//     when a visitor shares a scene (huddle) with a KindPC, the traveler is
//     promoted (a recurring_visitor row is created, VisitorState.RecurringID is
//     set) and the (returner, PC) familiarity is recorded/bumped. Recording at
//     meet-time durably captures the bond immediately; the promoted SET is the
//     same as "decided at departure" — the bar is "met a PC" either way.
//   - RETURN SCHEDULING — scheduleReturnerDeparture, from dispatchVisitorCleanup:
//     when a promoted traveler leaves, set next_return_at a configurable interval
//     out (wall-clock, matching the visitor ExpiresAt clock) and stamp last_seen.
//   - RETURN SPAWN — dispatchVisitorSpawn consults pickDueReturner (the in-memory
//     set loaded at boot) for a due returner before rolling a fresh stranger; no
//     per-tick DB read, no new timer (GUIDELINES: the durable store is loaded once
//     and mutated in memory, re-persisted each checkpoint).
//
// Persistence: durable, NON-swept (these rows outlive the visit) — the
// DiscoveredKind precedent, not the visitor/labor_contract generation-marker
// sweep. Loaded into World.RecurringVisitors at boot, cloned into the checkpoint
// snapshot, upserted each checkpoint by RecurringVisitorsRepo.SaveSnapshot.

// Return-interval defaults (wall-clock days). A promoted traveler is scheduled
// back a uniform-random number of days in [min, max] after departure — long
// enough that the absence reads as "across the seasons," the pacing a slow-burn
// arc needs. Overridable via WorldSettings.VisitorReturnMinDays / MaxDays
// (settings keys visitor_return_min_days / visitor_return_max_days) so a live run
// can tune the rhythm and a test can force a near-immediate return.
const (
	DefaultVisitorReturnMinDays = 14
	DefaultVisitorReturnMaxDays = 45
)

// RecurringVisitorID is the stable rvis-<8hex> identity that threads a returner's
// separate per-visit actor rows (each a fresh vstr-<8hex>) together.
type RecurringVisitorID string

// RecurringVisitor is the durable identity of a memorable returner. Persona slots
// are reused verbatim on every return; VisitCount / LastSeenAt / NextReturnAt
// evolve across visits. Acquaintances is the per-PC familiarity map.
type RecurringVisitor struct {
	ID          RecurringVisitorID
	Name        string // bare persona name ("Elias Drum"), sans the " the <archetype>" suffix
	Archetype   string
	Origin      string
	Disposition string
	VisitCount  int
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	// NextReturnAt is the wall-clock moment this returner is due back, or the zero
	// value while they are in-village (or not yet scheduled). Set at departure,
	// cleared when the return spawn consumes it.
	NextReturnAt time.Time
	Acquaintances map[ActorID]*RecurringAcquaintance
}

// RecurringAcquaintance is the bond a returner remembers toward one PC. Coarse by
// design (Tier 1): met-before + recency, not a staged arc. PCDisplayName is
// denormalized so perception renders without a join back to the actor aggregate.
type RecurringAcquaintance struct {
	PCActorID     ActorID
	PCDisplayName string
	FirstMetAt    time.Time
	LastMetAt     time.Time
}

// cloneRecurringVisitor deep-copies a RecurringVisitor (including its
// Acquaintances map + the pointed-to entries) so the checkpoint snapshot and the
// mem repo never alias live world state across a goroutine boundary.
func cloneRecurringVisitor(src *RecurringVisitor) *RecurringVisitor {
	if src == nil {
		return nil
	}
	cp := *src
	if src.Acquaintances != nil {
		cp.Acquaintances = make(map[ActorID]*RecurringAcquaintance, len(src.Acquaintances))
		for id, acq := range src.Acquaintances {
			if acq == nil {
				continue
			}
			a := *acq
			cp.Acquaintances[id] = &a
		}
	}
	return &cp
}

// newRecurringVisitorID mints a fresh rvis-<8hex> id. Prefix "rvis-" so the
// durable returner identity is visually distinct from a per-visit vstr- actor id
// in admin reads. crypto/rand via randomHex, same as newVisitorActorID.
func newRecurringVisitorID() RecurringVisitorID {
	return RecurringVisitorID("rvis-" + randomHex(8))
}

// personaNameFromDisplayName recovers the bare persona name from a visitor's
// composed DisplayName ("Elias Drum the peddler", or "... (1234)" when
// disambiguated) by cutting at the LAST " the " — dispatchVisitorSpawn appends the
// archetype suffix last, so trimming the final marker keeps a persona name that
// itself contains " the " intact. Mirrors perception.travelerPersonaName; kept
// engine-side so promotion can store the bare name without importing perception.
func personaNameFromDisplayName(displayName string) string {
	if i := strings.LastIndex(displayName, " the "); i >= 0 {
		return displayName[:i]
	}
	return displayName
}

// handleVisitorReturnerMeet is the ActorMet subscriber that promotes a visitor to
// a returner the first time they share a scene with a player, and records/bumps
// the per-PC familiarity on every such meeting. Runs on the world goroutine
// during emit (same as handleAcquaintance), so direct *World mutation is
// race-free. Non-ActorMet events, and meetings that aren't visitor↔PC, fall
// through untouched.
func handleVisitorReturnerMeet(w *World, evt Event) {
	met, ok := evt.(*ActorMet)
	if !ok {
		return
	}
	a, aok := w.Actors[met.A]
	b, bok := w.Actors[met.B]
	if !aok || !bok {
		return
	}
	visitor, pc := pairVisitorAndPC(a, b)
	if visitor == nil || pc == nil {
		return // not a traveler↔player meeting
	}
	rv := w.promoteVisitorIfNeeded(visitor, met.At)
	if rv == nil {
		return
	}
	recordReturnerAcquaintance(rv, pc, met.At)
}

// pairVisitorAndPC returns (visitor, pc) when exactly one of the pair is a
// transient traveler and the other is a player, else (nil, nil). A visitor↔NPC or
// visitor↔visitor meeting is not a promotion trigger — the returner arc is
// player-facing (Tier 1).
func pairVisitorAndPC(a, b *Actor) (visitor *Actor, pc *Actor) {
	switch {
	case a.VisitorState != nil && b.Kind == KindPC:
		return a, b
	case b.VisitorState != nil && a.Kind == KindPC:
		return b, a
	default:
		return nil, nil
	}
}

// promoteVisitorIfNeeded returns the RecurringVisitor backing this traveler,
// creating one (and linking it via VisitorState.RecurringID) on the first PC
// meeting. Returns the existing row for an already-promoted returner. Returns nil
// only on the defensive can't-happen path where RecurringID is set but its row is
// missing (a crash between meet and the checkpoint that would have persisted both
// — the link and the row write in the same Tx, so a consistent load never splits
// them; treat as un-promotable this tick rather than minting a duplicate).
func (w *World) promoteVisitorIfNeeded(visitor *Actor, at time.Time) *RecurringVisitor {
	vs := visitor.VisitorState
	if vs == nil {
		return nil
	}
	if vs.RecurringID != "" {
		rv := w.RecurringVisitors[RecurringVisitorID(vs.RecurringID)]
		if rv == nil {
			log.Printf("sim/recurring: visitor %s has RecurringID %q with no recurring_visitor row — skipping meet", visitor.ID, vs.RecurringID)
		}
		return rv
	}
	if w.RecurringVisitors == nil {
		w.RecurringVisitors = make(map[RecurringVisitorID]*RecurringVisitor)
	}
	id := newRecurringVisitorID()
	for _, exists := w.RecurringVisitors[id]; exists; _, exists = w.RecurringVisitors[id] {
		id = newRecurringVisitorID()
	}
	rv := &RecurringVisitor{
		ID:            id,
		Name:          personaNameFromDisplayName(visitor.DisplayName),
		Archetype:     vs.Archetype,
		Origin:        vs.Origin,
		Disposition:   vs.Disposition,
		VisitCount:    1,
		FirstSeenAt:   at,
		LastSeenAt:    at,
		Acquaintances: make(map[ActorID]*RecurringAcquaintance),
	}
	w.RecurringVisitors[id] = rv
	vs.RecurringID = string(id)
	log.Printf("sim/recurring: promoted visitor %s to returner %s (%s the %s from %s)",
		visitor.ID, id, rv.Name, rv.Archetype, rv.Origin)
	return rv
}

// recordReturnerAcquaintance inserts or refreshes the returner's familiarity with
// one PC: FirstMetAt is set once (first-met semantics, mirroring acquaintance.go);
// LastMetAt and the denormalized display name refresh on every meeting.
func recordReturnerAcquaintance(rv *RecurringVisitor, pc *Actor, at time.Time) {
	if rv.Acquaintances == nil {
		rv.Acquaintances = make(map[ActorID]*RecurringAcquaintance)
	}
	if acq, ok := rv.Acquaintances[pc.ID]; ok {
		acq.LastMetAt = at
		if pc.DisplayName != "" {
			acq.PCDisplayName = pc.DisplayName
		}
		return
	}
	rv.Acquaintances[pc.ID] = &RecurringAcquaintance{
		PCActorID:     pc.ID,
		PCDisplayName: pc.DisplayName,
		FirstMetAt:    at,
		LastMetAt:     at,
	}
}

// pickDueReturner returns the returner most overdue for a comeback — one whose
// NextReturnAt has passed and who is not currently in the village — or (nil,
// false) when none is due. Deterministic: earliest NextReturnAt wins, id
// tie-breaks. Called from dispatchVisitorSpawn on the world goroutine, so reading
// w.Actors / w.RecurringVisitors is race-free.
func (w *World) pickDueReturner(now time.Time) (*RecurringVisitor, bool) {
	if len(w.RecurringVisitors) == 0 {
		return nil, false
	}
	present := presentReturnerIDs(w)
	var best *RecurringVisitor
	for _, rv := range w.RecurringVisitors {
		if rv == nil || rv.NextReturnAt.IsZero() || now.Before(rv.NextReturnAt) {
			continue
		}
		if _, here := present[rv.ID]; here {
			continue
		}
		if best == nil || rv.NextReturnAt.Before(best.NextReturnAt) ||
			(rv.NextReturnAt.Equal(best.NextReturnAt) && rv.ID < best.ID) {
			best = rv
		}
	}
	return best, best != nil
}

// presentReturnerIDs is the set of RecurringVisitorIDs currently walking Salem
// (an in-flight visitor actor links to them via VisitorState.RecurringID), so a
// due returner already present is not spawned twice.
func presentReturnerIDs(w *World) map[RecurringVisitorID]struct{} {
	out := map[RecurringVisitorID]struct{}{}
	for _, a := range w.Actors {
		if a != nil && a.VisitorState != nil && a.VisitorState.RecurringID != "" {
			out[RecurringVisitorID(a.VisitorState.RecurringID)] = struct{}{}
		}
	}
	return out
}

// scheduleReturnerDeparture stamps a promoted traveler's departure onto its
// returner row: last_seen = now, and next_return_at a uniform-random interval out
// (wall-clock days in [min, max]). Called from dispatchVisitorCleanup when a
// visitor with a RecurringID is removed. No-op for an unpromoted visitor or a
// dangling id.
func (w *World) scheduleReturnerDeparture(rid RecurringVisitorID, now time.Time, r *rand.Rand, minDays, maxDays int) {
	rv := w.RecurringVisitors[rid]
	if rv == nil {
		return
	}
	if minDays <= 0 {
		minDays = DefaultVisitorReturnMinDays
	}
	if maxDays < minDays {
		maxDays = minDays
	}
	days := minDays
	if maxDays > minDays {
		days = minDays + r.Intn(maxDays-minDays+1)
	}
	rv.LastSeenAt = now
	rv.NextReturnAt = now.Add(time.Duration(days) * 24 * time.Hour)
	log.Printf("sim/recurring: returner %s (%s the %s) departed; due back in %dd (%s)",
		rv.ID, rv.Name, rv.Archetype, days, rv.NextReturnAt.Format(time.RFC3339))
}

// beginReturnerVisit marks a due returner as arriving for a fresh stay: bump the
// visit count and clear NextReturnAt so it is not re-picked while present.
// LastSeenAt is stamped at departure, not here.
func (rv *RecurringVisitor) beginReturnerVisit() {
	rv.VisitCount++
	rv.NextReturnAt = time.Time{}
}

// RecencyTier buckets how long ago a returner last saw a PC, computed engine-side
// so render maps a tier to phrase vocabulary rather than formatting a raw
// duration (scenes-not-stats; the felt-needs pattern generalized).
type RecencyTier int

const (
	RecencyRecent RecencyTier = iota // within a day — "just now" / "earlier"
	RecencyDays                      // days ago
	RecencyWeeks                     // a week or three ago
	RecencyMonths                    // over a month, under a season
	RecencyLong                      // a season or more
)

// recencyTierFor buckets an elapsed duration. Boundaries are deliberately coarse:
// the prose is fuzzy ("a few weeks back"), so exact days never surface.
func recencyTierFor(d time.Duration) RecencyTier {
	switch {
	case d < 24*time.Hour:
		return RecencyRecent
	case d < 14*24*time.Hour:
		return RecencyDays
	case d < 45*24*time.Hour:
		return RecencyWeeks
	case d < 120*24*time.Hour:
		return RecencyMonths
	default:
		return RecencyLong
	}
}

// ReturnerSnapshot is the render-ready projection of a returner's continuity,
// attached to ActorSnapshot at publish for a traveler who has visited before
// (VisitCount >= 2). Perception renders the self-preface continuity from it and
// cross-references KnownHere to recognize a co-present player. nil for a
// one-shot stranger or a first-visit (not-yet-returned) traveler.
type ReturnerSnapshot struct {
	VisitCount int
	KnownHere  []ReturnerKnownPC
}

// ReturnerKnownPC is one PC the returner remembers, most-recently-seen first.
type ReturnerKnownPC struct {
	PCActorID   ActorID
	DisplayName string
	Recency     RecencyTier
}

// buildReturnerSnapshot projects a traveler actor's durable returner identity into
// the render view, or nil when the actor is not a returner on a repeat visit. The
// VisitCount >= 2 gate is what keeps a freshly-promoted first-visit traveler from
// claiming "you've been here before" (and from a co-present PC "recognizing" a
// stranger they only just met this visit). Runs at publish on the world goroutine.
func buildReturnerSnapshot(w *World, a *Actor, now time.Time) *ReturnerSnapshot {
	if a == nil || a.VisitorState == nil || a.VisitorState.RecurringID == "" {
		return nil
	}
	rv := w.RecurringVisitors[RecurringVisitorID(a.VisitorState.RecurringID)]
	if rv == nil || rv.VisitCount < 2 {
		return nil
	}
	out := &ReturnerSnapshot{VisitCount: rv.VisitCount}
	for _, acq := range rv.Acquaintances {
		if acq == nil {
			continue
		}
		out.KnownHere = append(out.KnownHere, ReturnerKnownPC{
			PCActorID:   acq.PCActorID,
			DisplayName: acq.PCDisplayName,
			Recency:     recencyTierFor(now.Sub(acq.LastMetAt)),
		})
	}
	// Most-recently-seen first, id tie-break — a stable order so the preface names
	// the freshest bond first and the golden render is deterministic. Sort keys off
	// the recurring row's LastMetAt (not the tier) for a strict ordering.
	sort.Slice(out.KnownHere, func(i, j int) bool {
		li := rv.Acquaintances[out.KnownHere[i].PCActorID].LastMetAt
		lj := rv.Acquaintances[out.KnownHere[j].PCActorID].LastMetAt
		if !li.Equal(lj) {
			return li.After(lj)
		}
		return out.KnownHere[i].PCActorID < out.KnownHere[j].PCActorID
	})
	return out
}

// rehydrateRecurringVisitorsOnLoad loads the durable returner set into
// World.RecurringVisitors at boot (FinalizeLoad). MUST run AFTER
// rehydrateVisitorsOnLoad so w.Actors already holds the in-flight visitors whose
// recurring links this validates. A visitor pointing at a recurring_visitor row
// that isn't present (only reachable via an out-of-band edit — a consistent
// checkpoint writes the link + the row in one Tx) has its link cleared so it
// neither perceives itself as an un-backed returner nor re-promotes as a duplicate.
func (w *World) rehydrateRecurringVisitorsOnLoad(ctx context.Context) error {
	// A partially-wired repo (catalog-only loads, tests that hand-build a
	// sim.Repository without this tier) leaves RecurringVisitors nil — treat that
	// as "no returners" rather than panicking, matching the loader's nil-repo
	// tolerance for the reference catalogs.
	if w.repo.RecurringVisitors == nil {
		if w.RecurringVisitors == nil {
			w.RecurringVisitors = make(map[RecurringVisitorID]*RecurringVisitor)
		}
		return nil
	}
	recurring, err := w.repo.RecurringVisitors.LoadAll(ctx)
	if err != nil {
		return err
	}
	if recurring == nil {
		recurring = make(map[RecurringVisitorID]*RecurringVisitor)
	}
	w.RecurringVisitors = recurring
	for _, a := range w.Actors {
		if a == nil || a.VisitorState == nil || a.VisitorState.RecurringID == "" {
			continue
		}
		if w.RecurringVisitors[RecurringVisitorID(a.VisitorState.RecurringID)] == nil {
			log.Printf("sim: rehydrate recurring: visitor %s links to missing recurring_visitor %q — clearing link",
				a.ID, a.VisitorState.RecurringID)
			a.VisitorState.RecurringID = ""
		}
	}
	if len(w.RecurringVisitors) > 0 {
		log.Printf("sim: rehydrated %d recurring visitor(s)", len(w.RecurringVisitors))
	}
	return nil
}

// RegisterVisitorReturnerSubscriber wires the returner promotion/familiarity
// subscriber into the world (alongside RegisterAcquaintanceSubscriber). Must run
// on the world goroutine (before World.Run or from inside a Command.Fn).
func RegisterVisitorReturnerSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterVisitorReturnerSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleVisitorReturnerMeet))
}
