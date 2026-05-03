package main

// village_concerns — chronicler-authored named-entity facts.
//
// The chronicler authors noticeboard prose and, in the same reply, attaches
// structured concerns to named actors and structures. Targeted NPCs retain
// the fact in their perception ("Concerning the tavern (your workplace):
// 'A shawl was left here.'") without ever reading the board. The fact's
// lifetime is bound to the source's generation — when the noticeboard
// rotates, the prior posting's concerns age out (perception filter requires
// source_generation == current_generation; stale rows are swept lazily).
//
// v1 surfaces only the noticeboard source (concernSourceVillageObjectContent).
// Future sources (village_event, world_environment, engine-direct
// emergencies) can plug in by adding new concern_source_kind enum values
// and emitting rows with a different source_kind. The read path is
// source-agnostic — it joins on (target, current source generation) only.
//
// Design and rationale: shared/notes/codebase/salem/village-concerns.

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// concern_source_kind enum values. The Postgres enum has the same names
// (defined in migrations/ZBBS-117). Add a new constant here AND a new
// enum value in a follow-on migration when introducing a new source.
const (
	concernSourceVillageObjectContent = "village_object_content"
)

// concern_target_kind enum values.
const (
	concernTargetActor     = "actor"
	concernTargetStructure = "structure"
)

// Read-side cap. The chronicler can attach more than this; perception
// renders only the freshest few per category so a busy day doesn't
// drown the NPC's tick in retained facts.
const concernsPerCategoryCap = 3

// concernRow is one row loaded by loadConcernsForActor, already
// categorized for rendering. Category is one of "you", "work", "home".
type concernRow struct {
	Category      string
	StructureName string // populated when Category != "you"
	Text          string
}

// recordConcern inserts a single concern. The caller has already chosen
// the source generation — typically the value returned by
// saveObjectContent so the row is tagged with the same generation the
// content_text was written under.
func (app *App) recordConcern(ctx context.Context,
	sourceKind, sourceID string, sourceGeneration int,
	targetKind, targetID, text string,
) error {
	_, err := app.DB.Exec(ctx,
		`INSERT INTO village_concern
		     (source_kind, source_id, source_generation,
		      target_kind, target_id, text)
		 VALUES ($1, $2::uuid, $3, $4, $5::uuid, $6)`,
		sourceKind, sourceID, sourceGeneration,
		targetKind, targetID, text,
	)
	return err
}

// clearConcernsForSource deletes all concerns from a source. Called when
// a noticeboard's content is cleared without replacement (rotation back
// to a no-capacity state) so the fact tied to the prior prose disappears
// alongside it. Save-and-replace flows don't need this — the
// source_generation bump in saveObjectContent makes prior-gen rows
// invisible to perception, and the lazy janitor removes them later.
func (app *App) clearConcernsForSource(ctx context.Context, sourceKind, sourceID string) error {
	_, err := app.DB.Exec(ctx,
		`DELETE FROM village_concern
		  WHERE source_kind = $1 AND source_id = $2::uuid`,
		sourceKind, sourceID,
	)
	return err
}

