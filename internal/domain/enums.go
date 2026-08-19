package domain

type Side uint8

const (
	SideUnknown Side = iota
	SideBuy
	SideSell
)

func (s Side) Validate() error {
	if s != SideBuy && s != SideSell {
		return ErrInvalidSide
	}
	return nil
}

type OrderType uint8

const (
	OrderTypeUnknown OrderType = iota
	OrderTypeLimit
)

func (t OrderType) Validate() error {
	if t != OrderTypeLimit {
		return ErrInvalidOrderType
	}
	return nil
}

type OrderStatus uint8

const (
	OrderStatusUnknown OrderStatus = iota
	OrderStatusOpen
	OrderStatusPartiallyFilled
	OrderStatusFilled
	OrderStatusCanceled
)

func (s OrderStatus) Validate() error {
	switch s {
	case OrderStatusOpen, OrderStatusPartiallyFilled, OrderStatusFilled, OrderStatusCanceled:
		return nil
	default:
		return ErrInvalidStatus
	}
}

type TimeInForce uint8

const (
	TimeInForceUnknown TimeInForce = iota
	TimeInForceGTC
	TimeInForceIOC
	TimeInForceFOK
)

func (t TimeInForce) Validate() error {
	switch t {
	case TimeInForceGTC, TimeInForceIOC, TimeInForceFOK:
		return nil
	default:
		return ErrInvalidTimeInForce
	}
}
