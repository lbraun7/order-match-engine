package domain

import (
	"errors"
	"testing"
)

func TestAdvancedOrderValidation(t *testing.T) {
	valid := []PlaceOrder{
		{OrderID: "market", Side: SideBuy, OrderType: OrderTypeMarket, TimeInForce: TimeInForceIOC, Quantity: 1},
		{OrderID: "stop-market", Side: SideBuy, OrderType: OrderTypeStopMarket, TimeInForce: TimeInForceGTC, StopPrice: 100, Quantity: 1},
		{OrderID: "stop-limit", Side: SideSell, OrderType: OrderTypeStopLimit, TimeInForce: TimeInForceGTC, Price: 99, StopPrice: 100, Quantity: 1},
		{OrderID: "iceberg", ParticipantID: "p1", Side: SideSell, OrderType: OrderTypeIceberg, TimeInForce: TimeInForceGTC, Price: 100, Quantity: 10, DisplayQuantity: 2, SelfTradePrevention: SelfTradePreventionCancelBoth},
	}
	for _, command := range valid {
		if err := command.Validate(); err != nil {
			t.Errorf("%s: %v", command.OrderID, err)
		}
	}

	invalid := []struct {
		name    string
		command PlaceOrder
		want    error
	}{
		{"priced market", PlaceOrder{OrderID: "x", Side: SideBuy, OrderType: OrderTypeMarket, TimeInForce: TimeInForceIOC, Price: 1, Quantity: 1}, ErrInvalidPrice},
		{"stop without trigger", PlaceOrder{OrderID: "x", Side: SideBuy, OrderType: OrderTypeStopMarket, TimeInForce: TimeInForceGTC, Quantity: 1}, ErrInvalidStopPrice},
		{"display on limit", PlaceOrder{OrderID: "x", Side: SideBuy, OrderType: OrderTypeLimit, TimeInForce: TimeInForceGTC, Price: 1, Quantity: 2, DisplayQuantity: 1}, ErrInvalidDisplayQuantity},
		{"full display iceberg", PlaceOrder{OrderID: "x", Side: SideBuy, OrderType: OrderTypeIceberg, TimeInForce: TimeInForceGTC, Price: 1, Quantity: 2, DisplayQuantity: 2}, ErrInvalidDisplayQuantity},
		{"invalid STP", PlaceOrder{OrderID: "x", Side: SideBuy, OrderType: OrderTypeLimit, TimeInForce: TimeInForceGTC, Price: 1, Quantity: 1, SelfTradePrevention: 99}, ErrInvalidSelfTradePrevention},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if err := test.command.Validate(); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}
