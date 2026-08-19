package domain

import "fmt"

type Order struct {
	id                OrderID
	sequence          Sequence
	side              Side
	orderType         OrderType
	timeInForce       TimeInForce
	price             Price
	originalQuantity  Quantity
	remainingQuantity Quantity
	status            OrderStatus
}

func NewOrder(id OrderID, sequence Sequence, side Side, orderType OrderType, timeInForce TimeInForce, price Price, quantity Quantity) (*Order, error) {
	validations := []struct {
		name string
		err  error
	}{
		{"order ID", id.Validate()},
		{"sequence", sequence.Validate()},
		{"side", side.Validate()},
		{"order type", orderType.Validate()},
		{"time in force", timeInForce.Validate()},
		{"price", price.Validate()},
		{"quantity", quantity.Validate()},
	}
	for _, validation := range validations {
		if validation.err != nil {
			return nil, fmt.Errorf("%s: %w", validation.name, validation.err)
		}
	}

	return &Order{
		id: id, sequence: sequence, side: side, orderType: orderType,
		timeInForce: timeInForce, price: price, originalQuantity: quantity,
		remainingQuantity: quantity, status: OrderStatusOpen,
	}, nil
}

func (o *Order) ID() OrderID                 { return o.id }
func (o *Order) Sequence() Sequence          { return o.sequence }
func (o *Order) Side() Side                  { return o.side }
func (o *Order) Type() OrderType             { return o.orderType }
func (o *Order) TimeInForce() TimeInForce    { return o.timeInForce }
func (o *Order) Price() Price                { return o.price }
func (o *Order) OriginalQuantity() Quantity  { return o.originalQuantity }
func (o *Order) RemainingQuantity() Quantity { return o.remainingQuantity }
func (o *Order) ExecutedQuantity() Quantity  { return o.originalQuantity - o.remainingQuantity }
func (o *Order) Status() OrderStatus         { return o.status }

func (o *Order) Fill(quantity Quantity) error {
	if o.status != OrderStatusOpen && o.status != OrderStatusPartiallyFilled {
		return ErrOrderNotOpen
	}
	if err := quantity.Validate(); err != nil {
		return err
	}
	if quantity > o.remainingQuantity {
		return ErrOverfill
	}

	o.remainingQuantity -= quantity
	if o.remainingQuantity == 0 {
		o.status = OrderStatusFilled
	} else {
		o.status = OrderStatusPartiallyFilled
	}
	return nil
}

func (o *Order) Cancel() error {
	if o.status != OrderStatusOpen && o.status != OrderStatusPartiallyFilled {
		return ErrOrderNotOpen
	}
	o.status = OrderStatusCanceled
	return nil
}
