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
	ErrDuplicateOrderID       = errors.New("duplicate order ID")
	ErrOrderNotFound          = errors.New("order not found")
	ErrUnsupportedTimeInForce = errors.New("unsupported time in force")
)

// OrderSnapshot is an immutable view of an order at query time.
type OrderSnapshot struct {
	ID                domain.OrderID
	Sequence          domain.Sequence
	Side              domain.Side
	Type              domain.OrderType
	TimeInForce       domain.TimeInForce
	Price             domain.Price
	OriginalQuantity  domain.Quantity
	RemainingQuantity domain.Quantity
	ExecutedQuantity  domain.Quantity
	Status            domain.OrderStatus
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
	nextSequence domain.Sequence
	bids         bookSide
	asks         bookSide
	orders       map[domain.OrderID]*domain.Order
	active       map[domain.OrderID]orderLocation
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
	if command.TimeInForce != domain.TimeInForceGTC {
		return ErrUnsupportedTimeInForce
	}
	if _, exists := b.orders[command.OrderID]; exists {
		return fmt.Errorf("%s: %w", command.OrderID, ErrDuplicateOrderID)
	}
	return nil
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
	if err := b.canPlace(command); err != nil {
		return nil, nil, err
	}

	orderSequence := b.allocateSequence()
	order, err := domain.NewOrder(command.OrderID, orderSequence, command.Side, command.OrderType, command.TimeInForce, command.Price, command.Quantity)
	if err != nil {
		return nil, nil, err
	}
	b.orders[order.ID()] = order

	trades := b.match(order)
	if order.RemainingQuantity() > 0 {
		b.rest(order)
	}
	return order, trades, nil
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
		if err := order.Cancel(); err != nil {
			return OrderSnapshot{}, fmt.Errorf("%s: %w", command.OrderID, err)
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

type persistedBook struct {
	Sequence domain.Sequence `json:"sequence"`
	Orders   []OrderSnapshot `json:"orders"`
}

func (b *OrderBook) marshalState() ([]byte, error) {
	state := persistedBook{Sequence: b.nextSequence, Orders: make([]OrderSnapshot, 0, len(b.orders))}
	for _, order := range b.orders {
		state.Orders = append(state.Orders, snapshotOrder(order))
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
	for _, saved := range state.Orders {
		if saved.Sequence > state.Sequence {
			return nil, fmt.Errorf("order %s sequence exceeds snapshot: %w", saved.ID, ErrDuplicateOrderID)
		}
		if _, exists := book.orders[saved.ID]; exists {
			return nil, fmt.Errorf("%s: %w", saved.ID, ErrDuplicateOrderID)
		}
		order, err := domain.NewOrder(saved.ID, saved.Sequence, saved.Side, saved.Type, saved.TimeInForce, saved.Price, saved.OriginalQuantity)
		if err != nil {
			return nil, err
		}
		if saved.ExecutedQuantity > 0 {
			if err := order.Fill(saved.ExecutedQuantity); err != nil {
				return nil, err
			}
		}
		if saved.Status == domain.OrderStatusCanceled {
			if err := order.Cancel(); err != nil {
				return nil, err
			}
		}
		if snapshotOrder(order) != saved {
			return nil, fmt.Errorf("order %s state is inconsistent", saved.ID)
		}
		book.orders[saved.ID] = order
		if order.Status() == domain.OrderStatusOpen || order.Status() == domain.OrderStatusPartiallyFilled {
			book.rest(order)
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
	return snapshotOrder(order), nil
}

// Snapshot copies both sides in price priority and each level in FIFO order.
func (b *OrderBook) Snapshot() BookSnapshot {
	return BookSnapshot{
		Sequence: b.nextSequence,
		Bids:     snapshotSide(&b.bids),
		Asks:     snapshotSide(&b.asks),
	}
}

func (b *OrderBook) match(taker *domain.Order) []domain.Trade {
	opposite := &b.asks
	if taker.Side() == domain.SideSell {
		opposite = &b.bids
	}

	var trades []domain.Trade
	for taker.RemainingQuantity() > 0 && len(opposite.prices) > 0 {
		price := opposite.prices[0]
		if !crosses(taker.Side(), taker.Price(), price) {
			break
		}
		level := opposite.levels[price]
		for level.Len() > 0 && taker.RemainingQuantity() > 0 {
			element := level.Front()
			maker := element.Value.(*domain.Order)
			quantity := min(taker.RemainingQuantity(), maker.RemainingQuantity())
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
			}
		}
		if level.Len() == 0 {
			delete(opposite.levels, price)
			opposite.prices = opposite.prices[1:]
		}
	}
	return trades
}

func (b *OrderBook) rest(order *domain.Order) {
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
		ID: order.ID(), Sequence: order.Sequence(), Side: order.Side(), Type: order.Type(),
		TimeInForce: order.TimeInForce(), Price: order.Price(),
		OriginalQuantity: order.OriginalQuantity(), RemainingQuantity: order.RemainingQuantity(),
		ExecutedQuantity: order.ExecutedQuantity(), Status: order.Status(),
	}
}

func snapshotSide(side *bookSide) []PriceLevelSnapshot {
	levels := make([]PriceLevelSnapshot, 0, len(side.prices))
	for _, price := range side.prices {
		level := PriceLevelSnapshot{Price: price}
		for element := side.levels[price].Front(); element != nil; element = element.Next() {
			order := snapshotOrder(element.Value.(*domain.Order))
			level.Orders = append(level.Orders, order)
			level.TotalQuantity += order.RemainingQuantity
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
