package sim

// camp.go — LLM-798. Every player carries a tent. Pitched on open grass it makes
// their camp — the tent, a fire ring, a stool and a woodpile — and the tent is
// their home (HomeStructureID) until it is struck again: owner-only entry, the
// knock and let-in, "go home", and sleep (pcCanSleepHere), at a worse rest than a
// bed (pcRestFloor). Struck, the tent goes back in the pack and the home is gone.
//
// Pitching and striking are timed work the player is seen doing: a
// SourceActivity of kind pitch / strike with one window per beat (Steps, StepsDone).
// Each landed beat puts down or takes away one piece and arms the next window, so
// every client sees the camp go up piece by piece as the events arrive. Walking
// off, or going to bed, undoes the work (undoCampWork): a half-pitched camp comes
// down and the tent goes back in the pack; a half-struck camp stands again. The
// windows are not checkpointed, so a restart settles any camp left half done
// (settleUnfinishedCampsOnLoad): a packed tent with no home on it is a pitch that
// never finished and comes down; a tent that is someone's home is made whole.
//
// The camp is found by its owner: every piece is a village_object of a camp asset
// with OwnerActorID = the player. A player has at most one camp.
//
// The fire ring burns by the clock, like a hearth: LightCampFire sets one
// durable out-at time (VillageObject.HearthLitUntil, reused — the fire is not a
// TagHearth object and warms nothing) and UpdateCampFires turns the sprite
// burning → dying → ashes as the clock passes it.

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// TentItemKind is the tent in a player's pack. It carries CapabilityShelter,
// which keeps it out of every pay, gift and barter (resolvePayItems,
// KindBarterable): a player must never trade away their only shelter.
const TentItemKind ItemKind = "tent"

// CapabilityShelter marks an item that is a player's shelter (LLM-798).
const CapabilityShelter = "shelter"

// The camp's catalog assets, seeded by the LLM-798 migration with these ids.
const (
	TentAssetID         AssetID = "019e5f00-c401-7a10-9e00-000000798001"
	CampFireAssetID     AssetID = "019e5f00-c401-7a10-9e00-000000798002"
	CampStoolAssetID    AssetID = "019e5f00-c401-7a10-9e00-000000798003"
	CampWoodpileAssetID AssetID = "019e5f00-c401-7a10-9e00-000000798004"
)

// Tent and fire states. The tent's open / closed states carry the occupied /
// unoccupied tags, so refreshStructureOccupancyState opens the flap while
// someone is inside and awake; packed is the bundle on the ground while the
// tent is pitched or struck.
const (
	TentStatePacked = "packed"
	TentStateClosed = "closed"
	TentStateOpen   = "open"

	CampFireStateUnlit   = "unlit"
	CampFireStateBurning = "burning"
	CampFireStateDying   = "dying"
	CampFireStateAshes   = "ashes"
)

// Defaults for the live-tunable camp settings.
const (
	DefaultPitchSeconds         = 10
	DefaultStrikeSeconds        = 8
	DefaultTentRestFloor        = 6
	DefaultTentRestFloorFire    = 3
	DefaultCampFireBurnMinutes  = 180
	DefaultCampFireDyingMinutes = 60
)

// Camp beats, in pitch order. Strike runs them backwards.
const (
	CampBeatTent     = "tent"
	CampBeatFire     = "fire"
	CampBeatStool    = "stool"
	CampBeatWoodpile = "woodpile"
)

// campTentShare is the part of the whole pitch / strike the tent's beat takes;
// the props share the rest equally.
const campTentShare = 0.6

// campPenProbeTiles bounds the pen check: a flood fill from the stand tile over
// the grazer grid (gates shut) that stops short of this many tiles is closed in.
const campPenProbeTiles = 600

// campPiece is one piece of a camp at a fixed tile offset from the stand tile —
// the tile in front of the tent's door, where the player stands to pitch.
type campPiece struct {
	Beat   string
	Asset  AssetID
	Offset TilePos
}

