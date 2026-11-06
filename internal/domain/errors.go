package domain

import "errors"

// Stable domain errors can be inspected with errors.Is by callers.
var (
	ErrInvalidSymbol              = errors.New("invalid symbol")
	ErrInvalidOrderID             = errors.New("invalid order ID")
	ErrInvalidTradeID             = errors.New("invalid trade ID")
	ErrInvalidParticipantID       = errors.New("invalid participant ID")
	ErrInvalidSequence            = errors.New("invalid sequence")
	ErrInvalidPrice               = errors.New("invalid price")
	ErrInvalidQuantity            = errors.New("invalid quantity")
	ErrInvalidSide                = errors.New("invalid side")
	ErrInvalidOrderType           = errors.New("invalid order type")
	ErrInvalidStatus              = errors.New("invalid order status")
	ErrInvalidTimeInForce         = errors.New("invalid time in force")
	ErrInvalidSelfTradePrevention = errors.New("invalid self-trade prevention")
	ErrInvalidDisplayQuantity     = errors.New("invalid display quantity")
	ErrInvalidStopPrice           = errors.New("invalid stop price")
	ErrOrderNotOpen               = errors.New("order is not open")
	ErrOverfill                   = errors.New("fill quantity exceeds remaining quantity")
	ErrSameOrder                  = errors.New("maker and taker must be different orders")
)
