package sim

// damage_road.go — public works slice 3 (LLM-677): a tree comes down across a
// road, and stays there until a hand clears it.
//
// Unlike a well or a business, the damaged thing does not exist before the
// break: the break PLACES a road obstacle — one of the catalog's fallen-tree
// assets, picked at random — and the repair DELETES it. The obstacle is the
// damage: it carries TagRoadObstacle, DamagedAt is stamped as it lands, and
// PublicWorksKind reads it as PublicWorksRoad, so every public-works surface
// (bounty, cue, boards, ticker, PC thought) covers it through the same loop.
//
// The look (LLM-678, roadObstacleVariants): a snapped tree — the bare or
// winter top of a Growable Trees maple, chestnut or birch lying crown-west
// across the road, with its stump standing on the grass at the cut end — or the
// summer-forest broken trunk. The stump is its own 1-tile obstacle: clearing
// the road removes the top and starts the stump's clock (RoadStumpDays), and
// RemoveExpiredObjects takes it at the daily boundary once that has run out.
//
// The effect is the detour. Every top is an obstacle with a footprint the size
// of the drawn tree (migration LLM-678-fallen-trees), and the walk grid is
// rebuilt from live objects on every locomotion tick, so walkers route around
// it on the next tick with no pathfinder change. Nobody is slowed; they walk
// the long way.
//
// Where it lands (roadObstacleSites): the log must CUT a road — the road tiles
// on its sides no longer join through road once it is down — while open ground
// all around it keeps a way past. That takes north-south roads up to three tiles
// wide, east-west roads up to two, and bends alike (the art lies on a slant).
// Water anywhere near rules a site out, so a bridge never gets one; so does a
// building, fence or any other obstacle, or an object already standing there.
// The site must be within damageSiteLandmarkTiles of a named building — the
// obstacle is named by it ("Fallen maple by the Cole Residence") and lies where
// people walk. A snapped tree's stump must stand off the road, in line with the
// trunk past its cut end — so snapped trees fall across north-south roads, and
// the stumpless trunk is the one that can close an east-west road.

import (
	"errors"
	"log"
	"strings"
	"time"
)

// ErrNoRoadSite — no road tile qualifies for an obstacle just now, or the
// catalog carries neither fallen-tree asset.
var ErrNoRoadSite = errors.New("no road site can take a fallen tree just now")

// PublicWorksRoad is the damage kind of a road obstacle.
const PublicWorksRoad = "road"

// TagRoadObstacle marks a placed road obstacle — the object that IS the road's
// damage. Instance tag, checkpointed with the placement.
const TagRoadObstacle = "road_obstacle"

// roadObstacleVariant is one look a road break may take: a fallen-tree asset in
// one of its states, the name the placement carries (catalog names are the
// editor's), and — for a snapped tree — the stump left standing at the cut end.
type roadObstacleVariant struct {
	Asset AssetID
	State string // "" = the asset's default state
	Name  string
	// StumpState is the StormStumpAssetID state that stands at the cut end; ""
	// for a variant with no stump. StumpDX/DY is the stump anchor's world offset
	// from the top's anchor, measured off the art (llm-memory-village-tiles
	// tilesets/growable-trees/public-works-fallen-trees.json) at render scale 2.
	StumpState       string
	StumpDX, StumpDY float64
}

// The road-obstacle catalog (migration LLM-678-fallen-trees). Ids are fixed; a
// catalog without them (a test world, a fresh DB) places nothing.
const (
	FallenMapleAssetID    AssetID = "019e5f00-c401-7a10-9e00-000000678001"
	FallenChestnutAssetID AssetID = "019e5f00-c401-7a10-9e00-000000678002"
	FallenBirchAssetID    AssetID = "019e5f00-c401-7a10-9e00-000000678003"
	StormStumpAssetID     AssetID = "019e5f00-c401-7a10-9e00-000000678004"
	FallenTrunkAssetID    AssetID = "019e5f00-c401-7a10-9e00-000000678005"
)

