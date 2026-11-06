package engine

import (
	"container/list"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/lucasbraun96/order-match-engine/internal/domain"
)

var (
	ErrDuplicateOrderID      = errors.New("duplicate order ID")
	ErrOrderNotFound         = errors.New("order not found")
	ErrInvalidOrderSemantics = errors.New("invalid order semantics")
)

// OrderSnapshot is an immutable view of an order at query time.
type OrderSnapshot struct {
	ID                  domain.OrderID
	Sequence            domain.Sequence
	ParticipantID       domain.ParticipantID
	Side                domain.Side
	Type                domain.OrderType
	TimeInForce         domain.TimeInForce
	Price               domain.Price
	OriginalQuantity    domain.Quantity
	RemainingQuantity   domain.Quantity
	ExecutedQuantity    domain.Quantity
	StopPrice           domain.Price
	DisplayQuantity     domain.Quantity
	VisibleQuantity     domain.Quantity
	SelfTradePrevention domain.SelfTradePrevention
	Dormant             bool
	Status              domain.OrderStatus
}

// PriceLevelSnapshot contains resting orders in FIFO execution order.
type PriceLevelSnapshot struct {
	Price         domain.Price
	TotalQuantity domain.Quantity
	Orders        []OrderSnapshot
}

// BookSnapshot is a point-in-time copy of the visible order book.
type BookSnapshot struct {
	Sequence domain.Sequence
	Bids     []PriceLevelSnapshot
	Asks     []PriceLevelSnapshot
}

// OrderBook is a deterministic, in-memory limit order book. It is deliberately
// not concurrency-safe: a later engine layer owns its single-writer lifecycle.
type OrderBook struct {
	nextSequence   domain.Sequence
	bids           bookSide
	asks           bookSide
	orders         map[domain.OrderID]*domain.Order
	active         map[domain.OrderID]orderLocation
	stops          []*domain.Order
	lastTradePrice domain.Price
}

type bookSide struct {
	prices []domain.Price
	levels map[domain.Price]*list.List
}

type orderLocation struct {
	side    domain.Side
	price   domain.Price
	element *list.Element
}

func NewOrderBook() *OrderBook {
	return &OrderBook{
		bids:   bookSide{levels: make(map[domain.Price]*list.List)},
		asks:   bookSide{levels: make(map[domain.Price]*list.List)},
		orders: make(map[domain.OrderID]*domain.Order),
		active: make(map[domain.OrderID]orderLocation),
	}
}

func (b *OrderBook) canPlace(command domain.PlaceOrder) error {
	if err := command.Validate(); err != nil {
		return err
	}
	switch command.OrderType {
	case domain.OrderTypeLimit:
		// GTC, IOC, and FOK are supported.
	case domain.OrderTypeMarket:
		if command.TimeInForce != domain.TimeInForceIOC {
			return fmt.Errorf("market orders require IOC: %w", ErrInvalidOrderSemantics)
		}
	case domain.OrderTypeStopMarket, domain.OrderTypeStopLimit, domain.OrderTypeIceberg:
		if command.TimeInForce != domain.TimeInForceGTC {
			return fmt.Errorf("stop and iceberg orders require GTC: %w", ErrInvalidOrderSemantics)
		}
	default:
		return ErrInvalidOrderSemantics
	}
	if _, exists := b.orders[command.OrderID]; exists {
		return fmt.Errorf("%s: %w", command.OrderID, ErrDuplicateOrderID)
	}
	return nil
}

// Placement captures every state change caused by one placement, including
// stop activations and self-trade prevention cancellations.
type Placement struct {
	Order    *domain.Order
	Trades   []domain.Trade
	Affected []OrderSnapshot
}

func (b *OrderBook) canCancel(command domain.CancelOrder) error {
	if err := command.Validate(); err != nil {
		return err
	}
	order, exists := b.orders[command.OrderID]
	if !exists {
		return fmt.Errorf("%s: %w", command.OrderID, ErrOrderNotFound)
	}
	if order.Status() != domain.OrderStatusOpen && order.Status() != domain.OrderStatusPartiallyFilled {
		return fmt.Errorf("%s: %w", command.OrderID, domain.ErrOrderNotOpen)
	}
	return nil
}

