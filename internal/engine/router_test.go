package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lbraun7/order-match-engine/internal/domain"
)

func TestRouterProcessesSymbolsIndependently(t *testing.T) {
	router, err := NewRouter(64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = router.Shutdown(context.Background()) })

	const orders = 100
	var wg sync.WaitGroup
	for _, symbol := range []domain.Symbol{"BTC-USD", "ETH-USD", "SOL-USD"} {
		symbol := symbol
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < orders; i++ {
				id := domain.OrderID(fmt.Sprintf("order-%d", i))
				for {
					_, err := router.PlaceOrder(context.Background(), symbol, domain.PlaceOrder{
						OrderID: id, Side: domain.SideBuy, OrderType: domain.OrderTypeLimit,
						TimeInForce: domain.TimeInForceGTC, Price: domain.Price(100 - i%10), Quantity: 1,
					})
					if errors.Is(err, ErrQueueFull) {
						continue
					}
					if err != nil {
						t.Errorf("%s order %d: %v", symbol, i, err)
					}
					break
				}
			}
		}()
	}
	wg.Wait()

	for _, symbol := range router.Symbols() {
		snapshot, err := router.Snapshot(context.Background(), symbol)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Sequence != orders {
			t.Errorf("%s sequence = %d, want %d", symbol, snapshot.Sequence, orders)
		}
		order, err := router.GetOrder(context.Background(), symbol, "order-0")
		if err != nil || order.ID != "order-0" {
			t.Errorf("%s reused order ID was not isolated: order=%v err=%v", symbol, order, err)
		}
	}
	engines := router.Engines()
	if engines["BTC-USD"] == engines["ETH-USD"] {
		t.Fatal("symbols share an engine")
	}
	for _, e := range engines {
		_, capacity := e.QueueStats()
		if capacity != 64 {
			t.Fatalf("queue capacity = %d, want 64", capacity)
		}
	}
}

func TestRouterCreatesOneEnginePerSymbolAndOwnsLifecycle(t *testing.T) {
	var created atomic.Int64
	router, err := NewRouterWithFactory(func(domain.Symbol) (*Engine, error) {
		created.Add(1)
		return NewEngine(8)
	})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				_, err := router.Snapshot(context.Background(), "BTC-USD")
				if errors.Is(err, ErrQueueFull) {
					continue
				}
				if err != nil {
					t.Errorf("snapshot: %v", err)
				}
				return
			}
		}()
	}
	wg.Wait()
	if got := created.Load(); got != 1 {
		t.Fatalf("factory calls = %d, want 1", got)
	}
	if err := router.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Snapshot(context.Background(), "ETH-USD"); !errors.Is(err, ErrRouterClosed) {
		t.Fatalf("after shutdown error = %v, want ErrRouterClosed", err)
	}
	if err := router.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRouterRejectsEmptySymbol(t *testing.T) {
	router, err := NewRouter(8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = router.Shutdown(context.Background()) })
	if _, err := router.Snapshot(context.Background(), ""); !errors.Is(err, domain.ErrInvalidSymbol) {
		t.Fatalf("error = %v, want ErrInvalidSymbol", err)
	}
}

func TestRouterBackpressureIsIsolatedPerSymbol(t *testing.T) {
	router, err := NewRouter(1)
	if err != nil {
		t.Fatal(err)
	}
	blockedDone := make(chan struct{})
	close(blockedDone)
	blocked := &Engine{
		queue: make(chan request, 1), stop: make(chan struct{}), done: blockedDone, accepting: true,
	}
	healthy, err := NewEngine(1)
	if err != nil {
		t.Fatal(err)
	}
	router.engines["BLOCKED"] = blocked
	router.engines["HEALTHY"] = healthy
	t.Cleanup(func() { _ = router.Shutdown(context.Background()) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Put an already accepted request directly in the stalled symbol queue. The
	// worker is intentionally absent so its one slot remains occupied.
	blocked.queue <- request{command: snapshotCommand{}, result: make(chan response, 1)}
	if _, err := router.Snapshot(ctx, "BLOCKED"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request error = %v", err)
	}
	if _, err := router.Snapshot(context.Background(), "BLOCKED"); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("blocked symbol error = %v, want ErrQueueFull", err)
	}
	if _, err := router.Snapshot(context.Background(), "HEALTHY"); err != nil {
		t.Fatalf("healthy symbol was affected by other queue: %v", err)
	}
}

func BenchmarkRouterSymbolScalability(b *testing.B) {
	for _, symbolCount := range []int{1, 2, 4, 8} {
		b.Run(fmt.Sprintf("symbols-%d", symbolCount), func(b *testing.B) {
			router, err := NewRouter(4096)
			if err != nil {
				b.Fatal(err)
			}
			defer router.Shutdown(context.Background())
			var next atomic.Uint64
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					n := next.Add(1)
					symbol := domain.Symbol(fmt.Sprintf("SYM-%d", n%uint64(symbolCount)))
					command := domain.PlaceOrder{
						OrderID: domain.OrderID(fmt.Sprintf("order-%d", n)), Side: domain.SideBuy,
						OrderType: domain.OrderTypeLimit, TimeInForce: domain.TimeInForceGTC,
						Price: 100, Quantity: 1,
					}
					for {
						_, err := router.PlaceOrder(context.Background(), symbol, command)
						if errors.Is(err, ErrQueueFull) {
							continue
						}
						if err != nil {
							b.Fatal(err)
						}
						break
					}
				}
			})
		})
	}
}