// campLayout is the camp in pitch order. The tent's anchor is one tile north of
// the stand tile, and its door is the anchor tile (the Tent asset's door offset
// is 0,0), so the player pitches standing at the door.
var campLayout = []campPiece{
	{Beat: CampBeatTent, Asset: TentAssetID, Offset: TilePos{X: 0, Y: -1}},
	{Beat: CampBeatFire, Asset: CampFireAssetID, Offset: TilePos{X: 2, Y: 1}},
	{Beat: CampBeatStool, Asset: CampStoolAssetID, Offset: TilePos{X: 3, Y: 2}},
	{Beat: CampBeatWoodpile, Asset: CampWoodpileAssetID, Offset: TilePos{X: 1, Y: 2}},
}

// Errors for the camp routes. Each reads as the player's own situation.
var (
	ErrCampNoTent         = errors.New("you have no tent in your pack.")
	ErrCampAlreadyPitched = errors.New("your tent is already pitched — strike it before pitching it again.")
	ErrCampNotPitched     = errors.New("your tent is not pitched.")
	ErrCampBusy           = errors.New("you are busy — finish what you're doing first.")
	ErrCampIndoors        = errors.New("you need to be out of doors to do that.")
	ErrCampNotGrass       = errors.New("the ground here isn't open grass — find a patch of grass to pitch on.")
	ErrCampBlocked        = errors.New("something is in the way here — find a clear patch of grass.")
	ErrCampNearDoor       = errors.New("that is too close to a doorway — pitch a little way off.")
	ErrCampInPen          = errors.New("you can't pitch a tent inside a pen.")
	ErrCampNotAtTent      = errors.New("walk to the front of your tent first.")
	ErrCampOccupied       = errors.New("someone is inside your tent.")
	ErrCampNoFire         = errors.New("there is no fire ring at your camp.")
	ErrCampNotAtFire      = errors.New("walk to your fire first.")
	ErrCampFireLit        = errors.New("your fire is already burning.")
	ErrCampCatalogMissing = errors.New("the camp is not in the village catalog.")
)

// CampStartResult answers a pitch or strike start.
type CampStartResult struct {
	TentID VillageObjectID
	// DoneAt is when the last beat lands if nothing interrupts.
	DoneAt time.Time
}

// LightCampFireResult answers a lit fire.
type LightCampFireResult struct {
	FireID VillageObjectID
	// BurnsUntil is when it starts dying down; OutAt when it is ashes.
	BurnsUntil time.Time
	OutAt      time.Time
}

// Camp is a player's pitched camp as found in the world maps. Tent is always
// set; the props may be missing while the camp goes up or comes down.
type Camp struct {
	Tent     *VillageObject
	Fire     *VillageObject
	Stool    *VillageObject
	Woodpile *VillageObject
}

// FindCamp returns ownerID's camp, or nil when they have none pitched. Pure over
// the objects map, so a snapshot reader (/pc/me) and the world share it.
func FindCamp(objects map[VillageObjectID]*VillageObject, ownerID ActorID) *Camp {
	if ownerID == "" {
		return nil
	}
	var c Camp
	for _, o := range objects {
		if o == nil || o.OwnerActorID != ownerID {
			continue
		}
		switch o.AssetID {
		case TentAssetID:
			c.Tent = o
		case CampFireAssetID:
			c.Fire = o
		case CampStoolAssetID:
			c.Stool = o
		case CampWoodpileAssetID:
			c.Woodpile = o
		}
	}
	if c.Tent == nil {
		return nil
	}
	return &c
}

// piece returns the camp's object for a beat, nil when it isn't down.
func (c *Camp) piece(beat string) *VillageObject {
	switch beat {
	case CampBeatTent:
		return c.Tent
	case CampBeatFire:
		return c.Fire
	case CampBeatStool:
		return c.Stool
	case CampBeatWoodpile:
		return c.Woodpile
	}
	return nil
}

// IsTent reports whether obj is a pitched tent.
func IsTent(obj *VillageObject) bool {
	return obj != nil && obj.AssetID == TentAssetID
}

// campStandTile is the tile in front of a tent's door — where the player stands
// to pitch and strike, and the origin of the layout offsets.
func campStandTile(tent *VillageObject) TilePos {
	a := tent.Pos.Tile()
	return TilePos{X: a.X - campLayout[0].Offset.X, Y: a.Y - campLayout[0].Offset.Y}
}

