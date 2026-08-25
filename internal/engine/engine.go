package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/lucasbraun96/order-match-engine/internal/domain"
)

var (
	ErrInvalidQueueCapacity = errors.New("invalid queue capacity")
	ErrQueueFull            = errors.New("engine command queue is full")
	ErrEngineClosed         = errors.New("engine is shutting down")
)

// PlaceOrderResult is an immutable result of processing a place command.
type PlaceOrderResult struct {
	Order  OrderSnapshot
	Trades []domain.Trade
}

// request is the queue envelope. The buffered result channel lets the engine
// finish an accepted command even when its caller's context is canceled.
type request struct {
	command any
	result  chan response
}

type response struct {
	value any
	err   error
}

type getOrderCommand struct{ id domain.OrderID }
type snapshotCommand struct{}

// Engine serializes every operation through one bounded command queue and one
// goroutine. After construction, its order book is owned exclusively by that
// goroutine until Shutdown finishes.
type Engine struct {
	queue chan request
	stop  chan struct{}
	done  chan struct{}

	stateMu   sync.RWMutex
	accepting bool
}

// NewEngine starts a single-writer engine with the requested queue capacity.
func NewEngine(queueCapacity int) (*Engine, error) {
	if queueCapacity <= 0 {
		return nil, fmt.Errorf("%d: %w", queueCapacity, ErrInvalidQueueCapacity)
	}
	e := &Engine{
		queue:     make(chan request, queueCapacity),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		accepting: true,
	}
	go e.run(NewOrderBook())
	return e, nil
}

// PlaceOrder enqueues a command without waiting for queue space. ErrQueueFull
// is returned when configured capacity has been exhausted.
func (e *Engine) PlaceOrder(ctx context.Context, command domain.PlaceOrder) (PlaceOrderResult, error) {
	value, err := e.submit(ctx, command)
	if err != nil {
		return PlaceOrderResult{}, err
	}
	return value.(PlaceOrderResult), nil
}

// CancelOrder serializes cancellation with every other book operation.
func (e *Engine) CancelOrder(ctx context.Context, command domain.CancelOrder) (OrderSnapshot, error) {
	value, err := e.submit(ctx, command)
	if err != nil {
		return OrderSnapshot{}, err
	}
	return value.(OrderSnapshot), nil
}

// GetOrder returns an immutable order copy produced by the single writer.
func (e *Engine) GetOrder(ctx context.Context, id domain.OrderID) (OrderSnapshot, error) {
	value, err := e.submit(ctx, getOrderCommand{id: id})
	if err != nil {
		return OrderSnapshot{}, err
	}
	return value.(OrderSnapshot), nil
}

// Snapshot returns a consistent copy produced by the single writer.
func (e *Engine) Snapshot(ctx context.Context) (BookSnapshot, error) {
	value, err := e.submit(ctx, snapshotCommand{})
	if err != nil {
		return BookSnapshot{}, err
	}
	return value.(BookSnapshot), nil
}

// Shutdown rejects new commands, drains all accepted commands in FIFO order,
// and waits for the worker to stop. A canceled context only stops the wait; a
// later call may continue waiting for the same shutdown.
func (e *Engine) Shutdown(ctx context.Context) error {
	e.stateMu.Lock()
	if e.accepting {
		e.accepting = false
		close(e.stop)
	}
	e.stateMu.Unlock()

	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) submit(ctx context.Context, command any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	req := request{command: command, result: make(chan response, 1)}
	e.stateMu.RLock()
	if !e.accepting {
		e.stateMu.RUnlock()
		return nil, ErrEngineClosed
	}
	select {
	case e.queue <- req:
		e.stateMu.RUnlock()
	default:
		e.stateMu.RUnlock()
		return nil, ErrQueueFull
	}

	select {
	case result := <-req.result:
		return result.value, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *Engine) run(book *OrderBook) {
	defer close(e.done)
	for {
		select {
		case req := <-e.queue:
			e.process(book, req)
		case <-e.stop:
			for {
				select {
				case req := <-e.queue:
					e.process(book, req)
				default:
					return
				}
			}
		}
	}
}

func (e *Engine) process(book *OrderBook, req request) {
	var result response
	switch command := req.command.(type) {
	case domain.PlaceOrder:
		order, trades, err := book.PlaceOrder(command)
		result.err = err
		if err == nil {
			result.value = PlaceOrderResult{Order: snapshotOrder(order), Trades: trades}
		}
	case domain.CancelOrder:
		result.value, result.err = book.CancelOrder(command)
	case getOrderCommand:
		result.value, result.err = book.GetOrder(command.id)
	case snapshotCommand:
		result.value = book.Snapshot()
	default:
		panic(fmt.Sprintf("engine invariant violated: unsupported command %T", command))
	}
	req.result <- result
}
