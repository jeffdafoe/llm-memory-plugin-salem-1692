package sim

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"
)

// story.go — LLM-892. Salem is told as an interactive story: a player works
// through story beats (missions) grouped in chapters. Each beat has a finish
// condition the engine watches for (the story watcher below, a bus subscriber),
// gives Standing when done, and offers the next beat. Standing climbs through
// named ranks (World.StandingRanks, from standing_rank — LLM-895), and falls
// when the engine takes some away (AdjustStanding); the rank follows it both
// ways, and each change of rank is announced once.
//
// The catalog (World.StoryMissions) is authored reference data loaded from
// story_mission at FinalizeLoad and never written; the ladder likewise, except
// that the operator can move a threshold live (SetStandingRankThreshold).
// Progress (World.StoryProgress) and actor.Standing / actor.StandingRankSeen
// are world state saved by the checkpoint, so a finish and its Standing land in
// the same snapshot transaction.
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

// StandingRankStranger is the bottom rank's id: every player starts there,
// and a ladder that lacks it gets it (NormalizeStandingRanks).
const StandingRankStranger = "stranger"

// StandingRank is one rung of the Standing ladder (a standing_rank row).
type StandingRank struct {
	ID   string
	Name string
	At   int
	// Text is the rank-up popup's line; empty for the bottom rank.
	Text string
	// DownText is the popup's line when a player falls below this rank ("The
	// town no longer counts you a Freeman."); empty for the bottom rank.
	DownText string
	// Prose is the hover line by how full the bar to the next rank is: the
	// first entry for the first third, and so on. The top rank has one line.
	Prose []string
}

// NormalizeStandingRanks orders the ladder by threshold and makes it usable
// whatever the table holds: a rank at 0 named Stranger is added when none is
// at 0 or below, the bottom rank is pinned to 0, and a threshold at or below
// the rank before it is lifted to one above it, so the ladder stays strictly
// ascending. Ties sort by id, never by map or row order. A rank that would be
// lifted past MaxStanding could never be reached, so it is left out and logged
// (StandingRanksFit tells an edit in advance).
func NormalizeStandingRanks(in []StandingRank) []StandingRank {
	ranks, dropped := normalizeStandingRanks(in)
	for _, id := range dropped {
		log.Printf("sim: story: rank %s does not fit below MaxStanding; left out of the ladder", id)
	}
	return ranks
}

// StandingRanksFit reports whether every rank in raw keeps a place in the
// normalized ladder: false when resolving ties would push one past
// MaxStanding. The threshold edit refuses such an edit before writing it.
func StandingRanksFit(raw []StandingRank) bool {
	_, dropped := normalizeStandingRanks(raw)
	return len(dropped) == 0
}

func normalizeStandingRanks(in []StandingRank) ([]StandingRank, []string) {
	ranks := make([]StandingRank, len(in))
	copy(ranks, in)
	sort.SliceStable(ranks, func(i, j int) bool {
		if ranks[i].At != ranks[j].At {
			return ranks[i].At < ranks[j].At
		}
		return ranks[i].ID < ranks[j].ID
	})
	if len(ranks) == 0 || ranks[0].At > 0 {
		ranks = append([]StandingRank{{ID: StandingRankStranger, Name: "Stranger"}}, ranks...)
	}
	ranks[0].At = 0
	out := ranks[:1]
	var dropped []string
	for _, r := range ranks[1:] {
		prev := out[len(out)-1].At
		if r.At <= prev {
			if prev >= MaxStanding {
				dropped = append(dropped, r.ID)
				continue
			}
			r.At = prev + 1
		}
		out = append(out, r)
	}
	return out, dropped
}

// standingRanks is the live ladder; a world that loaded none (a test world)
// has the bottom rank alone.
func (w *World) standingRanks() []StandingRank {
	if len(w.StandingRanks) == 0 {
		return NormalizeStandingRanks(nil)
	}
	return w.StandingRanks
}

