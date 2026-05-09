package main

// Visitor archetype — transient VAs that arrive in the village, hang
// around for hours-to-days, deliver content (news / rumor / letter /
// goods / quest_hook), then depart. Replaces the chronicler's deleted
// role of injecting "outside news" into the village. Full design at
// shared/tasks/pending/zbbs-work-201-visitor-archetype/design.
//
// Phase 1 (this file): spawn / despawn / cleanup machinery, gated
// behind a spawn-chance setting that defaults to 0. The framework
// lands so the migration goes in cleanly; later phases wire payloads,
// the perception cue, and the salem-visitor memory-api agent template.
//
// The feature is gated by default — spawn chance is 0 out of the box,
// so an admin opts in by setting visitor_spawn_chance_permille > 0.
// Spawn coordinates are picked dynamically per spawn (see
// pickVisitorEdgeTile). Sprites are mapped per archetype in the
// visitorArchetypeSprite table below — every archetype in
// visitorArchetypePool MUST have a corresponding sprite entry, or the
// engine refuses to start (see init() at the bottom of this file).
//
// Three handlers run in runServerTickOnce in this order:
//   dispatchVisitorDespawn — start expired visitors walking back
//   dispatchVisitorCleanup — delete visitors past the grace window
//   dispatchVisitorSpawn   — probabilistically spawn a new visitor
// The despawn-before-cleanup ordering ensures a visitor that just
// expired this tick gets a chance to start its return walk before
// being eligible for hard deletion (which only fires after the grace
// window past expires_at, so first-tick deletion is impossible
// regardless of order).

import (
    "context"
    "database/sql"
    "fmt"
    "log"
    "math/rand"
    "strings"
    "time"
)

const (
    // Default permille (per-thousand) chance that a new visitor spawns
    // on any given server tick. 0 disables spawn entirely (the default,
    // so the feature is no-op until an admin opts in). At server tick
    // interval = 60s, a value of 1 means roughly 1 spawn per 1000
    // minutes (~16h game-time), so a setting of ~10-30 produces "one
    // visitor per game day on average" once we know the desired
    // cadence.
    defaultVisitorSpawnChancePermille = 0

    // Default stay-window bounds in minutes. Concrete value picked at
    // spawn time as a uniform random pull from [min, max].
    defaultVisitorMinStayMinutes = 240
    defaultVisitorMaxStayMinutes = 1440

    // Cap on simultaneous visitors. Cheap operator dial — set to 0 to
    // halt spawn even with chance > 0.
    defaultVisitorMaxConcurrent = 2

    // Grace window past expires_at before hard-delete. Lets the
    // departure walk complete (or fail-and-stall) before we yank the
    // row. 5 min covers a cross-village walk at default speed.
    visitorCleanupGraceMinutes = 5

    // Shared memory-api VA slug that every visitor's actor row points
    // at. The VA is provisioned once at operator setup with
    // dream_mode='none' (no soul / dreams / context-people / concerns
    // accumulate). Per-visitor identity is engine-injected per call
    // via the visitor self-identity preface in buildAgentPerception
    // and scene-scoped chat history; the VA itself stays stateless
    // across visitors. Phase 4 returner romance state lives engine-
    // side in recurring_visitor (TBD) and gets injected as prose, not
    // accumulated as VA-side learnings.
    visitorAgentName = "salem-visitor"

    // Maximum tile depth the edge-scan probes inward when picking a
    // visitor spawn or departure tile. Capped to keep visitors looking
    // like they're arriving from outside rather than appearing on a
    // road deep in the village. 30 tiles is roughly 1/6 of map width
    // (200 tiles) — enough slack for villages with a setback approach
    // road, tight enough that "from outside" still reads.
    visitorEdgeScanMaxDepth = 30

    // Maximum profile re-rolls when scrubbing visitor surnames against
    // seated actors. Five tries is enough headroom in practice — the
    // pool has 15 names and Salem currently has ~5 surnamed villagers,
    // so any single roll has ~33% odds of collision and 5 independent
    // tries push the residual collision rate well under 1%.
    surnameScrubMaxTries = 5

    // Perception-radius for the "Visitors here" cue. Within this
    // bounding box of the perceiver, a transient visitor is named
    // with their archetype + origin + disposition instead of the
    // generic "a stranger" descriptor from coLocatedHuddleMembers.
    // 80 px ≈ 2.5 tiles at default sprite scale — covers same-tile,
    // adjacent, and one-step-away. Persistent NPCs are not surfaced
    // by this block (they're handled by the existing co-located
    // perception sections).
    visitorPerceptionRadius = 80
)

