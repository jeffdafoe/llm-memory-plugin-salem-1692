package sim

import (
	"sort"
	"strings"
	"time"
)

// public_works_notices.go — LLM-654: the town's work on the notice boards.
//
// A damaged site — a broken well, or a damaged business (LLM-675) — puts two
// PINNED lines at the top of every notice board: what is broken, and what the
// town pays to mend it (or, when the chest cannot pay, where to draw water
// meanwhile / that the town cannot pay). Two lines, not one, because the live
// board has no 1-slip art: a lone line would snap down to the empty board.
//
// The lines are DERIVED from the damage state, never authored, so they come
// back after a restart even though board content is in-memory only. Two paths
// place them:
//
//   - At once, when a well breaks or is mended (damageObject / repairObject call
//     repostPublicWorksNotices): each board keeps its crier-authored lines after
//     the new pinned ones, sized to a frame the board has.
//   - At the crier's stop (cascade/noticeboard.go): she authors only the slips
//     the pinned lines leave free, posts pinned + authored, and reads them all
//     aloud — so the bounty is also cried in the square.
//
// NoticeboardContent.Pinned records how many leading lines are pinned, so a
// repost can tell the crier's lines from the town's.

// PublicWorksNoticeLines returns the pinned lines for every damaged site — a
// broken well, a damaged business (LLM-675) or a road obstacle (LLM-677) —
// lowest id first. Empty when nothing is damaged.
func PublicWorksNoticeLines(w *World) []string {
	broken := damagedSites(w)
	var out []string
	for _, obj := range broken {
		kind := PublicWorksKind(obj)
		fact := DamageFact(w.VillageObjects, w.Structures, w.Assets, obj)
		switch kind {
		case PublicWorksBusiness:
			out = append(out, fact+" — it can take in no new stock until it is mended.")
		case PublicWorksRoad:
			out = append(out, fact+" — walkers must go around it until it is cleared.")
		default:
			out = append(out, fact+" — no water can be drawn there until it is mended.")
		}
		bounty, _ := w.Settings.publicWorksTerms(kind)
		switch {
		case PublicWorksBountyOpen(w.Environment.TownChest, bounty, w.Settings.PublicWorksChestReserve):
			out = append(out, "The town pays "+coinsPhrase(bounty)+" to the hand who "+PublicWorksMendVerb(kind)+" it.")
		case kind == PublicWorksBusiness, kind == PublicWorksRoad:
			out = append(out, "The town cannot pay for "+PublicWorksMendNoun(kind)+" just now.")
		default:
			out = append(out, "Until it is mended, draw your water at the other well.")
		}
	}
	return out
}

// damagedSites lists every damaged well and business, lowest id first.
func damagedSites(w *World) []*VillageObject {
	var out []*VillageObject
	for _, obj := range w.VillageObjects {
		if IsDamagedSite(obj) {
			out = append(out, obj)
		}
	}
	sortObjectsByID(out)
	return out
}

// IsNoticeBoard reports whether obj currently shows a notice-board state.
func IsNoticeBoard(w *World, obj *VillageObject) bool {
	if obj == nil {
		return false
	}
	asset := w.Assets[obj.AssetID]
	if asset == nil {
		return false
	}
	st := asset.FindState(obj.CurrentState)
	return st != nil && st.HasTag(TagNoticeBoard)
}

// NoticeboardStateForCapacity returns the notice-board state best suited to show
// `want` notices, plus that state's declared capacity: an exact frame if the
// sheet has one, else the largest capacity <= want (the live board has frames
// for 0, 2, 3, 4 and 5 slips — no 1). ("", 0) when the asset has no
// notice-board state at all. Shared by the crier and the public-works repost.
func NoticeboardStateForCapacity(w *World, objectID VillageObjectID, want int) (string, int) {
	obj, ok := w.VillageObjects[objectID]
	if !ok || obj == nil {
		return "", 0
	}
	asset, ok := w.Assets[obj.AssetID]
	if !ok || asset == nil {
		return "", 0
	}
	bestState, bestCap := "", -1
	for _, s := range asset.RotatablePool() {
		if !s.HasTag(TagNoticeBoard) {
			continue
		}
		if c := ContentCapacityForState(s); c <= want && c > bestCap {
			bestState, bestCap = s.State, c
		}
	}
	if bestCap < 0 {
		return "", 0
	}
	return bestState, bestCap
}

