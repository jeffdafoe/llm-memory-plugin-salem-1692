package sim

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// story.go — LLM-892. Salem is told as an interactive story: a player works
// through story beats (missions) grouped in chapters. Each beat has a finish
// condition the engine watches for (the story watcher below, a bus subscriber),
// gives Standing when done, and offers the next beat. Standing climbs through
// named ranks (Stranger, Sojourner, Inhabitant, Freeman).
//
// The catalog (World.StoryMissions) is authored reference data loaded from
// story_mission at FinalizeLoad and never written. Progress
// (World.StoryProgress) and actor.Standing / actor.StandingRankSeen are world
// state saved by the checkpoint, so a finish and its Standing land in the same
// snapshot transaction.
//
// A beat can be finished two ways. Live, from the event that completes it (the
// player pitches the tent): the player sees a "finish" popup. Or by a state
// check, when the beat is offered or at load and the world already shows it done
// (the tent was standing before the beat was offered, or a crash lost the finish
// before the checkpoint): those beats are CaughtUp and the player sees them all
// in one "catch_up" popup. Event-only beats (a night's sleep, a meal, a word with
// a villager) have no state check.
//
// The popups the player has not closed yet are derived from durable state
// (BuildStoryView → Unseen), so a player who was offline sees them at the next
// login. Closing one is MarkStorySeen.

// StoryMissionID names one story beat ("arrival-1").
type StoryMissionID string

// StoryTriggerKind is what finishes a beat. A new kind is code here plus the
// story_mission CHECK list.
type StoryTriggerKind string

const (
	// StoryTriggerCampPitched: the player's tent stands and is their home.
	StoryTriggerCampPitched StoryTriggerKind = "camp_pitched"
	// StoryTriggerCampFireLit: the player's camp fire burns.
	StoryTriggerCampFireLit StoryTriggerKind = "camp_fire_lit"
	// StoryTriggerSleptAtHome: the player beds down inside their own home.
	StoryTriggerSleptAtHome StoryTriggerKind = "slept_at_home"
	// StoryTriggerMealAt: the player starts eating a meal at a place carrying
	// TriggerArgs.Tag.
	StoryTriggerMealAt StoryTriggerKind = "meal_at"
	// StoryTriggerSpokeWithVillager: the player speaks and an agent NPC hears it.
	StoryTriggerSpokeWithVillager StoryTriggerKind = "spoke_with_villager"
)

// StoryTriggerArgs are a beat's trigger parameters (story_mission.trigger_args).
type StoryTriggerArgs struct {
	// Tag (meal_at): the village_object tag the eating place must carry.
	Tag string `json:"tag,omitempty"`
}

// StoryMission is one authored story beat. Immutable once loaded.
type StoryMission struct {
	ID        StoryMissionID
	Chapter   string
	Seq       int
	Title     string
	OfferText string
	DoneText  string
	// AlreadyText is the clause the catch-up popup uses for this beat ("your
	// tent stands"). Empty falls back to the title.
	AlreadyText    string
	Trigger        StoryTriggerKind
	TriggerArgs    StoryTriggerArgs
	StandingReward int
	NextID         StoryMissionID
	// IsStart beats are offered to every player who has no row for them yet.
	IsStart bool
}

// StoryProgress is one player's record of one beat.
type StoryProgress struct {
	MissionID    StoryMissionID
	OfferedAt    time.Time
	FinishedAt   time.Time // zero while open
	OfferSeenAt  time.Time
	FinishSeenAt time.Time
	// CaughtUp marks a beat finished by a state check, not live; its finish is
	// shown in the combined catch-up popup.
	CaughtUp bool
}

// Open reports whether the beat is offered and not finished.
func (p *StoryProgress) Open() bool { return p != nil && p.FinishedAt.IsZero() }

// Clone returns a copy (the struct is flat).
func (p *StoryProgress) Clone() *StoryProgress {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}