// dispatchVisitorSpawn is the per-server-tick spawn handler. Reads
// settings, rolls the spawn chance, checks the concurrent cap,
// generates persona slots, looks up the archetype-mapped sprite,
// picks an edge approach tile, inserts the actor row, and starts the
// entry walk to a destination structure.
//
// Single gate: visitor_spawn_chance_permille = 0 (the default) blocks
// spawn entirely; bumping it above 0 opts the village into visitors.
// Other failure paths (no sprite row, no destination, no edge tile)
// log and skip the cycle.
//
// Concurrency: relies on runServerTickOnce being single-threaded per
// engine process (the existing invariant — one ticker, sequential
// handler dispatch). No DB-side advisory lock or capacity-claim row.
// If a future deploy runs multiple engine processes against one
// database, this handler would double-provision: two processes both
// roll a spawn-chance hit on the same minute and each create their
// own memory-api agent + actor row. Fix at that time would be a
// pg_advisory_xact_lock around the count + insert. Not a problem
// today.
func (app *App) dispatchVisitorSpawn(ctx context.Context) {
    chance := app.loadNonNegativeIntSetting(ctx, "visitor_spawn_chance_permille", defaultVisitorSpawnChancePermille)
    if chance == 0 {
        return
    }
    if chance > 1000 {
        chance = 1000
    }
    if rand.Intn(1000) >= chance {
        return
    }

    maxConcurrent := app.loadNonNegativeIntSetting(ctx, "visitor_max_concurrent", defaultVisitorMaxConcurrent)
    if maxConcurrent == 0 {
        return
    }

    var current int
    if err := app.DB.QueryRow(ctx,
        `SELECT COUNT(*) FROM actor WHERE visitor_expires_at IS NOT NULL`,
    ).Scan(&current); err != nil {
        log.Printf("visitor-spawn: count: %v", err)
        return
    }
    if current >= maxConcurrent {
        return
    }

    // Generate the persona, scrubbing the surname against every seated
    // actor (NPC + PC, excluding other visitors). If a roll collides,
    // re-roll up to surnameScrubMaxTries times. After the cap, ship
    // anyway with a log warning — a duplicate surname is a UX wrinkle,
    // not a correctness failure.
    existing := app.loadActorSurnames(ctx)
    var profile visitorProfile
    for tries := 0; tries < surnameScrubMaxTries; tries++ {
        profile = generateVisitorProfile()
        if !existing[extractSurname(profile.Name)] {
            break
        }
    }
    if existing[extractSurname(profile.Name)] {
        log.Printf("visitor-spawn: surname for %q still collides with a seated actor after %d tries; shipping anyway",
            profile.Name, surnameScrubMaxTries)
    }

    // Sprite is keyed off archetype, not a global setting — different
    // archetypes get visually-distinct sprites. The init() check below
    // guarantees every archetype in the pool has a mapping, so this
    // lookup can't fail by missing key. The DB lookup CAN still fail
    // if the sprite name doesn't match an npc_sprite row (e.g. a
    // mapping points at a sprite that ZBBS-055-style cleanup later
    // dropped); that's a deploy / migration error, not a runtime gate.
    spriteName := visitorArchetypeSprite[profile.Archetype]
    var spriteID string
    if err := app.DB.QueryRow(ctx,
        `SELECT id::text FROM npc_sprite WHERE name = $1 LIMIT 1`,
        spriteName,
    ).Scan(&spriteID); err != nil {
        log.Printf("visitor-spawn: sprite %q (for archetype %q) not found: %v", spriteName, profile.Archetype, err)
        return
    }

    // Pick the destination first so we can validate the edge spawn
    // tile is path-connected to it. If no destination structure is
    // placed, abort the spawn rather than dropping a stranded visitor.
    destStructureID, destX, destY, ok := app.pickVisitorDestination(ctx)
    if !ok {
        log.Printf("visitor-spawn: no destination structure placed; spawn skipped")
        return
    }

    spawnX, spawnY, ok := app.pickVisitorEdgeTile(ctx, destX, destY)
    if !ok {
        log.Printf("visitor-spawn: no valid edge tile this cycle; spawn skipped")
        return
    }

    minStay := app.loadNonNegativeIntSetting(ctx, "visitor_min_stay_minutes", defaultVisitorMinStayMinutes)
    maxStay := app.loadNonNegativeIntSetting(ctx, "visitor_max_stay_minutes", defaultVisitorMaxStayMinutes)
    if maxStay < minStay {
        maxStay = minStay
    }
    stayMinutes := minStay
    if maxStay > minStay {
        stayMinutes = minStay + rand.Intn(maxStay-minStay)
    }
    expiresAt := time.Now().Add(time.Duration(stayMinutes) * time.Minute)

    // display_name must be unique across the actor table. Construct as
    // "Name the Archetype" and append a (visitor) suffix on collision —
    // collisions with persistent NPCs are unlikely given the period
    // names but possible across simultaneous visitors with the same pull.
    displayName := fmt.Sprintf("%s the %s", profile.Name, profile.Archetype)
    var existing sql.NullString
    _ = app.DB.QueryRow(ctx,
        `SELECT display_name FROM actor WHERE display_name = $1 LIMIT 1`,
        displayName,
    ).Scan(&existing)
    if existing.Valid {
        // Suffix with the spawn timestamp to disambiguate. Compact and
        // stable for the visitor's lifetime.
        displayName = fmt.Sprintf("%s the %s (%d)", profile.Name, profile.Archetype, time.Now().Unix()%10000)
    }

    // LLM-backing wiring. Every visitor's actor row points at the
    // shared visitorAgentName VA on memory-api. Identity comes from
    // engine-injected per-call context (persona slots in the perception
    // preface, scene-scoped chat history) — the VA itself is stateless
    // (dream_mode='none'), so memory and learnings don't accumulate
    // across visitors. Provisioning the VA is a one-time operator-
    // setup step on memory-api; engine-side has no admin calls.
    var visitorID string
    err := app.DB.QueryRow(ctx,
        `INSERT INTO actor (
            display_name, sprite_id, current_x, current_y, facing,
            visitor_expires_at, visitor_archetype, visitor_origin, visitor_disposition,
            llm_memory_agent
         ) VALUES ($1, $2, $3, $4, 'south', $5, $6, $7, $8, $9)
         RETURNING id::text`,
        displayName, spriteID, spawnX, spawnY,
        expiresAt, profile.Archetype, profile.Origin, profile.Disposition,
        visitorAgentName,
    ).Scan(&visitorID)
    if err != nil {
        log.Printf("visitor-spawn: insert: %v", err)
        return
    }

    log.Printf("visitor-spawn: %s (id=%s, archetype=%s, origin=%s, disposition=%s, stay=%dm, agent=%s, spawn=(%.0f,%.0f))",
        displayName, visitorID, profile.Archetype, profile.Origin, profile.Disposition, stayMinutes, visitorAgentName, spawnX, spawnY)

    // Use startReturnWalk (npc_behaviors.go) instead of bare startNPCWalk
    // so the walk is wrapped in an npcRoute with EnterOnArrival=true and
    // markWalkTargetStructure is called for us. Without that wrapping,
    // applyArrival has no targetStructureID to anchor the loiter-arrival
    // huddle / cascade logic on, advanceBehavior never flips the visitor
    // inside, and the visitor sits at the loiter slot indefinitely with
    // a perception that gives the LLM no useful context to act on.
    npc := &behaviorNPC{ID: visitorID, CurX: spawnX, CurY: spawnY}
    if err := app.startReturnWalk(ctx, npc, destX, destY, destStructureID, "visitor-spawn", true); err != nil {
        log.Printf("visitor-spawn: startReturnWalk for %s: %v", displayName, err)
    }
}

