package domain

import (
	"errors"
	"testing"
)

func TestNewTradeValidation(t *testing.T) {
	tests := []struct {
		name         string
		id           TradeID
		sequence     Sequence
		maker, taker OrderID
		price        Price
		quantity     Quantity
		want         error
	}{
		{"valid", "trade-1", 3, "maker", "taker", 100, 2, nil},
		{"invalid trade ID", "", 3, "maker", "taker", 100, 2, ErrInvalidTradeID},
		{"invalid sequence", "trade-1", 0, "maker", "taker", 100, 2, ErrInvalidSequence},
		{"invalid maker", "trade-1", 3, "", "taker", 100, 2, ErrInvalidOrderID},
		{"invalid taker", "trade-1", 3, "maker", "", 100, 2, ErrInvalidOrderID},
		{"same order", "trade-1", 3, "same", "same", 100, 2, ErrSameOrder},
		{"invalid price", "trade-1", 3, "maker", "taker", 0, 2, ErrInvalidPrice},
		{"invalid quantity", "trade-1", 3, "maker", "taker", 100, 0, ErrInvalidQuantity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trade, err := NewTrade(tt.id, tt.sequence, tt.maker, tt.taker, tt.price, tt.quantity)
			if !errors.Is(err, tt.want) {
				t.Fatalf("got error %v, want %v", err, tt.want)
			}
			if tt.want == nil && (trade.ID() != tt.id || trade.Sequence() != tt.sequence || trade.MakerOrderID() != tt.maker || trade.TakerOrderID() != tt.taker || trade.Price() != tt.price || trade.Quantity() != tt.quantity) {
				t.Fatal("trade accessors do not preserve constructor values")
			}
		})
	}
}

func TestCommandValidation(t *testing.T) {
	valid := PlaceOrder{OrderID: "order-1", Side: SideSell, OrderType: OrderTypeLimit, TimeInForce: TimeInForceGTC, Price: 100, Quantity: 5}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid place command: %v", err)
	}

	tests := []struct {
		name string
		cmd  PlaceOrder
		want error
	}{
		{"ID", PlaceOrder{Side: SideSell, OrderType: OrderTypeLimit, TimeInForce: TimeInForceGTC, Price: 100, Quantity: 5}, ErrInvalidOrderID},
		{"side", PlaceOrder{OrderID: "order-1", OrderType: OrderTypeLimit, TimeInForce: TimeInForceGTC, Price: 100, Quantity: 5}, ErrInvalidSide},
		{"type", PlaceOrder{OrderID: "order-1", Side: SideSell, TimeInForce: TimeInForceGTC, Price: 100, Quantity: 5}, ErrInvalidOrderType},
		{"time in force", PlaceOrder{OrderID: "order-1", Side: SideSell, OrderType: OrderTypeLimit, Price: 100, Quantity: 5}, ErrInvalidTimeInForce},
		{"price", PlaceOrder{OrderID: "order-1", Side: SideSell, OrderType: OrderTypeLimit, TimeInForce: TimeInForceGTC, Quantity: 5}, ErrInvalidPrice},
		{"quantity", PlaceOrder{OrderID: "order-1", Side: SideSell, OrderType: OrderTypeLimit, TimeInForce: TimeInForceGTC, Price: 100}, ErrInvalidQuantity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cmd.Validate(); !errors.Is(err, tt.want) {
				t.Fatalf("got error %v, want %v", err, tt.want)
			}
		})
	}

	if err := (CancelOrder{OrderID: "order-1"}).Validate(); err != nil {
		t.Fatalf("valid cancel command: %v", err)
	}
	if err := (CancelOrder{}).Validate(); !errors.Is(err, ErrInvalidOrderID) {
		t.Fatalf("invalid cancel command got %v, want %v", err, ErrInvalidOrderID)
	}
}