// PlaceOrder assigns the order sequence, matches at maker prices, and rests any
// remaining quantity. A single monotonically increasing sequence is shared by
// accepted orders and generated trades.
func (b *OrderBook) PlaceOrder(command domain.PlaceOrder) (*domain.Order, []domain.Trade, error) {
	result, err := b.PlaceOrderDetailed(command)
	if err != nil {
		return nil, nil, err
	}
	return result.Order, result.Trades, nil
}

func (b *OrderBook) PlaceOrderDetailed(command domain.PlaceOrder) (Placement, error) {
	if err := b.canPlace(command); err != nil {
		return Placement{}, err
	}

	orderSequence := b.allocateSequence()
	order, err := domain.NewAdvancedOrder(command.OrderID, orderSequence, command.ParticipantID, command.Side,
		command.OrderType, command.TimeInForce, command.Price, command.Quantity, command.StopPrice,
		command.DisplayQuantity, command.SelfTradePrevention)
	if err != nil {
		return Placement{}, err
	}
	b.orders[order.ID()] = order
	if order.Type() == domain.OrderTypeStopMarket || order.Type() == domain.OrderTypeStopLimit {
		if !stopTriggered(order, b.lastTradePrice) {
			b.stops = append(b.stops, order)
			return Placement{Order: order, Affected: []OrderSnapshot{b.snapshotOrder(order)}}, nil
		}
	}
	if order.TimeInForce() == domain.TimeInForceFOK && !b.canFullyFill(order) {
		_ = order.Cancel()
		return Placement{Order: order, Affected: []OrderSnapshot{b.snapshotOrder(order)}}, nil
	}

	trades, affected := b.execute(order)
	for tradeIndex := 0; tradeIndex < len(trades); tradeIndex++ {
		triggered := b.takeTriggeredStops(trades[tradeIndex].Price())
		for _, stop := range triggered {
			moreTrades, moreAffected := b.execute(stop)
			trades = append(trades, moreTrades...)
			affected = append(affected, moreAffected...)
			affected = append(affected, b.snapshotOrder(stop))
		}
	}
	affected = append(affected, b.snapshotOrder(order))
	return Placement{Order: order, Trades: trades, Affected: deduplicateSnapshots(affected)}, nil
}

func (b *OrderBook) execute(order *domain.Order) ([]domain.Trade, []OrderSnapshot) {
	trades, affected := b.match(order)
	if len(trades) > 0 {
		b.lastTradePrice = trades[len(trades)-1].Price()
	}
	if order.RemainingQuantity() > 0 && (order.TimeInForce() == domain.TimeInForceIOC || order.Type() == domain.OrderTypeMarket || order.Type() == domain.OrderTypeStopMarket) {
		_ = order.Cancel()
	} else if order.RemainingQuantity() > 0 && order.Status() != domain.OrderStatusCanceled {
		order.RefreshDisplay()
		b.rest(order)
	}
	return trades, affected
}

// CancelOrder removes a resting order while preserving the FIFO order of the
// other entries at its price. Filled and already canceled orders are rejected
// by the domain transition with domain.ErrOrderNotOpen.
func (b *OrderBook) CancelOrder(command domain.CancelOrder) (OrderSnapshot, error) {
	if err := b.canCancel(command); err != nil {
		return OrderSnapshot{}, err
	}
	order := b.orders[command.OrderID]
	location, active := b.active[command.OrderID]
	if !active {
		for index, stop := range b.stops {
			if stop.ID() != command.OrderID {
				continue
			}
			if err := order.Cancel(); err != nil {
				return OrderSnapshot{}, fmt.Errorf("%s: %w", command.OrderID, err)
			}
			b.stops = append(b.stops[:index], b.stops[index+1:]...)
			b.allocateSequence()
			return snapshotOrder(order), nil
		}
		panic("engine invariant violated: open order missing from active index")
	}

	side := b.side(location.side)
	level := side.levels[location.price]
	if level == nil {
		panic("engine invariant violated: active order missing price level")
	}
	if err := order.Cancel(); err != nil {
		return OrderSnapshot{}, fmt.Errorf("%s: %w", command.OrderID, err)
	}
	level.Remove(location.element)
	delete(b.active, command.OrderID)
	if level.Len() == 0 {
		delete(side.levels, location.price)
		removePrice(side, location.price)
	}
	// Cancellations mutate visible market state and therefore consume a
	// sequence just like accepted orders and trades.
	b.allocateSequence()
	return snapshotOrder(order), nil
}

