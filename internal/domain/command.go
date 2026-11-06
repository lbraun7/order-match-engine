package domain

// PlaceOrder carries client intent. Sequence is deliberately absent: the
// engine assigns it when processing this command.
type PlaceOrder struct {
	OrderID             OrderID
	ParticipantID       ParticipantID
	Side                Side
	OrderType           OrderType
	TimeInForce         TimeInForce
	Price               Price
	Quantity            Quantity
	StopPrice           Price
	DisplayQuantity     Quantity
	SelfTradePrevention SelfTradePrevention
}

func (c PlaceOrder) Validate() error {
	_, err := NewAdvancedOrder(c.OrderID, 1, c.ParticipantID, c.Side, c.OrderType, c.TimeInForce,
		c.Price, c.Quantity, c.StopPrice, c.DisplayQuantity, c.SelfTradePrevention)
	return err
}

type CancelOrder struct {
	OrderID OrderID
}

func (c CancelOrder) Validate() error {
	return c.OrderID.Validate()
}

// MassCancel cancels all active orders, optionally filtering by participant.
// Symbol scope is provided by the router and is deliberately absent here.
type MassCancel struct {
	ParticipantID ParticipantID
}
