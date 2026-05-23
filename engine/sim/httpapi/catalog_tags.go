package httpapi

import (
	"net/http"
	"sort"
)

// Static editor tag allowlists — the vocabularies the editor's tag dropdowns
// render. Ported verbatim from v1 (engine/assets.go allowedStateTags +
// engine/village_object_tags.go allowedObjectTags). Hardcoded reference data:
// new tags require a code change so the vocabulary stays coherent (free-form
// typing fragments the set with typos / dash-vs-underscore variants).
//
// READ-SURFACE ONLY in v2. Unlike v1, the write path
// (sim.AddVillageObjectTag) validates charset/length only and does NOT gate on
// these allowlists — the dropdown constrains the editor UI, but the engine
// accepts any well-formed tag. So these maps live in the read-surface package,
// not in sim, and have no consumer beyond the two handlers below.

// allowedStateTags is the asset-state tag vocabulary served at
// GET /api/assets/state-tags. Identity tags that describe an asset template
// (the asset genuinely IS a notice-board, a laundry, day/night-active, etc.).
var allowedStateTags = map[string]bool{
	"rotatable":          true,
	"day-active":         true,
	"night-active":       true,
	"lamplighter-target": true,
	"laundry":            true,
	"notice-board":       true,
	"occupied":           true,
	"unoccupied":         true,
}

// allowedObjectTags is the per-instance object tag vocabulary served at
// GET /api/village/object-tags. Role tags applied to a specific placed
// village_object (the same asset can be a tavern in one placement and a plain
// house in another). Categorical tags (tavern/smithy/shop/…) double as
// social_tag values for the social scheduler.
var allowedObjectTags = map[string]bool{
	"tavern":        true,
	"smithy":        true,
	"shop":          true,
	"meeting-house": true,
	"well":          true,
	"outhouse":      true,
	"lodging":       true,
	// summon_point marks a placement (loiter point, future bell) where a
	// summoner walks to ring for a messenger. Underscore form by convention.
	"summon_point": true,
	// noticeboard_content (ZBBS-112) opts a placement into state-driven
	// content generation; disambiguated from the asset-state "notice-board"
	// tag by the underscore form.
	"noticeboard_content": true,
	// business (ZBBS-179) marks a placement as a place where someone goes to
	// transact — the canonical "is this a business" signal, orthogonal to the
	// category tags (which describe what kind).
	"business": true,
}

// handleObjectTags returns the per-instance object-tag allowlist, sorted, so
// the editor populates its dropdown from server truth.
func (s *Server) handleObjectTags(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, sortedTags(allowedObjectTags))
}

// handleStateTags returns the asset-state tag allowlist, sorted.
func (s *Server) handleStateTags(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, sortedTags(allowedStateTags))
}

// sortedTags returns the map's keys in stable alphabetical order so the wire
// response is deterministic and the editor dropdown is predictable. Always a
// non-nil slice — an empty allowlist serializes as [] rather than null.
func sortedTags(allow map[string]bool) []string {
	tags := make([]string, 0, len(allow))
	for tag := range allow {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}