// campPiecePos is where a beat's piece stands for a camp pitched from stand.
func campPiecePos(stand TilePos, p campPiece) WorldPos {
	return TilePos{X: stand.X + p.Offset.X, Y: stand.Y + p.Offset.Y}.Center()
}

// campBeatDuration is how long beat i of n takes out of total: the tent takes
// campTentShare, the props share the rest. Pitch counts the tent as beat 0,
// strike as its last beat; tentBeat says which.
func campBeatDuration(total time.Duration, beat, tentBeat, n int) time.Duration {
	if n <= 1 {
		return total
	}
	if beat == tentBeat {
		return time.Duration(float64(total) * campTentShare)
	}
	return time.Duration(float64(total) * (1 - campTentShare) / float64(n-1))
}

// pitchTotal / strikeTotal are the whole work's length, falling back to the
// defaults when unset.
func (s WorldSettings) pitchTotal() time.Duration {
	secs := s.PitchSeconds
	if secs <= 0 {
		secs = DefaultPitchSeconds
	}
	return time.Duration(secs) * time.Second
}

func (s WorldSettings) strikeTotal() time.Duration {
	secs := s.StrikeSeconds
	if secs <= 0 {
		secs = DefaultStrikeSeconds
	}
	return time.Duration(secs) * time.Second
}

// campBeat returns the layout piece for a step of a pitch or strike window.
func campBeat(kind SourceActivityKind, step int) campPiece {
	if kind == SourceActivityStrike {
		return campLayout[len(campLayout)-1-step]
	}
	return campLayout[step]
}

// campBeatWindow builds the window for step of a pitch or strike on the tent
// pitched from stand, starting at now.
func campBeatWindow(w *World, kind SourceActivityKind, tentID VillageObjectID, stand TilePos, step int, now time.Time) *SourceActivity {
	n := len(campLayout)
	total, tentBeat := w.Settings.pitchTotal(), 0
	if kind == SourceActivityStrike {
		total, tentBeat = w.Settings.strikeTotal(), n-1
	}
	p := campBeat(kind, step)
	return &SourceActivity{
		Kind:      kind,
		ObjectID:  tentID,
		StartedAt: now,
		Until:     now.Add(campBeatDuration(total, step, tentBeat, n)),
		Steps:     n,
		StepsDone: step,
		Beat:      p.Beat,
		Target:    campPiecePos(stand, p),
	}
}

// emitCampBeatStarted announces a beat window: the piece it works on and where.
func emitCampBeatStarted(w *World, actor *Actor, act *SourceActivity, now time.Time) {
	target := act.Target
	w.emit(&SourceActivityStarted{
		ActorID:  actor.ID,
		ObjectID: act.ObjectID,
		Kind:     act.Kind,
		Until:    act.Until,
		At:       now,
		Beat:     act.Beat,
		Target:   &target,
	})
}

// campWorkGate is the shared start gate for pitch, strike and light: an awake
// player standing out of doors with nothing else in hand. Lands a finished
// window first, so a just-ended one does not read as busy.
func campWorkGate(w *World, actor *Actor, now time.Time) error {
	if actor.Kind != KindPC {
		return ErrCampBusy
	}
	if actor.MoveIntent != nil || actor.SleepingUntil != nil {
		return ErrCampBusy
	}
	completeIfDue(w, actor.ID, actor, now)
	if actor.SourceActivity != nil {
		return ErrCampBusy
	}
	if actor.InsideStructureID != "" {
		return ErrCampIndoors
	}
	return nil
}

