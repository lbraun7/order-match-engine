package domain

// IDs are strings because their allocation strategy belongs to the engine or
// its caller, not to the domain model.
type (
	OrderID string
	TradeID string
)

// Numeric matching values are integers to avoid floating-point rounding.
type (
	Sequence uint64
	Price    int64
	Quantity int64
)

func (id OrderID) Validate() error {
	if id == "" {
		return ErrInvalidOrderID
	}
	return nil
}

func (id TradeID) Validate() error {
	if id == "" {
		return ErrInvalidTradeID
	}
	return nil
}

func (s Sequence) Validate() error {
	if s == 0 {
		return ErrInvalidSequence
	}
	return nil
}

func (p Price) Validate() error {
	if p <= 0 {
		return ErrInvalidPrice
	}
	return nil
}

func (q Quantity) Validate() error {
	if q <= 0 {
		return ErrInvalidQuantity
	}
	return nil
}