// CloneStoryProgress deep-copies the per-player progress map.
func CloneStoryProgress(in map[ActorID]map[StoryMissionID]*StoryProgress) map[ActorID]map[StoryMissionID]*StoryProgress {
	if len(in) == 0 {
		return nil
	}
	out := make(map[ActorID]map[StoryMissionID]*StoryProgress, len(in))
	for actorID, rows := range in {
		copied := make(map[StoryMissionID]*StoryProgress, len(rows))
		for id, p := range rows {
			copied[id] = p.Clone()
		}
		out[actorID] = copied
	}
	return out
}

// ---------------------------------------------------------------------------
// Standing ranks
// ---------------------------------------------------------------------------

const (
	DefaultStandingRankSojourner  = 50
	DefaultStandingRankInhabitant = 200
	DefaultStandingRankFreeman    = 600
)

// StandingRank is one rung of the Standing ladder.
type StandingRank struct {
	ID   string
	Name string
	At   int
	// Text is the rank-up popup's line.
	Text string
	// Prose is the hover line by how full the bar to the next rank is: the
	// first entry for the first third, and so on. The top rank has one line.
	Prose []string
}

// standingRanks is the ladder with the live thresholds. A threshold set at or
// below the rank before it is lifted to one above it, so the ladder stays
// strictly ascending whatever the settings say.
func (s *WorldSettings) standingRanks() []StandingRank {
	ranks := []StandingRank{
		{ID: "stranger", Name: "Stranger", At: 0, Prose: []string{
			"No one here knows your name.",
			"A few villagers have started to know your face.",
			"People nod to you on the road now.",
		}},
		{ID: "sojourner", Name: "Sojourner", At: settingOr(s.StandingRankSojourner, DefaultStandingRankSojourner),
			Text: "The town has marked you a Sojourner.", Prose: []string{
				"The village tolerates you, for now.",
				"Folk have started to ask after you.",
				"You are spoken of as one who may stay.",
			}},
		{ID: "inhabitant", Name: "Inhabitant", At: settingOr(s.StandingRankInhabitant, DefaultStandingRankInhabitant),
			Text: "The town has admitted you as an Inhabitant.", Prose: []string{
				"You are counted among the town's people.",
				"Your word carries some weight at the well.",
				"Men of standing greet you by name.",
			}},
		{ID: "freeman", Name: "Freeman", At: settingOr(s.StandingRankFreeman, DefaultStandingRankFreeman),
			Text: "The town has made you a Freeman, with a voice at town meeting.", Prose: []string{
				"You have a voice at town meeting.",
			}},
	}
	for i := 1; i < len(ranks); i++ {
		if ranks[i].At <= ranks[i-1].At {
			ranks[i].At = ranks[i-1].At + 1
		}
	}
	return ranks
}

