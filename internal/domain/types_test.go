package domain

import (
	"errors"
	"testing"
)

func TestPrimitiveValidation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"empty symbol", Symbol("").Validate(), ErrInvalidSymbol},
		{"valid symbol", Symbol("BTC-USD").Validate(), nil},
		{"empty order ID", OrderID("").Validate(), ErrInvalidOrderID},
		{"valid order ID", OrderID("order-1").Validate(), nil},
		{"empty trade ID", TradeID("").Validate(), ErrInvalidTradeID},
		{"valid trade ID", TradeID("trade-1").Validate(), nil},
		{"zero sequence", Sequence(0).Validate(), ErrInvalidSequence},
		{"positive sequence", Sequence(1).Validate(), nil},
		{"negative price", Price(-1).Validate(), ErrInvalidPrice},
		{"zero price", Price(0).Validate(), ErrInvalidPrice},
		{"positive price", Price(1).Validate(), nil},
		{"negative quantity", Quantity(-1).Validate(), ErrInvalidQuantity},
		{"zero quantity", Quantity(0).Validate(), ErrInvalidQuantity},
		{"positive quantity", Quantity(1).Validate(), nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(tt.err, tt.want) {
				t.Fatalf("got error %v, want %v", tt.err, tt.want)
			}
		})
	}
}

func TestEnumValidation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"unknown side", SideUnknown.Validate(), ErrInvalidSide},
		{"buy", SideBuy.Validate(), nil},
		{"sell", SideSell.Validate(), nil},
		{"unknown order type", OrderTypeUnknown.Validate(), ErrInvalidOrderType},
		{"limit", OrderTypeLimit.Validate(), nil},
		{"unknown status", OrderStatusUnknown.Validate(), ErrInvalidStatus},
		{"open", OrderStatusOpen.Validate(), nil},
		{"partially filled", OrderStatusPartiallyFilled.Validate(), nil},
		{"filled", OrderStatusFilled.Validate(), nil},
		{"canceled", OrderStatusCanceled.Validate(), nil},
		{"unknown time in force", TimeInForceUnknown.Validate(), ErrInvalidTimeInForce},
		{"GTC", TimeInForceGTC.Validate(), nil},
		{"IOC", TimeInForceIOC.Validate(), nil},
		{"FOK", TimeInForceFOK.Validate(), nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(tt.err, tt.want) {
				t.Fatalf("got error %v, want %v", tt.err, tt.want)
			}
		})
	}
}
