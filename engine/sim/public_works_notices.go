package sim

import (
	"sort"
	"strings"
	"time"
)

// public_works_notices.go — LLM-654: the town's work on the notice boards.
//
// A broken well puts two PINNED lines at the top of every notice board — what
// is broken, and what the town pays to mend it (or, when the chest cannot pay,
// where to draw water meanwhile). Two lines, not one, because the live board
// has no 1-slip art: a lone line would snap down to the empty board.
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

// PublicWorksNoticeLines returns the pinned lines for every broken well, lowest
// id first. Empty when every well is sound.
func PublicWorksNoticeLines(w *World) []string {
	var broken []*VillageObject
	for _, obj := range w.VillageObjects {
		if obj.IsWell() && obj.Damaged() {
			broken = append(broken, obj)
		}
	}
	sortObjectsByID(broken)
	var out []string
	open := PublicWorksBountyOpen(w.Environment.TownChest, w.Settings.PublicWorksBounty, w.Settings.PublicWorksChestReserve)
	for _, obj := range broken {
		site := damageObjectName(w, obj)
		out = append(out, "The windlass at the "+site+" is down — no water can be drawn there until it is mended.")
		if open {
			out = append(out, "The town pays "+coinsPhrase(w.Settings.PublicWorksBounty)+" to the hand who mends it.")
		} else {
			out = append(out, "Until it is mended, draw your water at the other well.")
		}
	}
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

// repostPublicWorksNotices brings every notice board up to date with the
// current pinned lines, keeping each board's crier-authored lines after them.
// Called when a well breaks or is mended.
func repostPublicWorksNotices(w *World, at time.Time) {
	pinned := PublicWorksNoticeLines(w)
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