// MassCancel cancels active and dormant orders in deterministic acceptance
// order. One sequence represents the atomic command regardless of count.
func (b *OrderBook) MassCancel(command domain.MassCancel) ([]OrderSnapshot, error) {
	var candidates []*domain.Order
	for _, order := range b.orders {
		if order.Status() != domain.OrderStatusOpen && order.Status() != domain.OrderStatusPartiallyFilled {
			continue
		}
		if command.ParticipantID != "" && order.ParticipantID() != command.ParticipantID {
			continue
		}
		candidates = append(candidates, order)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Sequence() < candidates[j].Sequence() })
	result := make([]OrderSnapshot, 0, len(candidates))
	for _, order := range candidates {
		if location, ok := b.active[order.ID()]; ok {
			side := b.side(location.side)
			level := side.levels[location.price]
			level.Remove(location.element)
			delete(b.active, order.ID())
			if level.Len() == 0 {
				delete(side.levels, location.price)
				removePrice(side, location.price)
			}
		} else {
			for index, stop := range b.stops {
				if stop.ID() == order.ID() {
					b.stops = append(b.stops[:index], b.stops[index+1:]...)
					break
				}
			}
		}
		if err := order.Cancel(); err != nil {
			return nil, err
		}
		result = append(result, snapshotOrder(order))
	}
	b.allocateSequence()
	return result, nil
}

type persistedBook struct {
	Sequence       domain.Sequence `json:"sequence"`
	LastTradePrice domain.Price    `json:"last_trade_price,omitempty"`
	Orders         []OrderSnapshot `json:"orders"`
}

func (b *OrderBook) marshalState() ([]byte, error) {
	state := persistedBook{Sequence: b.nextSequence, LastTradePrice: b.lastTradePrice, Orders: make([]OrderSnapshot, 0, len(b.orders))}
	for _, order := range b.orders {
		state.Orders = append(state.Orders, b.snapshotOrder(order))
	}
	sort.Slice(state.Orders, func(i, j int) bool { return state.Orders[i].Sequence < state.Orders[j].Sequence })
	return json.Marshal(state)
}

func restoreOrderBook(data []byte) (*OrderBook, error) {
	var state persistedBook
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode order book snapshot: %w", err)
	}
	book := NewOrderBook()
	book.nextSequence = state.Sequence
	book.lastTradePrice = state.LastTradePrice
	for _, saved := range state.Orders {
		if saved.Sequence > state.Sequence {
			return nil, fmt.Errorf("order %s sequence exceeds snapshot: %w", saved.ID, ErrDuplicateOrderID)
		}
		if _, exists := book.orders[saved.ID]; exists {
			return nil, fmt.Errorf("%s: %w", saved.ID, ErrDuplicateOrderID)
		}
		order, err := domain.NewAdvancedOrder(saved.ID, saved.Sequence, saved.ParticipantID, saved.Side, saved.Type,
			saved.TimeInForce, saved.Price, saved.OriginalQuantity, saved.StopPrice, saved.DisplayQuantity,
			saved.SelfTradePrevention)
		if err != nil {
			return nil, err
		}
		if saved.ExecutedQuantity > 0 {
			if err := order.Fill(saved.ExecutedQuantity); err != nil {
				return nil, err
			}
		}
		if saved.VisibleQuantity == 0 && saved.DisplayQuantity == 0 && saved.RemainingQuantity > 0 && saved.Status != domain.OrderStatusCanceled {
			saved.VisibleQuantity = saved.RemainingQuantity
		}
		if saved.Status == domain.OrderStatusCanceled {
			if err := order.Cancel(); err != nil {
				return nil, err
			}
		}
		if err := order.RestoreVisible(saved.VisibleQuantity); err != nil {
			return nil, err
		}
		book.orders[saved.ID] = order
		if saved.Dormant {
			book.stops = append(book.stops, order)
		} else if order.Status() == domain.OrderStatusOpen || order.Status() == domain.OrderStatusPartiallyFilled {
			book.rest(order)
		}
		if book.snapshotOrder(order) != saved {
			return nil, fmt.Errorf("order %s state is inconsistent", saved.ID)
		}
	}
	return book, nil
}

// GetOrder returns a copy of any accepted order, including terminal orders.
func (b *OrderBook) GetOrder(id domain.OrderID) (OrderSnapshot, error) {
	if err := id.Validate(); err != nil {
		return OrderSnapshot{}, err
	}
	order, exists := b.orders[id]
	if !exists {
		return OrderSnapshot{}, fmt.Errorf("%s: %w", id, ErrOrderNotFound)
	}
	return b.snapshotOrder(order), nil
}