// dispatchVisitorDespawn finds visitors whose stay window has expired
// and who are not already on a walk, then starts them walking toward
// a fresh edge tile picked via pickVisitorEdgeTile. Each visitor may
// exit a different edge than they arrived on — narratively this reads
// as "wandered off down the road," not "retraced their steps."
//
// A visitor whose return walk completes before the cleanup grace
// window may be redispatched on the next tick to a new edge tile;
// that's wasteful but bounded — cleanup hard-deletes the row a few
// minutes past expires_at regardless of position.
func (app *App) dispatchVisitorDespawn(ctx context.Context) {
    rows, err := app.DB.Query(ctx,
        `SELECT id::text FROM actor
         WHERE visitor_expires_at IS NOT NULL
           AND visitor_expires_at <= NOW()`,
    )
    if err != nil {
        log.Printf("visitor-despawn: query: %v", err)
        return
    }
    defer rows.Close()

    var pending []string
    for rows.Next() {
        var id string
        if err := rows.Scan(&id); err == nil {
            pending = append(pending, id)
        }
    }
    if len(pending) == 0 {
        return
    }

    app.NPCMovement.mu.Lock()
    walking := make(map[string]bool, len(app.NPCMovement.active))
    for id := range app.NPCMovement.active {
        walking[id] = true
    }
    app.NPCMovement.mu.Unlock()

    // Connectivity anchor for the edge-tile picker. If no destination
    // structure is placed, visitors stay put — cleanup will collect
    // them after the grace window.
    _, destX, destY, ok := app.pickVisitorDestination(ctx)
    if !ok {
        return
    }

    for _, id := range pending {
        if walking[id] {
            continue
        }
        edgeX, edgeY, ok := app.pickVisitorEdgeTile(ctx, destX, destY)
        if !ok {
            log.Printf("visitor-despawn: no edge tile available for %s; will be cleaned up after grace", id)
            continue
        }
        // startReturnWalk's setNPCInside(false, "") at the head of the
        // function gets the visitor out of the tavern (or whatever
        // structure they entered on arrival) before the walk to the
        // edge starts. EnterOnArrival=false leaves them outside at the
        // edge tile when the walk completes — cleanup collects the
        // row a few minutes later.
        npc := &behaviorNPC{ID: id}
        if err := app.startReturnWalk(ctx, npc, edgeX, edgeY, "", "visitor-despawn", false); err != nil {
            // No path is the typical case for a visitor stranded
            // somewhere unreachable. Cleanup will hard-delete past
            // the grace window regardless.
            log.Printf("visitor-despawn: startReturnWalk %s: %v", id, err)
        }
    }
}

