package pg

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// StructuresRepo reads and writes Structure rows against `structure`
// plus the `structure_room` child table. Owns both as one aggregate.
//
// SaveSnapshot uses the generation-marker pattern (Slice 9/10/11
// precedent — see `shared/notes/codebase/salem-engine-v2/pg-snapshot-pattern`).
// Both tables get per-row UPSERT inside the caller's checkpoint Tx,
// then per-table `DELETE WHERE snapshot_gen < $gen` prunes anything
// absent. Each table owns its own sequence; the parent's advisory lock
// covers both — structure_room never SaveSnapshots independently.
//
// Shared-Identity Bridge contract: every persisted structure.id MUST
// equal some village_object.id::text. Slice 12's migration validates
// this at deploy time; runtime SaveSnapshot does NOT cross-check the
// bridge (would couple Structures to VillageObjects). A LoadWorld
// startup check is the natural place to verify the bridge holds across
// loads. See engine/sim/structure_anchors.go:14-23 for the contract.
type StructuresRepo struct {
	pool Pool
}

// NewStructuresRepo constructs a StructuresRepo against the given pool.
func NewStructuresRepo(pool Pool) *StructuresRepo {
	return &StructuresRepo{pool: pool}
}

const loadAllSQLS = `
SELECT id, display_name, tags, leads_to_realm
  FROM structure`

const loadAllRoomsSQLS = `
SELECT id, structure_id, kind, name
  FROM structure_room`

const upsertSQLS = `
INSERT INTO structure (
    id, display_name, tags, leads_to_realm, snapshot_gen
) VALUES (
    $1, $2, $3, $4, $5
)
ON CONFLICT (id) DO UPDATE SET
    display_name   = EXCLUDED.display_name,
    tags           = EXCLUDED.tags,
    leads_to_realm = EXCLUDED.leads_to_realm,
    snapshot_gen   = EXCLUDED.snapshot_gen`

const upsertSQLSR = `
INSERT INTO structure_room (
    id, structure_id, kind, name, snapshot_gen
) VALUES (
    $1, $2, $3, $4, $5
)
ON CONFLICT (id) DO UPDATE SET
    structure_id = EXCLUDED.structure_id,
    kind         = EXCLUDED.kind,
    name         = EXCLUDED.name,
    snapshot_gen = EXCLUDED.snapshot_gen`

const deleteStaleSQLS = `DELETE FROM structure WHERE snapshot_gen < $1`

const deleteStaleSQLSR = `DELETE FROM structure_room WHERE snapshot_gen < $1`

const nextGenSQLS = `SELECT nextval('structure_snapshot_gen_seq')`

const nextGenSQLSR = `SELECT nextval('structure_room_snapshot_gen_seq')`

// advisoryLockSQLS is the single global lock for the structures
// aggregate (parent + child). Same pattern as Slice 11's huddles.
// Multi-realm upgrade path: replace 0 with hashtext($realm_id).
const advisoryLockSQLS = `SELECT pg_advisory_xact_lock(hashtext('structure_snapshot'), 0)`