// Snapshot copies both sides in price priority and each level in FIFO order.
func (b *OrderBook) Snapshot() BookSnapshot {
	return BookSnapshot{
		Sequence: b.nextSequence,
		Bids:     snapshotSide(&b.bids),
		Asks:     snapshotSide(&b.asks),
	}
}

func (b *OrderBook) match(taker *domain.Order) ([]domain.Trade, []OrderSnapshot) {
	opposite := &b.asks
	if taker.Side() == domain.SideSell {
		opposite = &b.bids
	}

	var trades []domain.Trade
	var affected []OrderSnapshot
	for taker.RemainingQuantity() > 0 && len(opposite.prices) > 0 {
		price := opposite.prices[0]
		if !crossesOrder(taker, price) {
			break
		}
		level := opposite.levels[price]
		for level.Len() > 0 && taker.RemainingQuantity() > 0 {
			element := level.Front()
			maker := element.Value.(*domain.Order)
			if taker.ParticipantID() != "" && taker.ParticipantID() == maker.ParticipantID() && taker.SelfTradePrevention() != domain.SelfTradePreventionNone {
				switch taker.SelfTradePrevention() {
				case domain.SelfTradePreventionCancelAggressor:
					_ = taker.Cancel()
					return trades, append(affected, b.snapshotOrder(taker))
				case domain.SelfTradePreventionCancelResting, domain.SelfTradePreventionCancelBoth:
					_ = maker.Cancel()
					level.Remove(element)
					delete(b.active, maker.ID())
					affected = append(affected, b.snapshotOrder(maker))
					if taker.SelfTradePrevention() == domain.SelfTradePreventionCancelBoth {
						_ = taker.Cancel()
						return trades, append(affected, b.snapshotOrder(taker))
					}
					continue
				}
			}
			quantity := min(taker.RemainingQuantity(), maker.VisibleQuantity())
			if err := maker.Fill(quantity); err != nil {
				panic(fmt.Sprintf("engine invariant violated while filling maker: %v", err))
			}
			if err := taker.Fill(quantity); err != nil {
				panic(fmt.Sprintf("engine invariant violated while filling taker: %v", err))
			}

			tradeSequence := b.allocateSequence()
			trade, err := domain.NewTrade(
				domain.TradeID(strconv.FormatUint(uint64(tradeSequence), 10)),
				tradeSequence, maker.ID(), taker.ID(), maker.Price(), quantity,
			)
			if err != nil {
				panic(fmt.Sprintf("engine invariant violated while creating trade: %v", err))
			}
			trades = append(trades, trade)

			if maker.RemainingQuantity() == 0 {
				level.Remove(element)
				delete(b.active, maker.ID())
			} else if maker.DisplayQuantity() > 0 && maker.VisibleQuantity() == 0 {
				level.Remove(element)
				maker.RefreshDisplay()
				newElement := level.PushBack(maker)
				b.active[maker.ID()] = orderLocation{side: maker.Side(), price: maker.Price(), element: newElement}
			}
			affected = append(affected, b.snapshotOrder(maker))
		}
		if level.Len() == 0 {
			delete(opposite.levels, price)
			opposite.prices = opposite.prices[1:]
		}
	}
	return trades, affected
}

func (b *OrderBook) rest(order *domain.Order) {
	order.RefreshDisplay()
	side := &b.bids
	descending := true
	if order.Side() == domain.SideSell {
		side = &b.asks
		descending = false
	}

	level, exists := side.levels[order.Price()]
	if !exists {
		level = list.New()
		side.levels[order.Price()] = level
		side.prices = insertPrice(side.prices, order.Price(), descending)
	}
	element := level.PushBack(order)
	b.active[order.ID()] = orderLocation{side: order.Side(), price: order.Price(), element: element}
}

func insertPrice(prices []domain.Price, price domain.Price, descending bool) []domain.Price {
	index := sort.Search(len(prices), func(i int) bool {
		if descending {
			return prices[i] < price
		}
		return prices[i] > price
	})
	prices = append(prices, 0)
	copy(prices[index+1:], prices[index:])
	prices[index] = price
	return prices
}

func (b *OrderBook) allocateSequence() domain.Sequence {
	b.nextSequence++
	return b.nextSequence
}

func (b *OrderBook) side(side domain.Side) *bookSide {
	if side == domain.SideSell {
		return &b.asks
	}
	return &b.bids
}

