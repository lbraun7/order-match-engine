package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/lbraun7/order-match-engine/internal/domain"
)

var ErrRouterClosed = errors.New("engine router is shutting down")

// EngineFactory creates the independently owned single writer for one symbol.
// It may restore symbol-specific durable state before returning.
type EngineFactory func(domain.Symbol) (*Engine, error)

// Router lazily routes each symbol to exactly one Engine. The per-symbol
// engines own separate queues and workers, so overload and latency in one book
// do not consume queue capacity in another.
type Router struct {
	mu        sync.RWMutex
	engines   map[domain.Symbol]*Engine
	factory   EngineFactory
	accepting bool
}

// NewRouter creates a router whose symbols use independent queues of the same
// capacity.
func NewRouter(queueCapacity int) (*Router, error) {
	if queueCapacity <= 0 {
		return nil, fmt.Errorf("%d: %w", queueCapacity, ErrInvalidQueueCapacity)
	}
	return NewRouterWithFactory(func(domain.Symbol) (*Engine, error) {
		return NewEngine(queueCapacity)
	})
}

// NewRouterWithFactory creates a router with application-defined engine
// construction, primarily for symbol-specific persistence.
func NewRouterWithFactory(factory EngineFactory) (*Router, error) {
	if factory == nil {
		return nil, errors.New("engine factory is required")
	}
	return &Router{engines: make(map[domain.Symbol]*Engine), factory: factory, accepting: true}, nil
}

func (r *Router) engine(symbol domain.Symbol) (*Engine, error) {
	if err := symbol.Validate(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	if !r.accepting {
		r.mu.RUnlock()
		return nil, ErrRouterClosed
	}
	e := r.engines[symbol]
	r.mu.RUnlock()
	if e != nil {
		return e, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.accepting {
		return nil, ErrRouterClosed
	}
	if e = r.engines[symbol]; e != nil {
		return e, nil
	}
	var err error
	e, err = r.factory(symbol)
	if err != nil {
		return nil, fmt.Errorf("create engine for %q: %w", symbol, err)
	}
	if e == nil {
		return nil, fmt.Errorf("create engine for %q: factory returned nil", symbol)
	}
	r.engines[symbol] = e
	return e, nil
}

func (r *Router) PlaceOrder(ctx context.Context, symbol domain.Symbol, command domain.PlaceOrder) (PlaceOrderResult, error) {
	e, err := r.engine(symbol)
	if err != nil {
		return PlaceOrderResult{}, err
	}
	return e.PlaceOrder(ctx, command)
}

func (r *Router) CancelOrder(ctx context.Context, symbol domain.Symbol, command domain.CancelOrder) (OrderSnapshot, error) {
	e, err := r.engine(symbol)
	if err != nil {
		return OrderSnapshot{}, err
	}
	return e.CancelOrder(ctx, command)
}

func (r *Router) MassCancel(ctx context.Context, symbol domain.Symbol, command domain.MassCancel) (MassCancelResult, error) {
	e, err := r.engine(symbol)
	if err != nil {
		return MassCancelResult{}, err
	}
	return e.MassCancel(ctx, command)
}

func (r *Router) GetOrder(ctx context.Context, symbol domain.Symbol, id domain.OrderID) (OrderSnapshot, error) {
	e, err := r.engine(symbol)
	if err != nil {
		return OrderSnapshot{}, err
	}
	return e.GetOrder(ctx, id)
}

func (r *Router) Snapshot(ctx context.Context, symbol domain.Symbol) (BookSnapshot, error) {
	e, err := r.engine(symbol)
	if err != nil {
		return BookSnapshot{}, err
	}
	return e.Snapshot(ctx)
}

func (r *Router) Stats(ctx context.Context, symbol domain.Symbol) (Stats, error) {
	e, err := r.engine(symbol)
	if err != nil {
		return Stats{}, err
	}
	return e.Stats(ctx)
}

// Engines returns a stable snapshot for observability. Callers must not shut
// down the returned engines; lifecycle remains owned by the router.
func (r *Router) Engines() map[domain.Symbol]*Engine {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[domain.Symbol]*Engine, len(r.engines))
	for symbol, e := range r.engines {
		result[symbol] = e
	}
	return result
}

// Symbols returns currently initialized symbols in lexical order.
func (r *Router) Symbols() []domain.Symbol {
	engines := r.Engines()
	symbols := make([]domain.Symbol, 0, len(engines))
	for symbol := range engines {
		symbols = append(symbols, symbol)
	}
	sort.Slice(symbols, func(i, j int) bool { return symbols[i] < symbols[j] })
	return symbols
}

// Shutdown atomically rejects new symbols and commands, then concurrently
// drains every initialized engine. It is safe to call more than once.
func (r *Router) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	r.accepting = false
	engines := make([]*Engine, 0, len(r.engines))
	for _, e := range r.engines {
		engines = append(engines, e)
	}
	r.mu.Unlock()

	var wg sync.WaitGroup
	errs := make(chan error, len(engines))
	for _, e := range engines {
		wg.Add(1)
		go func(e *Engine) {
			defer wg.Done()
			if err := e.Shutdown(ctx); err != nil {
				errs <- err
			}
		}(e)
	}
	wg.Wait()
	close(errs)
	var joined []error
	for err := range errs {
		joined = append(joined, err)
	}
	return errors.Join(joined...)
}
