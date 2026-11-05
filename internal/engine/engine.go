package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/lucasbraun96/order-match-engine/internal/domain"
	"github.com/lucasbraun96/order-match-engine/internal/eventlog"
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
type statsCommand struct{}

// Stats is a point-in-time operational view produced by the single writer.
// It intentionally contains only aggregate values so observability does not
// expose or copy the order book.
type Stats struct {
	Sequence      domain.Sequence
	QueueDepth    int
	QueueCapacity int
	BidLevels     int
	AskLevels     int
	RestingOrders int
}

// Engine serializes every operation through one bounded command queue and one
// goroutine. After construction, its order book is owned exclusively by that
// goroutine until Shutdown finishes.
type Engine struct {
	queue chan request
	stop  chan struct{}
	done  chan struct{}

	stateMu   sync.RWMutex
	accepting bool

	log              *eventlog.Log
	snapshotPath     string
	snapshotInterval uint64
	logEvents        uint64
}

// PersistenceConfig enables command logging and periodic state snapshots.
// SnapshotInterval zero disables snapshots while retaining the event log.
type PersistenceConfig struct {
	LogPath          string
	SnapshotPath     string
	SnapshotInterval uint64
}

// NewEngine starts a single-writer engine with the requested queue capacity.
func NewEngine(queueCapacity int) (*Engine, error) {
	return newEngine(queueCapacity, nil, NewOrderBook())
}

// NewPersistentEngine restores the latest valid snapshot, replays subsequent
// events, then starts the single writer. A command is accepted durably only
// after its complete event record has been flushed and before it mutates the
// in-memory book.
func NewPersistentEngine(queueCapacity int, config PersistenceConfig) (*Engine, error) {
	if queueCapacity <= 0 {
		return nil, fmt.Errorf("%d: %w", queueCapacity, ErrInvalidQueueCapacity)
	}
	if config.LogPath == "" {
		return nil, errors.New("event log path is required")
	}
	log, err := eventlog.Open(config.LogPath)
	if err != nil {
		return nil, err
	}
	events, err := log.ReadAll()
	if err != nil {
		log.Close()
		return nil, err
	}
	book := NewOrderBook()
	var consumed uint64
	if config.SnapshotPath != "" {
		snapshot, snapshotErr := eventlog.ReadSnapshot(config.SnapshotPath)
		if snapshotErr == nil {
			if snapshot.LogEvents > uint64(len(events)) {
				log.Close()
				return nil, errors.New("snapshot is ahead of event log")
			}
			book, err = restoreOrderBook(snapshot.State)
			if err != nil {
				log.Close()
				return nil, err
			}
			if uint64(book.nextSequence) != snapshot.Sequence {
				log.Close()
				return nil, errors.New("snapshot sequence does not match state")
			}
			consumed = snapshot.LogEvents
		} else if !errors.Is(snapshotErr, os.ErrNotExist) {
			log.Close()
			return nil, snapshotErr
		}
	}
	for _, event := range events[consumed:] {
		if err := replayEvent(book, event); err != nil {
			log.Close()
			return nil, err
		}
	}
	engine, err := newEngine(queueCapacity, log, book)
	if err != nil {
		log.Close()
		return nil, err
	}
	engine.snapshotPath = config.SnapshotPath
	engine.snapshotInterval = config.SnapshotInterval
	engine.logEvents = uint64(len(events))
	return engine, nil
}

func newEngine(queueCapacity int, log *eventlog.Log, book *OrderBook) (*Engine, error) {
	if queueCapacity <= 0 {
		return nil, fmt.Errorf("%d: %w", queueCapacity, ErrInvalidQueueCapacity)
	}
	e := &Engine{
		queue:     make(chan request, queueCapacity),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		accepting: true,
		log:       log,
	}
	go e.run(book)
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

// Stats returns aggregate engine and book state. Like every book query, it is
// serialized with mutations so the reported values form a consistent sample.
func (e *Engine) Stats(ctx context.Context) (Stats, error) {
	value, err := e.submit(ctx, statsCommand{})
	if err != nil {
		return Stats{}, err
	}
	return value.(Stats), nil
}

// QueueStats samples the channel occupancy without entering the queue. It is
// safe for concurrent callers and remains available when the queue is full.
func (e *Engine) QueueStats() (depth, capacity int) {
	return len(e.queue), cap(e.queue)
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
	defer func() {
		if e.log != nil {
			_ = e.log.Close()
		}
		close(e.done)
	}()
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
		if err := book.canPlace(command); err != nil {
			result.err = err
			break
		}
		if err := e.persist(book, eventlog.TypePlace, command); err != nil {
			result.err = err
			break
		}
		order, trades, err := book.PlaceOrder(command)
		result.err = err
		if err == nil {
			result.value = PlaceOrderResult{Order: snapshotOrder(order), Trades: trades}
		}
	case domain.CancelOrder:
		if err := book.canCancel(command); err != nil {
			result.err = err
			break
		}
		if err := e.persist(book, eventlog.TypeCancel, command); err != nil {
			result.err = err
			break
		}
		result.value, result.err = book.CancelOrder(command)
	case getOrderCommand:
		result.value, result.err = book.GetOrder(command.id)
	case snapshotCommand:
		result.value = book.Snapshot()
	case statsCommand:
		result.value = Stats{
			Sequence: book.nextSequence, QueueDepth: len(e.queue), QueueCapacity: cap(e.queue),
			BidLevels: len(book.bids.prices), AskLevels: len(book.asks.prices),
			RestingOrders: len(book.active),
		}
	default:
		panic(fmt.Sprintf("engine invariant violated: unsupported command %T", command))
	}
	if result.err == nil {
		e.maybeSnapshot(book)
	}
	req.result <- result
}

func (e *Engine) persist(book *OrderBook, eventType eventlog.Type, command any) error {
	if e.log == nil {
		return nil
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return err
	}
	event := eventlog.Event{Sequence: uint64(book.nextSequence + 1), Type: eventType, Version: eventlog.Version, Payload: payload}
	if err := e.log.Append(event); err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	e.logEvents++
	return nil
}

func (e *Engine) maybeSnapshot(book *OrderBook) {
	if e.log == nil || e.snapshotPath == "" || e.snapshotInterval == 0 || e.logEvents%e.snapshotInterval != 0 {
		return
	}
	state, err := book.marshalState()
	if err != nil {
		return
	}
	_ = eventlog.WriteSnapshot(e.snapshotPath, eventlog.Snapshot{
		LogEvents: e.logEvents, Sequence: uint64(book.nextSequence), State: state,
	})
}

func replayEvent(book *OrderBook, event eventlog.Event) error {
	if event.Sequence != uint64(book.nextSequence+1) {
		return fmt.Errorf("event sequence %d, want %d", event.Sequence, book.nextSequence+1)
	}
	switch event.Type {
	case eventlog.TypePlace:
		var command domain.PlaceOrder
		if err := json.Unmarshal(event.Payload, &command); err != nil {
			return err
		}
		_, _, err := book.PlaceOrder(command)
		return err
	case eventlog.TypeCancel:
		var command domain.CancelOrder
		if err := json.Unmarshal(event.Payload, &command); err != nil {
			return err
		}
		_, err := book.CancelOrder(command)
		return err
	default:
		return fmt.Errorf("unknown event type %d", event.Type)
	}
}