// roadObstacleVariants are the looks a road break picks from at random, all
// lying crown-west (LLM-678): the bare and winter tops of the Growable Trees
// maple, chestnut and birch, each snapped off its stump, and the summer-forest
// broken trunk.
var roadObstacleVariants = []roadObstacleVariant{
	{Asset: FallenMapleAssetID, State: "bare", Name: "Fallen maple", StumpState: "maple-bare", StumpDX: 79, StumpDY: -5},
	{Asset: FallenMapleAssetID, State: "winter", Name: "Fallen maple", StumpState: "maple-winter", StumpDX: 79, StumpDY: -5},
	{Asset: FallenChestnutAssetID, State: "bare", Name: "Fallen chestnut", StumpState: "chestnut-bare", StumpDX: 111, StumpDY: 25},
	{Asset: FallenChestnutAssetID, State: "winter", Name: "Fallen chestnut", StumpState: "chestnut-winter", StumpDX: 111, StumpDY: 25},
	{Asset: FallenBirchAssetID, State: "bare", Name: "Fallen birch", StumpState: "birch-bare", StumpDX: 73, StumpDY: 17},
	{Asset: FallenBirchAssetID, State: "winter", Name: "Fallen birch", StumpState: "birch-winter", StumpDX: 73, StumpDY: 17},
	{Asset: FallenTrunkAssetID, Name: "Fallen tree"},
}

// TagStormStump marks the stump a snapped tree leaves at the roadside. It stays
// while the top lies in the road and for RoadStumpDays after the clearing.
const TagStormStump = "storm_stump"

// stumpOffset is the stump's tile relative to the top's anchor tile — the same
// for every tile, since a top is placed at its anchor tile's centre.
func (v roadObstacleVariant) stumpOffset() (TilePos, bool) {
	if v.StumpState == "" {
		return TilePos{}, false
	}
	base := TilePos{X: MapW / 2, Y: MapH / 2}
	c := base.Center()
	t := WorldPos{X: c.X + v.StumpDX, Y: c.Y + v.StumpDY}.Tile()
	return TilePos{X: t.X - base.X, Y: t.Y - base.Y}, true
}

// available reports whether the catalog carries everything the variant places:
// the top as an obstacle in its state, and the stump in its state.
func (v roadObstacleVariant) available(assets map[AssetID]*Asset) bool {
	a := assets[v.Asset]
	if a == nil || !a.IsObstacle {
		return false
	}
	// The state placement will use: the variant's, else the asset's default.
	state := v.State
	if state == "" {
		state = a.DefaultState
	}
	if state == "" || a.FindState(state) == nil {
		return false
	}
	if v.StumpState == "" {
		return true
	}
	// The site rule counts on the stump blocking its tile.
	s := assets[StormStumpAssetID]
	return s != nil && s.IsObstacle && s.FindState(v.StumpState) != nil
}

// variantOf finds the variant a placed top was drawn from, by asset and state.
func variantOf(obj *VillageObject) (roadObstacleVariant, bool) {
	for _, v := range roadObstacleVariants {
		if v.Asset == obj.AssetID && (v.State == "" || v.State == obj.CurrentState) {
			return v, true
		}
	}
	return roadObstacleVariant{}, false
}

// Defaults for the live-tunable road damage settings.
const (
	// DefaultRoadDamageChancePermille is the daily chance, per thousand, that a
	// tree comes down across a road. 0 disables the daily roll.
	DefaultRoadDamageChancePermille = 20
	// DefaultRoadDamageStormChancePermille is the chance, per thousand, that a
	// tree comes down when a storm starts — storms are what fell trees.
	DefaultRoadDamageStormChancePermille = 300
	// DefaultRoadDamageMinGapHours is the quiet time after a clearing before
	// another tree may come down.
	DefaultRoadDamageMinGapHours = 24
	// DefaultPublicWorksRoadBounty is what the chest pays to clear a road.
	DefaultPublicWorksRoadBounty = 10
	// DefaultPublicWorksRoadRepairSeconds is how long clearing a road takes.
	DefaultPublicWorksRoadRepairSeconds = 3600
	// DefaultRoadStumpDays is how long a snapped tree's stump stands at the
	// roadside after the tree is cleared (LLM-678).
	DefaultRoadStumpDays = 7
)

// roadObstacleClearance is how many tiles of open, dry, walkable ground a site
// needs on every side of the log's footprint — the room the detour walks
// through, and the distance kept from buildings, fences and water.
const roadObstacleClearance = 2

