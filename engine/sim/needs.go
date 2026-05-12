package sim

import "log"

// Needs subsystem — in-memory port of engine/needs.go + needs_repo.go +
// consumption.go core types.
//
// Each villager (NPC or PC) carries a set of graduated quantities — hunger,
// thirst, tiredness — in Actor.Needs. Values are 0..NeedMax (24). Higher =
// more in need. They climb with simulated time (IncrementNeedsTick) and
// drop on consumption (ApplyConsumption), break/sleep recovery (separate
// subsystems), and dwell at need-recovery objects (dwell.go, ported later).
//
// The registry pattern lets a future fourth need slot in by adding one
// entry to Needs[] plus default thresholds — every consumer (perception,
// tick, consumption, label rendering) picks it up automatically.

const (
	// NeedMax is the hard ceiling on every need value.
	NeedMax = 24

	// MaxNeedsCatchupHours caps the increment applied after a long
	// downtime. Prevents a multi-hour outage from shock-spiking every
	// villager to peak need on the first tick after recovery.
	MaxNeedsCatchupHours = 12

	// NeedsHysteresisMargin keeps the "needs_resolved" detection from
	// chattering across the red threshold. Resolves when value drops at
	// least this far below red. Mirrors the legacy needsHysteresisMargin.
	NeedsHysteresisMargin = 2

	// Default red thresholds when settings rows are missing. Match the
	// values seeded by ZBBS-083.
	DefaultHungerRedThreshold    = 18
	DefaultThirstRedThreshold    = 12
	DefaultTirednessRedThreshold = 20

	// DefaultTirednessCriticalThresholdPct is the % of NeedMax at which
	// recovery-options perception lifts the on-shift gate that hides
	// home/inn/tavern from tired NPCs. Percent so it tracks NeedMax.
	DefaultTirednessCriticalThresholdPct = 90

	// DefaultNeedsTickAmount is the per-hour increment magnitude.
	DefaultNeedsTickAmount = 1

	// DefaultMovementFatiguePerTileX100 is fatigue per tile of movement,
	// stored ×100 to avoid float settings. 12 → +0.12 tiredness per tile.
	DefaultMovementFatiguePerTileX100 = 12

	// needSilentFloor — values below this are not surfaced in perception.
	needSilentFloor = 8
)

// Need describes one graduated quantity an actor carries. The registry
// (Needs) is the canonical list; ported per-need vocabulary is the same
// as legacy.
type Need struct {
	Key                 NeedKey
	Mild                string // tier 1 vocabulary — "peckish", "thirsty", "tired"
	Red                 string // tier 2 vocabulary — "hungry", "parched", "weary"
	Peak                string // tier 3 vocabulary — "starving", "desperate", "exhausted"
	DefaultThreshold    int
	ThresholdSettingKey string // setting row key — used when porting SettingsRepo
}

// Needs is the canonical registry. Iteration order is stable across
// processes; consumers that need a deterministic order (e.g. SELECT FOR
// UPDATE lock order in the future pg repo) can rely on this slice.
var Needs = []Need{
	{
		Key:                 "hunger",
		Mild:                "peckish",
		Red:                 "hungry",
		Peak:                "starving",
		DefaultThreshold:    DefaultHungerRedThreshold,
		ThresholdSettingKey: "hunger_red_threshold",
	},
	{
		Key:                 "thirst",
		Mild:                "thirsty",
		Red:                 "parched",
		Peak:                "desperate",
		DefaultThreshold:    DefaultThirstRedThreshold,
		ThresholdSettingKey: "thirst_red_threshold",
	},
	{
		Key:                 "tiredness",
		Mild:                "tired",
		Red:                 "weary",
		Peak:                "exhausted",
		DefaultThreshold:    DefaultTirednessRedThreshold,
		ThresholdSettingKey: "tiredness_red_threshold",
	},
}

// FindNeed returns the Need with the given key.
func FindNeed(key NeedKey) (Need, bool) {
	for _, n := range Needs {
		if n.Key == key {
			return n, true
		}
	}
	return Need{}, false
}

// NeedTier classifies a need's value into intensity bands.
type NeedTier int