func removePrice(side *bookSide, price domain.Price) {
	for i, candidate := range side.prices {
		if candidate == price {
			copy(side.prices[i:], side.prices[i+1:])
			side.prices = side.prices[:len(side.prices)-1]
			return
		}
	}
	panic("engine invariant violated: price level missing from sorted index")
}

func snapshotOrder(order *domain.Order) OrderSnapshot {
	return OrderSnapshot{
		ID: order.ID(), Sequence: order.Sequence(), ParticipantID: order.ParticipantID(), Side: order.Side(), Type: order.Type(),
		TimeInForce: order.TimeInForce(), Price: order.Price(),
		OriginalQuantity: order.OriginalQuantity(), RemainingQuantity: order.RemainingQuantity(),
		ExecutedQuantity: order.ExecutedQuantity(), StopPrice: order.StopPrice(), DisplayQuantity: order.DisplayQuantity(),
		VisibleQuantity: order.VisibleQuantity(), SelfTradePrevention: order.SelfTradePrevention(), Status: order.Status(),
	}
}

func (b *OrderBook) snapshotOrder(order *domain.Order) OrderSnapshot {
	snapshot := snapshotOrder(order)
	for _, stop := range b.stops {
		if stop.ID() == order.ID() {
			snapshot.Dormant = true
			break
		}
	}
	return snapshot
}

func snapshotSide(side *bookSide) []PriceLevelSnapshot {
	levels := make([]PriceLevelSnapshot, 0, len(side.prices))
	for _, price := range side.prices {
		level := PriceLevelSnapshot{Price: price}
		for element := side.levels[price].Front(); element != nil; element = element.Next() {
			order := snapshotOrder(element.Value.(*domain.Order))
			level.Orders = append(level.Orders, order)
			level.TotalQuantity += order.VisibleQuantity
		}
		levels = append(levels, level)
	}
	return levels
}

func crosses(takerSide domain.Side, takerPrice, makerPrice domain.Price) bool {
	if takerSide == domain.SideBuy {
		return takerPrice >= makerPrice
	}
	return takerPrice <= makerPrice
}

func crossesOrder(taker *domain.Order, makerPrice domain.Price) bool {
	if taker.Type() == domain.OrderTypeMarket || taker.Type() == domain.OrderTypeStopMarket {
		return true
	}
	return crosses(taker.Side(), taker.Price(), makerPrice)
}

func (b *OrderBook) canFullyFill(taker *domain.Order) bool {
	remaining := taker.RemainingQuantity()
	opposite := &b.asks
	if taker.Side() == domain.SideSell {
		opposite = &b.bids
	}
	for _, price := range opposite.prices {
		if !crossesOrder(taker, price) {
			break
		}
		for element := opposite.levels[price].Front(); element != nil; element = element.Next() {
			maker := element.Value.(*domain.Order)
			if taker.ParticipantID() != "" && taker.ParticipantID() == maker.ParticipantID() && taker.SelfTradePrevention() != domain.SelfTradePreventionNone {
				if taker.SelfTradePrevention() == domain.SelfTradePreventionCancelAggressor || taker.SelfTradePrevention() == domain.SelfTradePreventionCancelBoth {
					return false
				}
				continue
			}
			remaining -= maker.RemainingQuantity()
			if remaining <= 0 {
				return true
			}
		}
	}
	return false
}

func (b *OrderBook) takeTriggeredStops(lastPrice domain.Price) []*domain.Order {
	if lastPrice <= 0 || len(b.stops) == 0 {
		return nil
	}
	var triggered, pending []*domain.Order
	for _, order := range b.stops {
		if stopTriggered(order, lastPrice) {
			triggered = append(triggered, order)
		} else {
			pending = append(pending, order)
		}
	}
	b.stops = pending
	return triggered
}

func stopTriggered(order *domain.Order, lastPrice domain.Price) bool {
	return lastPrice > 0 && (order.Side() == domain.SideBuy && lastPrice >= order.StopPrice() || order.Side() == domain.SideSell && lastPrice <= order.StopPrice())
}

func deduplicateSnapshots(values []OrderSnapshot) []OrderSnapshot {
	latest := make(map[domain.OrderID]OrderSnapshot, len(values))
	for _, value := range values {
		latest[value.ID] = value
	}
	result := make([]OrderSnapshot, 0, len(latest))
	for _, value := range latest {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Sequence < result[j].Sequence })
	return result
}