// authoredNoticeLines returns a board's stored lines after its pinned ones —
// the crier's part.
func authoredNoticeLines(c *NoticeboardContent) []string {
	if c == nil {
		return nil
	}
	lines := splitNonEmptyLines(c.Text)
	if c.Pinned >= len(lines) {
		return nil
	}
	return lines[c.Pinned:]
}

func splitNonEmptyLines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// PostNoticeboardWithPinned sets board objectID to the frame for pinned +
// authored lines and stores them, pinned first. When the frame holds fewer
// slips than lines, authored lines are dropped before pinned ones. An empty
// result posts the empty board and clears the content. Returns the lines
// actually posted. MUST run on the world goroutine.
func PostNoticeboardWithPinned(w *World, objectID VillageObjectID, pinned, authored []string, at time.Time) []string {
	all := append(append([]string{}, pinned...), authored...)
	state, capacity := NoticeboardStateForCapacity(w, objectID, len(all))
	if state == "" {
		return nil
	}
	if capacity < len(all) {
		all = all[:capacity]
	}
	for i := range all {
		if runes := []rune(all[i]); len(runes) > MaxNoticeboardLineLen {
			all[i] = string(runes[:MaxNoticeboardLineLen])
		}
	}
	SetVillageObjectState(objectID, state).Fn(w)
	if len(all) == 0 {
		ClearNoticeboardContent(objectID, state, at).Fn(w)
		return nil
	}
	res, err := SaveNoticeboardContent(objectID, strings.Join(all, "\n"), state, at).Fn(w)
	if r, ok := res.(SaveNoticeboardContentResult); err != nil || !ok || !r.Applied {
		return nil
	}
	n := len(pinned)
	if n > len(all) {
		n = len(all)
	}
	w.NoticeboardContent[objectID].Pinned = n
	return all
}

// DamageNewsChanged is emitted when the town's broken-things news changes — a
// well broke or was mended, or the chest crossed the line where it can (or can
// no longer) pay the bounty, or the bounty setting moved. The client refreshes
// its ticker off it; the boards have already been reposted.
type DamageNewsChanged struct {
	EventBase
	At time.Time
}

func (DamageNewsChanged) isSimEvent() {}

// syncPublicWorksNews reposts the boards and emits DamageNewsChanged when the
// pinned lines differ from what was last posted. The lines are derived from
// the damage state, the chest and the settings, so every writer of any of those
// calls this: damageObject, repairObject, the estate-rate collection, the
// constable's wage, the settings route, and FinalizeLoad. A no-op while nothing
// changed, so the frequent chest writers cost one string compare.
func syncPublicWorksNews(w *World, at time.Time) {
	pinned := PublicWorksNoticeLines(w)
	// The key carries the damaged sites' ids as well as the text, so a break or
	// repair always counts as news even if two sites ever rendered alike.
	var ids []string
	for _, obj := range damagedSites(w) {
		ids = append(ids, string(obj.ID))
	}
	// Nothing broken is the empty key — the boot value — so a village with
	// nothing damaged never reposts (and never re-frames) its boards.
	key := ""
	if len(ids) > 0 || len(pinned) > 0 {
		key = strings.Join(ids, ",") + "\n" + strings.Join(pinned, "\n")
	}
	if key == w.publicWorksNewsKey {
		return
	}
	w.publicWorksNewsKey = key
	repostPublicWorksNotices(w, pinned, at)
	w.emit(&DamageNewsChanged{At: at})
}

// SyncPublicWorksNews is syncPublicWorksNews for callers outside the package
// (the umbilical settings route, after a live setting change). MUST run on the
// world goroutine.
func SyncPublicWorksNews(w *World, at time.Time) {
	syncPublicWorksNews(w, at)
}

// repostPublicWorksNotices brings every notice board up to date with the
// pinned lines, keeping each board's crier-authored lines after them.
func repostPublicWorksNotices(w *World, pinned []string, at time.Time) {
	var boards []*VillageObject
	for _, obj := range w.VillageObjects {
		if IsNoticeBoard(w, obj) {
			boards = append(boards, obj)
		}
	}
	sortObjectsByID(boards)
	for _, obj := range boards {
		var authored []string
		if w.NoticeboardContent != nil {
			authored = authoredNoticeLines(w.NoticeboardContent[obj.ID])
		}
		PostNoticeboardWithPinned(w, obj.ID, pinned, authored, at)
	}
}

func sortObjectsByID(objs []*VillageObject) {
	sort.Slice(objs, func(i, j int) bool { return objs[i].ID < objs[j].ID })
}
