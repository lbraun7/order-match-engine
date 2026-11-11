package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/lbraun7/order-match-engine/internal/domain"
)

func TestEngineRejectsInvalidCapacity(t *testing.T) {
	if _, err := NewEngine(0); !errors.Is(err, ErrInvalidQueueCapacity) {
		t.Fatalf("NewEngine(0) error = %v", err)
	}
}

func TestEngineReturnsQueueFull(t *testing.T) {
	engine := &Engine{
		queue:     make(chan request, 1),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		accepting: true,
	}
	engine.queue <- request{}

	if _, err := engine.submit(context.Background(), snapshotCommand{}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("submit error = %v, want queue full", err)
	}
}

func TestEngineProcessesConcurrentProducers(t *testing.T) {
	engine, err := NewEngine(256)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownEngine(t, engine) })

	const producers = 100
	var wg sync.WaitGroup
	errorsByProducer := make(chan error, producers)
	for i := 0; i < producers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := domain.OrderID(fmt.Sprintf("order-%03d", i))
			result, err := engine.PlaceOrder(context.Background(), command(id, domain.SideBuy, 100, 1))
			if err != nil {
				errorsByProducer <- err
				return
			}
			if result.Order.ID != id {
				errorsByProducer <- fmt.Errorf("result ID = %s, want %s", result.Order.ID, id)
			}
		}(i)
	}
	wg.Wait()
	close(errorsByProducer)
	for err := range errorsByProducer {
		t.Error(err)
	}

	snapshot, err := engine.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Sequence != producers || len(snapshot.Bids) != 1 || len(snapshot.Bids[0].Orders) != producers {
		t.Fatalf("unexpected snapshot: sequence=%d levels=%d orders=%d", snapshot.Sequence, len(snapshot.Bids), len(snapshot.Bids[0].Orders))
	}
	seen := make(map[domain.Sequence]bool, producers)
	for _, order := range snapshot.Bids[0].Orders {
		if seen[order.Sequence] {
			t.Fatalf("duplicate sequence %d", order.Sequence)
		}
		seen[order.Sequence] = true
	}
}

func TestEngineSerializesPlaceCancelAndQueries(t *testing.T) {
	engine, err := NewEngine(8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownEngine(t, engine) })

	placed, err := engine.PlaceOrder(context.Background(), command("order", domain.SideSell, 100, 3))
	if err != nil || placed.Order.Sequence != 1 {
		t.Fatalf("place = %+v, %v", placed, err)
	}
	got, err := engine.GetOrder(context.Background(), "order")
	if err != nil || got.Status != domain.OrderStatusOpen {
		t.Fatalf("get = %+v, %v", got, err)
	}
	canceled, err := engine.CancelOrder(context.Background(), domain.CancelOrder{OrderID: "order"})
	if err != nil || canceled.Status != domain.OrderStatusCanceled {
		t.Fatalf("cancel = %+v, %v", canceled, err)
	}
}

func TestEngineContextAndShutdown(t *testing.T) {
	engine, err := NewEngine(1)
	if err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := engine.Snapshot(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Snapshot error = %v, want context canceled", err)
	}
	if err := engine.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.PlaceOrder(context.Background(), command("late", domain.SideBuy, 100, 1)); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("PlaceOrder after shutdown error = %v", err)
	}
	if err := engine.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown = %v", err)
	}
}

func shutdownEngine(t *testing.T, engine *Engine) {
	t.Helper()
	if err := engine.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}
