package perception

import (
	"fmt"
	"strings"
)

// renderCoPresentBuyUnavailable is the co-present buy when pay_with_item is not
// offered this turn (LLM-1007): mid-walk it names stop as the way to buy now;
// otherwise it states the seller is here without naming a call she cannot make.
// Inputs arrive sanitized.
func renderCoPresentBuyUnavailable(b *strings.Builder, seller, itemLabel string, pay PayAvailability) {
	if pay == PayWalking {
		fmt.Fprintf(b, "%s is here with you and sells %s. You are walking — call stop first if you want to buy from %s now.\n", seller, itemLabel, seller)
		return
	}
	fmt.Fprintf(b, "%s is here with you and sells %s, but you cannot buy from %s just now.\n", seller, itemLabel, seller)
}
