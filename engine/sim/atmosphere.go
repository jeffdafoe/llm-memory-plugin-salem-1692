package sim

import (
	"fmt"
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
// Activity digest: AtmosphereContext.ActivityDigest carries a
// per-actor group-by-action-type count of committed actions since
// World.Environment.LastAtmosphereRefreshAt. Mirrors v1 chronicler's
// buildActivityDigest read against agent_action_log; v2 reads against
// the in-memory action log (engine/sim/action_log.go). Scope: NPCs
// only (atmosphere is village-mood-focused; PC actions are
// player-driven and noisy). First fire (LastAtmosphereRefreshAt
// zero) produces an empty digest — there's no prior window to digest
// against.

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

// ActivityDigestEntry is one actor's per-ActionType count in the
// since-last-refresh window. One entry per actor with at least one
// counted action. Counts is keyed by ActionType; values are the count
// of committed actions of that type within the window. Order across
// entries is DisplayName-ascending — see AtmosphereContext.ActivityDigest.
type ActivityDigestEntry struct {
	ActorID     ActorID
	DisplayName string
	Counts      map[ActionType]int
}

// AtmosphereContext and FetchAtmosphereContext have moved to
// village_context.go as VillageContext / FetchVillageContext (renamed
// when the noticeboard authoring slice added a second consumer).
// Type-alias + thin wrapper retained there for legacy callers.

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
