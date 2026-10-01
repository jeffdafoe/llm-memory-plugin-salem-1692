package sim

// damage_minor.go — minor works (LLM-690): small damage that stops nothing, for
// players only.
//
// The big breaks (a well, a business, a road) come a few times a week and a
// hand takes most of them within minutes, so a player would find almost no
// work. Minor works are the frequent, low-stakes tier: a fence rail down, a
// signpost arm hanging crooked, a crate lid knocked loose. A roll on a timer
// breaks one at a time up to an open cap; a player mends it by the same
// stepped mini-game as a big break (pc_repair.go) for a small bounty; an
// untaken one mends itself after a TTL.
//
// PC-ONLY, enforced engine-side. A minor work is not an IsDamagedSite, so the
// ticker, the notice boards and a hand's "## The town's works" cue — which all
// read IsDamagedSite — never see one, and no NPC pays an LLM turn for it.
// publicWorksSiteAt resolves one only for a player, and startPublicWorksRepair
// refuses it to anyone else.
//
// THE ART IS DATA. A minor work is a state flip on an existing placement, never
// a new one, so it needs nothing the walk grid or the editor must know. The
// asset carries the damaged states, tagged:
//
//   - minor-of-<state>   this state is the broken form of <state> (and mends
//     back to it).
//   - minor-form-<form>  the site state: the mini-game and the wording (fence,
//     signpost, crate). A state with minor-of- but no form tag is an EDGE —
//     the neighbour's half of a break that spans tiles.
//   - minor-left-<state> / minor-right-<state>  the site state's edges: the
//     placement of the same asset one tile left / right flips to <state>. The
//     Mana Seed ranch fence draws a break across three cells — the middle post
//     gone and the rails sagging into both neighbours — so a fence break is
//     the middle segment plus its two neighbours.
//
// Durable without a new column: the site's DamagedAt and every flipped state
// are checkpointed, and the tags say what each state mends back to. An edge is
// never Damaged — only the site is a minor work.

import (
	"context"
	"log"
	"math/rand/v2"
	"sort"
	"strings"
	"time"
)

// PublicWorksMinor is the site kind of a minor work.
const PublicWorksMinor = "minor"

// TagMinorWork marks a placement that is a minor work's site: added when it
// breaks, removed when it mends. PublicWorksKind keys on it (with DamagedAt),
// so an object damaged any other way is never taken for a minor work. A
// per-instance tag, so it is checkpointed with the placement.
const TagMinorWork = "minor-work"

// Minor-work forms — the mini-game and the wording.
const (
	MinorFormFence    = "fence"
	MinorFormSignpost = "signpost"
	MinorFormCrate    = "crate"
)

// Asset-state tag prefixes that describe minor works (see the file comment).
const (
	minorTagOf    = "minor-of-"
	minorTagForm  = "minor-form-"
	minorTagLeft  = "minor-left-"
	minorTagRight = "minor-right-"
)

// Defaults for the live-tunable minor-works settings: even odds at each pass,
// up to four open at once, each gone after a day; a small job for a small
// bounty.
const (
	DefaultMinorWorksChancePermille = 500
	DefaultMinorWorksOpenCap        = 4
	DefaultMinorWorksTTLHours       = 24
	DefaultPublicWorksMinorBounty   = 3
	DefaultPCRepairMinorSteps       = 5
	DefaultPCRepairMinorStepGapMs   = 3000
)

// MinorWorksTickerInterval is how often RunMinorWorksTicker rolls. Fixed: the
// chance is the tuning knob.
const MinorWorksTickerInterval = 30 * time.Minute

// minorWorksSiteTiles is how near a player must stand to a minor work.
const minorWorksSiteTiles = 2

