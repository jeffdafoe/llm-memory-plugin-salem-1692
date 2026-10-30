package sim

import (
	"fmt"
	"strings"
)

// night_color.go — LLM-842. The night pole of the client's day/night tint as a
// live world setting, so how dark the village gets after dusk is tuned by eye
// on the running village instead of by a client build.
//
// The engine does nothing with the colour itself. It validates and stores it,
// carries it on the public /world DTO (via the snapshot), and emits
// NightColorChanged when it is written so connected clients re-tint without a
// reload — the same shape as the zoom floors (SetZoomSettings).

// NightColorSettingKey is the setting-table key for the night tint.
const NightColorSettingKey = "world_night_color"

// DefaultNightColor is the night tint when the setting is unset or invalid. It
// is the darker of the two poles the client's NIGHT_COLOR comment named
// (#2F3A45 was the old, navigable-unlit pole).
const DefaultNightColor = "#202A35"

// NormalizeNightColor accepts "#RRGGBB" (any case, surrounding space ignored)
// and returns it upper-cased. Anything else is refused: the client parses this
// straight into a Color, and a value it cannot parse would tint the night
// black or not at all.
func NormalizeNightColor(raw string) (string, error) {
	v := strings.ToUpper(strings.TrimSpace(raw))
	if len(v) != 7 || v[0] != '#' {
		return "", fmt.Errorf("%s must be a colour like #202A35, got %q", NightColorSettingKey, raw)
	}
	for _, c := range v[1:] {
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'F') {
			return "", fmt.Errorf("%s must be a colour like #202A35, got %q", NightColorSettingKey, raw)
		}
	}
	return v, nil
}

func nightColorSetting() SettingSpec {
	return SettingSpec{
		Key: NightColorSettingKey, Kind: SettingKindString, Effect: SettingEffectImmediate, Persist: true,
		get: func(ws *WorldSettings) string { return ws.NightColor },
		set: func(ws *WorldSettings, raw string) error {
			v, err := NormalizeNightColor(raw)
			if err != nil {
				return err
			}
			ws.NightColor = v
			return nil
		},
	}
}

// SettingWritten tells connected clients about a setting they render directly,
// after a generic settings write (umbilical /settings/set) has applied it. A
// no-op for every key the client does not read live.
func SettingWritten(w *World, key string) {
	if key == NightColorSettingKey {
		w.emit(&NightColorChanged{NightColor: w.Settings.NightColor})
	}
}