func settingOr(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// standingRankIndex is the index of the highest rank whose threshold standing
// has reached.
func standingRankIndex(ranks []StandingRank, standing int) int {
	idx := 0
	for i, r := range ranks {
		if standing >= r.At {
			idx = i
		}
	}
	return idx
}

// standingProse picks the rank's hover line by how full the bar is.
func standingProse(ranks []StandingRank, idx, standing int) string {
	r := ranks[idx]
	if len(r.Prose) == 0 {
		return ""
	}
	if idx+1 >= len(ranks) || len(r.Prose) == 1 {
		return r.Prose[len(r.Prose)-1]
	}
	span := ranks[idx+1].At - r.At
	band := (standing - r.At) * len(r.Prose) / span
	if band < 0 {
		band = 0
	}
	if band >= len(r.Prose) {
		band = len(r.Prose) - 1
	}
	return r.Prose[band]
}

// ---------------------------------------------------------------------------
// The view (what /pc/me and the story_changed frame carry)
// ---------------------------------------------------------------------------

// Story popup kinds, in the order Unseen lists them.
const (
	StoryUnseenCatchUp = "catch_up"
	StoryUnseenFinish  = "finish"
	StoryUnseenRank    = "rank"
	StoryUnseenOffer   = "offer"
)

// StoryView is one player's story as the client sees it.
type StoryView struct {
	Standing int
	Rank     StandingRank
	NextRank *StandingRank // nil at the top rank
	Prose    string
	Mission  *StoryMissionView // the open beat, nil when none
	Unseen   []StoryUnseenItem
}

// StoryMissionView is the open beat.
type StoryMissionView struct {
	ID        StoryMissionID
	Chapter   string
	Title     string
	Text      string
	OfferedAt time.Time
}

// StoryUnseenItem is one popup the player has not closed yet. Key is what
// MarkStorySeen takes.
type StoryUnseenItem struct {
	Key          string
	Kind         string
	Chapter      string
	Title        string
	Titles       []string // catch_up only
	Text         string
	StandingGain int
	At           time.Time
}

// BuildStoryView derives a player's view from the catalog, their progress rows,
// Standing and rank-seen mark. Pure; World and Snapshot both feed it.
func BuildStoryView(missions map[StoryMissionID]*StoryMission, rows map[StoryMissionID]*StoryProgress, standing, rankSeen int, ranks []StandingRank) StoryView {
	idx := standingRankIndex(ranks, standing)
	v := StoryView{
		Standing: standing,
		Rank:     ranks[idx],
		Prose:    standingProse(ranks, idx, standing),
	}
	if idx+1 < len(ranks) {
		next := ranks[idx+1]
		v.NextRank = &next
	}

	type entry struct {
		m *StoryMission
		p *StoryProgress
	}
	var entries []entry
	for id, p := range rows {
		m := missions[id]
		if m == nil || p == nil {
			continue
		}
		entries = append(entries, entry{m, p})
	}

	// The open beat: the earliest offered, then by chapter order.
	var open []entry
	for _, e := range entries {
		if e.p.Open() {
			open = append(open, e)
		}
	}
	sort.Slice(open, func(i, j int) bool {
		if !open[i].p.OfferedAt.Equal(open[j].p.OfferedAt) {
			return open[i].p.OfferedAt.Before(open[j].p.OfferedAt)
		}
		return open[i].m.Seq < open[j].m.Seq
	})
	if len(open) > 0 {
		m, p := open[0].m, open[0].p
		v.Mission = &StoryMissionView{ID: m.ID, Chapter: m.Chapter, Title: m.Title, Text: m.OfferText, OfferedAt: p.OfferedAt}
	}

	var caught, finished []entry
	for _, e := range entries {
		if e.p.Open() || !e.p.FinishSeenAt.IsZero() {
			continue
		}
		if e.p.CaughtUp {
			caught = append(caught, e)
		} else {
			finished = append(finished, e)
		}
	}
	byFinish := func(list []entry) {
		sort.Slice(list, func(i, j int) bool {
			if !list[i].p.FinishedAt.Equal(list[j].p.FinishedAt) {
				return list[i].p.FinishedAt.Before(list[j].p.FinishedAt)
			}
			return list[i].m.Seq < list[j].m.Seq
		})
	}
	byFinish(caught)
	byFinish(finished)

	var latestFinish time.Time
	if len(caught) > 0 {
		item := StoryUnseenItem{Key: StoryUnseenCatchUp, Kind: StoryUnseenCatchUp, Chapter: caught[0].m.Chapter}
		var clauses []string
		for _, e := range caught {
			item.Titles = append(item.Titles, e.m.Title)
			item.StandingGain += e.m.StandingReward
			clause := e.m.AlreadyText
			if clause == "" {
				clause = strings.ToLower(e.m.Title)
			}
			clauses = append(clauses, clause)
			if e.p.FinishedAt.After(item.At) {
				item.At = e.p.FinishedAt
			}
		}
		item.Text = "You have already made a start here: " + joinClauses(clauses) + "."
		v.Unseen = append(v.Unseen, item)
		latestFinish = item.At
	}
	for _, e := range finished {
		v.Unseen = append(v.Unseen, StoryUnseenItem{
			Key: StoryUnseenFinish + ":" + string(e.m.ID), Kind: StoryUnseenFinish,
			Chapter: e.m.Chapter, Title: e.m.Title, Text: e.m.DoneText,
			StandingGain: e.m.StandingReward, At: e.p.FinishedAt,
		})
		if e.p.FinishedAt.After(latestFinish) {
			latestFinish = e.p.FinishedAt
		}
	}
	if idx > 0 && idx > rankSeen {
		r := ranks[idx]
		v.Unseen = append(v.Unseen, StoryUnseenItem{
			Key: StoryUnseenRank + ":" + r.ID, Kind: StoryUnseenRank,
			Title: r.Name, Text: r.Text, At: latestFinish,
		})
	}
	for _, e := range open {
		if !e.p.OfferSeenAt.IsZero() {
			continue
		}
		v.Unseen = append(v.Unseen, StoryUnseenItem{
			Key: StoryUnseenOffer + ":" + string(e.m.ID), Kind: StoryUnseenOffer,
			Chapter: e.m.Chapter, Title: e.m.Title, Text: e.m.OfferText, At: e.p.OfferedAt,
		})
	}
	return v
}

// joinClauses writes "a", "a and b", "a, b and c".
func joinClauses(c []string) string {
	switch len(c) {
	case 0:
		return ""
	case 1:
		return c[0]
	}
	return strings.Join(c[:len(c)-1], ", ") + " and " + c[len(c)-1]
}

// storyViewFor builds a live actor's view off the world.
func (w *World) storyViewFor(actor *Actor) StoryView {
	return BuildStoryView(w.StoryMissions, w.StoryProgress[actor.ID], actor.Standing, actor.StandingRankSeen, w.Settings.standingRanks())
}

// StoryChanged carries a player's whole story view after anything in it
// changed: a beat offered or finished, Standing moved, a popup closed. Login is
// stamped at emit time so the translator sends it to that player's sockets only
// (like ClientTrigger) without reading world state.
type StoryChanged struct {
	EventBase
	ActorID ActorID
	Login   string
	View    StoryView
	At      time.Time
}

func (StoryChanged) isSimEvent() {}

func (w *World) emitStoryChanged(actor *Actor, now time.Time) {
	w.emit(&StoryChanged{ActorID: actor.ID, Login: actor.LoginUsername, View: w.storyViewFor(actor), At: now})
}

// ---------------------------------------------------------------------------
// Offer, finish, catch up
// ---------------------------------------------------------------------------

// storyPlayer returns the actor when it is a player with a login.
func storyPlayer(w *World, id ActorID) *Actor {
	a := w.Actors[id]
	if a == nil || a.Kind != KindPC || a.LoginUsername == "" {
		return nil
	}
	return a
}

// storyStateHolds reports whether the world already shows m done for actor.
// False for the event-only kinds.
func storyStateHolds(w *World, actor *Actor, m *StoryMission, now time.Time) bool {
	switch m.Trigger {
	case StoryTriggerCampPitched:
		return storyTentIsHome(w, actor)
	case StoryTriggerCampFireLit:
		camp := FindCamp(w.VillageObjects, actor.ID)
		return camp != nil && CampFireLit(camp.Fire, now)
	}
	return false
}

// storyTentIsHome: the actor's home is their own standing tent.
func storyTentIsHome(w *World, actor *Actor) bool {
	if actor.HomeStructureID == "" {
		return false
	}
	tent := w.VillageObjects[VillageObjectID(actor.HomeStructureID)]
	return tent != nil && tent.AssetID == TentAssetID && tent.OwnerActorID == actor.ID && tent.CurrentState != TentStatePacked
}

// offerStoryMission records the offer and, when the world already shows the
// beat done, finishes it as caught up (which offers the next beat in turn).
// Reports whether anything changed.
func (w *World) offerStoryMission(actor *Actor, m *StoryMission, now time.Time) bool {
	rows := w.StoryProgress[actor.ID]
	if rows == nil {
		rows = make(map[StoryMissionID]*StoryProgress)
		w.StoryProgress[actor.ID] = rows
	}
	if rows[m.ID] != nil {
		return false
	}
	p := &StoryProgress{MissionID: m.ID, OfferedAt: now}
	rows[m.ID] = p
	if storyStateHolds(w, actor, m, now) {
		w.finishStoryMission(actor, m, p, now, true)
	}
	return true
}

// finishStoryMission stamps the finish, adds the Standing and offers the next
// beat.
func (w *World) finishStoryMission(actor *Actor, m *StoryMission, p *StoryProgress, now time.Time, caughtUp bool) {
	p.FinishedAt = now
	p.CaughtUp = caughtUp
	actor.Standing += m.StandingReward
	if m.NextID == "" {
		return
	}
	next := w.StoryMissions[m.NextID]
	if next == nil {
		log.Printf("sim: story: mission %s names unknown next mission %s", m.ID, m.NextID)
		return
	}
	w.offerStoryMission(actor, next, now)
}

// startStory offers every start beat the player has no row for, offers the
// next beat of a finished one that has no row (a chapter appended to the
// catalog after the player finished the last beat), and finishes as caught up
// any open beat the world already shows done (a finish a crash lost before the
// checkpoint). Reports whether anything changed.
func (w *World) startStory(actor *Actor, now time.Time) bool {
	changed := false
	for _, m := range w.storyStartMissions() {
		if w.offerStoryMission(actor, m, now) {
			changed = true
		}
	}
	for _, id := range sortedStoryIDs(w.StoryProgress[actor.ID]) {
		p := w.StoryProgress[actor.ID][id]
		m := w.StoryMissions[id]
		if m == nil {
			continue
		}
		if p.Open() {
			if storyStateHolds(w, actor, m, now) {
				w.finishStoryMission(actor, m, p, now, true)
				changed = true
			}
			continue
		}
		if next := w.StoryMissions[m.NextID]; next != nil && w.offerStoryMission(actor, next, now) {
			changed = true
		}
	}
	return changed
}

// storyStartMissions lists the start beats in chapter order.
func (w *World) storyStartMissions() []*StoryMission {
	var out []*StoryMission
	for _, m := range w.StoryMissions {
		if m.IsStart {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Chapter != out[j].Chapter {
			return out[i].Chapter < out[j].Chapter
		}
		return out[i].Seq < out[j].Seq
	})
	return out
}

func sortedStoryIDs(rows map[StoryMissionID]*StoryProgress) []StoryMissionID {
	ids := make([]StoryMissionID, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// StartStoryForPC offers a newly created player the start beats. CreatePC
// calls it; the load path calls startStory for every player instead.
func (w *World) StartStoryForPC(actor *Actor, now time.Time) {
	if actor == nil || actor.Kind != KindPC || actor.LoginUsername == "" {
		return
	}
	if w.startStory(actor, now) {
		w.emitStoryChanged(actor, now)
	}
}

// ---------------------------------------------------------------------------
// The watcher
// ---------------------------------------------------------------------------

// RegisterStoryWatcher subscribes the story watcher. Wired in cmd/engine.
func RegisterStoryWatcher(w *World) {
	w.Subscribe(SubscriberFunc(handleStoryEvent))
}

// handleStoryEvent finishes the open beat an event completes. It runs on the
// world goroutine inside the emitting command; finishing a beat may offer the
// next and emits StoryChanged, which this handler ignores.
func handleStoryEvent(w *World, evt Event) {
	if len(w.StoryMissions) == 0 {
		return
	}
	switch e := evt.(type) {
	case *NPCHomeStructureChanged:
		if actor := storyPlayer(w, e.ActorID); actor != nil && storyTentIsHome(w, actor) {
			w.finishStoryBeatsByTrigger(actor, StoryTriggerCampPitched, nil, e.At)
		}
	case *VillageObjectStateChanged:
		obj := w.VillageObjects[e.ObjectID]
		if obj == nil || obj.AssetID != CampFireAssetID || e.ToState != CampFireStateBurning {
			return
		}
		if actor := storyPlayer(w, obj.OwnerActorID); actor != nil {
			w.finishStoryBeatsByTrigger(actor, StoryTriggerCampFireLit, nil, e.At)
		}
	case *PCSleepStarted:
		actor := storyPlayer(w, e.ActorID)
		if actor == nil || actor.HomeStructureID == "" || actor.InsideStructureID != actor.HomeStructureID {
			return
		}
		w.finishStoryBeatsByTrigger(actor, StoryTriggerSleptAtHome, nil, e.At)
	case *DwellStarted:
		actor := storyPlayer(w, e.ActorID)
		if actor == nil {
			return
		}
		place := w.VillageObjects[e.StructureID]
		if place == nil {
			return
		}
		w.finishStoryBeatsByTrigger(actor, StoryTriggerMealAt, func(m *StoryMission) bool {
			return m.TriggerArgs.Tag != "" && place.HasTag(m.TriggerArgs.Tag)
		}, e.At)
	case *Spoke:
		actor := storyPlayer(w, e.SpeakerID)
		if actor == nil {
			return
		}
		for _, id := range e.RecipientIDs {
			if a := w.Actors[id]; a != nil && isAgentNPC(a) {
				w.finishStoryBeatsByTrigger(actor, StoryTriggerSpokeWithVillager, nil, e.At)
				return
			}
		}
	}
}

// finishStoryBeatsByTrigger finishes every open beat of the given kind that
// match accepts (nil accepts all), then emits one StoryChanged.
func (w *World) finishStoryBeatsByTrigger(actor *Actor, kind StoryTriggerKind, match func(*StoryMission) bool, now time.Time) {
	rows := w.StoryProgress[actor.ID]
	changed := false
	for _, id := range sortedStoryIDs(rows) {
		p := rows[id]
		m := w.StoryMissions[id]
		if m == nil || !p.Open() || m.Trigger != kind {
			continue
		}
		if match != nil && !match(m) {
			continue
		}
		w.finishStoryMission(actor, m, p, now, false)
		changed = true
	}
	if changed {
		w.emitStoryChanged(actor, now)
	}
}

// ---------------------------------------------------------------------------
// Closing a popup
// ---------------------------------------------------------------------------

// ErrStoryBadKey is a seen key that is empty or not of a known form.
var ErrStoryBadKey = errors.New("unknown story popup key")

// MarkStorySeen records that the player closed the popup with this key
// (StoryUnseenItem.Key). A key already seen, or no longer unseen, is a no-op.
// Returns the player's StoryView.
func MarkStorySeen(actorID ActorID, key string, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor := storyPlayer(w, actorID)
			if actor == nil {
				return nil, fmt.Errorf("MarkStorySeen: %q is not a player", actorID)
			}
			changed, err := w.markStorySeen(actor, strings.TrimSpace(key), now)
			if err != nil {
				return nil, err
			}
			if changed {
				w.emitStoryChanged(actor, now)
			}
			return w.storyViewFor(actor), nil
		},
	}
}

func (w *World) markStorySeen(actor *Actor, key string, now time.Time) (bool, error) {
	rows := w.StoryProgress[actor.ID]
	if key == StoryUnseenCatchUp {
		changed := false
		for _, p := range rows {
			if p.CaughtUp && !p.Open() && p.FinishSeenAt.IsZero() {
				p.FinishSeenAt = now
				changed = true
			}
		}
		return changed, nil
	}
	kind, id, ok := strings.Cut(key, ":")
	if !ok || id == "" {
		return false, fmt.Errorf("%w: %q", ErrStoryBadKey, key)
	}
	switch kind {
	case StoryUnseenOffer, StoryUnseenFinish:
		p := rows[StoryMissionID(id)]
		if p == nil {
			return false, nil
		}
		if kind == StoryUnseenOffer {
			if p.OfferSeenAt.IsZero() {
				p.OfferSeenAt = now
				return true, nil
			}
			return false, nil
		}
		if !p.Open() && p.FinishSeenAt.IsZero() {
			p.FinishSeenAt = now
			return true, nil
		}
		return false, nil
	case StoryUnseenRank:
		ranks := w.Settings.standingRanks()
		reached := standingRankIndex(ranks, actor.Standing)
		for i, r := range ranks {
			if r.ID == id {
				// A rank not reached yet must not move the mark, or its popup
				// would never show.
				if i > actor.StandingRankSeen && i <= reached {
					actor.StandingRankSeen = i
					return true, nil
				}
				return false, nil
			}
		}
		return false, nil
	}
	return false, fmt.Errorf("%w: %q", ErrStoryBadKey, key)
}

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

// rehydrateStoryOnLoad loads the catalog and the progress rows, drops rows for
// a departed actor or an unknown mission, and starts the story for every
// player (offering the start beats and catching up what the world shows done).
// Quiet: no StoryChanged at load — a client reads its story off /pc/me on
// connect. A missing repo (a test Repository without the tier) leaves no story.
func (w *World) rehydrateStoryOnLoad(ctx context.Context, now time.Time) error {
	if w.StoryProgress == nil {
		w.StoryProgress = make(map[ActorID]map[StoryMissionID]*StoryProgress)
	}
	if w.repo.Story == nil {
		return nil
	}
	missions, err := w.repo.Story.LoadMissions(ctx)
	if err != nil {
		return fmt.Errorf("load missions: %w", err)
	}
	if missions == nil {
		missions = make(map[StoryMissionID]*StoryMission)
	}
	w.StoryMissions = missions
	progress, err := w.repo.Story.LoadProgress(ctx)
	if err != nil {
		return fmt.Errorf("load progress: %w", err)
	}
	for actorID, rows := range progress {
		if storyPlayer(w, actorID) == nil {
			continue
		}
		kept := make(map[StoryMissionID]*StoryProgress, len(rows))
		for id, p := range rows {
			if missions[id] == nil {
				log.Printf("sim: story: dropping %s progress on unknown mission %s", actorID, id)
				continue
			}
			kept[id] = p
		}
		w.StoryProgress[actorID] = kept
	}
	ids := make([]ActorID, 0, len(w.Actors))
	for id := range w.Actors {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if actor := storyPlayer(w, id); actor != nil {
			w.startStory(actor, now)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Operator (umbilical)
// ---------------------------------------------------------------------------

// ErrStoryNotPlayer is an operator story call on an actor that is not a
// player with a login.
var ErrStoryNotPlayer = errors.New("actor is not a player with a login")

// StoryReadoutEntry is one player's story for the operator: the view the
// client gets plus the raw rows.
type StoryReadoutEntry struct {
	ActorID          ActorID
	Name             string
	Login            string
	StandingRankSeen int
	View             StoryView
	Rows             []StoryProgress // by OfferedAt, then mission id
}

// StoryReadout returns the story of one player (actorID set) or of every
// player with a login (actorID empty), by name.
func StoryReadout(actorID ActorID) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			var actors []*Actor
			if actorID != "" {
				if w.Actors[actorID] == nil {
					return nil, ErrActorNotFound
				}
				a := storyPlayer(w, actorID)
				if a == nil {
					return nil, ErrStoryNotPlayer
				}
				actors = append(actors, a)
			} else {
				for id := range w.Actors {
					if a := storyPlayer(w, id); a != nil {
						actors = append(actors, a)
					}
				}
				sort.Slice(actors, func(i, j int) bool { return actors[i].DisplayName < actors[j].DisplayName })
			}
			out := make([]StoryReadoutEntry, 0, len(actors))
			for _, a := range actors {
				e := StoryReadoutEntry{ActorID: a.ID, Name: a.DisplayName, Login: a.LoginUsername,
					StandingRankSeen: a.StandingRankSeen, View: w.storyViewFor(a)}
				for _, p := range w.StoryProgress[a.ID] {
					e.Rows = append(e.Rows, *p)
				}
				sort.Slice(e.Rows, func(i, j int) bool {
					if !e.Rows[i].OfferedAt.Equal(e.Rows[j].OfferedAt) {
						return e.Rows[i].OfferedAt.Before(e.Rows[j].OfferedAt)
					}
					return e.Rows[i].MissionID < e.Rows[j].MissionID
				})
				out = append(out, e)
			}
			return out, nil
		},
	}
}

// ResetStory wipes a player's story (rows, Standing, rank-seen mark) and starts
// it again: the start beats are offered, and what the world already shows done
// is caught up. For testing. The next checkpoint sweeps the old rows.
func ResetStory(actorID ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if w.Actors[actorID] == nil {
				return nil, ErrActorNotFound
			}
			a := storyPlayer(w, actorID)
			if a == nil {
				return nil, ErrStoryNotPlayer
			}
			delete(w.StoryProgress, a.ID)
			a.Standing = 0
			a.StandingRankSeen = 0
			w.startStory(a, now)
			w.emitStoryChanged(a, now)
			return w.storyViewFor(a), nil
		},
	}
}
