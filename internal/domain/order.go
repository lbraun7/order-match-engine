package domain

import "fmt"

type Order struct {
	id                  OrderID
	sequence            Sequence
	participantID       ParticipantID
	side                Side
	orderType           OrderType
	timeInForce         TimeInForce
	price               Price
	originalQuantity    Quantity
	remainingQuantity   Quantity
	stopPrice           Price
	displayQuantity     Quantity
	visibleQuantity     Quantity
	selfTradePrevention SelfTradePrevention
	status              OrderStatus
}

func NewOrder(id OrderID, sequence Sequence, side Side, orderType OrderType, timeInForce TimeInForce, price Price, quantity Quantity) (*Order, error) {
	return NewAdvancedOrder(id, sequence, "", side, orderType, timeInForce, price, quantity, 0, 0, SelfTradePreventionNone)
}

func NewAdvancedOrder(id OrderID, sequence Sequence, participantID ParticipantID, side Side, orderType OrderType, timeInForce TimeInForce, price Price, quantity Quantity, stopPrice Price, displayQuantity Quantity, selfTradePrevention SelfTradePrevention) (*Order, error) {
	validations := []struct {
		name string
		err  error
	}{
		{"order ID", id.Validate()},
		{"sequence", sequence.Validate()},
		{"side", side.Validate()},
		{"order type", orderType.Validate()},
		{"time in force", timeInForce.Validate()},
		{"quantity", quantity.Validate()},
		{"self-trade prevention", selfTradePrevention.Validate()},
	}
	for _, validation := range validations {
		if validation.err != nil {
			return nil, fmt.Errorf("%s: %w", validation.name, validation.err)
		}
	}
	if orderType != OrderTypeMarket && orderType != OrderTypeStopMarket {
		if err := price.Validate(); err != nil {
			return nil, fmt.Errorf("price: %w", err)
		}
	} else if price != 0 {
		return nil, fmt.Errorf("market price must be zero: %w", ErrInvalidPrice)
	}
	if orderType == OrderTypeStopMarket || orderType == OrderTypeStopLimit {
		if err := stopPrice.Validate(); err != nil {
			return nil, fmt.Errorf("stop price: %w", ErrInvalidStopPrice)
		}
	} else if stopPrice != 0 {
		return nil, fmt.Errorf("stop price is only valid for stop orders: %w", ErrInvalidStopPrice)
	}
	if orderType == OrderTypeIceberg {
		if displayQuantity <= 0 || displayQuantity >= quantity {
			return nil, ErrInvalidDisplayQuantity
		}
	} else if displayQuantity != 0 {
		return nil, ErrInvalidDisplayQuantity
	}
	visible := quantity
	if displayQuantity > 0 {
		visible = displayQuantity
	}

	return &Order{
		id: id, sequence: sequence, participantID: participantID, side: side, orderType: orderType,
		timeInForce: timeInForce, price: price, originalQuantity: quantity,
		remainingQuantity: quantity, stopPrice: stopPrice, displayQuantity: displayQuantity,
		visibleQuantity: visible, selfTradePrevention: selfTradePrevention, status: OrderStatusOpen,
	}, nil
}

func (o *Order) ID() OrderID                              { return o.id }
func (o *Order) Sequence() Sequence                       { return o.sequence }
func (o *Order) ParticipantID() ParticipantID             { return o.participantID }
func (o *Order) Side() Side                               { return o.side }
func (o *Order) Type() OrderType                          { return o.orderType }
func (o *Order) TimeInForce() TimeInForce                 { return o.timeInForce }
func (o *Order) Price() Price                             { return o.price }
func (o *Order) OriginalQuantity() Quantity               { return o.originalQuantity }
func (o *Order) RemainingQuantity() Quantity              { return o.remainingQuantity }
func (o *Order) StopPrice() Price                         { return o.stopPrice }
func (o *Order) DisplayQuantity() Quantity                { return o.displayQuantity }
func (o *Order) VisibleQuantity() Quantity                { return o.visibleQuantity }
func (o *Order) SelfTradePrevention() SelfTradePrevention { return o.selfTradePrevention }
func (o *Order) ExecutedQuantity() Quantity               { return o.originalQuantity - o.remainingQuantity }
func (o *Order) Status() OrderStatus                      { return o.status }

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
	o.visibleQuantity = max(0, o.visibleQuantity-quantity)
	if o.remainingQuantity == 0 {
		o.status = OrderStatusFilled
	} else {
		o.status = OrderStatusPartiallyFilled
	}
	return nil
}

// RestoreVisible is used only by validated recovery snapshots.
func (o *Order) RestoreVisible(quantity Quantity) error {
	if quantity < 0 || quantity > o.remainingQuantity || (o.displayQuantity == 0 && o.status != OrderStatusCanceled && quantity != o.remainingQuantity) || (o.displayQuantity > 0 && quantity > o.displayQuantity) {
		return ErrInvalidDisplayQuantity
	}
	o.visibleQuantity = quantity
	return nil
}

// RefreshDisplay replenishes an iceberg tranche after its visible quantity is
// exhausted. The engine moves the order to the back of its price level first.
func (o *Order) RefreshDisplay() {
	if o.displayQuantity == 0 || o.visibleQuantity > 0 || o.remainingQuantity == 0 {
		return
	}
	o.visibleQuantity = min(o.displayQuantity, o.remainingQuantity)
}

func (o *Order) Cancel() error {
	if o.status != OrderStatusOpen && o.status != OrderStatusPartiallyFilled {
		return ErrOrderNotOpen
	}
	o.status = OrderStatusCanceled
	o.visibleQuantity = 0
	return nil
}