// PitchTent starts the player pitching their tent where they stand: they face
// the door of the tent to be. Checks the ground, takes the tent from the pack,
// lays the bundle down (taking the spot at once) and opens the first beat; the
// tent stands when that beat lands, the props follow one beat each.
func PitchTent(actorID ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("PitchTent: actor %q not in world", actorID)
			}
			if err := campWorkGate(w, actor, now); err != nil {
				return nil, err
			}
			if actor.Inventory[TentItemKind] < 1 {
				return nil, ErrCampNoTent
			}
			if FindCamp(w.VillageObjects, actorID) != nil {
				return nil, ErrCampAlreadyPitched
			}
			tentAsset := w.Assets[TentAssetID]
			for _, p := range campLayout {
				if w.Assets[p.Asset] == nil {
					return nil, ErrCampCatalogMissing
				}
			}
			stand := actor.Pos
			if err := checkCampGround(w, actor, stand); err != nil {
				return nil, err
			}

			actor.Inventory[TentItemKind]--
			if actor.Inventory[TentItemKind] == 0 {
				delete(actor.Inventory, TentItemKind)
			}
			tent := newVillageObject(TentAssetID, tentAsset, campPiecePos(stand, campLayout[0]), "", "", TentStatePacked)
			tent.OwnerActorID = actorID
			tent.EntryPolicy = EntryPolicyOwner
			registerVillageObject(w, tent, tentAsset)

			act := campBeatWindow(w, SourceActivityPitch, tent.ID, stand, 0, now)
			actor.SourceActivity = act
			emitCampBeatStarted(w, actor, act, now)
			return CampStartResult{TentID: tent.ID, DoneAt: now.Add(w.Settings.pitchTotal())}, nil
		},
	}
}

// StrikeTent starts the player striking their camp: the props come up one beat
// each, then the tent is packed and goes back in the pack. The player stands at
// the front of the tent, and no one may be inside.
func StrikeTent(actorID ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("StrikeTent: actor %q not in world", actorID)
			}
			if err := campWorkGate(w, actor, now); err != nil {
				return nil, err
			}
			camp := FindCamp(w.VillageObjects, actorID)
			if camp == nil {
				return nil, ErrCampNotPitched
			}
			stand := campStandTile(camp.Tent)
			if actor.Pos.Chebyshev(stand) > 1 {
				return nil, ErrCampNotAtTent
			}
			if len(w.actorsByStructure[StructureID(camp.Tent.ID)]) > 0 {
				return nil, ErrCampOccupied
			}
			act := campBeatWindow(w, SourceActivityStrike, camp.Tent.ID, stand, 0, now)
			actor.SourceActivity = act
			emitCampBeatStarted(w, actor, act, now)
			return CampStartResult{TentID: camp.Tent.ID, DoneAt: now.Add(w.Settings.strikeTotal())}, nil
		},
	}
}

// landCampBeat lands a finished pitch or strike beat (the window is already
// cleared) and arms the next one, or finishes the work. Reports whether a
// completion went out; false (the tent is gone) makes the caller cancel.
func landCampBeat(w *World, actor *Actor, act *SourceActivity, now time.Time) bool {
	tent := w.VillageObjects[act.ObjectID]
	if !IsTent(tent) || tent.OwnerActorID != actor.ID {
		return false
	}
	stand := campStandTile(tent)
	p := campBeat(act.Kind, act.StepsDone)
	if act.Kind == SourceActivityPitch {
		if p.Beat == CampBeatTent {
			raiseTent(w, actor, tent)
		} else {
			placeCampPiece(w, actor.ID, stand, p)
		}
	} else {
		if p.Beat == CampBeatTent {
			dismantleCamp(w, actor.ID, tent.ID)
			returnTentToPack(actor)
		} else if obj := FindCamp(w.VillageObjects, actor.ID).piece(p.Beat); obj != nil {
			removeCampObject(w, obj.ID, now)
		}
	}

	next := act.StepsDone + 1
	more := next < act.Steps
	w.emit(&SourceActivityCompleted{
		ActorID:   actor.ID,
		ObjectID:  act.ObjectID,
		Kind:      act.Kind,
		Continues: more,
		At:        now,
	})
	if !more {
		return true
	}
	nextAct := campBeatWindow(w, act.Kind, act.ObjectID, stand, next, now)
	actor.SourceActivity = nextAct
	// Striking: the tent drops to its bundle as its own beat begins.
	if act.Kind == SourceActivityStrike && campBeat(act.Kind, next).Beat == CampBeatTent && tent.CurrentState != TentStatePacked {
		setVillageObjectStateInline(w, tent, TentStatePacked)
	}
	emitCampBeatStarted(w, actor, nextAct, now)
	return true
}