// LoadAll loads every structure row plus its structure_room children
// into memory.
//
// Runs against the pool directly (no Tx — read-only restart path).
// Relies on LoadAll running before the world goroutine starts and
// before any checkpoint writer can mutate these tables. Without that
// startup guarantee, the two queries could observe different committed
// states under READ COMMITTED.
//
// A structure_room row whose structure_id isn't in the loaded parent
// set surfaces as an error (FK CASCADE makes this impossible from
// valid writes; the guard surfaces schema drift loudly).
func (r *StructuresRepo) LoadAll(ctx context.Context) (map[sim.StructureID]*sim.Structure, error) {
	rows, err := r.pool.Query(ctx, loadAllSQLS)
	if err != nil {
		return nil, fmt.Errorf("pg structures LoadAll query: %w", err)
	}
	defer rows.Close()

	out := make(map[sim.StructureID]*sim.Structure)
	for rows.Next() {
		var (
			id           string
			displayName  string
			tags         []string
			leadsToRealm string
		)
		if err := rows.Scan(&id, &displayName, &tags, &leadsToRealm); err != nil {
			return nil, fmt.Errorf("pg structures LoadAll scan: %w", err)
		}
		// Defensive: pgx may scan an empty array as nil depending on
		// type-path; the column is NOT NULL DEFAULT '{}' so empty is the
		// expected shape. Normalize to empty-slice to keep in-memory
		// invariants consistent.
		if tags == nil {
			tags = []string{}
		}
		out[sim.StructureID(id)] = &sim.Structure{
			ID:           sim.StructureID(id),
			DisplayName:  displayName,
			Tags:         tags,
			LeadsToRealm: leadsToRealm,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg structures LoadAll iter: %w", err)
	}

	if err := r.loadAllRooms(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// loadAllRooms reads every structure_room row and attaches it to the
// parent's Rooms slice. Orphan rows (no parent loaded) → error.
func (r *StructuresRepo) loadAllRooms(ctx context.Context, structures map[sim.StructureID]*sim.Structure) error {
	rows, err := r.pool.Query(ctx, loadAllRoomsSQLS)
	if err != nil {
		return fmt.Errorf("pg structures LoadAll rooms query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id          int64
			structureID string
			kind        string
			name        string
		)
		if err := rows.Scan(&id, &structureID, &kind, &name); err != nil {
			return fmt.Errorf("pg structures LoadAll rooms scan: %w", err)
		}
		parent, ok := structures[sim.StructureID(structureID)]
		if !ok {
			return fmt.Errorf("pg structures LoadAll: orphan room row id=%d structure_id=%s (parent missing — schema drift or out-of-band write)",
				id, structureID)
		}
		parent.Rooms = append(parent.Rooms, &sim.Room{
			ID:          sim.RoomID(id),
			StructureID: sim.StructureID(structureID),
			Kind:        sim.RoomKind(kind),
			Name:        name,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg structures LoadAll rooms iter: %w", err)
	}
	return nil
}

// SaveSnapshot writes the full Structure + Rooms set durably using the
// generation-marker pattern (Slice 9/10/11 standard).
//
// Steps inside the caller's checkpoint Tx (order matters — parent
// settles before children sync):
//
//  0. Advisory lock — shared by both tables.
//  1. nextval(structure_snapshot_gen_seq) → $genStruct.
//  2. Per-row UPSERT structure. Substrate-boundary validation: reject
//     nil entries (NOT silent skip — design_review 2026-05-19 #5),
//     empty / whitespace-only s.ID, map-key ↔ s.ID mismatch, empty
//     DisplayName, duplicate RoomIDs across the full snapshot, room
//     shape errors (nil, ID ≤ 0, StructureID mismatch, empty Name).
//  3. DELETE structure WHERE snapshot_gen < $genStruct. Plain DELETE —
//     no self-FK. FK CASCADE from structure_room → structure drops
//     orphan rooms; further FK CASCADE from room_access → structure_room
//     drops access rows. (Acceptable indirect deletion — Actors-pg-impl
//     reconstructs RoomAccess on next checkpoint.)
//  4. nextval(structure_room_snapshot_gen_seq) → $genRoom.
//  5. Per-structure per-room UPSERT structure_room.
//  6. DELETE structure_room WHERE snapshot_gen < $genRoom.
//
// Empty structures map: both gens still bump, no UPSERTs run, both
// DELETEs sweep their tables.
func (r *StructuresRepo) SaveSnapshot(ctx context.Context, tx sim.Tx, structures map[sim.StructureID]*sim.Structure) error {
	if tx == nil {
		return fmt.Errorf("pg structures SaveSnapshot: nil tx")
	}

	// Step 0: advisory lock.
	if _, err := tx.Exec(ctx, advisoryLockSQLS); err != nil {
		return fmt.Errorf("pg structures SaveSnapshot: advisory lock: %w", err)
	}

	// Step 1: parent gen.
	var structGen int64
	if err := tx.QueryRow(ctx, nextGenSQLS).Scan(&structGen); err != nil {
		return fmt.Errorf("pg structures SaveSnapshot: nextval structure: %w", err)
	}

	// Validate the whole snapshot in a pre-pass to catch shape bugs
	// (duplicate RoomIDs, nil entries, mismatches) BEFORE we start
	// writing rows. Cheaper to abort early; clearer error messages.
	// Slice 12 differs from Slice 11 here because Rooms is a slice
	// (not a map) so duplicate IDs across the full snapshot need a
	// cross-structure check.
	// Validation uses strings.TrimSpace to match the DB-side btrim CHECKs
	// — without that, a whitespace-only ID / DisplayName / Name passes Go
	// validation and then trips the CHECK mid-Tx, leaving a worse error
	// than a clean substrate rejection. (code_review R1 2026-05-19.)
	// LLM-392: a bad structure is quarantined, not fatal. Sorted iteration so
	// the winner of a cross-structure duplicate-RoomID collision is stable
	// between checkpoints. See sim/checkpoint_quarantine.go.
	q := quarantineOf(tx)
	seenRoomIDs := make(map[sim.RoomID]sim.StructureID)
	seenRoomNames := make(map[sim.StructureID]map[string]struct{})
	for _, key := range sortedStructureIDs(structures) {
		s := structures[key]
		if s == nil {
			dropStructure(q, key, "nil structure entry (deletion goes through gen-marker absence, not nil)")
			continue
		}
		if strings.TrimSpace(string(s.ID)) == "" {
			dropStructure(q, key, "empty StructureID")
			continue
		}
		if s.ID != key {
			dropStructure(q, key, fmt.Sprintf("map key=%s does not match s.ID=%s", key, s.ID))
			continue
		}
		if strings.TrimSpace(s.DisplayName) == "" {
			dropStructure(q, key, "empty DisplayName (load-bearing for prompts)")
			continue
		}
		// Tag-element validation matches the (now repo-only) no-nulls /
		// no-empty invariant. The DB CHECK was dropped in R1 because
		// `array_position(tags, NULL)` has unreliable semantics for
		// null-element detection; pure repo validation replaces it. An empty
		// tag is dropped FROM the array rather than dropping the structure —
		// a blank tag is not worth losing a building over.
		if tags, changed := compactTags(s.Tags); changed {
			q.Clamp("structure", string(s.ID), fmt.Sprintf("dropped %d empty tag(s) from tags[]", len(s.Tags)-len(tags)))
			s.Tags = tags
		}
		// Room drops are keyed by POSITION (structure + slice index), never by
		// RoomID, and the write loop below re-derives the identical key. Two
		// reasons, both load-bearing:
		//
		//   - a room with an invalid id (<= 0) has no usable PK to key on, so a
		//     RoomID-keyed drop could not be matched at write time and the bad
		//     row would reach SQL anyway;
		//   - on a DUPLICATE RoomID, a RoomID-keyed drop would mark that id
		//     dropped for BOTH rooms — suppressing the survivor along with the
		//     loser. Position keys distinguish them, so the first room in sorted
		//     structure order keeps the id and only the later one is dropped.
		for i, room := range s.Rooms {
			id := structureRoomID(s.ID, i)
			if room == nil {
				q.Drop("structure_room", id, "nil room entry")
				continue
			}
			if room.ID <= 0 {
				q.Drop("structure_room", id, fmt.Sprintf("invalid RoomID=%d (must be > 0)", room.ID))
				continue
			}
			if room.StructureID != s.ID {
				q.Drop("structure_room", id, fmt.Sprintf("room id=%d has mismatched StructureID=%s (owner is %s)", room.ID, room.StructureID, s.ID))
				continue
			}
			if strings.TrimSpace(room.Name) == "" {
				q.Drop("structure_room", id, fmt.Sprintf("room id=%d has empty Name", room.ID))
				continue
			}
			if owner, dup := seenRoomIDs[room.ID]; dup {
				q.Drop("structure_room", id, fmt.Sprintf("duplicate RoomID=%d (already owned by structure %s, which keeps it)", room.ID, owner))
				continue
			}
			if seenRoomNames[s.ID] == nil {
				seenRoomNames[s.ID] = make(map[string]struct{})
			}
			if _, dup := seenRoomNames[s.ID][room.Name]; dup {
				q.Drop("structure_room", id, fmt.Sprintf("duplicate room name=%q in structure %s", room.Name, s.ID))
				continue
			}
			seenRoomIDs[room.ID] = s.ID
			seenRoomNames[s.ID][room.Name] = struct{}{}
		}
	}

	// Publish, under an id-shaped key, every room that will have NO durable row
	// after this checkpoint — so the ACTORS writer can drop a room_access grant
	// pointing at it. room_access.room_id -> structure_room(id) is a real,
	// non-deferred FK, and Structures runs before Actors, so this is the only
	// place that knows.
	//
	// Why a second key: this writer's own drops are keyed by slice POSITION,
	// which is right for its write loop but meaningless to a writer that only
	// holds a RoomID. Distinct key strings in the same table, so they never
	// collide. And it is computed AFTER the whole pass, because a room id that
	// one structure dropped as a DUPLICATE is still written by the structure
	// that legitimately owns it (seenRoomIDs) — marking it missing would drop a
	// grant on a room that exists perfectly well.
	for _, key := range sortedStructureIDs(structures) {
		s := structures[key]
		if s == nil {
			continue
		}
		// A dropped STRUCTURE takes every one of its rooms with it — the write
		// loop skips them all — and those rooms never reached the room-level
		// drop, because the pre-pass bailed on the structure before iterating
		// them. So both cases have to be covered here.
		structDropped := q.Dropped("structure", string(key))
		for i, room := range s.Rooms {
			if room == nil || room.ID <= 0 {
				continue
			}
			if !structDropped && !q.Dropped("structure_room", structureRoomID(s.ID, i)) {
				continue
			}
			if _, written := seenRoomIDs[room.ID]; written {
				continue // another structure legitimately writes this id
			}
			q.Drop("structure_room", roomRowID(room.ID),
				fmt.Sprintf("room %d will have no durable row this checkpoint", room.ID))
		}
	}

	// Step 2: upsert each structure. Keyed on the MAP KEY — what the pre-pass
	// drops under. Keying on s.ID would miss an empty-ID structure (s.ID is "")
	// and let it reach SQL.
	for key, s := range structures {
		if s == nil || q.Dropped("structure", string(key)) {
			continue
		}
		if _, err := tx.Exec(ctx, upsertSQLS,
			string(s.ID),   // $1 id
			s.DisplayName,  // $2 display_name
			s.Tags,         // $3 tags (TEXT[])
			s.LeadsToRealm, // $4 leads_to_realm
			structGen,      // $5 snapshot_gen
		); err != nil {
			return fmt.Errorf("pg structures SaveSnapshot: upsert structure id=%s: %w", s.ID, err)
		}
	}

	// Step 3: prune absent parents. FK CASCADE drops their rooms (and
	// transitively any room_access rows) — which is exactly why a dropped
	// structure must block this sweep: sweeping it would CASCADE-delete the
	// rooms of a structure we merely declined to rewrite.
	if err := execSweep(ctx, tx, "structure", deleteStaleSQLS, structGen); err != nil {
		return fmt.Errorf("pg structures SaveSnapshot: delete stale structure: %w", err)
	}

	// Step 4: child gen — independent tier from parent.
	var roomGen int64
	if err := tx.QueryRow(ctx, nextGenSQLSR).Scan(&roomGen); err != nil {
		return fmt.Errorf("pg structures SaveSnapshot: nextval structure_room: %w", err)
	}

	// Step 5: upsert each room of each structure. (Validation already
	// happened in the pre-pass; quarantined rooms — and every room of a
	// quarantined structure — are skipped here.)
	for key, s := range structures {
		if s == nil || q.Dropped("structure", string(key)) {
			continue
		}
		// Same position key the pre-pass dropped under — see the comment there.
		for i, room := range s.Rooms {
			if room == nil || q.Dropped("structure_room", structureRoomID(s.ID, i)) {
				continue
			}
			if _, err := tx.Exec(ctx, upsertSQLSR,
				int64(room.ID),           // $1 id
				string(room.StructureID), // $2 structure_id
				string(room.Kind),        // $3 kind
				room.Name,                // $4 name
				roomGen,                  // $5 snapshot_gen
			); err != nil {
				return fmt.Errorf("pg structures SaveSnapshot: upsert room id=%d structure=%s: %w", room.ID, room.StructureID, err)
			}
		}
	}

	// Step 6: prune absent room rows.
	if err := execSweep(ctx, tx, "structure_room", deleteStaleSQLSR, roomGen); err != nil {
		return fmt.Errorf("pg structures SaveSnapshot: delete stale structure_room: %w", err)
	}
	return nil
}

// dropStructure quarantines a structure and blocks its rooms' sweep — the
// structure keeps its durable row, so its rooms must keep theirs. (structure's
// own sweep would CASCADE the rooms away regardless; blocking structure_room
// as well keeps the two tiers consistent if only the child sweep were to run.)
func dropStructure(q *sim.Quarantine, id sim.StructureID, reason string) {
	q.Drop("structure", string(id), reason)
	q.BlockSweep("structure_room")
}

// sortedStructureIDs returns structure ids in a stable order, so a
// cross-structure duplicate-RoomID collision always resolves the same way.
func sortedStructureIDs(m map[sim.StructureID]*sim.Structure) []sim.StructureID {
	out := make([]sim.StructureID, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// compactTags removes empty tag elements, reporting whether it changed
// anything. An empty tag violates the repo-side no-empty invariant but is not
// worth dropping a whole structure over.
func compactTags(tags []string) ([]string, bool) {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if t == "" {
			continue
		}
		out = append(out, t)
	}
	return out, len(out) != len(tags)
}

// structureRoomID is the quarantine key for a room row: structure id + slice
// POSITION, not RoomID. The pre-pass and the write loop must derive it
// identically — see the comment in the pre-pass for why position and not the
// PK (an invalid id has no usable PK; a duplicate id would suppress the
// survivor along with the loser).
func structureRoomID(sid sim.StructureID, i int) string {
	return fmt.Sprintf("%s/#%d", sid, i)
}
