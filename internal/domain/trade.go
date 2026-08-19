package domain

import "fmt"

type Trade struct {
	id           TradeID
	sequence     Sequence
	makerOrderID OrderID
	takerOrderID OrderID
	price        Price
	quantity     Quantity
}

func NewTrade(id TradeID, sequence Sequence, makerOrderID, takerOrderID OrderID, price Price, quantity Quantity) (Trade, error) {
	validations := []struct {
		name string
		err  error
	}{
		{"trade ID", id.Validate()},
		{"sequence", sequence.Validate()},
		{"maker order ID", makerOrderID.Validate()},
		{"taker order ID", takerOrderID.Validate()},
		{"price", price.Validate()},
		{"quantity", quantity.Validate()},
	}
	for _, validation := range validations {
		if validation.err != nil {
			return Trade{}, fmt.Errorf("%s: %w", validation.name, validation.err)
		}
	}
	if makerOrderID == takerOrderID {
		return Trade{}, ErrSameOrder
	}

	return Trade{id: id, sequence: sequence, makerOrderID: makerOrderID, takerOrderID: takerOrderID, price: price, quantity: quantity}, nil
}

func (t Trade) ID() TradeID           { return t.id }
func (t Trade) Sequence() Sequence    { return t.sequence }
func (t Trade) MakerOrderID() OrderID { return t.makerOrderID }
func (t Trade) TakerOrderID() OrderID { return t.takerOrderID }
func (t Trade) Price() Price          { return t.price }
func (t Trade) Quantity() Quantity    { return t.quantity }