// undoCampWork reverses an abandoned pitch or strike (a no-op for any other
// kind): a camp that was going up comes down and the tent goes back in the pack;
// a camp that was coming down stands whole again.
func undoCampWork(w *World, actor *Actor, act *SourceActivity, now time.Time) {
	if act == nil || (act.Kind != SourceActivityPitch && act.Kind != SourceActivityStrike) {
		return
	}
	tent := w.VillageObjects[act.ObjectID]
	if !IsTent(tent) || tent.OwnerActorID != actor.ID {
		return
	}
	if act.Kind == SourceActivityPitch {
		dismantleCamp(w, actor.ID, tent.ID)
		returnTentToPack(actor)
		return
	}
	restoreCamp(w, actor.ID, tent)
}

// raiseTent stands the bundle: the tent becomes a structure, the player's home.
func raiseTent(w *World, actor *Actor, tent *VillageObject) {
	setVillageObjectStateInline(w, tent, TentStateClosed)
	sid := StructureID(tent.ID)
	if _, ok := w.Structures[sid]; !ok {
		name := actor.DisplayName + "'s Tent"
		if _, err := PromoteObjectToStructure(tent.ID, name, nil).Fn(w); err != nil {
			// The display name can only fail on length; the catalog name never does.
			if _, err := PromoteObjectToStructure(tent.ID, "", nil).Fn(w); err != nil {
				log.Printf("sim/camp: %q's tent %s could not become a structure: %v", actor.ID, tent.ID, err)
				return
			}
		}
	}
	actor.HomeStructureID = sid
	refreshStructureOccupancyState(w, sid)
}

// placeCampPiece puts down one prop of the camp pitched from stand.
func placeCampPiece(w *World, ownerID ActorID, stand TilePos, p campPiece) *VillageObject {
	asset := w.Assets[p.Asset]
	if asset == nil {
		return nil
	}
	state := asset.DefaultState
	if p.Asset == CampFireAssetID {
		state = CampFireStateUnlit
	}
	obj := newVillageObject(p.Asset, asset, campPiecePos(stand, p), "", "", state)
	obj.OwnerActorID = ownerID
	registerVillageObject(w, obj, asset)
	return obj
}

// restoreCamp stands a half-struck camp back up: the tent unpacked (the flap
// follows its occupancy) and any missing prop put back, the fire unlit.
func restoreCamp(w *World, ownerID ActorID, tent *VillageObject) {
	if tent.CurrentState == TentStatePacked {
		setVillageObjectStateInline(w, tent, TentStateClosed)
		refreshStructureOccupancyState(w, StructureID(tent.ID))
	}
	camp := FindCamp(w.VillageObjects, ownerID)
	stand := campStandTile(tent)
	for _, p := range campLayout[1:] {
		if camp.piece(p.Beat) == nil {
			placeCampPiece(w, ownerID, stand, p)
		}
	}
}

// dismantleCamp takes the whole camp off the map: every prop, then the tent's
// structure (anyone inside steps out, the home and every door-side record of it
// go) and the tent itself. The checkpoint prunes the rows on its next sweep.
func dismantleCamp(w *World, ownerID ActorID, tentID VillageObjectID) {
	now := time.Now().UTC()
	if camp := FindCamp(w.VillageObjects, ownerID); camp != nil {
		for _, obj := range []*VillageObject{camp.Woodpile, camp.Stool, camp.Fire} {
			if obj != nil {
				removeCampObject(w, obj.ID, now)
			}
		}
	}
	sid := StructureID(tentID)
	if _, ok := w.Structures[sid]; ok {
		for id := range w.actorsByStructure[sid] {
			if a := w.Actors[id]; a != nil {
				setActorInsideStructure(w, a, "")
			}
		}
		delete(w.actorsByStructure, sid)
		for _, a := range w.Actors {
			if a != nil && a.HomeStructureID == sid {
				a.HomeStructureID = ""
			}
		}
		for id, n := range w.DoorNotes {
			if n != nil && n.StructureID == sid {
				delete(w.DoorNotes, id)
			}
		}
		delete(w.GuestPasses, sid)
		delete(w.carryoverByStructure, sid)
		delete(w.Structures, sid)
	}
	if _, ok := w.VillageObjects[tentID]; ok {
		removeCampObject(w, tentID, now)
	}
}