// standingRankIndexByID is the index of the rank with id, or -1.
func standingRankIndexByID(ranks []StandingRank, id string) int {
	for i, r := range ranks {
		if r.ID == id {
			return i
		}
	}
	return -1
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
	// StoryUnseenRankDown: the player fell to a lower rank (LLM-895). Its key
	// names the rank they are at now, like a rank-up's.
	StoryUnseenRankDown = "rank_down"
	StoryUnseenOffer    = "offer"
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

// storyMissionLess orders beats by chapter, then seq, then id, so ties never
// fall to map order.
func storyMissionLess(a, b *StoryMission) bool {
	if a.Chapter != b.Chapter {
		return a.Chapter < b.Chapter
	}
	if a.Seq != b.Seq {
		return a.Seq < b.Seq
	}
	return a.ID < b.ID
}

// BuildStoryView derives a player's view from the catalog, their progress rows,
// Standing and rank-seen mark. Pure; World and Snapshot both feed it.
//
// rankSeen is the id of the last rank announced to the player. A current rank
// above it is a rank-up popup, below it a rank-down popup. An empty id reads as
// the bottom rank. An id the ladder no longer has reads as the current rank, so
// a removed rank never shows a popup by itself; the world rebases such a mark
// to a real rank at load and when the ladder is replaced (rebaseRankSeen).
func BuildStoryView(missions map[StoryMissionID]*StoryMission, rows map[StoryMissionID]*StoryProgress, standing int, rankSeen string, ranks []StandingRank) StoryView {
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

	// The open beat: the earliest offered, then by catalog order.
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
		return storyMissionLess(open[i].m, open[j].m)
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
			return storyMissionLess(list[i].m, list[j].m)
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
	seenIdx := 0
	if rankSeen != "" {
		seenIdx = standingRankIndexByID(ranks, rankSeen)
		if seenIdx < 0 {
			seenIdx = idx
		}
	}
	r := ranks[idx]
	switch {
	case idx > seenIdx:
		v.Unseen = append(v.Unseen, StoryUnseenItem{
			Key: StoryUnseenRank + ":" + r.ID, Kind: StoryUnseenRank,
			Title: r.Name, Text: r.Text, At: latestFinish,
		})
	case idx < seenIdx:
		// The line is the rank lost: a fall of two ranks at once reads as the
		// loss of the one the player last heard they held.
		v.Unseen = append(v.Unseen, StoryUnseenItem{
			Key: StoryUnseenRankDown + ":" + r.ID, Kind: StoryUnseenRankDown,
			Title: r.Name, Text: ranks[seenIdx].DownText, At: latestFinish,
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
	return BuildStoryView(w.StoryMissions, w.StoryProgress[actor.ID], actor.Standing, actor.StandingRankSeen, w.standingRanks())
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
	actor.Standing = addStanding(actor.Standing, m.StandingReward)
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
	sort.Slice(out, func(i, j int) bool { return storyMissionLess(out[i], out[j]) })
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
		// Every DwellStarted today is an item consumed (the three emitters in
		// item_commands.go and pay_with_item_commands.go set Kind); a dwell with
		// no item is not a meal.
		actor := storyPlayer(w, e.ActorID)
		if actor == nil || e.Kind == "" {
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
	case StoryUnseenRank, StoryUnseenRankDown:
		// Only the rank the player holds now moves the mark. A key for any
		// other (a popup closed after Standing moved again) is a no-op, and
		// the view shows the popup for where they stand now.
		ranks := w.standingRanks()
		current := ranks[standingRankIndex(ranks, actor.Standing)].ID
		if id != current || actor.StandingRankSeen == current {
			return false, nil
		}
		actor.StandingRankSeen = current
		return true, nil
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
	ranks, err := w.repo.Story.LoadRanks(ctx)
	if err != nil {
		return fmt.Errorf("load ranks: %w", err)
	}
	w.StandingRanks = NormalizeStandingRanks(ranks)
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
	for _, actor := range w.sortedStoryPlayers() {
		w.rebaseRankSeen(actor)
		w.startStory(actor, now)
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
	StandingRankSeen string
	View             StoryView
	Rows             []StoryProgress // by OfferedAt, then mission id
}

// StoryReadoutResult is StoryReadout's answer: the live ladder (LLM-895) and
// the players.
type StoryReadoutResult struct {
	Ranks   []StandingRank
	Players []StoryReadoutEntry
}

// StoryReadout returns the ladder and the story of one player (actorID set) or
// of every player with a login (actorID empty), by name.
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
			ranks := make([]StandingRank, len(w.standingRanks()))
			copy(ranks, w.standingRanks())
			return StoryReadoutResult{Ranks: ranks, Players: out}, nil
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
			a.StandingRankSeen = ""
			w.startStory(a, now)
			w.emitStoryChanged(a, now)
			return w.storyViewFor(a), nil
		},
	}
}

// ---------------------------------------------------------------------------
// Standing up and down (LLM-895)
// ---------------------------------------------------------------------------

// ErrStandingRankUnknown is a threshold edit on a rank the ladder lacks, or on
// the bottom rank, which is always at 0.
var ErrStandingRankUnknown = errors.New("no such rank above the bottom of the ladder")

// MaxStanding is the most Standing a player can hold: actor.standing is a
// Postgres integer. Every rise saturates here (addStanding), so no sum wraps.
const MaxStanding = math.MaxInt32

// addStanding is standing + delta held within [0, MaxStanding], checked before
// adding so a large delta can never wrap.
func addStanding(standing, delta int) int {
	switch {
	case delta > 0 && delta > MaxStanding-standing:
		return MaxStanding
	case delta < 0 && delta < -standing:
		return 0
	}
	return standing + delta
}

// adjustStanding moves a player's Standing by delta, within [0, MaxStanding],
// and emits StoryChanged when it moved. The rank follows (BuildStoryView
// announces the change). Returns the change applied. Every cause of a rise or
// fall outside a story beat goes through here; reason is for the log.
func (w *World) adjustStanding(actor *Actor, delta int, reason string, now time.Time) int {
	next := addStanding(actor.Standing, delta)
	applied := next - actor.Standing
	if applied == 0 {
		return 0
	}
	actor.Standing = next
	log.Printf("sim: story: %s Standing %+d to %d (%s)", actor.ID, applied, next, reason)
	w.emitStoryChanged(actor, now)
	return applied
}

// StandingAdjusted is AdjustStanding's answer: the change applied (smaller
// than asked when Standing would have gone below 0 or above MaxStanding) and
// the view after it.
type StandingAdjusted struct {
	Applied int
	View    StoryView
}

// AdjustStanding moves a player's Standing by delta (negative to lower it),
// within [0, MaxStanding]. For the operator today; the engine's own causes
// (reputation, a court ruling) call adjustStanding directly.
func AdjustStanding(actorID ActorID, delta int, reason string, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if w.Actors[actorID] == nil {
				return nil, ErrActorNotFound
			}
			a := storyPlayer(w, actorID)
			if a == nil {
				return nil, ErrStoryNotPlayer
			}
			applied := w.adjustStanding(a, delta, reason, now)
			return StandingAdjusted{Applied: applied, View: w.storyViewFor(a)}, nil
		},
	}
}

// rebaseRankSeen quietly sets a player's rank-seen mark to the rank they hold
// now when the mark names a rank the ladder no longer has, so later changes
// are announced against a rank that exists. Announces nothing. Runs after the
// ladder is loaded or replaced, never inside BuildStoryView (which stays pure).
func (w *World) rebaseRankSeen(actor *Actor) {
	ranks := w.standingRanks()
	if actor.StandingRankSeen == "" || standingRankIndexByID(ranks, actor.StandingRankSeen) >= 0 {
		return
	}
	current := ranks[standingRankIndex(ranks, actor.Standing)].ID
	log.Printf("sim: story: %s rank-seen %q is not in the ladder; set to %q", actor.ID, actor.StandingRankSeen, current)
	actor.StandingRankSeen = current
}

// sortedStoryPlayers is every player with a login, by id.
func (w *World) sortedStoryPlayers() []*Actor {
	ids := make([]ActorID, 0, len(w.Actors))
	for id := range w.Actors {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var out []*Actor
	for _, id := range ids {
		if a := storyPlayer(w, id); a != nil {
			out = append(out, a)
		}
	}
	return out
}

// ReplaceStandingRanks swaps in the ladder built from raw (standing_rank as it
// is now — the caller has just written a threshold and read the table back),
// normalized exactly as a load would, so the live ladder and the next boot's
// always agree. Rebases any rank-seen mark the new ladder lacks, and sends
// every player with a story their view again, since a rank may have changed
// under them. Returns the ladder.
func ReplaceStandingRanks(raw []StandingRank, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			w.StandingRanks = NormalizeStandingRanks(raw)
			for _, a := range w.sortedStoryPlayers() {
				w.rebaseRankSeen(a)
				if len(w.StoryProgress[a.ID]) > 0 {
					w.emitStoryChanged(a, now)
				}
			}
			out := make([]StandingRank, len(w.StandingRanks))
			copy(out, w.StandingRanks)
			return out, nil
		},
	}
}
