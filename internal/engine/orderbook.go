package engine

import (
	"container/list"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/lucasbraun96/order-match-engine/internal/domain"
)

var (
	ErrDuplicateOrderID       = errors.New("duplicate order ID")
	ErrUnsupportedTimeInForce = errors.New("unsupported time in force")
)

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

// PlaceOrder assigns the order sequence, matches at maker prices, and rests any
// remaining quantity. A single monotonically increasing sequence is shared by
// accepted orders and generated trades.
func (b *OrderBook) PlaceOrder(command domain.PlaceOrder) (*domain.Order, []domain.Trade, error) {
	if err := command.Validate(); err != nil {
		return nil, nil, err
	}
	if command.TimeInForce != domain.TimeInForceGTC {
		return nil, nil, ErrUnsupportedTimeInForce
	}
	if _, exists := b.orders[command.OrderID]; exists {
		return nil, nil, fmt.Errorf("%s: %w", command.OrderID, ErrDuplicateOrderID)
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
		side.prices = append(side.prices, order.Price())
		sort.Slice(side.prices, func(i, j int) bool {
			if descending {
				return side.prices[i] > side.prices[j]
			}
			return side.prices[i] < side.prices[j]
		})
	}
	element := level.PushBack(order)
	b.active[order.ID()] = orderLocation{side: order.Side(), price: order.Price(), element: element}
}

func (b *OrderBook) allocateSequence() domain.Sequence {
	b.nextSequence++
	return b.nextSequence
}

func crosses(takerSide domain.Side, takerPrice, makerPrice domain.Price) bool {
	if takerSide == domain.SideBuy {
		return takerPrice >= makerPrice
	}
	return takerPrice <= makerPrice
}