// removeCampObject deletes one camp object (and any overlay hung on it).
func removeCampObject(w *World, id VillageObjectID, now time.Time) {
	for _, rid := range deleteObjectCascade(w, id) {
		w.emit(&VillageObjectDeleted{ObjectID: rid, At: now})
	}
}

// returnTentToPack puts the tent back in the player's pack.
func returnTentToPack(actor *Actor) {
	if actor.Inventory == nil {
		actor.Inventory = map[ItemKind]int{}
	}
	actor.Inventory[TentItemKind]++
}

// checkCampGround is the pitch gate on the ground at stand: every tile of the
// camp is open grass, inside the map, walkable and clear of any object and of
// anyone standing there; none is beside a doorway; and the stand tile is not
// shut in a pen.
func checkCampGround(w *World, actor *Actor, stand TilePos) error {
	tiles := campTiles(w, stand)
	if w.Terrain == nil || len(w.Terrain.Data) != MapW*MapH {
		return ErrCampNotGrass
	}
	for _, t := range tiles {
		if t.X < 0 || t.X >= MapW || t.Y < 0 || t.Y >= MapH {
			return ErrCampBlocked
		}
		switch w.Terrain.Data[t.Y*MapW+t.X] {
		case TerrainLightGrass, TerrainDarkGrass:
		default:
			return ErrCampNotGrass
		}
	}
	grid, err := buildWalkGrid(w)
	if err != nil {
		return err
	}
	taken := objectFootprintTiles(w)
	for _, t := range tiles {
		if !grid.CanWalk(t.X, t.Y) || taken[t] {
			return ErrCampBlocked
		}
	}
	occupied := make(map[TilePos]bool, len(tiles))
	for _, t := range tiles {
		occupied[t] = true
	}
	for id := range w.outdoorActors {
		if a := w.Actors[id]; a != nil && a.ID != actor.ID && occupied[a.Pos] {
			return ErrCampBlocked
		}
	}
	for sid := range w.Structures {
		door, ok := structureEntryTile(w, sid)
		if !ok {
			continue
		}
		for _, t := range tiles {
			if t.Chebyshev(door) <= 1 {
				return ErrCampNearDoor
			}
		}
	}
	if campShutIn(w, stand) {
		return ErrCampInPen
	}
	return nil
}

// campTiles lists every tile the camp at stand would cover: the stand tile, the
// tent's footprint and each prop's tile.
func campTiles(w *World, stand TilePos) []TilePos {
	tiles := []TilePos{stand}
	for _, p := range campLayout {
		at := TilePos{X: stand.X + p.Offset.X, Y: stand.Y + p.Offset.Y}
		asset := w.Assets[p.Asset]
		if asset == nil {
			tiles = append(tiles, at)
			continue
		}
		for y := at.Y - asset.FootprintTop; y <= at.Y+asset.FootprintBottom; y++ {
			for x := at.X - asset.FootprintLeft; x <= at.X+asset.FootprintRight; x++ {
				tiles = append(tiles, TilePos{X: x, Y: y})
			}
		}
	}
	return tiles
}

// objectFootprintTiles is every tile a placed object stands on — its footprint,
// obstacle or not, so a tent never goes up over a flower bed or a signpost.
// Overlays hang on their parent and add nothing.
func objectFootprintTiles(w *World) map[TilePos]bool {
	taken := make(map[TilePos]bool)
	for _, obj := range w.VillageObjects {
		if obj == nil || obj.AttachedTo != "" {
			continue
		}
		a := obj.Pos.Tile()
		asset := w.Assets[obj.AssetID]
		if asset == nil {
			taken[a] = true
			continue
		}
		for y := a.Y - asset.FootprintTop; y <= a.Y+asset.FootprintBottom; y++ {
			for x := a.X - asset.FootprintLeft; x <= a.X+asset.FootprintRight; x++ {
				taken[TilePos{X: x, Y: y}] = true
			}
		}
	}
	return taken
}

