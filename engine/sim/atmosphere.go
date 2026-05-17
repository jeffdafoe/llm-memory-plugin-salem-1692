package sim

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// atmosphere.go — world-level atmosphere refresh substrate. The off-world
// sweep goroutine + LLM-call adapter lives in
// engine/sim/cascade/atmosphere.go; this file is sim-package primitives
// only.
//
// Mechanism (mirrors the consolidation slice shape but at world scope
// rather than per-relationship):
//
//   1. FetchAtmosphereContext runs on the world goroutine, captures a
//      snapshot of the inputs the prompt needs (Phase / Weather / prior
//      atmosphere / NPC roster grouped by structure), and returns it.
//      Everything in the snapshot is owned by the caller — strings are
//      Go-immutable, the Roster slice is freshly allocated.
//
//   2. Driver (engine/sim/cascade/atmosphere.go) calls salem-generic
//      with a prompt built from the snapshot.
//
//   3. ApplyAtmosphereRefresh runs back on the world goroutine: trim
//      reply, dedup against current atmosphere (no-op if identical
//      after trim), install + stamp WorldEnvironment.LastAtmosphereRefreshAt.
//
// Scope: world-level single string. No per-pair race-safety concern
// like consolidation has — last-write-wins is fine.
//
// Roster: NPCs only (Kind != KindPC). Grouped by Structure.DisplayName.
// Actors with InsideStructureID == "" or a missing/unnamed structure go
// to the outdoor bucket. Mirrors v1 chronicler's buildChroniclerNPCRoster
// shape (an open-air bucket + per-structure groups) without the v1
// JOIN against village_object/asset — v2's Structure.DisplayName is the
// direct source of truth.
//
// Activity digest (v1's agent_action_log group-by-NPC-by-action since
// last fire) deliberately omitted from the MVP — agent_action_log isn't
// ported to v2 yet (blocking C2 per-actor narrative consolidation too).
// Add to AtmosphereContext when the log lands.

// AtmosphereRefreshIntervalDefault is the fallback cadence when
// WorldSettings.AtmosphereRefreshInterval is unset. 4h — matches the
// locked design in shared/tasks/engine-in-memory-rewrite/start-here
// (replaces v1's chronicler phase-boundary fires). The actual default
// constant lives next to the driver in
// engine/sim/cascade/atmosphere.go (defaultAtmosphereRefreshInterval)
// for the same reason as IdleBackstop — cascade owns the goroutine.
// Exposed here so callers building tests or admin tools can reference
// the canonical default without reaching into the cascade package.
const AtmosphereRefreshIntervalDefault = 4 * time.Hour

// AtmosphereRosterEntry is one bucket in the NPC roster fed to the
// atmosphere prompt: a structure label (the structure's DisplayName,
// or empty for outdoor) plus the actors currently inside it. Names are
// sorted within the bucket for deterministic prompt rendering.
type AtmosphereRosterEntry struct {
	StructureLabel string
	DisplayNames   []string
}

// AtmosphereContext is the snapshot the world goroutine builds for the
// off-world atmosphere sweep. All fields are owned by the caller — no
// pointers back into world state.
//
// Roster is ordered: outdoor bucket (StructureLabel == "") last,
// structure groups in DisplayName-ascending order before it. Names
// within each bucket are sorted ascending. This matches v1's chronicler
// roster posture.
type AtmosphereContext struct {
	Now             time.Time
	Phase           Phase
	Weather         string
	PriorAtmosphere string
	Roster          []AtmosphereRosterEntry
}

// FetchAtmosphereContext returns a Command that snapshots the world-
// level inputs the atmosphere prompt needs. `at` is the wall-clock
// moment the sweep was driven; production passes time.Now(), tests pass
// a fixed value for determinism. The Roster slice is freshly allocated;
// the caller may mutate without affecting world state.
//
// Never returns an error — even an empty world produces a valid
// (possibly-empty-Roster) context.
func FetchAtmosphereContext(at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			ctx := AtmosphereContext{
				Now:             at,
				Phase:           w.Phase,
				Weather:         w.Environment.Weather,
				PriorAtmosphere: w.Environment.Atmosphere,
			}

			byLoc := make(map[string][]string)
			var outdoor []string
			for _, a := range w.Actors {
				if a == nil || a.Kind == KindPC {
					continue
				}
				if a.InsideStructureID == "" {
					outdoor = append(outdoor, a.DisplayName)
					continue
				}
				s, ok := w.Structures[a.InsideStructureID]
				if !ok || s == nil || s.DisplayName == "" {
					// Indoor-but-unnamed-structure falls through to
					// outdoor, matching v1 chronicler's "label empty
					// → openAir" branch.
					outdoor = append(outdoor, a.DisplayName)
					continue
				}
				byLoc[s.DisplayName] = append(byLoc[s.DisplayName], a.DisplayName)
			}

			labels := make([]string, 0, len(byLoc))
			for label := range byLoc {
				labels = append(labels, label)
			}
			sort.Strings(labels)
			for _, label := range labels {
				names := byLoc[label]
				sort.Strings(names)
				ctx.Roster = append(ctx.Roster, AtmosphereRosterEntry{
					StructureLabel: label,
					DisplayNames:   names,
				})
			}
			if len(outdoor) > 0 {
				sort.Strings(outdoor)
				ctx.Roster = append(ctx.Roster, AtmosphereRosterEntry{
					StructureLabel: "",
					DisplayNames:   outdoor,
				})
			}
			return ctx, nil
		},
	}
}

// ApplyAtmosphereRefresh returns a Command that installs `text` as the
// new World.Environment.Atmosphere and stamps LastAtmosphereRefreshAt.
//
// Dedup: if the trimmed text matches the trimmed current atmosphere
// exactly, the apply is a no-op — no write, no stamp change. Returns
// `false` in that case so the driver can log distinctly. The
// LLM-emits-same-text path is the common-enough miss case to merit
// the skip; the model gets a fresh attempt next cycle and typically
// produces something different.
//
// Returns `true` (Command Value) when a write occurred, `false` on
// dedup. Errors:
//   - empty text (after trim) — caller already trims, but this guard
//     defends the substrate invariant ("Atmosphere is never set to
//     whitespace-only via this path").
func ApplyAtmosphereRefresh(text string, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			trimmed := strings.TrimSpace(text)
			if trimmed == "" {
				return false, fmt.Errorf("ApplyAtmosphereRefresh: empty text")
			}
			if trimmed == strings.TrimSpace(w.Environment.Atmosphere) {
				return false, nil
			}
			w.Environment.Atmosphere = trimmed
			w.Environment.LastAtmosphereRefreshAt = at
			return true, nil
		},
	}
}