// loadConcernsForActor returns the live concerns to surface in this NPC's
// perception, sorted by category and capped at concernsPerCategoryCap per
// category. Filters on source_generation == current_generation so prior-
// posting concerns don't bleed through.
//
// Categories:
//   - "you"  — target_kind='actor' AND target_id == npc.id
//   - "work" — target_kind='structure' AND target_id == npc.work_structure_id
//   - "home" — target_kind='structure' AND target_id == npc.home_structure_id
//
// Workers-only on the structure cascade by design — patrons currently
// inside a structure do NOT auto-receive concerns. They learn by being
// told.
func (app *App) loadConcernsForActor(ctx context.Context,
	actorID string, homeStructureID, workStructureID string,
) ([]concernRow, error) {
	// Build the structure-target candidate list. Either ID can be empty
	// (NPC has no home or no work assigned). We pass them as nullable
	// parameters and the SQL filters accordingly.
	var home, work any
	if homeStructureID != "" {
		home = homeStructureID
	}
	if workStructureID != "" {
		work = workStructureID
	}

	// One query per category, then concat. Cleaner than a UNION ALL
	// inside a single statement and easier to trace when debugging.
	results := []concernRow{}

	youRows, err := app.DB.Query(ctx,
		`SELECT c.text
		   FROM village_concern c
		   JOIN village_object o ON o.id = c.source_id
		  WHERE c.target_kind = 'actor'
		    AND c.target_id = $1::uuid
		    AND c.source_kind = 'village_object_content'
		    AND c.source_generation = o.content_generation
		  ORDER BY c.created_at DESC
		  LIMIT $2`,
		actorID, concernsPerCategoryCap)
	if err != nil {
		return nil, err
	}
	for youRows.Next() {
		var text string
		if err := youRows.Scan(&text); err != nil {
			youRows.Close()
			return nil, err
		}
		results = append(results, concernRow{Category: "you", Text: text})
	}
	youRows.Close()

	if work != nil {
		rows, err := app.DB.Query(ctx,
			`SELECT c.text, COALESCE(s.display_name, '')
			   FROM village_concern c
			   JOIN village_object o ON o.id = c.source_id
			   JOIN village_object s ON s.id = c.target_id
			  WHERE c.target_kind = 'structure'
			    AND c.target_id = $1::uuid
			    AND c.source_kind = 'village_object_content'
			    AND c.source_generation = o.content_generation
			  ORDER BY c.created_at DESC
			  LIMIT $2`,
			work, concernsPerCategoryCap)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var text, name string
			if err := rows.Scan(&text, &name); err != nil {
				rows.Close()
				return nil, err
			}
			results = append(results, concernRow{Category: "work", StructureName: name, Text: text})
		}
		rows.Close()
	}

	// Skip the home query when home == work — same structure, would
	// produce duplicate lines.
	if home != nil && home != work {
		rows, err := app.DB.Query(ctx,
			`SELECT c.text, COALESCE(s.display_name, '')
			   FROM village_concern c
			   JOIN village_object o ON o.id = c.source_id
			   JOIN village_object s ON s.id = c.target_id
			  WHERE c.target_kind = 'structure'
			    AND c.target_id = $1::uuid
			    AND c.source_kind = 'village_object_content'
			    AND c.source_generation = o.content_generation
			  ORDER BY c.created_at DESC
			  LIMIT $2`,
			home, concernsPerCategoryCap)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var text, name string
			if err := rows.Scan(&text, &name); err != nil {
				rows.Close()
				return nil, err
			}
			results = append(results, concernRow{Category: "home", StructureName: name, Text: text})
		}
		rows.Close()
	}

	return results, nil
}

// renderConcerns formats a slice of concernRow into the perception lines
// emitted in section 3.0b. Returns "" when there are no concerns so the
// caller can skip the whole section.
func renderConcerns(rows []concernRow) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range rows {
		switch r.Category {
		case "you":
			fmt.Fprintf(&b, "Concerning you: %q\n", r.Text)
		case "work":
			if r.StructureName != "" {
				fmt.Fprintf(&b, "Concerning the %s (your workplace): %q\n", r.StructureName, r.Text)
			} else {
				fmt.Fprintf(&b, "Concerning your workplace: %q\n", r.Text)
			}
		case "home":
			if r.StructureName != "" {
				fmt.Fprintf(&b, "Concerning the %s (your home): %q\n", r.StructureName, r.Text)
			} else {
				fmt.Fprintf(&b, "Concerning your home: %q\n", r.Text)
			}
		}
	}
	return b.String()
}

// resolveTargetByName finds the actor or structure with the given
// display_name. Structures take precedence — if a name resolves both as
// a structure and as an actor, structure wins (stable layer of the
// village, less likely to be ambiguous than itinerant actors). Case-
// insensitive match. Returns (kind, id, nil) on success; an error on
// no-match or ambiguous-actor-match.
//
// Used by the chronicler tool / parser to validate the name it picked
// for record_concern. Loud failure is the design — silent drops would
// hide chronicler prompt drift.
func (app *App) resolveTargetByName(ctx context.Context, name string) (kind, id string, err error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", "", errors.New("empty target name")
	}

	// Structure first.
	var structureID string
	err = app.DB.QueryRow(ctx,
		`SELECT id::text FROM village_object
		  WHERE LOWER(display_name) = LOWER($1)
		  LIMIT 1`,
		trimmed,
	).Scan(&structureID)
	if err == nil {
		return concernTargetStructure, structureID, nil
	}

	// Actor fallback. Reject ambiguous matches loudly.
	rows, qerr := app.DB.Query(ctx,
		`SELECT id::text FROM actor
		  WHERE LOWER(display_name) = LOWER($1)
		  LIMIT 2`,
		trimmed,
	)
	if qerr != nil {
		return "", "", qerr
	}
	defer rows.Close()
	var actorIDs []string
	for rows.Next() {
		var aid string
		if scanErr := rows.Scan(&aid); scanErr != nil {
			return "", "", scanErr
		}
		actorIDs = append(actorIDs, aid)
	}
	switch len(actorIDs) {
	case 0:
		return "", "", fmt.Errorf("no structure or actor named %q", trimmed)
	case 1:
		return concernTargetActor, actorIDs[0], nil
	default:
		return "", "", fmt.Errorf("ambiguous: multiple actors named %q", trimmed)
	}
}
