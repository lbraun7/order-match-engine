package domain

// PlaceOrder carries client intent. Sequence is deliberately absent: the
// engine assigns it when processing this command.
type PlaceOrder struct {
	OrderID     OrderID
	Side        Side
	OrderType   OrderType
	TimeInForce TimeInForce
	Price       Price
	Quantity    Quantity
}

func (c PlaceOrder) Validate() error {
	_, err := NewOrder(c.OrderID, 1, c.Side, c.OrderType, c.TimeInForce, c.Price, c.Quantity)
	return err
}

type CancelOrder struct {
	OrderID OrderID
}

func (c CancelOrder) Validate() error {
	return c.OrderID.Validate()
}
