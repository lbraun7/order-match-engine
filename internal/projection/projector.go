// Package projection rebuilds queryable database views from the durable event
// log without participating in the matching engine's critical path.
package projection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/lucasbraun96/order-match-engine/internal/domain"
	"github.com/lucasbraun96/order-match-engine/internal/engine"
	"github.com/lucasbraun96/order-match-engine/internal/eventlog"
)

var ErrInvalidConfig = errors.New("invalid projection configuration")

// Event is one atomic database projection update. EventSequence is the durable
// command identifier and is used as the idempotency key.
type Event struct {
	EventSequence uint64
	EventType     eventlog.Type
	Orders        []engine.OrderSnapshot
	Trades        []domain.Trade
}

// Store persists projection updates and their checkpoint atomically.
type Store interface {
	Checkpoint(context.Context) (uint64, error)
	Apply(context.Context, Event) error
	Reset(context.Context) error
}

type Config struct {
	EventLogPath string
	PollInterval time.Duration
	RetryLimit   int
	RetryBackoff time.Duration
	Logger       *slog.Logger
}

type Stats struct {
	AppliedEvents uint64
	Retries       uint64
	Failures      uint64
}

type Projector struct {
	store  Store
	config Config
	book   *engine.OrderBook
	stats  struct {
		applied  atomic.Uint64
		retries  atomic.Uint64
		failures atomic.Uint64
	}
}

func New(store Store, config Config) (*Projector, error) {
	if store == nil || config.EventLogPath == "" || config.RetryLimit < 0 {
		return nil, ErrInvalidConfig
	}
	if config.PollInterval <= 0 {
		config.PollInterval = time.Second
	}
	if config.RetryBackoff <= 0 {
		config.RetryBackoff = 100 * time.Millisecond
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Projector{store: store, config: config, book: engine.NewOrderBook()}, nil
}

func (p *Projector) Stats() Stats {
	return Stats{AppliedEvents: p.stats.applied.Load(), Retries: p.stats.retries.Load(), Failures: p.stats.failures.Load()}
}

// Run continuously catches up with the event log. Exhausting the retry budget
// for one pass is observable but not fatal: the next poll starts a fresh,
// bounded attempt so temporary database outages cannot stop matching.
func (p *Projector) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.config.PollInterval)
	defer ticker.Stop()
	for {
		if err := p.Sync(ctx); err != nil && !errors.Is(err, context.Canceled) {
			p.stats.failures.Add(1)
			p.config.Logger.ErrorContext(ctx, "projection sync failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Rebuild clears the query model and replays every durable command.
func (p *Projector) Rebuild(ctx context.Context) error {
	if err := p.retry(ctx, "reset", p.store.Reset); err != nil {
		return err
	}
	p.book = engine.NewOrderBook()
	return p.Sync(ctx)
}

// Sync applies all currently available events after the store checkpoint.
func (p *Projector) Sync(ctx context.Context) error {
	events, err := eventlog.ReadFile(p.config.EventLogPath)
	if err != nil {
		return fmt.Errorf("read event log: %w", err)
	}
	checkpoint, err := checkpointWithRetry(ctx, p)
	if err != nil {
		return err
	}

	// Recreate in-memory reducer state on each pass. This also validates the
	// entire source and makes retries/restarts independent of process memory.
	p.book = engine.NewOrderBook()
	for _, logged := range events {
		projected, err := reduce(p.book, logged)
		if err != nil {
			return fmt.Errorf("reduce event %d: %w", logged.Sequence, err)
		}
		if logged.Sequence <= checkpoint {
			continue
		}
		if err := p.retry(ctx, "apply", func(ctx context.Context) error { return p.store.Apply(ctx, projected) }); err != nil {
			return fmt.Errorf("apply event %d: %w", logged.Sequence, err)
		}
		checkpoint = logged.Sequence
		p.stats.applied.Add(1)
	}
	return nil
}

func checkpointWithRetry(ctx context.Context, p *Projector) (uint64, error) {
	var checkpoint uint64
	err := p.retry(ctx, "checkpoint", func(ctx context.Context) error {
		var err error
		checkpoint, err = p.store.Checkpoint(ctx)
		return err
	})
	return checkpoint, err
}

func (p *Projector) retry(ctx context.Context, operation string, fn func(context.Context) error) error {
	var err error
	for attempt := 0; attempt <= p.config.RetryLimit; attempt++ {
		if err = fn(ctx); err == nil {
			return nil
		}
		if attempt == p.config.RetryLimit {
			break
		}
		p.stats.retries.Add(1)
		p.config.Logger.WarnContext(ctx, "projection operation retry", "operation", operation, "attempt", attempt+1, "error", err)
		timer := time.NewTimer(p.config.RetryBackoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("%s failed after %d attempts: %w", operation, p.config.RetryLimit+1, err)
}

func reduce(book *engine.OrderBook, logged eventlog.Event) (Event, error) {
	projected := Event{EventSequence: logged.Sequence, EventType: logged.Type}
	switch logged.Type {
	case eventlog.TypePlace:
		var command domain.PlaceOrder
		if err := json.Unmarshal(logged.Payload, &command); err != nil {
			return Event{}, err
		}
		order, trades, err := book.PlaceOrder(command)
		if err != nil {
			return Event{}, err
		}
		current, err := book.GetOrder(order.ID())
		if err != nil {
			return Event{}, err
		}
		projected.Orders = append(projected.Orders, current)
		projected.Trades = trades
		seen := map[domain.OrderID]bool{order.ID(): true}
		for _, trade := range trades {
			if seen[trade.MakerOrderID()] {
				continue
			}
			maker, err := book.GetOrder(trade.MakerOrderID())
			if err != nil {
				return Event{}, err
			}
			projected.Orders = append(projected.Orders, maker)
			seen[trade.MakerOrderID()] = true
		}
	case eventlog.TypeCancel:
		var command domain.CancelOrder
		if err := json.Unmarshal(logged.Payload, &command); err != nil {
			return Event{}, err
		}
		order, err := book.CancelOrder(command)
		if err != nil {
			return Event{}, err
		}
		projected.Orders = append(projected.Orders, order)
	default:
		return Event{}, fmt.Errorf("unknown event type %d", logged.Type)
	}
	return projected, nil
}