// dispatchVisitorCleanup hard-deletes visitor rows whose expires_at
// passed more than visitorCleanupGraceMinutes ago. Position-agnostic
// — a visitor stranded with no walk path still gets cleaned up after
// the grace window so we don't leak rows. Foreign-key cascades on
// agent_action_log and npc_acquaintance handle engine-side dependent
// rows. The shared salem-visitor VA on memory-api is not touched —
// it's persistent infrastructure, not per-visitor state.
func (app *App) dispatchVisitorCleanup(ctx context.Context) {
    cutoff := time.Now().Add(-time.Duration(visitorCleanupGraceMinutes) * time.Minute)
    res, err := app.DB.Exec(ctx,
        `DELETE FROM actor
         WHERE visitor_expires_at IS NOT NULL
           AND visitor_expires_at <= $1`,
        cutoff,
    )
    if err != nil {
        log.Printf("visitor-cleanup: delete: %v", err)
        return
    }
    if n := res.RowsAffected(); n > 0 {
        log.Printf("visitor-cleanup: deleted %d expired visitor row(s)", n)
    }
}

// pickVisitorEdgeTile picks a road tile near a randomly-chosen map
// edge for a visitor to spawn or depart on. Algorithm:
//
//  1. Shuffle the four edges (top / bottom / left / right).
//  2. For each edge in order, sweep depth 0 .. visitorEdgeScanMaxDepth
//     perpendicular to the edge. At each depth, collect tiles whose
//     raw terrain byte is 1 (dirt) or 4 (cobblestone) — i.e. roads.
//     Shuffle the candidates at that depth and return the first one
//     that's both walkable in the obstacle-aware walkGrid AND path-
//     connected to (anchorX, anchorY) via findPathToAdjacent.
//  3. If no edge yields a valid candidate within the depth cap,
//     return ok=false. Caller skips this spawn cycle (or abandons
//     the despawn walk and lets cleanup collect the visitor).
//
// Edges blocked entirely by impassable terrain — Salem's north edge
// has continuous water, for example — are skipped naturally: zero
// road candidates appear at any depth, so the algorithm rotates to
// the next shuffled edge without special-casing.
//
// anchorX / anchorY are world-pixel coords used for the connectivity
// check. Typical anchor is the tavern's loiter point; the building
// tile itself is impassable so we use findPathToAdjacent to validate
// against an adjacent walkable tile.
func (app *App) pickVisitorEdgeTile(ctx context.Context, anchorX, anchorY float64) (float64, float64, bool) {
    var terrain []byte
    if err := app.DB.QueryRow(ctx,
        `SELECT data FROM village_terrain WHERE id = 1`,
    ).Scan(&terrain); err != nil {
        log.Printf("visitor-edge-tile: load terrain: %v", err)
        return 0, 0, false
    }
    if len(terrain) != mapW*mapH {
        log.Printf("visitor-edge-tile: terrain size mismatch: got %d, want %d", len(terrain), mapW*mapH)
        return 0, 0, false
    }

    g, err := app.loadWalkGrid(ctx)
    if err != nil {
        log.Printf("visitor-edge-tile: load walk grid: %v", err)
        return 0, 0, false
    }

    anchorTileX, anchorTileY := worldToTile(anchorX, anchorY)
    anchor := gridPoint{anchorTileX, anchorTileY}

    isRoadByte := func(b byte) bool { return b == 1 || b == 4 }

    // Each edge is described by a function that maps (depth, along)
    // → tile coords, plus the length of the "along" axis. depth=0 is
    // the literal edge row/column.
    type edgeMap struct {
        coord    func(depth, along int) gridPoint
        alongLen int
    }
    edges := []edgeMap{
        {func(d, a int) gridPoint { return gridPoint{a, d} }, mapW},               // top
        {func(d, a int) gridPoint { return gridPoint{a, mapH - 1 - d} }, mapW},    // bottom
        {func(d, a int) gridPoint { return gridPoint{d, a} }, mapH},               // left
        {func(d, a int) gridPoint { return gridPoint{mapW - 1 - d, a} }, mapH},    // right
    }
    rand.Shuffle(len(edges), func(i, j int) { edges[i], edges[j] = edges[j], edges[i] })

    for _, e := range edges {
        for depth := 0; depth < visitorEdgeScanMaxDepth; depth++ {
            var candidates []gridPoint
            for along := 0; along < e.alongLen; along++ {
                p := e.coord(depth, along)
                if isRoadByte(terrain[p.Y*mapW+p.X]) {
                    candidates = append(candidates, p)
                }
            }
            if len(candidates) == 0 {
                continue
            }
            rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
            for _, c := range candidates {
                if !g.canWalk(c.X, c.Y) {
                    continue
                }
                if path, _ := findPathToAdjacent(g, c, anchor); path != nil {
                    pt := tileToWorld(c.X, c.Y)
                    return pt.X, pt.Y, true
                }
            }
        }
    }
    return 0, 0, false
}