const (
	// NeedSilent — value < needSilentFloor. NPC isn't aware; perception
	// suppresses it.
	NeedSilent NeedTier = 0
	// NeedMild — value in [needSilentFloor, threshold). Awareness without
	// distress; perception's distress block filters these out.
	NeedMild NeedTier = 1
	// NeedRed — value in [threshold, NeedMax). Distress; perception
	// surfaces, chronicler/perception-build may dispatch.
	NeedRed NeedTier = 2
	// NeedPeak — value == NeedMax. Critical distress.
	NeedPeak NeedTier = 3
)

// Tier classifies a value against this need's threshold.
func (n Need) Tier(value, threshold int) NeedTier {
	if value < needSilentFloor {
		return NeedSilent
	}
	if value >= NeedMax {
		return NeedPeak
	}
	if value >= threshold {
		return NeedRed
	}
	return NeedMild
}

// Label returns the vocabulary word for the given tier. Empty for
// NeedSilent — perception code reads that as the "don't surface" signal.
func (n Need) Label(tier NeedTier) string {
	switch tier {
	case NeedMild:
		return n.Mild
	case NeedRed:
		return n.Red
	case NeedPeak:
		return n.Peak
	default:
		return ""
	}
}

// NeedThresholds is a key→threshold lookup for the red-tier boundary per
// need. Lives on WorldSettings; loaded at startup.
type NeedThresholds map[NeedKey]int

// Get returns the threshold for the given need key, or the registry default
// if absent. Safe in the unlikely case of a partial settings load.
func (t NeedThresholds) Get(key NeedKey) int {
	if v, ok := t[key]; ok {
		return v
	}
	if n, ok := FindNeed(key); ok {
		return n.DefaultThreshold
	}
	return 0
}

// DefaultNeedThresholds builds a NeedThresholds with registry defaults.
// Used at world startup if settings haven't been loaded yet.
func DefaultNeedThresholds() NeedThresholds {
	out := make(NeedThresholds, len(Needs))
	for _, n := range Needs {
		out[n.Key] = n.DefaultThreshold
	}
	return out
}

// NeedLabel returns the descriptor for a value against its threshold.
// Convenience for callers that have a key string but don't need a Need.
func NeedLabel(key NeedKey, value, threshold int) string {
	n, ok := FindNeed(key)
	if !ok {
		return ""
	}
	return n.Label(n.Tier(value, threshold))
}

// NeedLabelTier returns 0/1/2/3 — silent/mild/red/peak — without needing
// to lookup the vocabulary. Used by filters that classify by tier.
func NeedLabelTier(value, threshold int) NeedTier {
	if value < needSilentFloor {
		return NeedSilent
	}
	if value >= NeedMax {
		return NeedPeak
	}
	if value >= threshold {
		return NeedRed
	}
	return NeedMild
}

// ClampNeed bounds v into [0, NeedMax]. Centralized so every mutation path
// applies the same invariant.
func ClampNeed(v int) int {
	if v < 0 {
		return 0
	}
	if v > NeedMax {
		return NeedMax
	}
	return v
}

// NeedResolveThreshold returns the value at which a previously-red need
// is considered resolved — red threshold minus the hysteresis margin,
// floored at 1. Prevents chatter at the boundary.
func NeedResolveThreshold(redThreshold int) int {
	floor := redThreshold - NeedsHysteresisMargin
	if floor < 1 {
		return 1
	}
	return floor
}

// SnapshotNeeds returns a copy of the actor's needs as a NeedSet, with
// missing keys logged. Useful for perception build (diff against
// pre-tick state) and for the post-consume readback callers.
//
// Returns an empty NeedSet if actor is nil. Defensive — easier to handle
// at callsites than nil-pointer panics.
func SnapshotNeeds(a *Actor) NeedSet {
	if a == nil || a.Needs == nil {
		return NeedSet{}
	}
	out := make(NeedSet, len(a.Needs))
	for k, v := range a.Needs {
		out[k] = v
	}
	for _, n := range Needs {
		if _, ok := out[n.Key]; !ok {
			log.Printf("sim/needs: actor %s missing need row %q (treating as 0)", a.ID, n.Key)
			out[n.Key] = 0
		}
	}
	return out
}

// NeedSet is one actor's view across the registry. Keyed by NeedKey.
type NeedSet map[NeedKey]int

// Get returns the value for the given need key, or 0 if absent.
func (s NeedSet) Get(key NeedKey) int {
	return s[key]
}

// GetOK returns the value plus a presence flag — distinguishes a real
// 0 from a missing row.
func (s NeedSet) GetOK(key NeedKey) (int, bool) {
	v, ok := s[key]
	return v, ok
}
