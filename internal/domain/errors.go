package domain

import "errors"

// Stable domain errors can be inspected with errors.Is by callers.
var (
	ErrInvalidOrderID     = errors.New("invalid order ID")
	ErrInvalidTradeID     = errors.New("invalid trade ID")
	ErrInvalidSequence    = errors.New("invalid sequence")
	ErrInvalidPrice       = errors.New("invalid price")
	ErrInvalidQuantity    = errors.New("invalid quantity")
	ErrInvalidSide        = errors.New("invalid side")
	ErrInvalidOrderType   = errors.New("invalid order type")
	ErrInvalidStatus      = errors.New("invalid order status")
	ErrInvalidTimeInForce = errors.New("invalid time in force")
	ErrOrderNotOpen       = errors.New("order is not open")
	ErrOverfill           = errors.New("fill quantity exceeds remaining quantity")
	ErrSameOrder          = errors.New("maker and taker must be different orders")
)