// IsRoadObstacle reports whether o is a placed road obstacle. Nil-safe.
func (o *VillageObject) IsRoadObstacle() bool {
	return o != nil && o.HasTag(TagRoadObstacle)
}

// AtRoadObstacle reports whether an actor at pos stands at the obstacle: within
// LoiterAttributionTiles of its loiter pin, the tile move_to walks a hand to
// (south of the log). Pure, so StartRepair and the cue agree.
func AtRoadObstacle(pos TilePos, obj *VillageObject, asset *Asset) bool {
	if obj == nil || asset == nil {
		return false
	}
	return pos.Chebyshev(computeLoiterTile(obj, asset)) <= LoiterAttributionTiles
}

// rollRoadDamage rolls once for a tree coming down across a road. The guards
// are the other kinds': chance 0 is off, one obstacle at a time, and none
// within the min gap of the last clearing. Returns the placed obstacle, or nil.
func rollRoadDamage(w *World, trigger string, permille int, rng DamageRoller, now time.Time) *VillageObject {
	if w == nil || permille <= 0 || rng == nil {
		return nil
	}
	if anyDamaged(w, PublicWorksRoad) {
		return nil
	}
	if gap, last := w.Settings.RoadDamageMinGapHours, w.Environment.LastRoadRepairAt; gap > 0 && !last.IsZero() && now.Sub(last) < time.Duration(gap)*time.Hour {
		return nil
	}
	if rng.Float64() >= float64(permille)/1000 {
		return nil
	}
	obj, err := placeRoadObstacle(w, trigger, rng, now)
	if err != nil {
		log.Printf("sim/damage: a tree should have come down (%s) but %v", trigger, err)
		return nil
	}
	return obj
}

// placeRoadObstacle brings a tree down across a road now: picks a variant and
// a qualifying site at random, places the top named by its landmark with its
// stump standing at the cut end, and damages the top — which stamps DamagedAt,
// logs, emits ObjectDamaged and reposts the boards.
func placeRoadObstacle(w *World, trigger string, rng DamageRoller, now time.Time) (*VillageObject, error) {
	var choices []roadObstacleVariant
	for _, v := range roadObstacleVariants {
		if v.available(w.Assets) {
			choices = append(choices, v)
		}
	}
	if len(choices) == 0 {
		return nil, ErrNoRoadSite
	}
	choice := choices[pickIndex(rng, len(choices))]
	asset := w.Assets[choice.Asset]
	grid, err := buildWalkGrid(w)
	if err != nil {
		return nil, err
	}
	stump, hasStump := choice.stumpOffset()
	var stumpAt *TilePos
	if hasStump {
		stumpAt = &stump
	}
	sites := roadObstacleSites(w, grid, asset, stumpAt)
	if len(sites) == 0 {
		return nil, ErrNoRoadSite
	}
	site := sites[pickIndex(rng, len(sites))]
	state := choice.State
	if state == "" {
		state = asset.DefaultState
	}
	pos := TileToWorld(site)
	obj := placeVillageObject(w, choice.Asset, asset, pos, "", "", state)
	obj.Tags = []string{TagRoadObstacle}
	obj.DisplayName = choice.Name
	w.emit(&VillageObjectDisplayNameChanged{ObjectID: obj.ID, DisplayName: obj.DisplayName, At: now})
	if hasStump {
		// Unnamed, like the shop debris: nobody passing is attributed to it.
		s := placeVillageObject(w, StormStumpAssetID, w.Assets[StormStumpAssetID],
			WorldPos{X: pos.X + choice.StumpDX, Y: pos.Y + choice.StumpDY}, "", "", choice.StumpState)
		s.Tags = []string{TagStormStump}
	}
	damageObject(w, obj, trigger, now)
	return obj, nil
}

// pickIndex turns one roll into an index in [0, n).
func pickIndex(rng DamageRoller, n int) int {
	i := int(rng.Float64() * float64(n))
	if i >= n {
		i = n - 1
	}
	if i < 0 {
		i = 0
	}
	return i
}

