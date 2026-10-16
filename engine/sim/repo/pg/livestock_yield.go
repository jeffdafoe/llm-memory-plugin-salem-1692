package pg

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// livestockYieldJSON is the actor.livestock_yield wire shape (LLM-797): a milking
// animal's gather row plus its pending feed bonus. A dedicated struct, as with
// moveDestinationJSON, so the persisted format is this layer's own contract.
type livestockYieldJSON struct {
	GatherItem         string     `json:"gather_item"`
	AvailableQuantity  int        `json:"available_quantity"`
	MaxQuantity        int        `json:"max_quantity"`
	RefreshMode        string     `json:"refresh_mode"`
	RefreshPeriodHours int        `json:"refresh_period_hours"`
	LastRefreshAt      *time.Time `json:"last_refresh_at,omitempty"`
	FedBonus           int        `json:"fed_bonus,omitempty"`
}

// encodeLivestockYield is the column value: NULL for an actor with no yield.
func encodeLivestockYield(y *sim.LivestockYield) any {
	if y == nil || y.Row.AvailableQuantity == nil || y.Row.MaxQuantity == nil || y.Row.RefreshPeriodHours == nil {
		return nil
	}
	blob, err := json.Marshal(livestockYieldJSON{
		GatherItem:         string(y.Row.GatherItem),
		AvailableQuantity:  *y.Row.AvailableQuantity,
		MaxQuantity:        *y.Row.MaxQuantity,
		RefreshMode:        string(y.Row.RefreshMode),
		RefreshPeriodHours: *y.Row.RefreshPeriodHours,
		LastRefreshAt:      y.Row.LastRefreshAt,
		FedBonus:           y.FedBonus,
	})
	if err != nil {
		return nil
	}
	return blob
}

// decodeLivestockYield parses the column; nil blob (NULL) → nil yield.
func decodeLivestockYield(blob []byte) (*sim.LivestockYield, error) {
	if len(blob) == 0 {
		return nil, nil
	}
	var in livestockYieldJSON
	if err := json.Unmarshal(blob, &in); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	avail, max, period := in.AvailableQuantity, in.MaxQuantity, in.RefreshPeriodHours
	y := &sim.LivestockYield{
		Row: sim.ObjectRefresh{
			GatherItem:         sim.ItemKind(in.GatherItem),
			AvailableQuantity:  &avail,
			MaxQuantity:        &max,
			RefreshMode:        sim.RefreshMode(in.RefreshMode),
			RefreshPeriodHours: &period,
			LastRefreshAt:      in.LastRefreshAt,
		},
		FedBonus: in.FedBonus,
	}
	if err := sim.ValidateLivestockYield(y); err != nil {
		return nil, err
	}
	return y, nil
}