// pickVisitorDestination picks a structure for a freshly-spawned
// visitor to walk to. Prefers the tavern (the village's natural
// gathering point for outsiders); falls back to any tagged structure;
// returns ok=false if the village has no destinations placed.
//
// Returns the structure's id alongside its anchor coords so the caller
// can pass it to startReturnWalk for arrival-time inside-flip and the
// loiter-arrival huddle / cascade machinery in applyArrival.
//
// Tavern selection mirrors the pc/create lodging lookup pattern in
// pc_handlers.go — JOIN to village_object_tag with tag='tavern',
// oldest placement wins.
func (app *App) pickVisitorDestination(ctx context.Context) (string, float64, float64, bool) {
    var id string
    var x, y float64
    err := app.DB.QueryRow(ctx,
        `SELECT o.id::text, o.x, o.y FROM village_object o
         JOIN village_object_tag vot ON vot.object_id = o.id AND vot.tag = 'tavern'
         ORDER BY o.created_at ASC
         LIMIT 1`,
    ).Scan(&id, &x, &y)
    if err == nil {
        return id, x, y, true
    }
    if err != sql.ErrNoRows {
        log.Printf("visitor-dest: tavern lookup: %v", err)
    }

    // Fallback: any tagged structure (the village_object_tag join
    // filters out un-tagged decorative props, which we don't want
    // visitors walking to).
    err = app.DB.QueryRow(ctx,
        `SELECT o.id::text, o.x, o.y FROM village_object o
         JOIN village_object_tag vot ON vot.object_id = o.id
         ORDER BY random() LIMIT 1`,
    ).Scan(&id, &x, &y)
    if err == nil {
        return id, x, y, true
    }
    return "", 0, 0, false
}

