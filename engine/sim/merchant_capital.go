package sim

// merchant_capital.go — LLM-294. The working-capital coin floor: the purse
// balance below which a keeper sitting on unsold sellable stock is steered to
// conserve coin (hold off buying, sell down its shelves) instead of restocking.
// The determination + overstock qualifier live in the perception package (that is
// where the render targets are); this file carries only the sim-side default the
// pg loader seeds when the setting key is absent.
//
// MerchantCoinFloor semantics mirror LaborProduceBoostPct, NOT SeekWorkCoinCeiling:
// the pg loader seeds MerchantCoinFloorDefault when merchant_coin_floor is unset, and
// an explicit 0 STICKS and disables the gate (the off-switch). So there is no
// effective-value indirection — the snapshot mirrors the raw WorldSettings value and
// the perception gate reads it as "floor > 0 && coins < floor" (0 ⇒ feature off).

// MerchantCoinFloorDefault is the coin balance below which a stock-rich keeper is
// steered to conserve (LLM-294). Seeded by the pg loader when merchant_coin_floor is
// absent; an operator can raise/lower it or set 0 to disable via the umbilical. A
// guesstimate, tuned live.
const MerchantCoinFloorDefault = 10