// roadObstacleSites lists every anchor tile where asset may come down across a
// road, in row-major order. A site qualifies when:
//
//   - the anchor tile is road (dirt or cobblestone);
//   - the footprint plus roadObstacleClearance on every side is walkable in the
//     current grid and dry — open ground for the detour, and nothing built,
//     fenced or wet close by (a bridge is over water, so it never qualifies);
//   - no other placed object stands on the footprint;
//   - no actor stands on the footprint (nobody is felled);
//   - the log cuts the road: the road tiles bordering the footprint fall into
//     at least two groups that no longer join through road inside the
//     clearance box;
//   - a named building is within damageSiteLandmarkTiles;
//   - with a stump (stump, relative to the anchor): the stump's tile is open,
//     off the footprint, unoccupied and NOT road — the tree grew beside the
//     road, and the stump that outlives the clearing never blocks it.
//
// The whole clearance box is walkable in the grid, so a way past the log
// always exists; the stump takes one tile of it at most, on the far side of
// the cut end, and a box two tiles deep still leaves a ring round both.
func roadObstacleSites(w *World, grid *WalkGrid, asset *Asset, stump *TilePos) []TilePos {
	if w.Terrain == nil || len(w.Terrain.Data) != MapW*MapH || asset == nil {
		return nil
	}
	isRoad := func(x, y int) bool {
		if x < 0 || x >= MapW || y < 0 || y >= MapH {
			return false
		}
		b := w.Terrain.Data[y*MapW+x]
		return b == TerrainDirt || b == TerrainCobblestone
	}
	isOpen := func(x, y int) bool {
		if !grid.CanWalk(x, y) {
			return false
		}
		b := w.Terrain.Data[y*MapW+x]
		return b != TerrainShallowWater && b != TerrainDeepWater
	}
	occupied := make(map[TilePos]struct{})
	for _, o := range w.VillageObjects {
		if o != nil && o.AttachedTo == "" {
			occupied[o.Pos.Tile()] = struct{}{}
		}
	}
	for _, a := range w.Actors {
		if a != nil {
			occupied[a.Pos] = struct{}{}
		}
	}
	landmarks := namedStructureTiles(w)

	var out []TilePos
	for y := 0; y < MapH; y++ {
		for x := 0; x < MapW; x++ {
			if !isRoad(x, y) {
				continue
			}
			fx0, fx1 := x-asset.FootprintLeft, x+asset.FootprintRight
			fy0, fy1 := y-asset.FootprintTop, y+asset.FootprintBottom
			bx0, bx1 := fx0-roadObstacleClearance, fx1+roadObstacleClearance
			by0, by1 := fy0-roadObstacleClearance, fy1+roadObstacleClearance
			inFootprint := func(tx, ty int) bool { return tx >= fx0 && tx <= fx1 && ty >= fy0 && ty <= fy1 }
			inBox := func(tx, ty int) bool { return tx >= bx0 && tx <= bx1 && ty >= by0 && ty <= by1 }
			if !boxAll(bx0, bx1, by0, by1, isOpen) {
				continue
			}
			if footprintOccupied(fx0, fx1, fy0, fy1, occupied) {
				continue
			}
			if roadGroupsAround(fx0, fx1, fy0, fy1, inFootprint, inBox, isRoad) < 2 {
				continue
			}
			if stump != nil {
				sx, sy := x+stump.X, y+stump.Y
				if inFootprint(sx, sy) || !isOpen(sx, sy) || isRoad(sx, sy) {
					continue
				}
				if _, taken := occupied[TilePos{X: sx, Y: sy}]; taken {
					continue
				}
			}
			if !landmarkWithin(TilePos{X: x, Y: y}, landmarks, damageSiteLandmarkTiles) {
				continue
			}
			out = append(out, TilePos{X: x, Y: y})
		}
	}
	return out
}

// boxAll reports whether ok holds for every tile of the box.
func boxAll(x0, x1, y0, y1 int, ok func(x, y int) bool) bool {
	for ty := y0; ty <= y1; ty++ {
		for tx := x0; tx <= x1; tx++ {
			if !ok(tx, ty) {
				return false
			}
		}
	}
	return true
}

// footprintOccupied reports whether any footprint tile holds an object anchor
// or an actor.
func footprintOccupied(x0, x1, y0, y1 int, occupied map[TilePos]struct{}) bool {
	for ty := y0; ty <= y1; ty++ {
		for tx := x0; tx <= x1; tx++ {
			if _, ok := occupied[TilePos{X: tx, Y: ty}]; ok {
				return true
			}
		}
	}
	return false
}