// loadActorSurnames builds a set of last-token surnames from every
// non-visitor actor's display_name. Used by dispatchVisitorSpawn to
// keep new visitors from arriving with a surname that collides with
// a seated villager (Crane, Thorne, Ward, etc.) or PC. Built fresh
// each spawn so admin-added NPCs are picked up automatically with no
// engine restart. Visitors themselves are excluded so two visitors
// don't collide-check against each other when the second one rolls.
func (app *App) loadActorSurnames(ctx context.Context) map[string]bool {
    surnames := map[string]bool{}
    rows, err := app.DB.Query(ctx,
        `SELECT display_name FROM actor WHERE visitor_expires_at IS NULL`,
    )
    if err != nil {
        log.Printf("visitor-spawn: loadActorSurnames: %v", err)
        return surnames
    }
    defer rows.Close()
    for rows.Next() {
        var name string
        if err := rows.Scan(&name); err == nil {
            if surname := extractSurname(name); surname != "" {
                surnames[surname] = true
            }
        }
    }
    return surnames
}

// extractSurname returns the lowercase last whitespace-delimited token
// of a display_name. "Master Whitcombe" → "whitcombe", "Ezekiel Crane"
// → "crane". Empty string for empty/whitespace-only names; for single-
// token names the token itself (treats "Tobias" as both first name and
// surname for collision purposes — defensive).
func extractSurname(name string) string {
    parts := strings.Fields(name)
    if len(parts) == 0 {
        return ""
    }
    return strings.ToLower(parts[len(parts)-1])
}

// coLocatedVisitor describes one visitor near a perceiver. The slots
// feed the "Visitors here" perception block so the LLM has concrete
// material to lead with — name + archetype + origin + disposition —
// instead of the generic "a stranger" descriptor that comes from
// coLocatedHuddleMembers' acquaintance-fallback path.
type coLocatedVisitor struct {
    DisplayName string
    Archetype   string
    Origin      string
    Disposition string
}

// coLocatedVisitors returns visitor actors within visitorPerceptionRadius
// world-pixels of the perceiver, ordered by display_name for stable
// output. Empty when no visitor is nearby (the steady-state case in
// a village with no current visitors). Self-excluded — a visitor
// who is themselves a perceiver doesn't see themselves listed.
func (app *App) coLocatedVisitors(ctx context.Context, perceiverID string, perceiverX, perceiverY float64) []coLocatedVisitor {
    rows, err := app.DB.Query(ctx,
        `SELECT display_name, visitor_archetype, visitor_origin, visitor_disposition
         FROM actor
         WHERE visitor_expires_at IS NOT NULL
           AND id::text != $1
           AND ABS(current_x - $2) < $4
           AND ABS(current_y - $3) < $4
         ORDER BY display_name`,
        perceiverID, perceiverX, perceiverY, float64(visitorPerceptionRadius),
    )
    if err != nil {
        log.Printf("co-located-visitors: query: %v", err)
        return nil
    }
    defer rows.Close()

    var result []coLocatedVisitor
    for rows.Next() {
        var v coLocatedVisitor
        var archetype, origin, disposition sql.NullString
        if err := rows.Scan(&v.DisplayName, &archetype, &origin, &disposition); err != nil {
            continue
        }
        if archetype.Valid {
            v.Archetype = archetype.String
        }
        if origin.Valid {
            v.Origin = origin.String
        }
        if disposition.Valid {
            v.Disposition = disposition.String
        }
        result = append(result, v)
    }
    return result
}