// campShutIn reports whether stand is closed in — a pen, its gates shut. A flood
// fill over the grazer grid (the walk grid with gates closed) that runs out
// before campPenProbeTiles tiles has found the inside of a fence.
func campShutIn(w *World, stand TilePos) bool {
	grid, err := buildGrazerWalkGrid(w)
	if err != nil {
		return false
	}
	seen := map[TilePos]bool{stand: true}
	queue := []TilePos{stand}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, d := range [4]TilePos{{X: 0, Y: -1}, {X: 0, Y: 1}, {X: -1, Y: 0}, {X: 1, Y: 0}} {
			n := TilePos{X: cur.X + d.X, Y: cur.Y + d.Y}
			if seen[n] || !grid.CanWalk(n.X, n.Y) {
				continue
			}
			seen[n] = true
			if len(seen) >= campPenProbeTiles {
				return false
			}
			queue = append(queue, n)
		}
	}
	return true
}

// LightCampFire lights the player's camp fire — the landing of the client's
// fire game. The player stands by the ring, and the fire is out.
func LightCampFire(actorID ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("LightCampFire: actor %q not in world", actorID)
			}
			if err := campWorkGate(w, actor, now); err != nil {
				return nil, err
			}
			camp := FindCamp(w.VillageObjects, actorID)
			if camp == nil {
				return nil, ErrCampNotPitched
			}
			if camp.Fire == nil {
				return nil, ErrCampNoFire
			}
			if actor.Pos.Chebyshev(camp.Fire.Pos.Tile()) > 2 {
				return nil, ErrCampNotAtFire
			}
			if camp.Fire.HearthLitUntil.After(now) {
				return nil, ErrCampFireLit
			}
			burn, dying := w.Settings.campFireMinutes()
			out := now.Add(burn + dying)
			camp.Fire.HearthLitUntil = out
			setCampFireState(w, camp.Fire, now)
			return LightCampFireResult{FireID: camp.Fire.ID, BurnsUntil: out.Add(-dying), OutAt: out}, nil
		},
	}
}

// campFireMinutes is how long a lit fire burns, then dies down.
func (s WorldSettings) campFireMinutes() (burn, dying time.Duration) {
	b, d := s.CampFireBurnMinutes, s.CampFireDyingMinutes
	if b <= 0 {
		b = DefaultCampFireBurnMinutes
	}
	if d < 0 {
		d = 0
	}
	return time.Duration(b) * time.Minute, time.Duration(d) * time.Minute
}

// CampFireState is the state a camp fire shows at now: unlit until first lit,
// then burning, dying down for its last stretch, and ashes once out.
func CampFireState(fire *VillageObject, now time.Time, dying time.Duration) string {
	switch {
	case fire.HearthLitUntil.IsZero():
		return CampFireStateUnlit
	case !fire.HearthLitUntil.After(now):
		return CampFireStateAshes
	case fire.HearthLitUntil.Sub(now) <= dying:
		return CampFireStateDying
	}
	return CampFireStateBurning
}

// CampFireLit reports whether the fire is burning or dying down at now.
func CampFireLit(fire *VillageObject, now time.Time) bool {
	return fire != nil && fire.HearthLitUntil.After(now)
}

func setCampFireState(w *World, fire *VillageObject, now time.Time) {
	_, dying := w.Settings.campFireMinutes()
	if want := CampFireState(fire, now, dying); fire.CurrentState != want {
		setVillageObjectStateInline(w, fire, want)
	}
}

// UpdateCampFires turns every camp fire's sprite to its state on the clock.
// Run from the room sweep, once a minute.
func UpdateCampFires(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			n := 0
			for _, obj := range w.VillageObjects {
				if obj == nil || obj.AssetID != CampFireAssetID {
					continue
				}
				before := obj.CurrentState
				setCampFireState(w, obj, now)
				if obj.CurrentState != before {
					n++
				}
			}
			return n, nil
		},
	}
}