var cardinalSteps = [4]TilePos{{X: 1}, {X: -1}, {Y: 1}, {Y: -1}}

// roadGroupsAround counts the groups the road bordering the footprint falls
// into once the footprint is blocked: road tiles next to the footprint, joined
// through road tiles inside the box but outside the footprint. Two or more
// means the log cuts the road.
func roadGroupsAround(fx0, fx1, fy0, fy1 int, inFootprint, inBox func(x, y int) bool, isRoad func(x, y int) bool) int {
	seen := make(map[TilePos]struct{})
	groups := 0
	for ty := fy0 - 1; ty <= fy1+1; ty++ {
		for tx := fx0 - 1; tx <= fx1+1; tx++ {
			start := TilePos{X: tx, Y: ty}
			if inFootprint(tx, ty) || !isRoad(tx, ty) {
				continue
			}
			if _, done := seen[start]; done {
				continue
			}
			touches := false
			for _, d := range cardinalSteps {
				if inFootprint(tx+d.X, ty+d.Y) {
					touches = true
					break
				}
			}
			if !touches {
				continue
			}
			groups++
			queue := []TilePos{start}
			seen[start] = struct{}{}
			for len(queue) > 0 {
				c := queue[0]
				queue = queue[1:]
				for _, d := range cardinalSteps {
					n := TilePos{X: c.X + d.X, Y: c.Y + d.Y}
					if _, done := seen[n]; done || !inBox(n.X, n.Y) || inFootprint(n.X, n.Y) || !isRoad(n.X, n.Y) {
						continue
					}
					seen[n] = struct{}{}
					queue = append(queue, n)
				}
			}
		}
	}
	return groups
}

// namedStructureTiles returns the anchor tile of every named building — the
// landmarks DamageSiteLabel names a site by.
func namedStructureTiles(w *World) []TilePos {
	var out []TilePos
	for id, o := range w.VillageObjects {
		if o == nil || o.DisplayName == "" {
			continue
		}
		if _, ok := w.Structures[StructureID(id)]; ok {
			out = append(out, o.Pos.Tile())
		}
	}
	return out
}

// landmarkWithin reports whether any landmark is within tiles of p.
func landmarkWithin(p TilePos, landmarks []TilePos, tiles int) bool {
	for _, l := range landmarks {
		if p.Chebyshev(l) <= tiles {
			return true
		}
	}
	return false
}

// removeRoadObstacle clears a road: deletes the obstacle and stamps the road
// kind's min-gap anchor. Reports whether it was removed. DeleteVillageObject
// fails only for a missing object or a structure — an obstacle this code
// placed is neither, but a structure tagged road_obstacle by hand would be —
// and then nothing changes: the road stays blocked and the repair has not
// landed.
func removeRoadObstacle(w *World, obj *VillageObject, now time.Time) bool {
	if _, err := DeleteVillageObject(obj.ID).Fn(w); err != nil {
		log.Printf("sim/damage: removing road obstacle %s: %v", obj.ID, err)
		return false
	}
	w.Environment.LastRoadRepairAt = now
	startStumpClock(w, obj, now)
	return true
}