// visitorProfile holds the per-instance persona slots a freshly-
// spawned visitor gets. Picked from hardcoded pools below in v1;
// later phases may move pools to memory-api notes for easier
// extension without engine deploy.
type visitorProfile struct {
    Name        string
    Archetype   string
    Origin      string
    Disposition string
}

func generateVisitorProfile() visitorProfile {
    return visitorProfile{
        Name:        visitorNamePool[rand.Intn(len(visitorNamePool))],
        Archetype:   visitorArchetypePool[rand.Intn(len(visitorArchetypePool))],
        Origin:      visitorOriginPool[rand.Intn(len(visitorOriginPool))],
        Disposition: visitorDispositionPool[rand.Intn(len(visitorDispositionPool))],
    }
}

// Period-flavored pools. New England colonial-era names; archetypes a
// small village would actually receive; fictional/historical next-
// village strings; short adjectives the model can use to color voice.
//
// Names are male-coded only because every available sprite family in
// visitorArchetypeSprite is male-coded (Merchant, Old Man, Man) — a
// female-coded name on a male sprite reads as a sprite-asset bug, not
// a stylistic choice. Female visitor names will return when the sprite
// library expands. Surnames are also chosen to not match any of Salem's
// current seated villagers (Crane, Thorne, Ward, Ellis, Smith); the
// dynamic surname scrub in dispatchVisitorSpawn handles any drift as
// new villagers are added or this pool grows.
var visitorNamePool = []string{
    "Master Whitcombe", "Brother Ashford", "Elias Drum",
    "Roger Standish", "Tobias Hewes", "Master Babbage",
    "Jonas Penhallow", "Jeremiah Soames", "Nathaniel Pratt",
    "Caleb Wendell", "Obadiah Brewster", "Ephraim Pollard",
    "Silas Withrow", "Asa Larkin", "Daniel Holcomb",
}

var visitorArchetypePool = []string{
    "peddler", "traveling scholar", "messenger", "itinerant musician",
    "journeyman tinsmith", "circuit preacher", "wool-buyer",
    "pewterer", "wandering surgeon", "almanac-seller",
}

var visitorOriginPool = []string{
    "Boston", "Marblehead", "Andover", "Ipswich", "Topsfield",
    "Lynn", "Salem Town", "the next valley over",
    "the coast road", "Beverly", "Wenham", "Rowley",
}

var visitorDispositionPool = []string{
    "weary", "warm", "reserved", "curious", "mercenary",
    "talkative", "wary", "earnest", "wry", "withdrawn",
}

// visitorArchetypeSprite maps each archetype to an npc_sprite.name.
// The init() below enforces that every archetype in
// visitorArchetypePool has an entry here — adding a new archetype
// without picking a sprite makes the engine refuse to start, so the
// mismatch can't ship to a deploy.
//
// Sprite reuse across archetypes is intentional given the current
// shortage of period-appropriate sheets — variant suffixes (v00, v01)
// give us a few visually-distinct options within a family, but we run
// out before covering ten archetypes 1:1. Trade off: peddler /
// wool-buyer / almanac-seller all read as "merchant-coded" silhouettes,
// which is fine for transient strangers. Expand this map once the
// sprite library grows.
var visitorArchetypeSprite = map[string]string{
    "peddler":             "Merchant B (v00)",
    "traveling scholar":   "Old Man A (v01)",
    "messenger":           "Man A (v00)",
    "itinerant musician":  "Man B (v00)",
    "journeyman tinsmith": "Merchant C (v00)",
    "circuit preacher":    "Old Man B (v00)",
    "wool-buyer":          "Merchant A (v01)",
    "pewterer":            "Merchant C (v01)",
    "wandering surgeon":   "Old Man A (v02)",
    "almanac-seller":      "Old Man B (v01)",
}

// init enforces archetype-pool / sprite-map exhaustiveness at engine
// startup. Adding an archetype to visitorArchetypePool without a
// corresponding visitorArchetypeSprite entry panics here and the
// engine refuses to start — caught in `go test` and at boot, so the
// mismatch can't reach a running deploy.
func init() {
    for _, archetype := range visitorArchetypePool {
        if _, ok := visitorArchetypeSprite[archetype]; !ok {
            panic("visitor.go: archetype " + archetype + " has no sprite mapping in visitorArchetypeSprite")
        }
    }
}
