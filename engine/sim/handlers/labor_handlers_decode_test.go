package handlers

import (
	"encoding/json"
	"fmt"
	"testing"
)

// labor_handlers_decode_test.go — LLM-190. The solicit_work duration bound moved
// to the 4h–8h band (240..480; floor raised from 2h to 4h in LLM-500);
// DecodeSolicitWorkArgs enforces it against
// sim.MinLaborDurationMinutes / MaxLaborDurationMinutes. LLM-225 added the
// in-kind reward leg (reward_items) with the coins-or-goods-or-both validity
// rule.

func TestDecodeSolicitWorkArgs_DurationBounds(t *testing.T) {
	body := func(dur int) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"employer":"Josiah","reward":5,"duration_minutes":%d}`, dur))
	}
	// Below the 4h floor (LLM-500 raised it from 2h). 239 is just under; 120 —
	// the old 2h floor — is now rejected too.
	for _, dur := range []int{239, 120} {
		if _, err := DecodeSolicitWorkArgs(body(dur)); err == nil {
			t.Errorf("duration %d should be rejected (below the 240 floor)", dur)
		}
	}
	// Each in-band tier endpoint is accepted.
	for _, dur := range []int{240, 360, 480} {
		if _, err := DecodeSolicitWorkArgs(body(dur)); err != nil {
			t.Errorf("duration %d should be accepted: %v", dur, err)
		}
	}
	// Above the 8h ceiling.
	if _, err := DecodeSolicitWorkArgs(body(481)); err == nil {
		t.Errorf("duration 481 should be rejected (above the 480 ceiling)")
	}
}

// TestDecodeSolicitWorkArgs_RewardItems — LLM-225: the reward may be coins,
// goods (reward_items), or both; a pay-nothing offer (0 coins, no goods) is
// rejected at decode.
func TestDecodeSolicitWorkArgs_RewardItems(t *testing.T) {
	decode := func(body string) (any, error) {
		return DecodeSolicitWorkArgs(json.RawMessage(body))
	}

	// Coins + goods.
	res, err := decode(`{"employer":"Hannah","reward":2,"reward_items":[{"item":"porridge","qty":1}],"duration_minutes":240}`)
	if err != nil {
		t.Fatalf("coins+goods should decode: %v", err)
	}
	args := res.(SolicitWorkArgs)
	if len(args.RewardItems) != 1 || args.RewardItems[0].Item != "porridge" || args.RewardItems[0].Qty != 1 {
		t.Errorf("RewardItems = %+v, want [{porridge 1}]", args.RewardItems)
	}

	// Goods-only (0 coins) is valid.
	if _, err := decode(`{"employer":"Hannah","reward":0,"reward_items":[{"item":"porridge","qty":1}],"duration_minutes":240}`); err != nil {
		t.Errorf("goods-only reward should decode: %v", err)
	}

	// The stringified-array weak-model tolerance rides payItemList.
	if res, err := decode(`{"employer":"Hannah","reward":0,"reward_items":"[{\"item\":\"porridge\",\"qty\":1}]","duration_minutes":240}`); err != nil {
		t.Errorf("stringified reward_items should decode: %v", err)
	} else if args := res.(SolicitWorkArgs); len(args.RewardItems) != 1 {
		t.Errorf("stringified RewardItems = %+v, want 1 line", args.RewardItems)
	}

	// Pay-nothing: 0 coins and no goods.
	if _, err := decode(`{"employer":"Hannah","reward":0,"duration_minutes":240}`); err == nil {
		t.Error("0-coin, no-goods reward should be rejected")
	}
	// Negative coins.
	if _, err := decode(`{"employer":"Hannah","reward":-1,"reward_items":[{"item":"porridge","qty":1}],"duration_minutes":240}`); err == nil {
		t.Error("negative reward should be rejected")
	}
	// Per-line qty floor rides validatePayItemsDecode.
	if _, err := decode(`{"employer":"Hannah","reward":0,"reward_items":[{"item":"porridge","qty":0}],"duration_minutes":240}`); err == nil {
		t.Error("qty-0 reward_items line should be rejected")
	}
	// Entry cap (9 lines > 8).
	lines := `{"item":"a1","qty":1}`
	for i := 2; i <= 9; i++ {
		lines += fmt.Sprintf(`,{"item":"a%d","qty":1}`, i)
	}
	if _, err := decode(`{"employer":"Hannah","reward":0,"reward_items":[` + lines + `],"duration_minutes":240}`); err == nil {
		t.Error("9-line reward_items should be rejected (8-entry cap)")
	}
}