// startStumpClock gives the stump a cleared tree leaves behind its expiry:
// RoadStumpDays from the clearing, after which RemoveExpiredObjects takes it.
// The stump is found where the top's variant put it; one already on a clock is
// left alone. RoadStumpDays of 0 or less removes it with the tree.
func startStumpClock(w *World, top *VillageObject, now time.Time) {
	v, ok := variantOf(top)
	if !ok {
		return
	}
	off, ok := v.stumpOffset()
	if !ok {
		return
	}
	anchor := top.Pos.Tile()
	want := TilePos{X: anchor.X + off.X, Y: anchor.Y + off.Y}
	var ids []VillageObjectID
	for id, o := range w.VillageObjects {
		if o != nil && o.HasTag(TagStormStump) && o.ExpiresAt.IsZero() && o.Pos.Tile() == want {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if w.Settings.RoadStumpDays <= 0 {
			if _, err := DeleteVillageObject(id).Fn(w); err != nil {
				log.Printf("sim/damage: removing stump %s: %v", id, err)
			}
			continue
		}
		w.VillageObjects[id].ExpiresAt = now.Add(time.Duration(w.Settings.RoadStumpDays) * 24 * time.Hour)
	}
}

// RemoveExpiredObjects deletes every placement whose ExpiresAt has passed —
// the storm stumps a cleared road leaves (LLM-678). Run at the durable daily
// boundary (checkAndRotate), so a stump goes at the first boundary past its
// expiry and a restart neither skips nor repeats it.
//
// It first starts the clock on any stump whose tree is gone without the repair
// having started it (clockOrphanStumps), so a stump never outlives its tree
// for good.
func RemoveExpiredObjects(now time.Time) Command {
	return Command{Fn: func(w *World) (any, error) {
		removed := clockOrphanStumps(w, now)
		var ids []VillageObjectID
		for id, o := range w.VillageObjects {
			if o != nil && !o.ExpiresAt.IsZero() && !now.Before(o.ExpiresAt) {
				ids = append(ids, id)
			}
		}
		for _, id := range ids {
			if _, err := DeleteVillageObject(id).Fn(w); err != nil {
				log.Printf("sim/damage: removing expired %s: %v", id, err)
				continue
			}
			removed++
			log.Printf("sim/damage: expired placement %s removed", id)
		}
		return removed, nil
	}}
}

// clockOrphanStumps starts the clock on every stump still waiting on its tree
// (no ExpiresAt) whose tree no longer lies where its variant would put the
// stump — the top deleted in the editor, moved, or re-stated, anything but the
// town's clearing, which starts the clock itself. Keyed on what is in the
// world, not on how the tree left, so no path strands a stump for good. The
// clock runs from this sweep, at most a day after the tree went. Returns how
// many stumps it removed outright (RoadStumpDays of 0 or less).
func clockOrphanStumps(w *World, now time.Time) int {
	standing := make(map[TilePos]struct{})
	for _, o := range w.VillageObjects {
		if !o.IsRoadObstacle() {
			continue
		}
		v, ok := variantOf(o)
		if !ok {
			continue
		}
		if off, ok := v.stumpOffset(); ok {
			a := o.Pos.Tile()
			standing[TilePos{X: a.X + off.X, Y: a.Y + off.Y}] = struct{}{}
		}
	}
	removed := 0
	var orphans []VillageObjectID
	for id, o := range w.VillageObjects {
		if o == nil || !o.HasTag(TagStormStump) || !o.ExpiresAt.IsZero() {
			continue
		}
		if _, ok := standing[o.Pos.Tile()]; !ok {
			orphans = append(orphans, id)
		}
	}
	for _, id := range orphans {
		if w.Settings.RoadStumpDays <= 0 {
			if _, err := DeleteVillageObject(id).Fn(w); err != nil {
				log.Printf("sim/damage: removing orphaned stump %s: %v", id, err)
			} else {
				removed++
			}
			continue
		}
		w.VillageObjects[id].ExpiresAt = now.Add(time.Duration(w.Settings.RoadStumpDays) * 24 * time.Hour)
		log.Printf("sim/damage: stump %s outlived its tree; removed in %d days", id, w.Settings.RoadStumpDays)
	}
	return removed
}

// roadObstacleFact is the "what is broken" opening for a road obstacle — "A
// fallen maple lies across the road by the Cole Residence".
func roadObstacleFact(objects map[VillageObjectID]*VillageObject, structures map[StructureID]*Structure, assets map[AssetID]*Asset, obj *VillageObject) string {
	name := "fallen tree"
	if obj.DisplayName != "" {
		name = strings.ToLower(obj.DisplayName)
	}
	fact := "A " + name + " lies across the road"
	if landmark := damageSiteLandmark(objects, structures, obj); landmark != "" {
		fact += " by " + WithDefiniteArticle(landmark)
	}
	return fact
}

// ForceRoadDamage is the operator control that brings a tree down now: no roll
// and no guards but a free site. trigger is DamageTriggerForce or
// DamageTriggerStorm. Returns the placed obstacle's id.
func ForceRoadDamage(trigger string, rng DamageRoller) Command {
	return Command{Fn: func(w *World) (any, error) {
		obj, err := placeRoadObstacle(w, trigger, rng, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		return obj.ID, nil
	}}
}