// stateTagValue returns the rest of the first tag on s that starts with prefix.
func stateTagValue(s *AssetState, prefix string) (string, bool) {
	if s == nil {
		return "", false
	}
	for _, t := range s.Tags {
		if v, ok := strings.CutPrefix(t, prefix); ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// minorVariants returns the site states an object showing state can break
// into — tagged minor-of-<state> and carrying a form — lowest id first.
func minorVariants(a *Asset, state string) []*AssetState {
	if a == nil || state == "" {
		return nil
	}
	var out []*AssetState
	for i := range a.States {
		s := &a.States[i]
		if !stateHasTag(s, minorTagOf+state) {
			continue
		}
		if _, ok := stateTagValue(s, minorTagForm); ok {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// MinorWorkForm is the form of a minor work — its current state's minor-form
// tag — or "" for anything else. Pure over the maps.
func MinorWorkForm(assets map[AssetID]*Asset, obj *VillageObject) string {
	if obj == nil || !obj.Damaged() {
		return ""
	}
	a := assets[obj.AssetID]
	if a == nil {
		return ""
	}
	form, _ := stateTagValue(a.FindState(obj.CurrentState), minorTagForm)
	return form
}

// minorNeighbour returns the placement of obj's asset one tile left (dx -1) or
// right (dx +1) of it, or nil.
func minorNeighbour(w *World, obj *VillageObject, dx int) *VillageObject {
	want := obj.Pos.Tile()
	want.X += dx
	var best *VillageObject
	for _, o := range w.VillageObjects {
		if o == nil || o == obj || o.AssetID != obj.AssetID || o.Pos.Tile() != want {
			continue
		}
		if best == nil || o.ID < best.ID {
			best = o
		}
	}
	return best
}

// minorEdges resolves the neighbours a site state flips, each with the state
// it flips to. ok is false when an edge the state needs has no sound neighbour
// in the state the edge mends back to — the break cannot be drawn there.
func minorEdges(w *World, obj *VillageObject, a *Asset, site *AssetState) (edges []*VillageObject, states []string, ok bool) {
	for _, side := range []struct {
		prefix string
		dx     int
	}{{minorTagLeft, -1}, {minorTagRight, 1}} {
		edgeState, needed := stateTagValue(site, side.prefix)
		if !needed {
			continue
		}
		edge := a.FindState(edgeState)
		mendsTo, _ := stateTagValue(edge, minorTagOf)
		n := minorNeighbour(w, obj, side.dx)
		if edge == nil || n == nil || n.Damaged() || n.CurrentState != mendsTo {
			return nil, nil, false
		}
		edges = append(edges, n)
		states = append(states, edgeState)
	}
	return edges, states, true
}

// damageMinor breaks obj into the site state variant: flips it and any edges,
// stamps DamagedAt, and emits ObjectDamaged. Reports false (and changes
// nothing) when obj is already damaged or an edge cannot be drawn.
func damageMinor(w *World, obj *VillageObject, variant *AssetState, now time.Time) bool {
	if obj == nil || obj.Damaged() || variant == nil {
		return false
	}
	a := w.Assets[obj.AssetID]
	edges, edgeStates, ok := minorEdges(w, obj, a, variant)
	if !ok {
		return false
	}
	for i, e := range edges {
		setVillageObjectStateInline(w, e, edgeStates[i])
	}
	setVillageObjectStateInline(w, obj, variant.State)
	if _, err := AddVillageObjectTag(obj.ID, TagMinorWork).Fn(w); err != nil {
		log.Printf("sim/damage: tagging minor work %s: %v", obj.ID, err)
	}
	obj.DamagedAt = now
	name := damageObjectName(w, obj)
	log.Printf("sim/damage: minor work — %s (%s) is %s", name, obj.ID, variant.State)
	w.emit(&ObjectDamaged{ObjectID: obj.ID, Name: name, Trigger: DamageTriggerWear, At: now})
	return true
}

// restoreMinor returns a minor work and its edges to the states their
// minor-of- tags name. An edge is found as the neighbour showing the state the
// site names for that side.
func restoreMinor(w *World, obj *VillageObject) {
	a := w.Assets[obj.AssetID]
	if a == nil {
		return
	}
	site := a.FindState(obj.CurrentState)
	for _, side := range []struct {
		prefix string
		dx     int
	}{{minorTagLeft, -1}, {minorTagRight, 1}} {
		edgeState, needed := stateTagValue(site, side.prefix)
		if !needed {
			continue
		}
		n := minorNeighbour(w, obj, side.dx)
		if n == nil || n.CurrentState != edgeState {
			continue
		}
		if mendsTo, ok := stateTagValue(a.FindState(edgeState), minorTagOf); ok {
			setVillageObjectStateInline(w, n, mendsTo)
		}
	}
	if mendsTo, ok := stateTagValue(site, minorTagOf); ok {
		setVillageObjectStateInline(w, obj, mendsTo)
	}
	if _, err := RemoveVillageObjectTag(obj.ID, TagMinorWork).Fn(w); err != nil {
		log.Printf("sim/damage: untagging minor work %s: %v", obj.ID, err)
	}
}

// minorWorkCandidates lists every sound placement that could break into a
// minor work now — a variant of its state whose edges can be drawn — with its
// variants, lowest id first.
func minorWorkCandidates(w *World) ([]*VillageObject, map[VillageObjectID][]*AssetState) {
	var objs []*VillageObject
	variants := make(map[VillageObjectID][]*AssetState)
	for _, obj := range w.VillageObjects {
		if obj == nil || obj.Damaged() {
			continue
		}
		a := w.Assets[obj.AssetID]
		for _, v := range minorVariants(a, obj.CurrentState) {
			if _, _, ok := minorEdges(w, obj, a, v); ok {
				variants[obj.ID] = append(variants[obj.ID], v)
			}
		}
		if len(variants[obj.ID]) > 0 {
			objs = append(objs, obj)
		}
	}
	sortObjectsByID(objs)
	return objs, variants
}

// openMinorWorks lists every open minor work, lowest id first.
func openMinorWorks(w *World) []*VillageObject {
	var out []*VillageObject
	for _, obj := range w.VillageObjects {
		if PublicWorksKind(obj) == PublicWorksMinor {
			out = append(out, obj)
		}
	}
	sortObjectsByID(out)
	return out
}

// pick returns an index in [0, n) off one roll.
func pick(rng DamageRoller, n int) int {
	i := int(rng.Float64() * float64(n))
	if i >= n {
		i = n - 1
	}
	return i
}

// RollMinorWorks is one pass of the minor-works timer: mend the open minor
// works past their TTL (none a player is mending), then — below the open cap —
// roll the chance and break one random candidate into a random variant.
// Returns the object broken, or nil.
func RollMinorWorks(now time.Time, rng DamageRoller) Command {
	return Command{Fn: func(w *World) (any, error) {
		return rollMinorWorks(w, rng, now), nil
	}}
}

func rollMinorWorks(w *World, rng DamageRoller, now time.Time) *VillageObject {
	s := w.Settings
	ttl := time.Duration(orDefault(s.MinorWorksTTLHours, DefaultMinorWorksTTLHours)) * time.Hour
	open := 0
	for _, obj := range openMinorWorks(w) {
		if now.Sub(obj.DamagedAt) >= ttl && !objectUnderRepair(w, obj.ID, "") {
			log.Printf("sim/damage: minor work %s went untaken — it mends itself", obj.ID)
			repairObject(w, obj, "", 0, now)
			continue
		}
		open++
	}
	// A cap or chance of 0 turns the spawn off; only a negative falls back.
	if open >= s.MinorWorksOpenCap || s.MinorWorksChancePermille <= 0 || rng == nil {
		return nil
	}
	if rng.Float64() >= float64(s.MinorWorksChancePermille)/1000 {
		return nil
	}
	objs, variants := minorWorkCandidates(w)
	if len(objs) == 0 {
		return nil
	}
	obj := objs[pick(rng, len(objs))]
	vs := variants[obj.ID]
	if !damageMinor(w, obj, vs[pick(rng, len(vs))], now) {
		return nil
	}
	return obj
}

func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// minorWorksFact is DamageFact for a minor work, by form, placed by the
// nearest named building — "A rail has come down on the fence by the Mill".
func minorWorksFact(objects map[VillageObjectID]*VillageObject, structures map[StructureID]*Structure, assets map[AssetID]*Asset, obj *VillageObject) string {
	by := ""
	if landmark := damageSiteLandmark(objects, structures, obj); landmark != "" {
		by = " by " + WithDefiniteArticle(landmark)
	}
	switch MinorWorkForm(assets, obj) {
	case MinorFormFence:
		return "A rail has come down on the fence" + by
	case MinorFormSignpost:
		return "The arm of the signpost" + by + " hangs crooked"
	case MinorFormCrate:
		return "The lid has been knocked loose on the crate" + by
	}
	return "Something wants mending" + by
}

// atMinorWork reports whether a player stands at a minor work: within
// minorWorksSiteTiles of it, so a click on the fence segment beside the break
// still finds it.
func atMinorWork(w *World, actor *Actor, obj *VillageObject) bool {
	return obj.Pos.Tile().Chebyshev(actor.Pos) <= minorWorksSiteTiles
}

// minorWorksRoller adapts math/rand/v2's global source to DamageRoller.
type minorWorksRoller struct{}

func (minorWorksRoller) Float64() float64 { return rand.Float64() }

// RunMinorWorksTicker owns the minor-works goroutine: a RollMinorWorks pass
// every MinorWorksTickerInterval.
func RunMinorWorksTicker(ctx context.Context, w *World) {
	t := time.NewTicker(MinorWorksTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.beatTicker("minor_works")
			if _, err := w.SendContext(ctx, RollMinorWorks(time.Now().UTC(), minorWorksRoller{})); err != nil && ctx.Err() == nil {
				log.Printf("sim/damage: minor-works pass failed: %v", err)
			}
		}
	}
}