// pcRestFloor is the tiredness a sleeping player's rest stops at. A bed (or any
// home that isn't a tent) rests them fully (0). Their own tent stops at
// TentRestFloor, or TentRestFloorFire when their camp fire was burning or dying
// down when they lay down. Bed-down is read back off the wake cap
// (executePCSleep sets SleepingUntil = bed-down + the cap), so the answer holds
// across a restart without a stamp of its own; the fire cannot be lit again
// while the player sleeps, as any action wakes them first.
func pcRestFloor(w *World, pc *Actor) int {
	if pc == nil || pc.SleepingUntil == nil || pc.InsideStructureID == "" || pc.InsideStructureID != pc.HomeStructureID {
		return 0
	}
	if !IsTent(w.VillageObjects[VillageObjectID(pc.InsideStructureID)]) {
		return 0
	}
	bedDown := pc.SleepingUntil.Add(-DefaultPCSleepMaxDurationHours * time.Hour)
	if camp := FindCamp(w.VillageObjects, pc.ID); camp != nil && CampFireLit(camp.Fire, bedDown) {
		return w.Settings.TentRestFloorFire
	}
	return w.Settings.TentRestFloor
}

// CampPlaceLabel names where a tent stands for the player — "by the Inn", the
// nearest named building that isn't another tent — or "" when the village has
// none.
func CampPlaceLabel(objects map[VillageObjectID]*VillageObject, structures map[StructureID]*Structure, tent *VillageObject) string {
	if tent == nil {
		return ""
	}
	here := tent.Pos.Tile()
	var best string
	var bestID VillageObjectID
	bestDist := -1
	for id, o := range objects {
		if o == nil || id == tent.ID || o.DisplayName == "" || IsTent(o) {
			continue
		}
		if _, ok := structures[StructureID(id)]; !ok {
			continue
		}
		d := here.Chebyshev(o.Pos.Tile())
		if bestDist < 0 || d < bestDist || (d == bestDist && id < bestID) {
			best, bestID, bestDist = o.DisplayName, id, d
		}
	}
	if best == "" {
		return ""
	}
	return "by " + WithDefiniteArticle(best)
}

// settleUnfinishedCampsOnLoad finishes what a restart cut off. Pitch and strike
// windows are not checkpointed, so a camp can load half done: a packed tent that
// is nobody's home is a pitch that never finished — it comes down and the tent
// goes back in the pack; a tent that is its owner's home (a strike under way, or
// a pitch whose tent had stood) is made whole. Runs in FinalizeLoad.
func settleUnfinishedCampsOnLoad(w *World) int {
	var tents []*VillageObject
	for _, obj := range w.VillageObjects {
		if IsTent(obj) {
			tents = append(tents, obj)
		}
	}
	settled := 0
	for _, tent := range tents {
		owner := w.Actors[tent.OwnerActorID]
		if owner != nil && owner.HomeStructureID == StructureID(tent.ID) {
			camp := FindCamp(w.VillageObjects, owner.ID)
			if tent.CurrentState == TentStatePacked || camp.Fire == nil || camp.Stool == nil || camp.Woodpile == nil {
				restoreCamp(w, owner.ID, tent)
				settled++
			}
			continue
		}
		if tent.CurrentState != TentStatePacked && owner != nil {
			continue
		}
		dismantleCamp(w, tent.OwnerActorID, tent.ID)
		if owner != nil {
			returnTentToPack(owner)
		}
		settled++
	}
	return settled
}

// useCampItem runs the camp's Your Pack actions (the LLM-800 item actions):
// Pitch on the carried tent, Strike on the pitched one. A pitched tent is out
// of the pack, so this is answered before UseItem's carrying check. handled is
// false for anything that is not a camp action; a refusal is an item-action
// refusal the pack shows as it is.
func useCampItem(w *World, a *Actor, kind ItemKind, action string, now time.Time) (handled bool, err error) {
	if kind != TentItemKind {
		return false, nil
	}
	switch action {
	case ItemActionPitch:
		_, err = PitchTent(a.ID, now).Fn(w)
	case ItemActionStrike:
		_, err = StrikeTent(a.ID, now).Fn(w)
	default:
		return false, nil
	}
	if err != nil {
		msg := err.Error()
		if msg != "" {
			msg = strings.ToUpper(msg[:1]) + msg[1:]
		}
		return true, refuseItemAction(msg)
	}
	return true, nil
}
