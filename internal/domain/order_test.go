package domain

import (
	"errors"
	"testing"
)

func validOrder(t *testing.T) *Order {
	t.Helper()
	order, err := NewOrder("order-1", 1, SideBuy, OrderTypeLimit, TimeInForceGTC, 100, 10)
	if err != nil {
		t.Fatalf("NewOrder() unexpected error: %v", err)
	}
	return order
}

func TestNewOrderValidation(t *testing.T) {
	tests := []struct {
		name        string
		id          OrderID
		sequence    Sequence
		side        Side
		orderType   OrderType
		timeInForce TimeInForce
		price       Price
		quantity    Quantity
		want        error
	}{
		{"valid", "order-1", 1, SideBuy, OrderTypeLimit, TimeInForceGTC, 100, 10, nil},
		{"invalid ID", "", 1, SideBuy, OrderTypeLimit, TimeInForceGTC, 100, 10, ErrInvalidOrderID},
		{"invalid sequence", "order-1", 0, SideBuy, OrderTypeLimit, TimeInForceGTC, 100, 10, ErrInvalidSequence},
		{"invalid side", "order-1", 1, SideUnknown, OrderTypeLimit, TimeInForceGTC, 100, 10, ErrInvalidSide},
		{"invalid type", "order-1", 1, SideBuy, OrderTypeUnknown, TimeInForceGTC, 100, 10, ErrInvalidOrderType},
		{"invalid time in force", "order-1", 1, SideBuy, OrderTypeLimit, TimeInForceUnknown, 100, 10, ErrInvalidTimeInForce},
		{"invalid price", "order-1", 1, SideBuy, OrderTypeLimit, TimeInForceGTC, 0, 10, ErrInvalidPrice},
		{"invalid quantity", "order-1", 1, SideBuy, OrderTypeLimit, TimeInForceGTC, 100, 0, ErrInvalidQuantity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			order, err := NewOrder(tt.id, tt.sequence, tt.side, tt.orderType, tt.timeInForce, tt.price, tt.quantity)
			if !errors.Is(err, tt.want) {
				t.Fatalf("got error %v, want %v", err, tt.want)
			}
			if tt.want != nil && order != nil {
				t.Fatal("invalid input returned a non-nil order")
			}
		})
	}
}

func TestOrderLifecycle(t *testing.T) {
	order := validOrder(t)

	if order.ID() != "order-1" || order.Sequence() != 1 || order.Side() != SideBuy || order.Type() != OrderTypeLimit || order.TimeInForce() != TimeInForceGTC || order.Price() != 100 {
		t.Fatal("order accessors do not preserve constructor values")
	}
	if order.OriginalQuantity() != 10 || order.RemainingQuantity() != 10 || order.ExecutedQuantity() != 0 || order.Status() != OrderStatusOpen {
		t.Fatal("new order has unexpected quantity or status")
	}

	if err := order.Fill(4); err != nil {
		t.Fatalf("partial fill: %v", err)
	}
	if order.RemainingQuantity() != 6 || order.ExecutedQuantity() != 4 || order.Status() != OrderStatusPartiallyFilled {
		t.Fatal("partial fill did not update the order correctly")
	}
	if err := order.Fill(6); err != nil {
		t.Fatalf("complete fill: %v", err)
	}
	if order.RemainingQuantity() != 0 || order.ExecutedQuantity() != 10 || order.Status() != OrderStatusFilled {
		t.Fatal("complete fill did not update the order correctly")
	}
	if err := order.Fill(1); !errors.Is(err, ErrOrderNotOpen) {
		t.Fatalf("fill of completed order got %v, want %v", err, ErrOrderNotOpen)
	}
}

func TestFillRejectsInvalidQuantityWithoutChangingOrder(t *testing.T) {
	tests := []struct {
		name     string
		quantity Quantity
		want     error
	}{
		{"negative", -1, ErrInvalidQuantity},
		{"zero", 0, ErrInvalidQuantity},
		{"overfill", 11, ErrOverfill},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			order := validOrder(t)
			if err := order.Fill(tt.quantity); !errors.Is(err, tt.want) {
				t.Fatalf("got error %v, want %v", err, tt.want)
			}
			if order.RemainingQuantity() != 10 || order.ExecutedQuantity() != 0 || order.Status() != OrderStatusOpen {
				t.Fatal("rejected fill changed the order")
			}
		})
	}
}

func TestCancelOrder(t *testing.T) {
	order := validOrder(t)
	if err := order.Fill(2); err != nil {
		t.Fatal(err)
	}
	if err := order.Cancel(); err != nil {
		t.Fatalf("Cancel(): %v", err)
	}
	if order.Status() != OrderStatusCanceled || order.RemainingQuantity() != 8 || order.ExecutedQuantity() != 2 {
		t.Fatal("cancel changed quantities or assigned the wrong status")
	}
	if err := order.Cancel(); !errors.Is(err, ErrOrderNotOpen) {
		t.Fatalf("second cancel got %v, want %v", err, ErrOrderNotOpen)
	}
}
