package messagebox

import (
	"context"
	"errors"
	"testing"

	"github.com/bsv-blockchain/go-sdk/wallet"
)

// The wallet AuthFetch is handed cannot create an action, so a 402 never
// becomes a payment.
func TestNoPayRefusesActions(t *testing.T) {
	if _, err := (noPay{}).CreateAction(context.Background(), wallet.CreateActionArgs{}, ""); !errors.Is(err, ErrPayment) {
		t.Fatal(err)
	}
}
